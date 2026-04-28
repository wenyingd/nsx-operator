/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package dns

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/vmware-tanzu/nsx-operator/pkg/logger"
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/realizestate"
	nsxutil "github.com/vmware-tanzu/nsx-operator/pkg/nsx/util"
	extdns "github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/endpoint"
	"github.com/vmware-tanzu/nsx-operator/pkg/util"
)

var (
	log                   = logger.Log
	_   DNSRecordProvider = (*DNSRecordService)(nil)
)

// NSX Policy path: /orgs/{org}/projects/{project}/dns-records/{record-id}
var projectDNSRecordPathRe = regexp.MustCompile(`^/orgs/([^/]+)/projects/([^/]+)/dns-records/([^/]+)$`)

// dnsZoneCache is a thread-safe zone path → DNS domain name cache.
type dnsZoneCache struct {
	mu sync.RWMutex
	m  map[string]string
}

func newDNSZoneCache() *dnsZoneCache {
	return &dnsZoneCache{m: make(map[string]string)}
}

// NewDNSZoneCacheFromMap creates a dnsZoneCache pre-populated from m.
func NewDNSZoneCacheFromMap(m map[string]string) *dnsZoneCache {
	c := newDNSZoneCache()
	for k, v := range m {
		c.m[k] = v
	}
	return c
}

func (c *dnsZoneCache) get(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.m[key]
	return v, ok
}

func (c *dnsZoneCache) set(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = value
}

// DNSRecordService reconciles DNS rows against NSX (store + future HAPI); optional VPCService and DNSZoneMap for zone validation.
type DNSRecordService struct {
	common.Service
	VPCService              common.VPCServiceProvider
	DNSRecordStore          *RecordStore
	DNSZoneMap              *dnsZoneCache
	ProjectDnsRecordBuilder *common.PolicyTreeBuilder[*model.ProjectDnsRecord]
}

// CreateOrUpdateRecords upserts batch rows into the store and NSX placeholder. Returns (storeMutated, err).
func (s *DNSRecordService) CreateOrUpdateRecords(ctx context.Context, batch *AggregatedDNSEndpoints) (bool, error) {
	toUpsert, toRemove, err := s.applyDNSUpsertRows(batch)
	if err != nil {
		return false, err
	}
	if len(toUpsert) == 0 && len(toRemove) == 0 {
		return false, nil
	}
	toApply, err := s.syncProjectDnsRecordsInNSX(ctx, toUpsert, toRemove)
	if err != nil {
		return false, err
	}
	if err := s.DNSRecordStore.Apply(toApply); err != nil {
		return false, err
	}
	return true, nil
}

// syncProjectDnsRecordsInNSX sends upserts then deletes via OrgRoot hierarchy patch. For each upsert it checks
// realization on the patched record path (like Subnet), then GETs the record from NSX for the store; on realization
// failure it deletes the record via Policy API.
func (s *DNSRecordService) syncProjectDnsRecordsInNSX(ctx context.Context, toUpsert, toRemove []*model.ProjectDnsRecord) ([]*model.ProjectDnsRecord, error) {
	removeOps := make([]*model.ProjectDnsRecord, 0, len(toRemove))
	for _, rec := range toRemove {
		cp := *rec
		cp.MarkedForDelete = common.Bool(true)
		removeOps = append(removeOps, &cp)
	}

	batch := append(append([]*model.ProjectDnsRecord(nil), toUpsert...), removeOps...)

	if len(batch) == 0 {
		return nil, nil
	}
	log.Info("Patching ProjectDnsRecord batch on NSX", "count", len(batch))
	if err := s.ProjectDnsRecordBuilder.PagingUpdateResources(ctx, batch, common.DefaultHAPIChildrenCount, s.NSXClient, nil); err != nil {
		return nil, err
	}
	if len(toUpsert) == 0 {
		return removeOps, nil
	}
	// Check the realization state of the created or updated records, and collect the ready Record object from NSX,
	// so that the objects in the store are consistent with NSX.
	realizeService := realizestate.InitializeRealizeState(s.Service)
	refreshed := make([]*model.ProjectDnsRecord, 0, len(toUpsert))
	for _, rec := range toUpsert {
		orgID, projectID, recordID, perr := parseProjectDNSRecordPolicyPath(*rec.Path)
		if perr != nil {
			return nil, perr
		}
		if err := realizeService.CheckRealizeState(util.NSXTRealizeRetry, *rec.Path, []string(nil)); err != nil {
			log.Error(err, "Failed to check ProjectDnsRecord realization state", "Id", recordID)
			if delErr := s.deleteProjectDnsRecordOnNSX(rec); delErr != nil {
				log.Error(delErr, "Failed to delete ProjectDnsRecord after realization check failure", "Id", recordID)
				return nil, fmt.Errorf("realization check failed: %w; deletion failed: %v", err, delErr)
			}
			return nil, err
		}
		live, gerr := s.NSXClient.DnsRecordsClient.Get(orgID, projectID, recordID)
		gerr = nsxutil.TransNSXApiError(gerr)
		if gerr != nil {
			return nil, gerr
		}
		lc := live
		refreshed = append(refreshed, &lc)
	}
	return append(append([]*model.ProjectDnsRecord(nil), refreshed...), removeOps...), nil
}

// validateEndpointRowConflict returns a row for ep in zone, or an error on FQDN conflict.
func (s *DNSRecordService) validateEndpointRowConflict(zonePath string, ep *extdns.Endpoint, recordName string, owner *ResourceRef) (*EndpointRow, error) {
	createdFor := resourceKindToCreatedFor(owner.Kind)
	if createdFor == "" {
		return nil, fmt.Errorf("unsupported resource kind %q for DNS record", owner.Kind)
	}
	fqdn := strings.ToLower(ep.DNSName)
	recTypeForIdx := strings.ToLower(strings.TrimSpace(ep.RecordType))
	idxKey := dnsRecordZonePathFQDNIndexKey(zonePath, fqdn, recTypeForIdx)
	recs := s.DNSRecordStore.GetByIndex(indexKeyDNSRecordZonePathFQDN, idxKey)
	currentNNKey := ownerNNIndexKeyForResourceRef(owner)
	for _, rec := range recs {
		if getDNSRecordOwnerNamespacedName(rec) == currentNNKey {
			return NewEndpointRow(ep, zonePath, recordName), nil
		}
		if *rec.RecordType != ep.RecordType {
			continue
		}
		extRecValues := sortedCopyStrings(rec.RecordValues)
		newRecValues := sortedCopyStrings(ep.Targets)
		if !slices.Equal(extRecValues, newRecValues) {
			err := fmt.Errorf("FQDN %s is configured with different values in DNS zone %s", fqdn, zonePath)
			log.Error(err, "FQDN targets conflict with existing record", "resource", getDNSRecordOwnerNamespacedName(rec))
			return nil, err
		}
		effectiveOwner, ok := resourceRefFromDNSRecord(rec)
		if !ok {
			err := fmt.Errorf("FQDN %s has an existing DNS record with incomplete owner metadata in DNS zone %s", fqdn, zonePath)
			log.Error(err, "cannot adopt shared DNS record")
			return nil, err
		}
		row := NewEndpointRow(ep, zonePath, recordName)
		primaryNN := primaryOwnerNNIndexKeyFromRecord(rec)
		row.effectiveOwner = effectiveOwner
		existing := firstTagValue(rec.Tags, common.TagScopeDNSRecordContributingOwners)
		row.contributingOwnerKeys = mergeContributingOwnerKeys(existing, currentNNKey, primaryNN)
		return row, nil
	}
	return NewEndpointRow(ep, zonePath, recordName), nil
}

// classifyOwnerRemoval handles the three-way decision for a record that references deletedOwnerKey
// (either as primary or contributing owner) and appends to toDelete or toUpdate accordingly:
//   - primary owner, no contributors → mark for delete
//   - primary owner, has contributors → promote first contributor, append promoted record to toUpdate
//   - contributing owner → drop key from contributing tag, append updated record to toUpdate
func (s *DNSRecordService) classifyOwnerRemoval(rec *model.ProjectDnsRecord, deletedOwnerKey string, toDelete, toUpdate *[]*model.ProjectDnsRecord) error {
	contribs := parseContributingOwnersFromRecord(rec) // already sorted
	if getDNSRecordOwnerNamespacedName(rec) == deletedOwnerKey {
		if len(contribs) == 0 {
			cp := *rec
			cp.MarkedForDelete = common.Bool(true)
			*toDelete = append(*toDelete, &cp)
			return nil
		}
		upd, err := s.recordAfterPrimaryDeletePromotion(rec, contribs)
		if err != nil {
			return err
		}
		*toUpdate = append(*toUpdate, upd)
		return nil
	}
	if upd, ok := recordAfterContributingRemoval(rec, deletedOwnerKey); ok {
		*toUpdate = append(*toUpdate, upd)
	}
	return nil
}

// applyDNSUpsertRows computes upserts and removals for a reconcile batch:
//   - desired rows: build the target record, compare with store, enqueue update if changed.
//   - stale rows: records once owned by batch.Owner that are no longer in the desired set
//     are either deleted (sole owner) or re-tagged (promote/remove-contributor).
func (s *DNSRecordService) applyDNSUpsertRows(batch *AggregatedDNSEndpoints) ([]*model.ProjectDnsRecord, []*model.ProjectDnsRecord, error) {
	if batch.Owner == nil {
		if len(batch.Rows) > 0 {
			return nil, nil, fmt.Errorf("aggregated DNS batch has rows but Owner is nil")
		}
		return nil, nil, nil
	}

	desiredRecs := make([]*model.ProjectDnsRecord, 0, len(batch.Rows))
	for _, row := range batch.Rows {
		if rec := s.BuildProjectDnsRecord(batch.Owner, row); rec != nil {
			desiredRecs = append(desiredRecs, rec)
		}
	}

	// Collect all records where batch.Owner is primary or contributing.
	owner := batch.Owner
	ownerNNKey, ownedRecs := s.collectRecordsByOwner(owner.Kind, owner.GetNamespace(), owner.GetName())

	// compareRecords: new/content-changed records go to toUpsert; stale go to staleRecs.
	toUpsert, staleRecs := compareRecords(desiredRecs, ownedRecs)

	var toRemove []*model.ProjectDnsRecord
	for _, rec := range staleRecs {
		if err := s.classifyOwnerRemoval(rec, ownerNNKey, &toRemove, &toUpsert); err != nil {
			return nil, nil, err
		}
	}
	return toUpsert, toRemove, nil
}

func (s *DNSRecordService) collectRecordsByOwner(ownerKind, ownerNamespace, ownerName string) (string, []*model.ProjectDnsRecord) {
	createdFor := resourceKindToCreatedFor(ownerKind)
	if createdFor == "" {
		return "", nil
	}
	ownerNNKey := dnsRecordOwnerKey(createdFor, dnsRecordOwnerNamespacedNameKey(ownerNamespace, ownerName))
	primRecs := s.DNSRecordStore.GetByOwnerResourceNamespacedName(ownerKind, ownerNamespace, ownerName)
	contribRecs := s.DNSRecordStore.ListRecordsReferencingContributingOwner(ownerNNKey)
	return ownerNNKey, dedupeRecordsByPath(slices.Concat(primRecs, contribRecs))
}

// DeleteRecordByOwnerNN deletes or retags rows for kind/ns/name. Returns (storeMutated, err).
func (s *DNSRecordService) DeleteRecordByOwnerNN(ctx context.Context, kind, namespace, name string) (bool, error) {
	deletedNNKey, all := s.collectRecordsByOwner(kind, namespace, name)
	if deletedNNKey == "" || len(all) == 0 {
		return false, nil
	}

	var toUpdate, toDelete []*model.ProjectDnsRecord
	for _, rec := range all {
		if err := s.classifyOwnerRemoval(rec, deletedNNKey, &toDelete, &toUpdate); err != nil {
			return false, err
		}
	}

	cacheChanged := len(toDelete) > 0 || len(toUpdate) > 0
	if !cacheChanged {
		return false, nil
	}
	toApply, err := s.syncProjectDnsRecordsInNSX(ctx, toUpdate, toDelete)
	if err != nil {
		return false, err
	}
	if err := s.DNSRecordStore.Apply(toApply); err != nil {
		return false, err
	}
	return true, nil
}

func gatewayIndexTagFromRecord(rec *model.ProjectDnsRecord) string {
	return firstTagValue(rec.Tags, common.TagScopeDNSRecordGatewayIndexList)
}

// recordAfterPrimaryDeletePromotion returns the updated record when the primary owner is removed but contributors remain.
func (s *DNSRecordService) recordAfterPrimaryDeletePromotion(rec *model.ProjectDnsRecord, sortedContribNNKeys []string) (*model.ProjectDnsRecord, error) {
	promotedNN := sortedContribNNKeys[0]
	remaining := append([]string(nil), sortedContribNNKeys[1:]...)
	createdFor, ns, name, ok := parseOwnerNNIndexKey(promotedNN)
	if !ok {
		return nil, fmt.Errorf("invalid contributing owner key %q", promotedNN)
	}
	kind := resourceKindFromCreatedForTag(createdFor)
	if kind == "" {
		return nil, fmt.Errorf("unknown created-for tag in contributing owner key %q", promotedNN)
	}
	newOwner := &ResourceRef{Kind: kind, Object: &metav1.ObjectMeta{Namespace: ns, Name: name}}
	out := *rec
	out.Tags = appendRecordOwnershipTags(s.tagsForOwner(newOwner), gatewayIndexTagFromRecord(rec), formatContributingOwnersTag(remaining))
	out.MarkedForDelete = nil
	return &out, nil
}

// recordAfterContributingRemoval removes deletedNNKey from the contributing tag; returns (updatedRecord, changed).
func recordAfterContributingRemoval(rec *model.ProjectDnsRecord, deletedNNKey string) (*model.ProjectDnsRecord, bool) {
	keys := parseContributingOwnersFromRecord(rec)
	if !slices.Contains(keys, deletedNNKey) {
		return nil, false
	}
	newContribKeys := slices.DeleteFunc(keys, func(k string) bool { return k == deletedNNKey })
	out := *rec
	out.Tags = replaceContributingOwnersInTags(rec.Tags, newContribKeys)
	out.MarkedForDelete = nil
	return &out, true
}

func dedupeRecordsByPath(recs []*model.ProjectDnsRecord) []*model.ProjectDnsRecord {
	seen := make(map[string]*model.ProjectDnsRecord)
	for _, r := range recs {
		if r == nil || r.Path == nil {
			continue
		}
		p := strings.TrimSpace(*r.Path)
		if p == "" {
			continue
		}
		seen[p] = r
	}
	out := make([]*model.ProjectDnsRecord, 0, len(seen))
	for _, r := range seen {
		out = append(out, r)
	}
	return out
}

// ListRecordOwnerResource returns owner namespaced names grouped by ResourceRef.Kind from the in-memory DNS record store.
func (s *DNSRecordService) ListRecordOwnerResource() map[string]sets.Set[types.NamespacedName] {
	return s.DNSRecordStore.GroupRecordsByResourceKind()
}

// ListReferredGatewayNN returns Gateway NNs referenced by store index.
func (s *DNSRecordService) ListReferredGatewayNN() sets.Set[types.NamespacedName] {
	gatewaySet := sets.New[types.NamespacedName]()
	for elem := range s.DNSRecordStore.ListIndexFuncValues(indexKeyDNSRecordGatewayNN) {
		gwConfig := strings.Split(elem, "/")
		if len(gwConfig) < 2 {
			continue
		}
		gwNamespace, gwName := gwConfig[0], gwConfig[1]
		gatewaySet.Insert(types.NamespacedName{Namespace: gwNamespace, Name: gwName})
	}
	return gatewaySet
}

// DeleteRecordsForOwnerOutsideAllowedZones deletes primary-owner DNS records whose zone_path is not in allowedZonePaths.
func (s *DNSRecordService) DeleteRecordsForOwnerOutsideAllowedZones(ctx context.Context, kind, namespace, name string, allowedZonePaths sets.Set[string]) (bool, error) {
	if allowedZonePaths == nil {
		allowedZonePaths = sets.New[string]()
	}
	// Only delete the ProjectDnsRecord which is owned by the kind/namespace/name; the contributed records are
	// deleted in the reconciliation by the record owner.
	owned := s.DNSRecordStore.GetByOwnerResourceNamespacedName(kind, namespace, name)
	var toDelete []*model.ProjectDnsRecord
	for _, rec := range owned {
		// rec.ZonePath is not nil which is guarded when creating the ProjectDnsRecord.
		zp := strings.TrimSpace(*rec.ZonePath)
		if allowedZonePaths.Has(zp) {
			continue
		}
		cp := *rec
		cp.MarkedForDelete = common.Bool(true)
		toDelete = append(toDelete, &cp)
	}
	if len(toDelete) == 0 {
		return false, nil
	}

	toApply, syncErr := s.syncProjectDnsRecordsInNSX(ctx, nil, toDelete)
	if syncErr != nil {
		return false, syncErr
	}
	return true, s.DNSRecordStore.Apply(toApply)
}

func getCluster(s *DNSRecordService) string {
	return s.NSXConfig.Cluster
}

// parseProjectDNSRecordPolicyPath splits a Policy ProjectDnsRecord path into org, project, and record id.
func parseProjectDNSRecordPolicyPath(path string) (orgID, projectID, recordID string, err error) {
	p := strings.TrimSpace(path)
	if p == "" {
		return "", "", "", fmt.Errorf("empty ProjectDnsRecord path")
	}
	matches := projectDNSRecordPathRe.FindStringSubmatch(p)
	if len(matches) != 4 {
		return "", "", "", fmt.Errorf("invalid ProjectDnsRecord path %q: expected /orgs/{org}/projects/{project}/dns-records/{record-id}", path)
	}
	orgID, projectID, recordID = matches[1], matches[2], matches[3]
	if strings.TrimSpace(recordID) == "" {
		return "", "", "", fmt.Errorf("empty record id in ProjectDnsRecord path %q", path)
	}
	return orgID, projectID, recordID, nil
}

func (s *DNSRecordService) deleteProjectDnsRecordOnNSX(live *model.ProjectDnsRecord) error {
	orgID, projectID, recordID, err := parseProjectDNSRecordPolicyPath(*live.Path)
	if err != nil {
		return err
	}
	err = s.NSXClient.DnsRecordsClient.Delete(orgID, projectID, recordID)
	return nsxutil.TransNSXApiError(err)
}

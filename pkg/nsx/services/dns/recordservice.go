/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package dns

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/vmware-tanzu/nsx-operator/pkg/logger"
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
)

var (
	log = logger.Log
)

// DNSRecordService handles validation and configuration of DNS records.
// DNSRecordStore holds NSX DNS record state; initialize with BuildDNSRecordStore() when constructing the service.
type DNSRecordService struct {
	common.Service
	DNSRecordStore *DNSRecordStore
	// GetPermittedZonesFunc, when non-nil, overrides getPermittedZones.
	// Used in tests to inject a fake zone resolver without monkey-patching.
	GetPermittedZonesFunc GetPermittedZonesFunc
}

// CreateOrUpdateDNSRecords validates the FQDNs in rec against the permitted DNS zones for
// rec.Owner's namespace, then creates or updates the corresponding DNS records in NSX and
// the local store.
//
// If any FQDNs are invalid (not matching a permitted zone), all existing DNS records for the
// owner are deleted and an error is returned so the caller can mark the resource as failed.
func (s *DNSRecordService) CreateOrUpdateDNSRecords(ctx context.Context, rec *Record) error {
	if rec == nil || len(rec.Hostnames) == 0 {
		return nil
	}

	zones, err := s.getPermittedZonesForRecord(ctx, rec)
	if err != nil {
		return err
	}

	fqdnsByZone, invalidFQDNs, dupFQDNs := s.classifyFQDNsByZone(rec.Hostnames, zones, rec.Owner)

	if len(invalidFQDNs) > 0 {
		if delErr := s.DeleteDNSRecordsByOwner(ctx, rec.Owner.Kind, string(rec.Owner.GetUID())); delErr != nil {
			return delErr
		}
		return fmt.Errorf("invalid FQDNs are detected: [%s]", strings.Join(invalidFQDNs, ","))
	}

	if len(dupFQDNs) > 0 {
		if delErr := s.DeleteDNSRecordsByOwner(ctx, rec.Owner.Kind, string(rec.Owner.GetUID())); delErr != nil {
			return delErr
		}
		return fmt.Errorf("FQDNs are claimed by other resources: [%s]", strings.Join(dupFQDNs, ","))
	}

	if len(fqdnsByZone) == 0 {
		return nil
	}

	rr := &recordRequest{
		fqdns:           fqdnsByZone,
		ips:             rec.Addresses,
		owner:           rec.Owner,
		addressProvider: rec.AddressProvider,
		forSVService:    rec.ForSVService,
	}
	return s.configureDNSRecords(ctx, rr)
}

func (s *DNSRecordService) DeleteDNSRecordsByOwner(ctx context.Context, kind string, uid string) error {
	dnsRecords := s.DNSRecordStore.GetByOwnerResourceUID(kind, uid)
	return s.deleteDNSRecords(ctx, dnsRecords)
}

func (s *DNSRecordService) DeleteAllDNSRecordsInGateway(ctx context.Context, gwNamespace, gwName string) error {
	dnsRecords := s.DNSRecordStore.GetByIndex(indexKeyDNSRecordNamespacedName, dnsRecordGatewayKey(gwNamespace, gwName))
	return s.deleteDNSRecords(ctx, dnsRecords)
}

func (s *DNSRecordService) DeleteOrphanedDNSRecordsInGateway(ctx context.Context, gwNamespace, gwName string, desiredOwners []*ResourceRef) error {
	dnsRecords := s.DNSRecordStore.GetByIndex(indexKeyDNSRecordNamespacedName, dnsRecordGatewayKey(gwNamespace, gwName))
	if len(dnsRecords) == 0 {
		return nil
	}
	desiredKeys := sets.New[string]()
	for _, o := range desiredOwners {
		createdFor := resourceKindToCreatedFor(o.Kind)
		if createdFor == "" {
			continue
		}
		desiredKeys.Insert(dnsRecordOwnerKey(createdFor, string(o.GetUID())))
	}
	var orphaned []*DNSRecord
	for _, rec := range dnsRecords {
		ownerKey := getDNSRecordOwnerKey(rec)
		if ownerKey == "" || !desiredKeys.Has(ownerKey) {
			orphaned = append(orphaned, rec)
		}
	}
	return s.deleteDNSRecords(ctx, orphaned)
}

func (s *DNSRecordService) ListGatewayNamespacedName() sets.Set[types.NamespacedName] {
	gatewaySet := sets.New[types.NamespacedName]()
	for elem := range s.DNSRecordStore.ListIndexFuncValues(indexKeyDNSRecordNamespacedName) {
		gwConfig := strings.Split(elem, "/")
		if len(gwConfig) < 2 {
			continue
		}
		gwNamespace, gwName := gwConfig[0], gwConfig[1]
		gatewaySet.Insert(types.NamespacedName{Namespace: gwNamespace, Name: gwName})
	}
	return gatewaySet
}

// configureDNSRecords diffs the expected records (built from rr) against the existing ones in
// the store, applies creates/updates for changed records, and deletes stale ones.
func (s *DNSRecordService) configureDNSRecords(ctx context.Context, rr *recordRequest) error {
	expected := buildDNSRecordResource(ctx, s, rr)

	ownerUID := ""
	if rr.owner != nil {
		ownerUID = string(rr.owner.GetUID())
	}
	existing := s.DNSRecordStore.GetByOwnerResourceUID(rr.owner.Kind, ownerUID)

	existingComparables := make([]common.Comparable, 0, len(existing))
	for _, rec := range existing {
		existingComparables = append(existingComparables, DNSRecordToComparable(rec))
	}
	expectedComparables := make([]common.Comparable, 0, len(expected))
	for _, rec := range expected {
		expectedComparables = append(expectedComparables, DNSRecordToComparable(rec))
	}

	changed, stale := common.CompareResources(existingComparables, expectedComparables)

	if len(changed) > 0 {
		changedRecords := make([]*DNSRecord, 0, len(changed))
		for _, c := range changed {
			changedRecords = append(changedRecords, ComparableToDNSRecord(c))
		}
		updatedRecords, err := s.createOrUpdateDNSRecordsInNSX(ctx, changedRecords)
		if err != nil {
			return err
		}
		if err := s.DNSRecordStore.Apply(updatedRecords); err != nil {
			return err
		}
	}

	if len(stale) > 0 {
		staleRecords := make([]*DNSRecord, 0, len(stale))
		for _, st := range stale {
			rec := ComparableToDNSRecord(st)
			cp := *rec
			cp.MarkedForDelete = common.Bool(true)
			staleRecords = append(staleRecords, &cp)
		}
		if err := s.deleteDNSRecordsInNSX(ctx, staleRecords); err != nil {
			return err
		}
		if err := s.DNSRecordStore.Apply(staleRecords); err != nil {
			return err
		}
	}

	return nil
}

func (s *DNSRecordService) deleteDNSRecords(ctx context.Context, records []*DNSRecord) error {
	if len(records) == 0 {
		return nil
	}

	// Copy each record before setting MarkedForDelete.  GetByIndex returns
	// direct pointers into cache.Indexer; mutating them in place races with
	// concurrent Reconcile goroutines or the GC goroutine that may hold the
	// same pointer.  The copies share the same Id/Tags (read-only after
	// creation), so the store's keyFunc and index functions work correctly on
	// the copies.
	toDelete := make([]*DNSRecord, len(records))
	for i, rec := range records {
		cp := *rec
		cp.MarkedForDelete = common.Bool(true)
		toDelete[i] = &cp
	}

	if err := s.deleteDNSRecordsInNSX(ctx, toDelete); err != nil {
		return err
	}

	s.DNSRecordStore.Apply(toDelete)

	return nil
}

// classifyFQDNsByZone partitions hostnames into three groups in a single pass:
//   - byZone:  map from zone path → FQDNs that match the zone and are not claimed by another owner
//   - invalid: FQDNs that do not match any permitted zone
//   - dup:     FQDNs that match a zone but are already claimed by a different owner within that zone
//
// For hostnames that match multiple zones, the longest (most-specific) domain wins.
func (s *DNSRecordService) classifyFQDNsByZone(hostnames []string, zones []ZoneConfig, owner *ResourceRef) (byZone map[string][]string, invalid, dup []string) {
	byZone = make(map[string][]string)

	ownerKey := ""
	if owner != nil {
		ownerKey = dnsRecordOwnerKey(resourceKindToCreatedFor(owner.Kind), string(owner.GetUID()))
	}

	for _, h := range hostnames {
		best := bestMatchingZone(h, zones)
		if best == nil {
			invalid = append(invalid, h)
			continue
		}
		if ownerKey != "" && s.fqdnClaimedByOtherOwnerInZone(best.Path, h, ownerKey) {
			dup = append(dup, h)
			continue
		}
		byZone[best.Path] = append(byZone[best.Path], h)
	}
	return
}

// bestMatchingZone returns the ZoneConfig whose Domain is the longest match for fqdn,
// or nil if no zone matches.
func bestMatchingZone(fqdn string, zones []ZoneConfig) *ZoneConfig {
	var best *ZoneConfig
	for i := range zones {
		z := &zones[i]
		if isDomainMatch(fqdn, z.Domain) && (best == nil || len(z.Domain) > len(best.Domain)) {
			best = z
		}
	}
	return best
}

// fqdnClaimedByOtherOwnerInZone reports whether any DNS record in the given zone already
// has fqdn and belongs to an owner other than currentOwnerKey.
func (s *DNSRecordService) fqdnClaimedByOtherOwnerInZone(zonePath, fqdn, currentOwnerKey string) bool {
	for _, rec := range s.DNSRecordStore.GetByIndex(indexKeyDNSRecordFQDN, fqdn) {
		if ptrStr(rec.DnsZonePath) != zonePath {
			continue
		}
		ownerKey := getDNSRecordOwnerKey(rec)
		if ownerKey != "" && ownerKey != currentOwnerKey {
			return true
		}
	}
	return false
}

// getPermittedZonesForRecord resolves the permitted DNS zones for rec.Owner's namespace.
func (s *DNSRecordService) getPermittedZonesForRecord(ctx context.Context, rec *Record) ([]ZoneConfig, error) {
	ns := ""
	if rec.Owner != nil {
		ns = rec.Owner.GetNamespace()
	}

	if s.GetPermittedZonesFunc != nil {
		return s.GetPermittedZonesFunc(ctx, ns)
	}
	return s.getPermittedZones(ctx, ns)
}

// getPermittedZones fetches the permitted DNS zones for the given namespace from NSX.
// TODO: implement this via NSX HAPI when the DNS zone resource API is available.
func (s *DNSRecordService) getPermittedZones(_ context.Context, _ string) ([]ZoneConfig, error) {
	return nil, nil
}

// createOrUpdateDNSRecordsInNSX applies DNS record create/update operations to NSX.
// The stub returns the input records unchanged; replace with real HAPI calls when available.
func (s *DNSRecordService) createOrUpdateDNSRecordsInNSX(_ context.Context, records []*DNSRecord) ([]*DNSRecord, error) {
	return records, nil
}

// deleteDNSRecordsInNSX deletes DNS records from NSX.
// The stub is a no-op; replace with real HAPI calls when available.
func (s *DNSRecordService) deleteDNSRecordsInNSX(_ context.Context, _ []*DNSRecord) error {
	return nil
}

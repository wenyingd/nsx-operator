/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package dns

import (
	"context"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
)

// DNSRecordService handles validation and configuration of DNS records.
// DNSRecordStore holds NSX DNS record state; initialize with BuildDNSRecordStore() when constructing the service.
type DNSRecordService struct {
	common.Service
	DNSRecordStore *DNSRecordStore
}

func (s *DNSRecordService) CreateOrUpdateDNSRecords(ctx context.Context, batch *OwnerEndpoints) error {
	if batch == nil {
		return nil
	}
	// TODO: validate FQDNs against permitted DNS zones.
	updatedRecords, err := s.createOrUpdateDNSRecordsInNSX(ctx, batch)
	if err != nil {
		return err
	}
	s.DNSRecordStore.Apply(updatedRecords)
	return nil
}

func (s *DNSRecordService) DeleteDNSRecordsByOwner(ctx context.Context, kind string, uid string) error {
	dnsRecords := s.DNSRecordStore.GetByOwnerResourceUID(kind, uid)
	return s.deleteDNSRecords(ctx, dnsRecords)
}

func (s *DNSRecordService) DeleteAllDNSRecordsInGateway(ctx context.Context, gwNamespace, gwName string) error {
	dnsRecords := s.DNSRecordStore.GetByIndex(indexKeyDNSRecordNamespacedName, dnsRecordGatewayKey(gwNamespace, gwName))
	return s.deleteDNSRecords(ctx, dnsRecords)
}

func (s *DNSRecordService) listOrphanedDNSRecordsInGateway(gwNamespace, gwName string, desiredOwners []*ResourceRef) []*DNSRecord {
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
	return orphaned
}

func (s *DNSRecordService) DeleteOrphanedDNSRecordsInGateway(ctx context.Context, gwNamespace, gwName string, desiredOwners []*ResourceRef) error {
	orphaned := s.listOrphanedDNSRecordsInGateway(gwNamespace, gwName, desiredOwners)
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

func (s *DNSRecordService) deleteDNSRecords(ctx context.Context, records []*DNSRecord) error {
	if len(records) == 0 {
		return nil
	}

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

// createOrUpdateDNSRecordsInNSX maps OwnerEndpoints (ExternalDNS-style) into store DNSRecords.
// NSX API calls remain in deleteDNSRecordsInNSX / future HAPI; this layer owns store reconciliation per owner.
func (s *DNSRecordService) createOrUpdateDNSRecordsInNSX(_ context.Context, batch *OwnerEndpoints) ([]*DNSRecord, error) {
	if batch == nil || batch.Owner == nil || batch.AddressProvider == nil || s.DNSRecordStore == nil {
		return nil, nil
	}
	createdFor := resourceKindToCreatedFor(batch.Owner.Kind)
	if createdFor == "" {
		return nil, nil
	}

	existing := s.DNSRecordStore.GetByOwnerResourceUID(batch.Owner.Kind, string(batch.Owner.GetUID()))
	desiredIDs := sets.New[string]()
	var out []*DNSRecord
	seen := sets.New[string]()

	for _, ep := range batch.Endpoints {
		if ep == nil {
			continue
		}
		id := stableDNSRecordID(batch.Owner, ep)
		if seen.Has(id) {
			continue
		}
		seen.Insert(id)
		desiredIDs.Insert(id)
		out = append(out, dnsRecordFromEndpoint(id, batch, ep))
	}

	for _, old := range existing {
		if old.Id == nil {
			continue
		}
		if desiredIDs.Has(*old.Id) {
			continue
		}
		cp := *old
		cp.MarkedForDelete = common.Bool(true)
		out = append(out, &cp)
	}

	return out, nil
}

// TODO: Implement this function to delete DNS record in NSX using HAPI
func (s *DNSRecordService) deleteDNSRecordsInNSX(ctx context.Context, records []*DNSRecord) error {
	return nil
}

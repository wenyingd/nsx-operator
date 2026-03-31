/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package dns

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// newSvc creates a DNSRecordService with a fresh store seeded with the given records.
func newSvc(recs ...*DNSRecord) *DNSRecordService {
	store := BuildDNSRecordStore()
	for _, r := range recs {
		_ = store.Apply([]*DNSRecord{r})
	}
	return &DNSRecordService{DNSRecordStore: store}
}

// newSvcWithZones creates a DNSRecordService that returns fixed zones from GetPermittedZonesFunc
// and seeds the store with the given records.
func newSvcWithZones(zones []ZoneConfig, recs ...*DNSRecord) *DNSRecordService {
	svc := newSvc(recs...)
	svc.GetPermittedZonesFunc = func(_ context.Context, _ string) ([]ZoneConfig, error) {
		return zones, nil
	}
	return svc
}

func Test_DeleteDNSRecordsByOwner(t *testing.T) {
	t.Run("empty store is no-op", func(t *testing.T) {
		assert.NoError(t, newSvc().DeleteDNSRecordsByOwner(context.Background(), ResourceKindGateway, "uid-1"))
	})
	t.Run("removes matching record", func(t *testing.T) {
		rec := makeRecordWithGatewayOwnerTags("rec-1", "gw-uid-1", "ns1", "gw1")
		svc := newSvc(rec)
		require.Len(t, svc.DNSRecordStore.GetByOwnerResourceUID(ResourceKindGateway, "gw-uid-1"), 1)
		assert.NoError(t, svc.DeleteDNSRecordsByOwner(context.Background(), ResourceKindGateway, "gw-uid-1"))
		assert.Empty(t, svc.DNSRecordStore.GetByOwnerResourceUID(ResourceKindGateway, "gw-uid-1"))
	})
}

func Test_DeleteAllDNSRecordsInGateway(t *testing.T) {
	t.Run("empty store is no-op", func(t *testing.T) {
		assert.NoError(t, newSvc().DeleteAllDNSRecordsInGateway(context.Background(), "ns1", "gw1"))
	})
	t.Run("removes all records for gateway", func(t *testing.T) {
		rec := makeRecordWithGatewayOwnerTags("rec-1", "gw-uid-1", "ns1", "gw1")
		svc := newSvc(rec)
		require.Len(t, svc.DNSRecordStore.GetByIndex(indexKeyDNSRecordNamespacedName, "ns1/gw1"), 1)
		assert.NoError(t, svc.DeleteAllDNSRecordsInGateway(context.Background(), "ns1", "gw1"))
		assert.Empty(t, svc.DNSRecordStore.GetByIndex(indexKeyDNSRecordNamespacedName, "ns1/gw1"))
	})
}

func Test_DeleteOrphanedDNSRecordsInGateway(t *testing.T) {
	tests := []struct {
		name       string
		storeRec   *DNSRecord
		owners     []*ResourceRef
		surviveKey string // if non-empty, record with this key must remain in store
		removeKey  string // if non-empty, record with this key must be gone
	}{
		{
			name:     "empty store is no-op",
			storeRec: nil,
		},
		{
			name:       "desired owner keeps record",
			storeRec:   makeRecordWithGatewayOwnerTags("rec-1", "gw-uid-1", "ns1", "gw1"),
			owners:     []*ResourceRef{{Kind: ResourceKindGateway, Object: &metav1.ObjectMeta{UID: "gw-uid-1"}}},
			surviveKey: "rec-1",
		},
		{
			name:      "unmatched owner removes orphaned record",
			storeRec:  makeRecordWithGatewayOwnerTags("rec-old", "old-uid", "ns1", "gw1"),
			owners:    []*ResourceRef{{Kind: ResourceKindGateway, Object: &metav1.ObjectMeta{UID: "new-uid"}}},
			removeKey: "rec-old",
		},
		{
			name:      "nil desired owners deletes all records",
			storeRec:  makeRecordWithGatewayOwnerTags("rec-1", "gw-uid-1", "ns1", "gw1"),
			owners:    nil,
			removeKey: "rec-1",
		},
		{
			name:      "unknown owner kind is treated as orphan",
			storeRec:  makeRecordWithGatewayOwnerTags("rec-1", "gw-uid-1", "ns1", "gw1"),
			owners:    []*ResourceRef{{Kind: "Unknown", Object: &metav1.ObjectMeta{UID: "gw-uid-1"}}},
			removeKey: "rec-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var svc *DNSRecordService
			if tt.storeRec != nil {
				svc = newSvc(tt.storeRec)
			} else {
				svc = newSvc()
			}
			err := svc.DeleteOrphanedDNSRecordsInGateway(context.Background(), "ns1", "gw1", tt.owners)
			assert.NoError(t, err)
			if tt.surviveKey != "" {
				assert.NotNil(t, svc.DNSRecordStore.GetByKey(tt.surviveKey), "record %s must survive", tt.surviveKey)
			}
			if tt.removeKey != "" {
				assert.Nil(t, svc.DNSRecordStore.GetByKey(tt.removeKey), "record %s must be removed", tt.removeKey)
			}
		})
	}
}

// Test_deleteDNSRecords_CopyOnWrite verifies that deleteDNSRecords does NOT mutate the original
// *DNSRecord pointers retrieved from the store, which is the race-condition fix.
func Test_deleteDNSRecords_CopyOnWrite(t *testing.T) {
	rec := makeRecordWithGatewayOwnerTags("rec-1", "gw-uid-1", "ns1", "gw1")
	svc := newSvc(rec)

	// Capture the live pointer stored in the index before deletion.
	before := svc.DNSRecordStore.GetByIndex(indexKeyDNSRecordNamespacedName, "ns1/gw1")
	require.Len(t, before, 1)
	original := before[0]
	require.Nil(t, original.MarkedForDelete, "precondition: MarkedForDelete must be nil")

	require.NoError(t, svc.deleteDNSRecords(context.Background(), before))

	// The original pointer and the slice element must NOT have been mutated.
	assert.Nil(t, original.MarkedForDelete, "original pointer must not be mutated (copy-on-write)")
	assert.Nil(t, before[0].MarkedForDelete, "retrieved slice element must not be mutated")
	// The record must have been removed from the store via the copy.
	assert.Nil(t, svc.DNSRecordStore.GetByKey("rec-1"), "record must be removed from store")
}

func Test_deleteDNSRecords_EmptyIsNoop(t *testing.T) {
	assert.NoError(t, newSvc().deleteDNSRecords(context.Background(), nil))
	assert.NoError(t, newSvc().deleteDNSRecords(context.Background(), []*DNSRecord{}))
}

func Test_CreateOrUpdateDNSRecords(t *testing.T) {
	ctx := context.Background()
	zones := []ZoneConfig{{Path: "/zone/test", Domain: "example.com"}}
	owner := makeOwnerRef(ResourceKindGateway, "ns1", "gw1", "gw-uid-1")

	tests := []struct {
		name       string
		rec        *Record
		seedRecs   []*DNSRecord
		wantErr    bool
		wantErrMsg string
		checkStore func(t *testing.T, svc *DNSRecordService)
	}{
		{
			name: "nil record is no-op",
			rec:  nil,
		},
		{
			name: "empty hostnames is no-op",
			rec:  &Record{Owner: owner},
		},
		{
			name: "invalid FQDN deletes existing records and returns error",
			rec: &Record{
				Owner:     owner,
				Hostnames: []string{"svc.notmydomain.net"},
				Addresses: []net.IP{net.ParseIP("1.2.3.4")},
			},
			seedRecs:   []*DNSRecord{makeRecordWithGatewayOwnerTags("old-rec", "gw-uid-1", "ns1", "gw1")},
			wantErr:    true,
			wantErrMsg: "invalid FQDNs",
			checkStore: func(t *testing.T, svc *DNSRecordService) {
				assert.Empty(t, svc.DNSRecordStore.GetByOwnerResourceUID(ResourceKindGateway, "gw-uid-1"))
			},
		},
		{
			name: "dup FQDN (claimed by other gateway) deletes existing records and returns error",
			rec: &Record{
				Owner:     owner,
				Hostnames: []string{"svc.example.com"},
				Addresses: []net.IP{net.ParseIP("1.2.3.4")},
			},
			seedRecs:   []*DNSRecord{makeDNSRecordInZone("other-rec", "svc.example.com", "/zone/test", "other-gw-uid", "ns1", "other-gw")},
			wantErr:    true,
			wantErrMsg: "claimed by other",
			checkStore: func(t *testing.T, svc *DNSRecordService) {
				assert.Empty(t, svc.DNSRecordStore.GetByOwnerResourceUID(ResourceKindGateway, "gw-uid-1"))
			},
		},
		{
			name: "valid FQDN with IPs creates record in store",
			rec: &Record{
				Owner:     owner,
				Hostnames: []string{"svc.example.com"},
				Addresses: []net.IP{net.ParseIP("1.2.3.4")},
			},
			checkStore: func(t *testing.T, svc *DNSRecordService) {
				recs := svc.DNSRecordStore.GetByOwnerResourceUID(ResourceKindGateway, "gw-uid-1")
				require.Len(t, recs, 1)
				assert.Equal(t, "svc.example.com", ptrStr(recs[0].Fqdn))
			},
		},
		{
			name: "valid FQDN with no IPs creates no records",
			rec: &Record{
				Owner:     owner,
				Hostnames: []string{"svc.example.com"},
				Addresses: nil,
			},
			checkStore: func(t *testing.T, svc *DNSRecordService) {
				assert.Empty(t, svc.DNSRecordStore.GetByOwnerResourceUID(ResourceKindGateway, "gw-uid-1"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newSvcWithZones(zones, tt.seedRecs...)
			err := svc.CreateOrUpdateDNSRecords(ctx, tt.rec)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrMsg)
			} else {
				require.NoError(t, err)
			}
			if tt.checkStore != nil {
				tt.checkStore(t, svc)
			}
		})
	}
}

func Test_classifyFQDNsByZone(t *testing.T) {
	zones := []ZoneConfig{
		{Path: "/zone/base", Domain: "example.com"},
		{Path: "/zone/sub", Domain: "sub.example.com"},
	}
	owner := makeOwnerRef(ResourceKindGateway, "ns1", "gw1", "gw-uid-1")
	otherUID := "gw-uid-2"

	t.Run("no zones sends all hostnames to invalid", func(t *testing.T) {
		svc := newSvc()
		byZone, invalid, dup := svc.classifyFQDNsByZone([]string{"svc.example.com"}, nil, owner)
		assert.Empty(t, byZone)
		assert.Equal(t, []string{"svc.example.com"}, invalid)
		assert.Empty(t, dup)
	})

	t.Run("hostname matches zone goes to byZone", func(t *testing.T) {
		svc := newSvc()
		byZone, invalid, dup := svc.classifyFQDNsByZone([]string{"svc.example.com"}, zones, owner)
		assert.Contains(t, byZone["/zone/base"], "svc.example.com")
		assert.Empty(t, invalid)
		assert.Empty(t, dup)
	})

	t.Run("hostname does not match any zone goes to invalid", func(t *testing.T) {
		svc := newSvc()
		byZone, invalid, dup := svc.classifyFQDNsByZone([]string{"svc.unknown.net"}, zones, owner)
		assert.Empty(t, byZone)
		assert.Equal(t, []string{"svc.unknown.net"}, invalid)
		assert.Empty(t, dup)
	})

	t.Run("longest zone match wins for sub-domain", func(t *testing.T) {
		svc := newSvc()
		byZone, _, _ := svc.classifyFQDNsByZone([]string{"api.sub.example.com"}, zones, owner)
		assert.Contains(t, byZone["/zone/sub"], "api.sub.example.com")
		assert.Empty(t, byZone["/zone/base"])
	})

	t.Run("fqdn claimed by other owner in same zone goes to dup", func(t *testing.T) {
		existing := makeDNSRecordInZone("other-rec", "svc.example.com", "/zone/base", otherUID, "ns1", "gw2")
		svc := newSvc(existing)
		byZone, invalid, dup := svc.classifyFQDNsByZone([]string{"svc.example.com"}, zones, owner)
		assert.Empty(t, byZone)
		assert.Empty(t, invalid)
		assert.Equal(t, []string{"svc.example.com"}, dup)
	})

	t.Run("fqdn claimed by same owner stays in byZone", func(t *testing.T) {
		existing := makeDNSRecordInZone("my-rec", "svc.example.com", "/zone/base", string(owner.GetUID()), "ns1", "gw1")
		svc := newSvc(existing)
		byZone, invalid, dup := svc.classifyFQDNsByZone([]string{"svc.example.com"}, zones, owner)
		assert.Contains(t, byZone["/zone/base"], "svc.example.com")
		assert.Empty(t, invalid)
		assert.Empty(t, dup)
	})

	t.Run("fqdn claimed by other in a different zone is not a dup", func(t *testing.T) {
		// Record is in /zone/sub but hostname matches /zone/base — no conflict in /zone/base.
		existing := makeDNSRecordInZone("other-zone-rec", "svc.example.com", "/zone/sub", otherUID, "ns1", "gw2")
		svc := newSvc(existing)
		byZone, invalid, dup := svc.classifyFQDNsByZone([]string{"svc.example.com"}, zones, owner)
		assert.Contains(t, byZone["/zone/base"], "svc.example.com")
		assert.Empty(t, invalid)
		assert.Empty(t, dup)
	})

	t.Run("nil owner skips dup check", func(t *testing.T) {
		existing := makeDNSRecordInZone("other-rec", "svc.example.com", "/zone/base", otherUID, "ns1", "gw2")
		svc := newSvc(existing)
		byZone, invalid, dup := svc.classifyFQDNsByZone([]string{"svc.example.com"}, zones, nil)
		assert.Contains(t, byZone["/zone/base"], "svc.example.com")
		assert.Empty(t, invalid)
		assert.Empty(t, dup)
	})
}

func Test_bestMatchingZone(t *testing.T) {
	zones := []ZoneConfig{
		{Path: "/zone/base", Domain: "example.com"},
		{Path: "/zone/sub", Domain: "sub.example.com"},
	}

	t.Run("nil zones returns nil", func(t *testing.T) {
		assert.Nil(t, bestMatchingZone("svc.example.com", nil))
	})
	t.Run("no matching zone returns nil", func(t *testing.T) {
		assert.Nil(t, bestMatchingZone("svc.unknown.net", zones))
	})
	t.Run("single matching zone is returned", func(t *testing.T) {
		got := bestMatchingZone("api.other.example.com", zones)
		require.NotNil(t, got)
		assert.Equal(t, "/zone/base", got.Path)
	})
	t.Run("longest domain match wins", func(t *testing.T) {
		got := bestMatchingZone("api.sub.example.com", zones)
		require.NotNil(t, got)
		assert.Equal(t, "/zone/sub", got.Path)
	})
}

func Test_fqdnClaimedByOtherOwnerInZone(t *testing.T) {
	const (
		zonePath = "/zone/test"
		fqdn     = "svc.example.com"
	)
	myOwnerKey := dnsRecordOwnerKey(resourceKindToCreatedFor(ResourceKindGateway), "gw-uid-1")

	t.Run("empty store returns false", func(t *testing.T) {
		assert.False(t, newSvc().fqdnClaimedByOtherOwnerInZone(zonePath, fqdn, myOwnerKey))
	})
	t.Run("same owner returns false", func(t *testing.T) {
		rec := makeDNSRecordInZone("my-rec", fqdn, zonePath, "gw-uid-1", "ns1", "gw1")
		assert.False(t, newSvc(rec).fqdnClaimedByOtherOwnerInZone(zonePath, fqdn, myOwnerKey))
	})
	t.Run("different owner in same zone returns true", func(t *testing.T) {
		rec := makeDNSRecordInZone("other-rec", fqdn, zonePath, "gw-uid-2", "ns1", "gw2")
		assert.True(t, newSvc(rec).fqdnClaimedByOtherOwnerInZone(zonePath, fqdn, myOwnerKey))
	})
	t.Run("different owner in different zone returns false", func(t *testing.T) {
		rec := makeDNSRecordInZone("other-rec", fqdn, "/zone/other", "gw-uid-2", "ns1", "gw2")
		assert.False(t, newSvc(rec).fqdnClaimedByOtherOwnerInZone(zonePath, fqdn, myOwnerKey))
	})
	t.Run("record with no owner tags (empty owner key) returns false", func(t *testing.T) {
		rec := &DNSRecord{Id: strPtr("no-owner"), Fqdn: strPtr(fqdn), DnsZonePath: strPtr(zonePath)}
		assert.False(t, newSvc(rec).fqdnClaimedByOtherOwnerInZone(zonePath, fqdn, myOwnerKey))
	})
}

func Test_getPermittedZonesForRecord(t *testing.T) {
	ctx := context.Background()
	owner := makeOwnerRef(ResourceKindGateway, "ns1", "gw1", "gw-uid-1")
	rec := &Record{Owner: owner}

	t.Run("uses injected GetPermittedZonesFunc with correct namespace", func(t *testing.T) {
		want := []ZoneConfig{{Path: "/zone/test", Domain: "example.com"}}
		var capturedNS string
		svc := newSvc()
		svc.GetPermittedZonesFunc = func(_ context.Context, ns string) ([]ZoneConfig, error) {
			capturedNS = ns
			return want, nil
		}
		got, err := svc.getPermittedZonesForRecord(ctx, rec)
		require.NoError(t, err)
		assert.Equal(t, want, got)
		assert.Equal(t, "ns1", capturedNS)
	})

	t.Run("falls back to getPermittedZones when func is nil", func(t *testing.T) {
		svc := newSvc()
		got, err := svc.getPermittedZonesForRecord(ctx, rec)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("nil owner passes empty namespace to func", func(t *testing.T) {
		var capturedNS = "not-empty"
		svc := newSvc()
		svc.GetPermittedZonesFunc = func(_ context.Context, ns string) ([]ZoneConfig, error) {
			capturedNS = ns
			return nil, nil
		}
		_, _ = svc.getPermittedZonesForRecord(ctx, &Record{Owner: nil})
		assert.Equal(t, "", capturedNS)
	})
}

func Test_configureDNSRecords(t *testing.T) {
	ctx := context.Background()
	owner := makeOwnerRef(ResourceKindGateway, "ns1", "gw1", "gw-uid-1")

	makeRR := func(fqdns map[string][]string, ips []net.IP) *recordRequest {
		return &recordRequest{fqdns: fqdns, ips: ips, owner: owner}
	}
	storeFor := func(svc *DNSRecordService) []*DNSRecord {
		return svc.DNSRecordStore.GetByOwnerResourceUID(ResourceKindGateway, "gw-uid-1")
	}

	t.Run("no expected and empty store creates nothing", func(t *testing.T) {
		svc := newSvc()
		require.NoError(t, svc.configureDNSRecords(ctx, makeRR(map[string][]string{}, nil)))
		assert.Empty(t, storeFor(svc))
	})

	t.Run("new record is created in store", func(t *testing.T) {
		svc := newSvc()
		rr := makeRR(map[string][]string{"/zone/test": {"svc.example.com"}}, []net.IP{net.ParseIP("1.2.3.4")})
		require.NoError(t, svc.configureDNSRecords(ctx, rr))
		recs := storeFor(svc)
		require.Len(t, recs, 1)
		assert.Equal(t, "svc.example.com", ptrStr(recs[0].Fqdn))
	})

	t.Run("unchanged record is idempotent", func(t *testing.T) {
		svc := newSvc()
		rr := makeRR(map[string][]string{"/zone/test": {"svc.example.com"}}, []net.IP{net.ParseIP("1.2.3.4")})
		require.NoError(t, svc.configureDNSRecords(ctx, rr))
		require.NoError(t, svc.configureDNSRecords(ctx, rr))
		assert.Len(t, storeFor(svc), 1)
	})

	t.Run("stale record is deleted from store", func(t *testing.T) {
		svc := newSvc()
		rr := makeRR(map[string][]string{"/zone/test": {"svc.example.com"}}, []net.IP{net.ParseIP("1.2.3.4")})
		require.NoError(t, svc.configureDNSRecords(ctx, rr))
		require.Len(t, storeFor(svc), 1)

		// Reconcile with empty FQDNs: existing record becomes stale.
		require.NoError(t, svc.configureDNSRecords(ctx, makeRR(map[string][]string{}, nil)))
		assert.Empty(t, storeFor(svc))
	})

	t.Run("IP update replaces old record with new one", func(t *testing.T) {
		svc := newSvc()
		rr1 := makeRR(map[string][]string{"/zone/test": {"svc.example.com"}}, []net.IP{net.ParseIP("1.2.3.4")})
		require.NoError(t, svc.configureDNSRecords(ctx, rr1))

		rr2 := makeRR(map[string][]string{"/zone/test": {"svc.example.com"}}, []net.IP{net.ParseIP("5.6.7.8")})
		require.NoError(t, svc.configureDNSRecords(ctx, rr2))

		recs := storeFor(svc)
		require.Len(t, recs, 1)
		assert.Equal(t, "5.6.7.8", ptrStr(recs[0].IpAddress))
	})
}

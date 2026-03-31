/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package dns

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt-mp/nsx/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
)

func strPtr(s string) *string { return &s }

// makeOwnerRef constructs a ResourceRef for the given kind, namespace, name and UID.
func makeOwnerRef(kind, namespace, name, uid string) *ResourceRef {
	return &ResourceRef{
		Kind:   kind,
		Object: &metav1.ObjectMeta{Namespace: namespace, Name: name, UID: types.UID(uid)},
	}
}

// makeDNSRecordInZone creates a Gateway-owned DNSRecord with Fqdn and DnsZonePath set.
// Used to seed the store for duplicate-FQDN detection tests.
func makeDNSRecordInZone(id, fqdn, zonePath, ownerUID, ownerNS, ownerName string) *DNSRecord {
	return &DNSRecord{
		Id:          strPtr(id),
		Fqdn:        strPtr(fqdn),
		DnsZonePath: strPtr(zonePath),
		Tags: []model.Tag{
			{Scope: strPtr(common.TagScopeDNSRecordFor), Tag: strPtr(common.TagValueDNSRecordForGateway)},
			{Scope: strPtr(common.TagScopeGatewayUID), Tag: strPtr(ownerUID)},
			{Scope: strPtr(common.TagScopeGatewayNamespace), Tag: strPtr(ownerNS)},
			{Scope: strPtr(common.TagScopeGatewayName), Tag: strPtr(ownerName)},
		},
	}
}

// makeRecordWithOwnerTags creates a DNSRecord owned by a ListenerSet, also indexed to default/gw1.
func makeRecordWithOwnerTags(id, ns, name, uid string) *DNSRecord {
	return &DNSRecord{
		Id: strPtr(id),
		Tags: []model.Tag{
			{Scope: strPtr(common.TagScopeDNSRecordFor), Tag: strPtr(common.TagValueDNSRecordForListenerSet)},
			{Scope: strPtr(common.TagScopeNamespace), Tag: strPtr(ns)},
			{Scope: strPtr(common.TagScopeListenerSetName), Tag: strPtr(name)},
			{Scope: strPtr(common.TagScopeListenerSetUID), Tag: strPtr(uid)},
			{Scope: strPtr(common.TagScopeGatewayName), Tag: strPtr("gw1")},
			{Scope: strPtr(common.TagScopeGatewayNamespace), Tag: strPtr("default")},
		},
	}
}

// makeRecordWithGatewayOwnerTags creates a DNSRecord owned by a Gateway.
func makeRecordWithGatewayOwnerTags(id, gwUID, gwNs, gwName string) *DNSRecord {
	return &DNSRecord{
		Id: strPtr(id),
		Tags: []model.Tag{
			{Scope: strPtr(common.TagScopeDNSRecordFor), Tag: strPtr(common.TagValueDNSRecordForGateway)},
			{Scope: strPtr(common.TagScopeGatewayUID), Tag: strPtr(gwUID)},
			{Scope: strPtr(common.TagScopeGatewayNamespace), Tag: strPtr(gwNs)},
			{Scope: strPtr(common.TagScopeGatewayName), Tag: strPtr(gwName)},
		},
	}
}

func Test_dnsRecordKeyFunc(t *testing.T) {
	t.Run("valid record returns id", func(t *testing.T) {
		k, err := dnsRecordKeyFunc(&DNSRecord{Id: strPtr("id-1")})
		require.NoError(t, err)
		assert.Equal(t, "id-1", k)
	})
	t.Run("nil Id returns error", func(t *testing.T) {
		_, err := dnsRecordKeyFunc(&DNSRecord{})
		assert.Error(t, err)
	})
	t.Run("unknown type returns error", func(t *testing.T) {
		_, err := dnsRecordKeyFunc("not-a-record")
		assert.Error(t, err)
	})
}

func Test_Apply_AddThenDelete(t *testing.T) {
	store := BuildDNSRecordStore()
	rec := makeRecordWithGatewayOwnerTags("rec-1", "gw-uid-1", "ns1", "gw1")

	require.NoError(t, store.Apply([]*DNSRecord{rec}))
	assert.NotNil(t, store.GetByKey("rec-1"), "record must be present after Add")

	cp := *rec
	cp.MarkedForDelete = common.Bool(true)
	require.NoError(t, store.Apply([]*DNSRecord{&cp}))
	assert.Nil(t, store.GetByKey("rec-1"), "record must be absent after Delete")
}

func Test_GetByKey(t *testing.T) {
	store := BuildDNSRecordStore()
	assert.Nil(t, store.GetByKey("missing"))

	rec := makeRecordWithGatewayOwnerTags("rec-1", "uid-1", "ns1", "gw1")
	require.NoError(t, store.Apply([]*DNSRecord{rec}))
	got := store.GetByKey("rec-1")
	require.NotNil(t, got)
	assert.Equal(t, "rec-1", *got.Id)
}

func Test_GetByOwnerResourceUID(t *testing.T) {
	tests := []struct {
		name      string
		rec       *DNSRecord
		queryKind string
		queryUID  string
		wantID    string
		wantEmpty bool
	}{
		{
			name:      "ListenerSet owner",
			rec:       makeRecordWithOwnerTags("ls-rec", "ns1", "ls1", "uid-ls1"),
			queryKind: ResourceKindListenerSet,
			queryUID:  "uid-ls1",
			wantID:    "ls-rec",
		},
		{
			name:      "Gateway owner",
			rec:       makeRecordWithGatewayOwnerTags("gw-rec", "gw-uid-1", "ns1", "gw1"),
			queryKind: ResourceKindGateway,
			queryUID:  "gw-uid-1",
			wantID:    "gw-rec",
		},
		{
			name:      "unknown kind returns empty",
			rec:       makeRecordWithOwnerTags("rec-1", "ns1", "ls1", "uid-ls1"),
			queryKind: "UnknownKind",
			queryUID:  "uid-ls1",
			wantEmpty: true,
		},
		{
			name:      "mismatched UID returns empty",
			rec:       makeRecordWithGatewayOwnerTags("gw-rec", "gw-uid-1", "ns1", "gw1"),
			queryKind: ResourceKindGateway,
			queryUID:  "other-uid",
			wantEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := BuildDNSRecordStore()
			require.NoError(t, store.Apply([]*DNSRecord{tt.rec}))
			got := store.GetByOwnerResourceUID(tt.queryKind, tt.queryUID)
			if tt.wantEmpty {
				assert.Empty(t, got)
			} else {
				require.Len(t, got, 1)
				assert.Equal(t, tt.wantID, *got[0].Id)
			}
		})
	}
}

func Test_resourceKindToCreatedFor(t *testing.T) {
	tests := []struct {
		kind string
		want string
	}{
		{ResourceKindGateway, common.TagValueDNSRecordForGateway},
		{ResourceKindListenerSet, common.TagValueDNSRecordForListenerSet},
		{ResourceKindService, common.TagValueDNSRecordForService},
		{"Unknown", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			assert.Equal(t, tt.want, resourceKindToCreatedFor(tt.kind))
		})
	}
}

func Test_IndexByGatewayNamespacedName_GatewayOwner(t *testing.T) {
	store := BuildDNSRecordStore()
	rec := makeRecordWithGatewayOwnerTags("gw-rec", "gw-uid-1", "ns1", "gw1")
	require.NoError(t, store.Apply([]*DNSRecord{rec}))

	got := store.GetByIndex(indexKeyDNSRecordNamespacedName, "ns1/gw1")
	require.Len(t, got, 1)
	assert.Equal(t, "gw-rec", *got[0].Id)
}

func Test_ListGatewayNamespacedName(t *testing.T) {
	store := BuildDNSRecordStore()
	// makeRecordWithOwnerTags sets GatewayNamespace="default", GatewayName="gw1"
	rec := makeRecordWithOwnerTags("r1", "default", "ls1", "uid1")
	require.NoError(t, store.Apply([]*DNSRecord{rec}))

	svc := &DNSRecordService{DNSRecordStore: store}
	got := svc.ListGatewayNamespacedName()
	assert.True(t, got.Has(types.NamespacedName{Namespace: "default", Name: "gw1"}))
}

func Test_indexDNSRecordByOwnerUID_ErrorPaths(t *testing.T) {
	t.Run("unknown type returns error", func(t *testing.T) {
		_, err := indexDNSRecordByOwnerUID("not-a-record")
		assert.Error(t, err)
	})
	t.Run("record with no owner tags returns empty keys", func(t *testing.T) {
		rec := &DNSRecord{Id: strPtr("rec-1")}
		keys, err := indexDNSRecordByOwnerUID(rec)
		assert.NoError(t, err)
		assert.Empty(t, keys)
	})
}

func Test_indexDNSRecordByNamespacedName_ErrorPaths(t *testing.T) {
	t.Run("unknown type returns error", func(t *testing.T) {
		_, err := indexDNSRecordByNamespacedName("not-a-record")
		assert.Error(t, err)
	})
	t.Run("record with no tags returns empty keys", func(t *testing.T) {
		rec := &DNSRecord{Id: strPtr("rec-1")}
		keys, err := indexDNSRecordByNamespacedName(rec)
		assert.NoError(t, err)
		assert.Empty(t, keys)
	})
	t.Run("ListenerSet owner missing gateway name tags returns empty", func(t *testing.T) {
		rec := &DNSRecord{
			Id: strPtr("rec-1"),
			Tags: []model.Tag{
				{Scope: strPtr(common.TagScopeDNSRecordFor), Tag: strPtr(common.TagValueDNSRecordForListenerSet)},
				// gateway name/namespace tags intentionally omitted
			},
		}
		keys, err := indexDNSRecordByNamespacedName(rec)
		assert.NoError(t, err)
		assert.Empty(t, keys)
	})
	t.Run("Service owner returns empty keys (not indexed by gateway)", func(t *testing.T) {
		rec := &DNSRecord{
			Id: strPtr("rec-1"),
			Tags: []model.Tag{
				{Scope: strPtr(common.TagScopeDNSRecordFor), Tag: strPtr(common.TagValueDNSRecordForService)},
				{Scope: strPtr(common.TagScopeServiceUID), Tag: strPtr("svc-uid-1")},
			},
		}
		keys, err := indexDNSRecordByNamespacedName(rec)
		assert.NoError(t, err)
		assert.Empty(t, keys)
	})
}

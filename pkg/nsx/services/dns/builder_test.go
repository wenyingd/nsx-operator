/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package dns

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	nsxmodel "github.com/vmware/vsphere-automation-sdk-go/services/nsxt-mp/nsx/model"

	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
)

// tagsToMap converts an NSX tag slice into a scope→value map for easy assertions.
func tagsToMap(tags []nsxmodel.Tag) map[string]string {
	m := make(map[string]string, len(tags))
	for _, tag := range tags {
		if tag.Scope != nil && tag.Tag != nil {
			m[*tag.Scope] = *tag.Tag
		}
	}
	return m
}

func Test_buildDNSRecordID(t *testing.T) {
	id1 := buildDNSRecordID("cluster1", "uid-1", "/zone/test", "svc.example.com", "1.2.3.4")
	id2 := buildDNSRecordID("cluster1", "uid-1", "/zone/test", "svc.example.com", "1.2.3.4")
	assert.Equal(t, id1, id2, "same inputs produce identical ID")
	assert.NotEmpty(t, id1)

	id3 := buildDNSRecordID("cluster1", "uid-2", "/zone/test", "svc.example.com", "1.2.3.4")
	assert.NotEqual(t, id1, id3, "different ownerUID yields different ID")
}

var buildTagsCases = []struct {
	name        string
	owner       *ResourceRef
	addrProv    *ResourceRef
	nsUID       string
	forSV       bool
	wantFor     string
	extraChecks func(t *testing.T, m map[string]string)
}{
	{
		name:    "nil owner produces only cluster tag",
		owner:   nil,
		wantFor: "",
		extraChecks: func(t *testing.T, m map[string]string) {
			_, has := m[common.TagScopeDNSRecordFor]
			assert.False(t, has, "no DNSRecordFor tag when owner is nil")
		},
	},
	{
		name:    "Gateway owner",
		owner:   makeOwnerRef(ResourceKindGateway, "ns1", "gw1", "gw-uid-1"),
		wantFor: common.TagValueDNSRecordForGateway,
		extraChecks: func(t *testing.T, m map[string]string) {
			assert.Equal(t, "gw-uid-1", m[common.TagScopeGatewayUID])
			assert.Equal(t, "gw1", m[common.TagScopeGatewayName])
			assert.Equal(t, "ns1", m[common.TagScopeGatewayNamespace])
		},
	},
	{
		name:     "ListenerSet owner with address provider",
		owner:    makeOwnerRef(ResourceKindListenerSet, "ns1", "ls1", "ls-uid-1"),
		addrProv: makeOwnerRef(ResourceKindGateway, "ns1", "gw1", "gw-uid-1"),
		wantFor:  common.TagValueDNSRecordForListenerSet,
		extraChecks: func(t *testing.T, m map[string]string) {
			assert.Equal(t, "ls-uid-1", m[common.TagScopeListenerSetUID])
			assert.Equal(t, "ls1", m[common.TagScopeListenerSetName])
			assert.Equal(t, "gw-uid-1", m[common.TagScopeGatewayUID])
			assert.Equal(t, "gw1", m[common.TagScopeGatewayName])
		},
	},
	{
		name:    "ListenerSet owner without address provider uses empty strings",
		owner:   makeOwnerRef(ResourceKindListenerSet, "ns1", "ls1", "ls-uid-1"),
		wantFor: common.TagValueDNSRecordForListenerSet,
		extraChecks: func(t *testing.T, m map[string]string) {
			assert.Equal(t, "", m[common.TagScopeGatewayUID])
		},
	},
	{
		name:    "Service owner",
		owner:   makeOwnerRef(ResourceKindService, "ns1", "svc1", "svc-uid-1"),
		wantFor: common.TagValueDNSRecordForService,
		extraChecks: func(t *testing.T, m map[string]string) {
			assert.Equal(t, "svc-uid-1", m[common.TagScopeServiceUID])
			assert.Equal(t, "svc1", m[common.TagScopeServiceName])
		},
	},
	{
		name:    "namespace UID tag is added when provided",
		owner:   makeOwnerRef(ResourceKindGateway, "ns1", "gw1", "gw-uid-1"),
		nsUID:   "ns-uid-1",
		wantFor: common.TagValueDNSRecordForGateway,
		extraChecks: func(t *testing.T, m map[string]string) {
			assert.Equal(t, "ns-uid-1", m[common.TagScopeNamespaceUID])
		},
	},
	{
		name:    "forSVService flag adds supervisor-service tag",
		owner:   makeOwnerRef(ResourceKindGateway, "ns1", "gw1", "gw-uid-1"),
		forSV:   true,
		wantFor: common.TagValueDNSRecordForGateway,
		extraChecks: func(t *testing.T, m map[string]string) {
			assert.Equal(t, "true", m[common.TagScopeForSupervisorService])
		},
	},
}

func Test_buildTags(t *testing.T) {
	for _, tt := range buildTagsCases {
		t.Run(tt.name, func(t *testing.T) {
			rr := &recordRequest{owner: tt.owner, addressProvider: tt.addrProv, forSVService: tt.forSV}
			tags := buildTags("cluster1", tt.nsUID, rr)
			m := tagsToMap(tags)
			assert.Equal(t, "cluster1", m[common.TagScopeCluster])
			if tt.wantFor != "" {
				assert.Equal(t, tt.wantFor, m[common.TagScopeDNSRecordFor])
			}
			if tt.extraChecks != nil {
				tt.extraChecks(t, m)
			}
		})
	}
}

func Test_buildDNSRecordResource_EmptyFQDNs(t *testing.T) {
	svc := newSvc()
	rr := &recordRequest{fqdns: map[string][]string{}, ips: []net.IP{net.ParseIP("1.2.3.4")}}
	assert.Nil(t, buildDNSRecordResource(context.Background(), svc, rr))
}

func Test_buildDNSRecordResource_EmptyIPs(t *testing.T) {
	svc := newSvc()
	rr := &recordRequest{fqdns: map[string][]string{"/zone": {"svc.example.com"}}}
	assert.Nil(t, buildDNSRecordResource(context.Background(), svc, rr))
}

func Test_buildDNSRecordResource_IPv4(t *testing.T) {
	owner := makeOwnerRef(ResourceKindGateway, "ns1", "gw1", "gw-uid-1")
	svc := newSvc()
	rr := &recordRequest{
		fqdns: map[string][]string{"/zone/test": {"svc.example.com"}},
		ips:   []net.IP{net.ParseIP("1.2.3.4")},
		owner: owner,
	}
	records := buildDNSRecordResource(context.Background(), svc, rr)
	require.Len(t, records, 1)
	rec := records[0]
	assert.Equal(t, "svc.example.com", ptrStr(rec.Fqdn))
	assert.Equal(t, "1.2.3.4", ptrStr(rec.IpAddress))
	assert.Equal(t, "A", ptrStr(rec.RecordType))
	assert.Equal(t, "/zone/test", ptrStr(rec.DnsZonePath))
	assert.NotEmpty(t, ptrStr(rec.Id))
}

func Test_buildDNSRecordResource_IPv6(t *testing.T) {
	owner := makeOwnerRef(ResourceKindGateway, "ns1", "gw1", "gw-uid-1")
	svc := newSvc()
	rr := &recordRequest{
		fqdns: map[string][]string{"/zone/test": {"svc.example.com"}},
		ips:   []net.IP{net.ParseIP("2001:db8::1")},
		owner: owner,
	}
	records := buildDNSRecordResource(context.Background(), svc, rr)
	require.Len(t, records, 1)
	assert.Equal(t, "AAAA", ptrStr(records[0].RecordType))
}

func Test_buildDNSRecordResource_MultipleIPsAndFQDNs(t *testing.T) {
	owner := makeOwnerRef(ResourceKindGateway, "ns1", "gw1", "gw-uid-1")
	svc := newSvc()
	rr := &recordRequest{
		fqdns: map[string][]string{"/zone/test": {"a.example.com", "b.example.com"}},
		ips:   []net.IP{net.ParseIP("1.2.3.4"), net.ParseIP("5.6.7.8")},
		owner: owner,
	}
	// 2 FQDNs × 2 IPs = 4 records
	records := buildDNSRecordResource(context.Background(), svc, rr)
	assert.Len(t, records, 4)
}

/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package dns

import (
	"context"
	"fmt"
	"net"
	"strings"

	nsxmodel "github.com/vmware/vsphere-automation-sdk-go/services/nsxt-mp/nsx/model"

	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
	"github.com/vmware-tanzu/nsx-operator/pkg/util"
)

// buildDNSRecordResource builds []*DNSRecord for each (zone, fqdn, ip) combination in rr.
// Returns nil when rr.fqdns is empty.
func buildDNSRecordResource(ctx context.Context, svc *DNSRecordService, rr *recordRequest) []*DNSRecord {
	if len(rr.fqdns) == 0 || len(rr.ips) == 0 {
		return nil
	}

	var cluster string
	if svc.NSXConfig != nil {
		cluster = svc.NSXConfig.Cluster
	}

	var nsUID string
	if svc.Client != nil && rr.owner != nil {
		nsUID = string(svc.GetNamespaceUID(rr.owner.GetNamespace()))
	}

	ownerUID := ""
	if rr.owner != nil {
		ownerUID = string(rr.owner.GetUID())
	}

	var records []*DNSRecord
	for zonePath, fqdns := range rr.fqdns {
		for _, fqdn := range fqdns {
			for _, ip := range rr.ips {
				record := buildSingleRecord(svc, cluster, ownerUID, nsUID, zonePath, fqdn, ip, rr)
				if record != nil {
					records = append(records, record)
				}
			}
		}
	}
	return records
}

func buildSingleRecord(svc *DNSRecordService, cluster, ownerUID, nsUID, zonePath, fqdn string, ip net.IP, rr *recordRequest) *DNSRecord {
	ipStr := ip.String()
	recordType := "AAAA"
	if ip.To4() != nil {
		recordType = "A"
	}

	id := buildDNSRecordID(cluster, ownerUID, zonePath, fqdn, ipStr)
	for recordIdExists(svc.DNSRecordStore, id) {
		salt := util.TruncateUIDHash(id + zonePath + fqdn + ipStr)
		id = buildDNSRecordID(cluster, ownerUID+salt, zonePath, fqdn, ipStr)
	}

	tags := buildDNSRecordTags(cluster, nsUID, rr)

	return &DNSRecord{
		Id:           common.String(id),
		DisplayName:  common.String(fmt.Sprintf("%s:%s", fqdn, ipStr)),
		Fqdn:         common.String(fqdn),
		IpAddress:    common.String(ipStr),
		RecordType:   common.String(recordType),
		DnsZonePath:  common.String(zonePath),
		RecordValues: []string{ipStr},
		Tags:         tags,
	}
}

// buildDNSRecordID returns a deterministic ID based on the logical key of the record.
func buildDNSRecordID(cluster, ownerUID, zonePath, fqdn, ipStr string) string {
	key := strings.Join([]string{cluster, ownerUID, zonePath, fqdn, ipStr}, "|")
	return fmt.Sprintf("dnsrecord-%s", util.TruncateUIDHash(key))
}

// recordIdExists checks whether an ID is already present in the store.
func recordIdExists(store *DNSRecordStore, id string) bool {
	return store.GetByKey(id) != nil
}

// buildDNSRecordTags constructs the NSX tag slice for a DNS record.
func buildDNSRecordTags(cluster, nsUID string, rr *recordRequest) []nsxmodel.Tag {
	tags := buildTags(cluster, nsUID, rr)
	return tags
}

// buildTags assembles all NSX tags for a DNS record from cluster/owner/addressProvider info.
func buildTags(cluster, nsUID string, rr *recordRequest) []nsxmodel.Tag {
	tags := []nsxmodel.Tag{
		{Scope: common.String(common.TagScopeCluster), Tag: common.String(cluster)},
	}

	if rr.owner == nil {
		return tags
	}

	ownerUID := string(rr.owner.GetUID())
	ownerName := rr.owner.GetName()
	ownerNS := rr.owner.GetNamespace()

	switch rr.owner.Kind {
	case ResourceKindGateway:
		tags = append(tags,
			nsxmodel.Tag{Scope: common.String(common.TagScopeDNSRecordFor), Tag: common.String(common.TagValueDNSRecordForGateway)},
			nsxmodel.Tag{Scope: common.String(common.TagScopeGatewayUID), Tag: common.String(ownerUID)},
			nsxmodel.Tag{Scope: common.String(common.TagScopeGatewayName), Tag: common.String(ownerName)},
			nsxmodel.Tag{Scope: common.String(common.TagScopeGatewayNamespace), Tag: common.String(ownerNS)},
		)
	case ResourceKindListenerSet:
		addrUID, addrName, addrNS := "", "", ""
		if rr.addressProvider != nil {
			addrUID = string(rr.addressProvider.GetUID())
			addrName = rr.addressProvider.GetName()
			addrNS = rr.addressProvider.GetNamespace()
		}
		tags = append(tags,
			nsxmodel.Tag{Scope: common.String(common.TagScopeDNSRecordFor), Tag: common.String(common.TagValueDNSRecordForListenerSet)},
			nsxmodel.Tag{Scope: common.String(common.TagScopeListenerSetUID), Tag: common.String(ownerUID)},
			nsxmodel.Tag{Scope: common.String(common.TagScopeListenerSetName), Tag: common.String(ownerName)},
			nsxmodel.Tag{Scope: common.String(common.TagScopeGatewayUID), Tag: common.String(addrUID)},
			nsxmodel.Tag{Scope: common.String(common.TagScopeGatewayName), Tag: common.String(addrName)},
			nsxmodel.Tag{Scope: common.String(common.TagScopeGatewayNamespace), Tag: common.String(addrNS)},
		)
	case ResourceKindService:
		tags = append(tags,
			nsxmodel.Tag{Scope: common.String(common.TagScopeDNSRecordFor), Tag: common.String(common.TagValueDNSRecordForService)},
			nsxmodel.Tag{Scope: common.String(common.TagScopeServiceUID), Tag: common.String(ownerUID)},
			nsxmodel.Tag{Scope: common.String(common.TagScopeServiceName), Tag: common.String(ownerName)},
		)
	}

	if nsUID != "" {
		tags = append(tags, nsxmodel.Tag{Scope: common.String(common.TagScopeNamespaceUID), Tag: common.String(nsUID)})
	}
	if rr.forSVService {
		tags = append(tags, nsxmodel.Tag{Scope: common.String(common.TagScopeForSupervisorService), Tag: common.String("true")})
	}

	return tags
}

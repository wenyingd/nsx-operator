/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package dns

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt-mp/nsx/model"

	extdns "github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/endpoint"

	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
)

// stableDNSRecordID returns a deterministic store/API key for one owner + ExternalDNS endpoint.
func stableDNSRecordID(owner *ResourceRef, ep *extdns.Endpoint) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s",
		owner.Kind,
		owner.GetNamespace(),
		owner.GetName(),
		string(owner.GetUID()),
		ep.DNSName,
		ep.RecordType,
		ep.SetIdentifier,
	)))
	return "dns-" + hex.EncodeToString(h[:16])
}

func modelTag(scope, value string) model.Tag {
	s, v := scope, value
	return model.Tag{Scope: &s, Tag: &v}
}

func dnsRecordTagsForOwner(batch *OwnerEndpoints) []model.Tag {
	owner := batch.Owner
	ap := batch.AddressProvider
	createdFor := resourceKindToCreatedFor(owner.Kind)
	tags := []model.Tag{
		modelTag(common.TagScopeDNSRecordFor, createdFor),
		modelTag(common.TagScopeGatewayUID, string(ap.GetUID())),
		modelTag(common.TagScopeGatewayName, ap.GetName()),
		modelTag(common.TagScopeGatewayNamespace, ap.GetNamespace()),
	}
	switch owner.Kind {
	case ResourceKindGateway:
		tags = append(tags, modelTag(common.TagScopeGatewayUID, string(owner.GetUID())))
	case ResourceKindListenerSet:
		tags = append(tags,
			modelTag(common.TagScopeListenerSetUID, string(owner.GetUID())),
			modelTag(common.TagScopeListenerSetName, owner.GetName()),
			modelTag(common.TagScopeNamespace, owner.GetNamespace()),
		)
	case ResourceKindHTTPRoute:
		tags = append(tags, modelTag(common.TagScopeHTTPRouteUID, string(owner.GetUID())))
	case ResourceKindGRPCRoute:
		tags = append(tags, modelTag(common.TagScopeGRPCRouteUID, string(owner.GetUID())))
	case ResourceKindTLSRoute:
		tags = append(tags, modelTag(common.TagScopeTLSRouteUID, string(owner.GetUID())))
	case ResourceKindService:
		tags = append(tags, modelTag(common.TagScopeServiceUID, string(owner.GetUID())))
	}
	return tags
}

func dnsRecordFromEndpoint(id string, batch *OwnerEndpoints, ep *extdns.Endpoint) *DNSRecord {
	fqdn := ep.DNSName
	rt := ep.RecordType
	values := append([]string(nil), ep.Targets...)
	var ip *string
	if len(values) > 0 && (ep.RecordType == extdns.RecordTypeA || ep.RecordType == extdns.RecordTypeAAAA) {
		ip = &values[0]
	}
	dn := fmt.Sprintf("%s %s", ep.DNSName, ep.RecordType)
	return &DNSRecord{
		Id:           &id,
		DisplayName:  &dn,
		Tags:         dnsRecordTagsForOwner(batch),
		Fqdn:         &fqdn,
		IpAddress:    ip,
		RecordType:   &rt,
		RecordValues: values,
	}
}

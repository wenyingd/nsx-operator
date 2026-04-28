/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package dns

import (
	"slices"
	"strings"

	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
)

func parseContributingOwnersFromRecord(rec *model.ProjectDnsRecord) []string {
	return parseContributingOwnersTag(firstTagValue(rec.Tags, common.TagScopeDNSRecordContributingOwners))
}

func parseContributingOwnersTag(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	seen := sets.New[string]()
	for _, p := range strings.Split(raw, ",") {
		k := strings.TrimSpace(p)
		if k != "" {
			seen.Insert(k)
		}
	}
	out := seen.UnsortedList()
	slices.Sort(out)
	return out
}

func formatContributingOwnersTag(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	cp := sortedCopyStrings(keys)
	return strings.Join(cp, ",")
}

// mergeContributingOwnerKeys returns sorted unique contributing keys (excludes primaryNNKey).
func mergeContributingOwnerKeys(existing string, add string, primaryNNKey string) string {
	raw := strings.Join([]string{existing, add}, ",")
	seen := sets.New[string]()
	for _, p := range strings.Split(raw, ",") {
		k := strings.TrimSpace(p)
		if k == "" || k == primaryNNKey {
			continue
		}
		if k != "" {
			seen.Insert(k)
		}
	}
	return formatContributingOwnersTag(seen.UnsortedList())
}

func resourceRefFromDNSRecord(rec *model.ProjectDnsRecord) (*ResourceRef, bool) {
	if rec == nil {
		return nil, false
	}
	createdFor, ns, name, ok := ownerCreatedForAndNNFromDNSRecord(rec)
	if !ok || ns == "" || name == "" {
		return nil, false
	}
	kind := resourceKindFromCreatedForTag(createdFor)
	if kind == "" {
		return nil, false
	}
	meta := metav1.ObjectMeta{Namespace: ns, Name: name}
	return &ResourceRef{Kind: kind, Object: &meta}, true
}

// parseOwnerNNIndexKey parses "createdFor/ns/name" owner index keys.
func parseOwnerNNIndexKey(key string) (createdFor, ns, name string, ok bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", "", "", false
	}
	parts := strings.SplitN(key, "/", 3)
	if len(parts) != 3 {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

func ownerNNIndexKeyForResourceRef(owner *ResourceRef) string {
	if owner == nil {
		return ""
	}
	createdFor := resourceKindToCreatedFor(owner.Kind)
	if createdFor == "" {
		return ""
	}
	return dnsRecordOwnerKey(createdFor, dnsRecordOwnerNamespacedNameKey(owner.GetNamespace(), owner.GetName()))
}

func primaryOwnerNNIndexKeyFromRecord(rec *model.ProjectDnsRecord) string {
	return getDNSRecordOwnerNamespacedName(rec)
}

// appendRecordOwnershipTags appends the optional GatewayIndexList and ContributingOwners tags
// onto tags when their values are non-empty, returning the extended slice. The caller is
// responsible for passing a slice it owns so this function may append to it directly.
func appendRecordOwnershipTags(tags []model.Tag, gwKey string, ctag string) []model.Tag {
	if gwKey = strings.TrimSpace(gwKey); gwKey != "" {
		tags = append(tags, modelTag(common.TagScopeDNSRecordGatewayIndexList, gwKey))
	}
	if ctag != "" {
		tags = append(tags, modelTag(common.TagScopeDNSRecordContributingOwners, ctag))
	}
	return tags
}

func replaceContributingOwnersInTags(tags []model.Tag, newContribKeys []string) []model.Tag {
	out := make([]model.Tag, 0)
	for _, t := range tags {
		if t.Scope == nil {
			continue
		}
		if *t.Scope != common.TagScopeDNSRecordContributingOwners {
			out = append(out, t)
			continue
		}
		if len(newContribKeys) > 0 {
			out = append(out, modelTag(common.TagScopeDNSRecordContributingOwners, formatContributingOwnersTag(newContribKeys)))
		}
	}
	return out
}

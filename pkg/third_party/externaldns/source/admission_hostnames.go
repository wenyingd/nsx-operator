/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

// Gateway admission hostname filtering (Gateway + ListenerSet scope vs route hostnames).
// Semantics mirror sigs.k8s.io/external-dns/source/gateway.go matchRouteToListener (gwMatchingHost,
// skip when listener host and route host are both empty). Attribution: see package doc.go.

package source

import (
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	extann "github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/annotations"
)

// CollectAdmissionHostnameFilters returns admission hostnames from the Gateway’s Spec.Listeners
// and from each ListenerSet’s Spec.Listeners (in order: gateway listeners, then listenerSets as
// given, then each set’s entries). A nil listener Hostname becomes "" (matches any route host),
// matching ExternalDNS gateway listener semantics. Invalid non-empty hostnames are omitted.
// Duplicate values are deduplicated preserving first-seen order.
func CollectAdmissionHostnameFilters(gw *gatewayv1.Gateway, listenerSets []gatewayv1.ListenerSet) []string {
	if gw == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	add := func(s string) {
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for i := range gw.Spec.Listeners {
		lis := &gw.Spec.Listeners[i]
		if lis.Hostname == nil {
			add("")
			continue
		}
		h := strings.TrimSpace(string(*lis.Hostname))
		if h == "" {
			add("")
			continue
		}
		if c, ok := GatewayCanonicalHost(h); ok {
			add(c)
		}
	}
	for lsIdx := range listenerSets {
		for i := range listenerSets[lsIdx].Spec.Listeners {
			e := &listenerSets[lsIdx].Spec.Listeners[i]
			if e.Hostname == nil {
				add("")
				continue
			}
			h := strings.TrimSpace(string(*e.Hostname))
			if h == "" {
				add("")
				continue
			}
			if c, ok := GatewayCanonicalHost(h); ok {
				add(c)
			}
		}
	}
	return out
}

// RouteHostnamesMatchingAdmission maps raw route hostname candidates (from RouteHostnames /
// mergeGatewayRouteHostnameAnnotations) to DNS hostnames using GwMatchingHost against allowed
// filters; when both filter and route host are empty, that pair is skipped (ExternalDNS). For a
// non-empty route host, if multiple filters yield different canonical hosts, the most specific wins
// (more labels; wildcard DNS names penalized; lexicographic tie-break). For an empty route host,
// every distinct listener-derived match is kept (several scoped listeners).
//
// If the chosen hostname is a wildcard DNS name (leading "*."), it is dropped unless the route
// has a non-empty external-dns.alpha.kubernetes.io/hostname annotation entry.
func RouteHostnamesMatchingAdmission(allowed []string, routeMeta *metav1.ObjectMeta, rawRouteHostnames []string) ([]string, error) {
	if len(allowed) == 0 || len(rawRouteHostnames) == 0 {
		return nil, nil
	}
	allowWildcardDNSName := routeHasExternalDNSHostnameAnnotation(routeMeta)
	var out []string
	seen := make(map[string]struct{})
	for _, rtHost := range rawRouteHostnames {
		for _, best := range admissionMatchesForRouteHost(allowed, rtHost) {
			if strings.HasPrefix(best, "*.") && !allowWildcardDNSName {
				continue
			}
			if _, ok := seen[best]; ok {
				continue
			}
			seen[best] = struct{}{}
			out = append(out, best)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return NormalizeHostnameStrings(out), nil
}

func routeHasExternalDNSHostnameAnnotation(meta *metav1.ObjectMeta) bool {
	if meta == nil {
		return false
	}
	for _, h := range extann.HostnamesFromAnnotations(meta.GetAnnotations()) {
		if strings.TrimSpace(h) != "" {
			return true
		}
	}
	return false
}

// admissionMatchesForRouteHost returns distinct DNS hostnames allowed for one route hostname
// token. When both a filter and rtHost are empty, that pair is skipped (ExternalDNS). For an
// empty rtHost and several non-empty listener filters, every distinct match is returned so
// multiple scoped listeners each yield a record. For a non-empty rtHost, if multiple filters
// ever produced different canonical hosts, the most specific one is kept.
func admissionMatchesForRouteHost(allowed []string, rtHost string) []string {
	seen := make(map[string]struct{})
	var candidates []string
	for _, f := range allowed {
		if f == "" && rtHost == "" {
			continue
		}
		h, ok := GwMatchingHost(f, rtHost)
		if !ok || h == "" {
			continue
		}
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		candidates = append(candidates, h)
	}
	if len(candidates) <= 1 {
		return candidates
	}
	if rtHost == "" {
		return candidates
	}
	best := candidates[0]
	for i := 1; i < len(candidates); i++ {
		if hostnameMoreSpecific(candidates[i], best) {
			best = candidates[i]
		}
	}
	return []string{best}
}

// hostnameMoreSpecific reports whether a should be preferred over b for DNS admission tie-breaking.
func hostnameMoreSpecific(a, b string) bool {
	if a == b {
		return false
	}
	ra, rb := hostnameSpecificityRank(a), hostnameSpecificityRank(b)
	if ra != rb {
		return ra > rb
	}
	return a < b
}

func hostnameSpecificityRank(h string) int {
	if h == "" {
		return -1
	}
	labels := strings.Count(h, ".") + 1
	r := labels * 4
	if strings.HasPrefix(h, "*.") {
		r -= 2
	}
	return r
}

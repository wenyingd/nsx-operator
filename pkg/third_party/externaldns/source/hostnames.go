/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

// Hostname helpers for Gateway / ListenerSet / routes. Attribution: see package doc.go.
package source

import (
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	extann "github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/annotations"
)

// ExternalDNSHostnameAnnotation is the standard ExternalDNS hostname override annotation.
const ExternalDNSHostnameAnnotation = extann.HostnameKey

// GetDesiredHostnames returns hostnames for DNS, preferring ExternalDNS hostname annotation(s)
// (same as annotations.HostnamesFromAnnotations). If absent or empty, spec hostnames are used.
func GetDesiredHostnames(obj metav1.Object, specHostnames []string) []string {
	if obj == nil {
		return normalizeHostnamesList(specHostnames)
	}
	ann := obj.GetAnnotations()
	if ann == nil {
		return normalizeHostnamesList(specHostnames)
	}
	if h := extann.HostnamesFromAnnotations(ann); len(h) > 0 {
		out := make([]string, 0, len(h))
		for _, p := range h {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return normalizeHostnamesList(specHostnames)
}

func normalizeHostnamesList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, h := range in {
		h = strings.TrimSpace(h)
		if h != "" {
			out = append(out, h)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// NormalizeHostnameStrings trims each entry and drops empty strings. Use for comparing or
// deduplicating hostname lists after RouteHostnames or other merges.
func NormalizeHostnameStrings(in []string) []string {
	return normalizeHostnamesList(in)
}

// GatewayListenerHostnames returns trimmed hostnames from a Gateway's Spec.Listeners[*].Hostname.
func GatewayListenerHostnames(listeners []gatewayv1.Listener) []string {
	var raw []string
	for i := range listeners {
		if listeners[i].Hostname != nil {
			raw = append(raw, string(*listeners[i].Hostname))
		}
	}
	return normalizeHostnamesList(raw)
}

// ListenerSetEntryHostnames returns trimmed hostnames from a ListenerSet's Spec.Listeners[*].Hostname.
func ListenerSetEntryHostnames(entries []gatewayv1.ListenerEntry) []string {
	var raw []string
	for i := range entries {
		if entries[i].Hostname != nil {
			raw = append(raw, string(*entries[i].Hostname))
		}
	}
	return normalizeHostnamesList(raw)
}

// RouteSpecHostnames converts HTTPRoute/GRPCRoute/TLSRoute Spec.Hostnames to trimmed, non-empty strings.
func RouteSpecHostnames(hostnames []gatewayv1.Hostname) []string {
	raw := make([]string, 0, len(hostnames))
	for i := range hostnames {
		raw = append(raw, string(hostnames[i]))
	}
	return normalizeHostnamesList(raw)
}

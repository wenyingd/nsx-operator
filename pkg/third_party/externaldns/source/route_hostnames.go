/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

// Route hostname merging follows source/gateway.go (*gatewayRouteResolver).hosts():
// spec hostnames, optional FQDN-template hosts, then gateway-hostname-source + hostname annotations.
// Attribution: see package doc.go.

package source

import (
	"log/slog"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	extann "github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/annotations"
)

const (
	// GatewayHostnameSourceAnnotationOnly matches external-dns gatewayHostnameSourceAnnotationOnlyValue.
	GatewayHostnameSourceAnnotationOnly = "annotation-only"
	// GatewayHostnameSourceDefinedHostsOnly matches external-dns gatewayHostnameSourceDefinedHostsOnlyValue.
	GatewayHostnameSourceDefinedHostsOnly = "defined-hosts-only"
)

// RouteHostnames returns hostname candidates for a route object, following ExternalDNS
// gateway-hostname-source and external-dns.alpha.kubernetes.io/hostname rules.
// It is equivalent to RouteHostnamesForRoute(meta, specHostnames, nil, ignoreHostnameAnnotation).
func RouteHostnames(meta *metav1.ObjectMeta, specHostnames []string, ignoreHostnameAnnotation bool) ([]string, error) {
	return RouteHostnamesForRoute(meta, specHostnames, nil, ignoreHostnameAnnotation)
}

// RouteHostnamesForRoute merges route Spec hostnames and optional FQDN-template hostnames (the output of
// ExternalDNS templateEngine.ExecFQDN when enabled), then applies the same gateway-hostname-source logic as
// source/gateway.go hosts(). nsx-operator does not execute FQDN templates here; pass fqdnTemplateHostnames
// when an external template step produces extra names.
//
// routeSpecHostnamesEmpty must reflect len(route.Spec.Hostnames)==0 before templates, matching ExternalDNS
// use of len(rt.Hostnames())==0 for the empty-string placeholder branch.
func RouteHostnamesForRoute(meta *metav1.ObjectMeta, specHostnames, fqdnTemplateHostnames []string, ignoreHostnameAnnotation bool) ([]string, error) {
	hostnames := append([]string(nil), specHostnames...)
	if len(fqdnTemplateHostnames) > 0 {
		hostnames = append(hostnames, fqdnTemplateHostnames...)
	}
	routeSpecHostnamesEmpty := len(specHostnames) == 0
	return mergeGatewayRouteHostnameAnnotations(meta, hostnames, routeSpecHostnamesEmpty, ignoreHostnameAnnotation)
}

func mergeGatewayRouteHostnameAnnotations(meta *metav1.ObjectMeta, hostnames []string, routeSpecHostnamesEmpty bool, ignoreHostnameAnnotation bool) ([]string, error) {
	if meta == nil {
		return hostnames, nil
	}
	ann := meta.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	raw, hasKey := ann[extann.GatewayHostnameSourceKey]
	if !hasKey {
		return appendAnnotationHostsDefault(hostnames, ann, routeSpecHostnamesEmpty, ignoreHostnameAnnotation), nil
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case GatewayHostnameSourceAnnotationOnly:
		if ignoreHostnameAnnotation {
			return []string{}, nil
		}
		h := extann.HostnamesFromAnnotations(ann)
		if h == nil {
			return []string{}, nil
		}
		return h, nil
	case GatewayHostnameSourceDefinedHostsOnly:
		return hostnames, nil
	default:
		slog.Default().Warn("invalid gateway-hostname-source, falling back to default behavior",
			"annotation", extann.GatewayHostnameSourceKey,
			"namespace", meta.Namespace,
			"name", meta.Name,
			"value", raw,
		)
		return appendAnnotationHostsDefault(hostnames, ann, routeSpecHostnamesEmpty, ignoreHostnameAnnotation), nil
	}
}

// appendAnnotationHostsDefault merges external-dns.alpha.kubernetes.io/hostname with spec/template
// hostnames for the default gateway-hostname-source path (same annotation keys as ExternalDNS hosts()).
// Non-empty annotation hostnames are prepended so DNS admission and record generation prefer explicit
// operator overrides over route spec names (nsx-operator; upstream appends annotations after spec).
func appendAnnotationHostsDefault(hostnames []string, ann map[string]string, routeSpecHostnamesEmpty, ignoreHostnameAnnotation bool) []string {
	var out []string
	if !ignoreHostnameAnnotation {
		out = append(out, extann.HostnamesFromAnnotations(ann)...)
	}
	if routeSpecHostnamesEmpty {
		out = append(out, "")
	}
	out = append(out, hostnames...)
	return out
}

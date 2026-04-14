// Copyright 2017 The Kubernetes Authors.
// Copyright 2026 Broadcom, Inc.
//
// SPDX-License-Identifier: Apache-2.0
//
// Derived from sigs.k8s.io/external-dns/source/annotations/annotations.go (subset).
// Attribution: see package doc.go (constants only; no functions in this file).

package annotations

const (
	// DefaultAnnotationPrefix is the default ExternalDNS annotation prefix.
	DefaultAnnotationPrefix = "external-dns.alpha.kubernetes.io/"
	// HostnameKey defines the desired hostname(s) (comma-separated).
	HostnameKey = DefaultAnnotationPrefix + "hostname"
	// GatewayHostnameSourceKey selects how Gateway API route hostnames are combined with annotations.
	GatewayHostnameSourceKey = DefaultAnnotationPrefix + "gateway-hostname-source"
)

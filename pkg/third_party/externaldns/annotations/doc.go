// Package annotations provides a small subset of ExternalDNS source annotation helpers.
// Upstream path: sigs.k8s.io/external-dns/source/annotations (keys + processors hostname helpers).
//
// # Direct copy from external-dns (same logic; same exported names)
//
//	SplitHostnameAnnotation
//	HostnamesFromAnnotations
//
// # Modified from external-dns
//
//	extractHostnamesFromAnnotations — upstream does not short-circuit on nil map; this copy returns nil if input is nil (defensive).
//
// # Not from external-dns (nsx-operator / subset)
//
//	keys.go — only DefaultAnnotationPrefix, HostnameKey, GatewayHostnameSourceKey constants aligned with upstream annotation key strings (no functions).
package annotations

// Copyright 2025 The Kubernetes Authors.
// Copyright 2026 Broadcom, Inc.
//
// SPDX-License-Identifier: Apache-2.0
//
// Derived from sigs.k8s.io/external-dns/source/annotations/processors.go (hostname helpers).
// Attribution: see package doc.go.

package annotations

import (
	"strings"
)

// SplitHostnameAnnotation splits a comma-separated hostname annotation string into a slice of hostnames.
func SplitHostnameAnnotation(input string) []string {
	return strings.Split(strings.TrimSpace(strings.ReplaceAll(input, " ", "")), ",")
}

func extractHostnamesFromAnnotations(input map[string]string, key string) []string {
	if input == nil {
		return nil
	}
	annotation, ok := input[key]
	if !ok {
		return nil
	}
	return SplitHostnameAnnotation(annotation)
}

// HostnamesFromAnnotations extracts hostnames from the standard ExternalDNS hostname annotation.
func HostnamesFromAnnotations(input map[string]string) []string {
	return extractHostnamesFromAnnotations(input, HostnameKey)
}

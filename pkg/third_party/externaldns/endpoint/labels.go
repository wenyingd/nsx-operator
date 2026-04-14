// Copyright 2017 The Kubernetes Authors.
// Copyright 2026 Broadcom, Inc.
//
// SPDX-License-Identifier: Apache-2.0
//
// Derived from sigs.k8s.io/external-dns/endpoint/labels.go (subset).
// Attribution: see package doc.go.

package endpoint

const (
	// HeritageLabelKey is the ExternalDNS heritage label (TXT serialization uses heritage=external-dns).
	HeritageLabelKey = "heritage"
	// HeritageExternalDNSValue is the heritage value for records produced by ExternalDNS-compatible sources.
	HeritageExternalDNSValue = "external-dns"
	// DefaultSourceOwnerID is the default Endpoint owner label when none is configured (ExternalDNS OwnerLabelKey).
	DefaultSourceOwnerID = "nsx-operator"

	// OwnerLabelKey is the name of the label that defines the owner of an Endpoint.
	OwnerLabelKey = "owner"
	// ResourceLabelKey identifies the Kubernetes resource requesting the DNS name.
	ResourceLabelKey = "resource"
)

// Labels stores metadata related to the endpoint.
type Labels map[string]string

// NewLabels returns empty Labels.
func NewLabels() Labels {
	return map[string]string{}
}

// ApplyExternalDNSSourceLabels sets heritage and owner on an Endpoint for compatibility with
// ExternalDNS registry/TXT conventions. EndpointsForHostname already sets ResourceLabelKey when resource is non-empty.
func ApplyExternalDNSSourceLabels(ep *Endpoint, ownerID string) {
	if ep == nil {
		return
	}
	if ep.Labels == nil {
		ep.Labels = NewLabels()
	}
	ep.Labels[HeritageLabelKey] = HeritageExternalDNSValue
	if ownerID == "" {
		ownerID = DefaultSourceOwnerID
	}
	ep.Labels[OwnerLabelKey] = ownerID
}

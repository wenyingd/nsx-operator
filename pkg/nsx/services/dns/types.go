/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package dns

import (
	"context"
	"net"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ResourceKindGateway     = "Gateway"
	ResourceKindListenerSet = "ListenerSet"
	ResourceKindService     = "Service"
)

// ResourceRef identifies a K8s resource by Kind, Namespace, Name, and UID.
// Used for both the address provider (e.g. Gateway) and the owner (Gateway or ListenerSet) of a Record.
type ResourceRef struct {
	metav1.Object
	Kind string
}

// Record represents one desired DNS mapping: a set of IPs (from the Gateway) and FQDNs
// (from Gateway or ListenerSet listeners), with references to the address provider and owner.
type Record struct {
	// Addresses are the IPs for A/AAAA records (from Gateway status.Addresses).
	Addresses []net.IP
	// Hostnames are the FQDNs to map (from Gateway or ListenerSet listener hostnames).
	Hostnames []string
	// AddressProvider identifies the resource that provides the IPs (the Gateway or Service).
	AddressProvider *ResourceRef
	// Owner identifies the resource that owns this record (Gateway or ListenerSet or Service).
	Owner        *ResourceRef
	ForSVService bool
}

// ZoneConfig represents a permitted NSX DNS forwarding zone.
type ZoneConfig struct {
	// Path is the NSX resource path for the DNS forwarder zone.
	Path string
	// Domain is the DNS domain name (e.g. "example.com").
	Domain string
}

// recordRequest groups FQDNs (keyed by NSX zone path) and IP addresses for a single owner.
// It is the internal representation passed to configureDNSRecords / buildDNSRecordResource.
type recordRequest struct {
	// fqdns maps NSX zone path → list of FQDNs that belong to that zone.
	fqdns           map[string][]string
	ips             []net.IP
	owner           *ResourceRef
	addressProvider *ResourceRef
	forSVService    bool
}

// GetPermittedZonesFunc is the type of the function that resolves permitted DNS zones for a
// given namespace.  The field on DNSRecordService with this type allows tests to inject a fake
// implementation without monkey-patching the private getPermittedZones method.
type GetPermittedZonesFunc func(ctx context.Context, namespace string) ([]ZoneConfig, error)

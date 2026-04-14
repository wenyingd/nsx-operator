/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package dns

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	extdns "github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/endpoint"
)

const (
	ResourceKindGateway     = "Gateway"
	ResourceKindListenerSet = "ListenerSet"
	ResourceKindHTTPRoute   = "HTTPRoute"
	ResourceKindGRPCRoute   = "GRPCRoute"
	ResourceKindTLSRoute    = "TLSRoute"
	ResourceKindService     = "Service"
)

// ResourceRef identifies a K8s resource by Kind, Namespace, Name, and UID.
// Used for both the address provider (e.g. Gateway) and the owner of a DNS batch.
type ResourceRef struct {
	metav1.Object
	Kind string
}

// OwnerEndpoints groups ExternalDNS-style Endpoints for one Kubernetes owner
// (Gateway, ListenerSet, or Route), sharing the same address provider (typically the Gateway).
type OwnerEndpoints struct {
	AddressProvider *ResourceRef
	Owner           *ResourceRef
	ForSVService    bool
	Endpoints       []*extdns.Endpoint
}

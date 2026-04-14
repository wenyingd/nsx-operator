/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */
// Attribution: see package doc.go.

package source

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// routeConditionProgrammed is the Gateway API Route condition type for programmed (ready for use).
const routeConditionProgrammed = "Programmed"

// HTTPRouteParentReadyForGateway returns true when the HTTPRoute status includes a parent entry
// for the given Gateway with RouteConditionAccepted and RouteConditionProgrammed set to True.
//
// ExternalDNS gateway resolver (source/gateway.go) requires RouteConditionAccepted on the parent
// status entry; nsx-operator additionally requires RouteConditionProgrammed=True so DNS is only
// published once the data plane has programmed the route.
func HTTPRouteParentReadyForGateway(route *gatewayv1.HTTPRoute, gw types.NamespacedName) bool {
	if route == nil {
		return false
	}
	for i := range route.Status.Parents {
		ps := &route.Status.Parents[i]
		if !ParentRefMatchesGateway(&ps.ParentRef, route.Namespace, gw) {
			continue
		}
		if conditionTrue(ps.Conditions, string(gatewayv1.RouteConditionAccepted)) &&
			conditionTrue(ps.Conditions, routeConditionProgrammed) {
			return true
		}
	}
	return false
}

// ParentRefMatchesGateway reports whether ref identifies the given Gateway (group/kind/name/namespace).
func ParentRefMatchesGateway(ref *gatewayv1.ParentReference, routeNamespace string, gw types.NamespacedName) bool {
	if ref == nil {
		return false
	}
	group := gatewayv1.GroupName
	if ref.Group != nil && string(*ref.Group) != "" {
		group = string(*ref.Group)
	}
	if group != gatewayv1.GroupName {
		return false
	}
	kind := "Gateway"
	if ref.Kind != nil && string(*ref.Kind) != "" {
		kind = string(*ref.Kind)
	}
	if kind != "Gateway" {
		return false
	}
	if string(ref.Name) != gw.Name {
		return false
	}
	ns := routeNamespace
	if ref.Namespace != nil && string(*ref.Namespace) != "" {
		ns = string(*ref.Namespace)
	}
	return ns == gw.Namespace
}

func conditionTrue(conds []metav1.Condition, typ string) bool {
	for j := range conds {
		if conds[j].Type == typ && conds[j].Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package source

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestObjectNamespacedNameFromParentRef(t *testing.T) {
	gwGroup := gatewayv1.Group(gatewayv1.GroupName)
	gwKind := gatewayv1.Kind("Gateway")
	lsKind := gatewayv1.Kind("ListenerSet")

	refGW := &gatewayv1.ParentReference{Group: &gwGroup, Kind: &gwKind, Name: "gw1"}
	nn, ok := GatewayNamespacedNameFromParentRef(refGW, "ns1")
	assert.True(t, ok)
	assert.Equal(t, "ns1/gw1", nn.String())

	refLS := &gatewayv1.ParentReference{Group: &gwGroup, Kind: &lsKind, Name: "ls1"}
	nn, ok = ListenerSetNamespacedNameFromParentRef(refLS, "ns1")
	assert.True(t, ok)
	assert.Equal(t, "ns1/ls1", nn.String())
}

func TestRouteAcceptedForParentRef(t *testing.T) {
	gwGroup := gatewayv1.Group(gatewayv1.GroupName)
	gwKind := gatewayv1.Kind("Gateway")
	refGW := gatewayv1.ParentReference{Group: &gwGroup, Kind: &gwKind, Name: "gw1"}

	parents := []gatewayv1.RouteParentStatus{
		{
			ParentRef: refGW,
			Conditions: []metav1.Condition{
				{Type: string(gatewayv1.RouteConditionAccepted), Status: metav1.ConditionTrue},
			},
		},
	}

	assert.True(t, RouteAcceptedForParentRef(parents, "ns1", refGW))
}

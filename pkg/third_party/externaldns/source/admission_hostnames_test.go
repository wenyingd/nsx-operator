/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package source

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	extann "github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/annotations"
)

func TestCollectAdmissionHostnameFilters(t *testing.T) {
	gwGroup := gatewayv1.Group(gatewayv1.GroupName)
	gwKind := gatewayv1.Kind("Gateway")
	gw := &gatewayv1.Gateway{
		Spec: gatewayv1.GatewaySpec{
			Listeners: []gatewayv1.Listener{
				{Name: "l1", Hostname: nil},
				{Name: "l2", Hostname: hostnamePtr("  Api.Example.COM ")},
			},
		},
	}
	ls := gatewayv1.ListenerSet{
		Spec: gatewayv1.ListenerSetSpec{
			ParentRef: gatewayv1.ParentGatewayReference{Group: &gwGroup, Kind: &gwKind, Name: "gw1"},
			Listeners: []gatewayv1.ListenerEntry{
				{Name: "e1", Hostname: hostnamePtr("other.example.com")},
			},
		},
	}
	got := CollectAdmissionHostnameFilters(gw, []gatewayv1.ListenerSet{ls})
	assert.Equal(t, []string{"", "api.example.com", "other.example.com"}, got)
}

func TestRouteHostnamesMatchingAdmission_skipsBothEmpty(t *testing.T) {
	meta := &metav1.ObjectMeta{Name: "r1", Namespace: "ns"}
	got, err := RouteHostnamesMatchingAdmission([]string{""}, meta, []string{""})
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestRouteHostnamesMatchingAdmission_emptyRouteHostMultiListener(t *testing.T) {
	meta := &metav1.ObjectMeta{Name: "r1", Namespace: "ns"}
	got, err := RouteHostnamesMatchingAdmission([]string{"a.example.com", "b.example.com"}, meta, []string{""})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"a.example.com", "b.example.com"}, got)
}

func TestRouteHostnamesMatchingAdmission_wildcardDroppedWithoutAnnotation(t *testing.T) {
	meta := &metav1.ObjectMeta{Name: "r1", Namespace: "ns"}
	got, err := RouteHostnamesMatchingAdmission([]string{""}, meta, []string{"*.example.com"})
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestRouteHostnamesMatchingAdmission_wildcardAllowedWithHostnameAnnotation(t *testing.T) {
	meta := &metav1.ObjectMeta{
		Name: "r1", Namespace: "ns",
		Annotations: map[string]string{
			extann.HostnameKey: "explicit.example.org",
		},
	}
	got, err := RouteHostnamesMatchingAdmission([]string{""}, meta, []string{"*.example.com"})
	require.NoError(t, err)
	assert.Equal(t, []string{"*.example.com"}, got)
}

func TestRouteHostnamesMatchingAdmission_noAllowed(t *testing.T) {
	meta := &metav1.ObjectMeta{Name: "r1", Namespace: "ns"}
	got, err := RouteHostnamesMatchingAdmission(nil, meta, []string{"foo.com"})
	require.NoError(t, err)
	assert.Nil(t, got)
}

func hostnamePtr(s string) *gatewayv1.Hostname {
	h := gatewayv1.Hostname(s)
	return &h
}

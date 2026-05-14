/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package source

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestBuildAdmissionHostCacheRows(t *testing.T) {
	h1 := gatewayv1.Hostname("foo.com")
	h2 := gatewayv1.Hostname("*.bar.com")
	gw := &gatewayv1.Gateway{
		Spec: gatewayv1.GatewaySpec{
			Listeners: []gatewayv1.Listener{
				{Name: "l1", Hostname: &h1},
				{Name: "l2", Hostname: &h2},
			},
		},
	}
	ls := []gatewayv1.ListenerSet{
		{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns1", Name: "ls1"},
			Spec: gatewayv1.ListenerSetSpec{
				Listeners: []gatewayv1.ListenerEntry{
					{Name: "l3", Hostname: &h1},
				},
			},
		},
	}
	rows := BuildAdmissionHostCacheRows(gw, ls)
	require.Len(t, rows, 3)
	assert.Equal(t, "foo.com", rows[0].Filter)
	assert.Equal(t, "*.bar.com", rows[1].Filter)
	assert.Equal(t, "foo.com", rows[2].Filter)
	assert.True(t, rows[2].FromListenerSet)
	assert.Equal(t, "ns1", rows[2].ListenerSet.Namespace)
}

func TestAdmissionHostnameFiltersForRouteParentFromRows(t *testing.T) {
	rows := []AdmissionHostCacheRow{
		{FromListenerSet: false, Section: "l1", Filter: "foo.com"},
		{FromListenerSet: true, ListenerSet: types.NamespacedName{Namespace: "ns1", Name: "ls1"}, Section: "l2", Filter: "bar.com"},
	}
	gwGroup := gatewayv1.Group(gatewayv1.GroupName)
	gwKind := gatewayv1.Kind("Gateway")
	lsKind := gatewayv1.Kind("ListenerSet")

	refGW := &gatewayv1.ParentReference{Group: &gwGroup, Kind: &gwKind, Name: "gw1"}
	assert.Equal(t, []string{"foo.com"}, AdmissionHostnameFiltersForRouteParentFromRows(rows, refGW, "ns1"))

	refLS := &gatewayv1.ParentReference{Group: &gwGroup, Kind: &lsKind, Name: "ls1"}
	assert.Equal(t, []string{"bar.com"}, AdmissionHostnameFiltersForRouteParentFromRows(rows, refLS, "ns1"))
}

func TestRouteHostnamesMatchingAdmission(t *testing.T) {
	allowed := []string{"*.example.com", "foo.bar.com"}
	meta := &metav1.ObjectMeta{}

	res, err := RouteHostnamesMatchingAdmission(allowed, meta, []string{"test.example.com"}, "", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"test.example.com"}, res)

	res, err = RouteHostnamesMatchingAdmission(allowed, meta, []string{"foo.bar.com"}, "", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"foo.bar.com"}, res)

	res, err = RouteHostnamesMatchingAdmission(allowed, meta, []string{"baz.com"}, "", "")
	require.NoError(t, err)
	assert.Nil(t, res)
}

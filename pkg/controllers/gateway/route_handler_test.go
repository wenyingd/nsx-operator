/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package gateway

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/vmware-tanzu/nsx-operator/pkg/controllers/common"
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/dns"
	extann "github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/annotations"
	extdns "github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/endpoint"
	extdnssrc "github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/source"
)

func gwParentRef(gwName string) gatewayv1.ParentReference {
	g := gatewayv1.Group(gatewayv1.GroupName)
	k := gatewayv1.Kind("Gateway")
	return gatewayv1.ParentReference{Group: &g, Kind: &k, Name: gatewayv1.ObjectName(gwName)}
}

func routeParentReady(parent gatewayv1.ParentReference) gatewayv1.RouteParentStatus {
	return gatewayv1.RouteParentStatus{
		ParentRef: parent,
		Conditions: []metav1.Condition{
			{Type: string(gatewayv1.RouteConditionAccepted), Status: metav1.ConditionTrue},
			{Type: "Programmed", Status: metav1.ConditionTrue},
		},
	}
}

func routeHandlerTestGateway(name, ns string) *gatewayv1.Gateway {
	return newTestGateway(name, ns, "10.0.0.1", false, func(g *gatewayv1.Gateway) {
		g.Spec.Listeners = []gatewayv1.Listener{
			{Name: "l1", Hostname: nil, Port: 80, Protocol: gatewayv1.HTTPProtocolType},
		}
	})
}

func Test_collectRouteEndpoints_annotationOverridesSpecHostnames(t *testing.T) {
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns1"}}
	gw := routeHandlerTestGateway("gw1", "ns1")
	gwRef := getGatewayReference(gw)
	allowed := extdnssrc.CollectAdmissionHostnameFilters(gw, nil)
	targets := extdns.NewTargets("10.0.0.1")

	parent := gwParentRef("gw1")

	tests := []struct {
		name    string
		route   client.Object
		collect func(*GatewayReconciler, *[]*dns.OwnerEndpoints) error
	}{
		{
			name: "HTTPRoute",
			route: &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name: "hr-ann", Namespace: "ns1",
					Annotations: map[string]string{extann.HostnameKey: "ann.com"},
				},
				Spec: gatewayv1.HTTPRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{parent}},
					Hostnames:       []gatewayv1.Hostname{"spec.com"},
					Rules:           []gatewayv1.HTTPRouteRule{{}},
				},
				Status: gatewayv1.HTTPRouteStatus{
					RouteStatus: gatewayv1.RouteStatus{
						Parents: []gatewayv1.RouteParentStatus{routeParentReady(parent)},
					},
				},
			},
			collect: func(r *GatewayReconciler, batches *[]*dns.OwnerEndpoints) error {
				seen := sets.New[string]()
				return collectRouteEndpoints(ctx, r, gwRef, targets, common.NormalNs, allowed, seen, batches,
					listHTTPRouteItems, getHTTPRouteInfo, checkHTTPRouteParentReady)
			},
		},
		{
			name: "GRPCRoute",
			route: &gatewayv1.GRPCRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name: "gr-ann", Namespace: "ns1",
					Annotations: map[string]string{extann.HostnameKey: "ann.com"},
				},
				Spec: gatewayv1.GRPCRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{parent}},
					Hostnames:       []gatewayv1.Hostname{"spec.com"},
					Rules:           []gatewayv1.GRPCRouteRule{{}},
				},
				Status: gatewayv1.GRPCRouteStatus{
					RouteStatus: gatewayv1.RouteStatus{Parents: []gatewayv1.RouteParentStatus{routeParentReady(parent)}},
				},
			},
			collect: func(r *GatewayReconciler, batches *[]*dns.OwnerEndpoints) error {
				seen := sets.New[string]()
				return collectRouteEndpoints(ctx, r, gwRef, targets, common.NormalNs, allowed, seen, batches,
					listGRPCRouteItems, getGRPCRouteInfo, checkGRPCRouteParentReady)
			},
		},
		{
			name: "TLSRoute",
			route: &gatewayv1.TLSRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name: "tr-ann", Namespace: "ns1",
					Annotations: map[string]string{extann.HostnameKey: "ann.com"},
				},
				Spec: gatewayv1.TLSRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{parent}},
					Hostnames:       []gatewayv1.Hostname{"spec.com"},
					Rules:           []gatewayv1.TLSRouteRule{{}},
				},
				Status: gatewayv1.TLSRouteStatus{
					RouteStatus: gatewayv1.RouteStatus{
						Parents: []gatewayv1.RouteParentStatus{routeParentReady(parent)},
					},
				},
			},
			collect: func(r *GatewayReconciler, batches *[]*dns.OwnerEndpoints) error {
				seen := sets.New[string]()
				return collectRouteEndpoints(ctx, r, gwRef, targets, common.NormalNs, allowed, seen, batches,
					listTLSRouteItems, getTLSRouteInfo, checkTLSRouteParentReady)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := fakeClientForGatewayTests(ns, gw, tt.route)
			r := &GatewayReconciler{Client: fc}
			var batches []*dns.OwnerEndpoints
			require.NoError(t, tt.collect(r, &batches))
			require.Len(t, batches, 1)
			require.NotEmpty(t, batches[0].Endpoints)
			assert.Equal(t, "ann.com", batches[0].Endpoints[0].DNSName)
		})
	}
}

func Test_collectRouteEndpoints_GRPCRoute_sortedByNamespaceName(t *testing.T) {
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns1"}}
	gw := routeHandlerTestGateway("gw1", "ns1")
	gwRef := getGatewayReference(gw)
	allowed := extdnssrc.CollectAdmissionHostnameFilters(gw, nil)
	targets := extdns.NewTargets("10.0.0.1")
	parent := gwParentRef("gw1")

	// Routes share spec.hostnames; per-route external-dns hostname annotations yield distinct DNS
	// names so each route produces an OwnerEndpoints batch and processing order is observable.
	makeGRPC := func(name, annHost string) *gatewayv1.GRPCRoute {
		return &gatewayv1.GRPCRoute{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "ns1",
				Annotations: map[string]string{extann.HostnameKey: annHost},
			},
			Spec: gatewayv1.GRPCRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{parent}},
				Hostnames:       []gatewayv1.Hostname{"shared.example.com"},
				Rules:           []gatewayv1.GRPCRouteRule{{}},
			},
			Status: gatewayv1.GRPCRouteStatus{
				RouteStatus: gatewayv1.RouteStatus{
					Parents: []gatewayv1.RouteParentStatus{routeParentReady(parent)},
				},
			},
		}
	}
	z := makeGRPC("zebra", "dns.zebra.example.com")
	a := makeGRPC("alpha", "dns.alpha.example.com")
	b := makeGRPC("beta", "dns.beta.example.com")

	fc := fakeClientForGatewayTests(ns, gw, z, a, b)
	r := &GatewayReconciler{Client: fc}
	seen := sets.New[string]()
	var batches []*dns.OwnerEndpoints
	require.NoError(t, collectRouteEndpoints(ctx, r, gwRef, targets, common.NormalNs, allowed, seen, &batches,
		listGRPCRouteItems, getGRPCRouteInfo, checkGRPCRouteParentReady))
	require.Len(t, batches, 3)
	assert.Equal(t, []string{"alpha", "beta", "zebra"}, []string{
		batches[0].Owner.GetName(),
		batches[1].Owner.GetName(),
		batches[2].Owner.GetName(),
	})
	assert.Equal(t, []string{"dns.alpha.example.com", "dns.beta.example.com", "dns.zebra.example.com"}, []string{
		batches[0].Endpoints[0].DNSName,
		batches[1].Endpoints[0].DNSName,
		batches[2].Endpoints[0].DNSName,
	})
}

func Test_updateTLSRouteParentDNSCondition_matchingParentOnly(t *testing.T) {
	ctx := context.Background()
	gwNN := types.NamespacedName{Namespace: "ns1", Name: "gw1"}
	otherParent := gwParentRef("other-gw")
	targetParent := gwParentRef("gw1")
	tr := &gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tls1", Namespace: "ns1", UID: types.UID("uid-tls1"),
			Generation: 42,
		},
		Spec: gatewayv1.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				// Indexed parent must be first so the fake client field index matches gwNN.
				ParentRefs: []gatewayv1.ParentReference{targetParent, otherParent},
			},
			Hostnames: []gatewayv1.Hostname{"tls.example.com"},
			Rules:     []gatewayv1.TLSRouteRule{{}},
		},
		Status: gatewayv1.TLSRouteStatus{
			RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{
					{
						ParentRef: targetParent,
						Conditions: []metav1.Condition{
							{Type: string(gatewayv1.RouteConditionAccepted), Status: metav1.ConditionTrue},
							{Type: "Programmed", Status: metav1.ConditionTrue},
						},
					},
					{
						ParentRef: otherParent,
						Conditions: []metav1.Condition{
							{Type: string(gatewayv1.RouteConditionAccepted), Status: metav1.ConditionTrue},
						},
					},
				},
			},
		},
	}
	fc := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tr).
		WithStatusSubresource(&gatewayv1.TLSRoute{}).
		Build()
	r := &GatewayReconciler{Client: fc}
	cond := buildDNSReadyCondition(nil)
	assert.Zero(t, cond.ObservedGeneration)
	require.NoError(t, updateRouteParentDNSCondition(ctx, r, gwNN, types.NamespacedName{Namespace: "ns1", Name: "tls1"}, cond, getTLSRouteParentStatus))

	updated := &gatewayv1.TLSRoute{}
	require.NoError(t, fc.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: "tls1"}, updated))
	require.Len(t, updated.Status.Parents, 2)

	assert.Equal(t, gatewayv1.ObjectName("gw1"), updated.Status.Parents[0].ParentRef.Name)
	var gwDNS *metav1.Condition
	for i := range updated.Status.Parents[0].Conditions {
		if updated.Status.Parents[0].Conditions[i].Type == conditionTypeDNSReady {
			gwDNS = &updated.Status.Parents[0].Conditions[i]
			break
		}
	}
	require.NotNil(t, gwDNS)
	assert.Equal(t, metav1.ConditionTrue, gwDNS.Status)
	assert.Equal(t, reasonDNSRecordConfigured, gwDNS.Reason)
	assert.Equal(t, int64(42), gwDNS.ObservedGeneration)

	assert.Equal(t, gatewayv1.ObjectName("other-gw"), updated.Status.Parents[1].ParentRef.Name)
	for _, c := range updated.Status.Parents[1].Conditions {
		assert.NotEqual(t, conditionTypeDNSReady, c.Type, "non-matching parent must not get DNSReady")
	}
}

/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package gateway

import (
	"cmp"
	"context"
	"fmt"
	"github.com/vmware-tanzu/nsx-operator/pkg/controllers/common"
	extdns "github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/endpoint"
	extdnssrc "github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/source"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/retry"
	"slices"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/dns"
)

// routeParentGatewayIndex indexes HTTPRoute, GRPCRoute, and TLSRoute by parent Gateway namespaced name.
const routeParentGatewayIndex = "routeParentGateway"

// routeEnqueueHandler returns a handler that maps Gateway API Route events to parent Gateway reconcile requests.
func (r *GatewayReconciler) routeEnqueueHandler() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(r.routesToGatewayMapFunc)
}

func (r *GatewayReconciler) routesToGatewayMapFunc(ctx context.Context, obj client.Object) []reconcile.Request {
	var requests []reconcile.Request
	parent := parentGatewaysFromRouteObject(obj)
	if parent == nil {
		return requests
	}

	gw := &gatewayv1.Gateway{}
	if err := r.Client.Get(ctx, *parent, gw); err != nil {
		log.Error(err, "Failed to fetch the parent Gateway for Route", "Gateway", parent.String())
		return requests
	}

	if !shouldProcessGateway(gw) {
		log.Debug("Skipping Route enqueue: Parent Gateway is not managed", "Gateway", parent.String(),
			"Route", fmt.Sprintf("%s/%s", obj.GetNamespace(), obj.GetName()))
		return requests
	}
	log.Debug("Route enqueue: reconcile parent Gateway", "Gateway", parent.String(),
		"Route", fmt.Sprintf("%s/%s", obj.GetNamespace(), obj.GetName()))

	return append(requests, reconcile.Request{NamespacedName: *parent})
}

// parentGatewaysFromRouteObject extracts parent Gateway names from HTTPRoute, GRPCRoute, or TLSRoute parentRefs.
func parentGatewaysFromRouteObject(obj client.Object) *types.NamespacedName {
	switch o := obj.(type) {
	case *gatewayv1.HTTPRoute:
		return parentGatewaysFromParentRefs(o.Namespace, o.Spec.ParentRefs)
	case *gatewayv1.GRPCRoute:
		return parentGatewaysFromParentRefs(o.Namespace, o.Spec.ParentRefs)
	case *gatewayv1.TLSRoute:
		return parentGatewaysFromParentRefs(o.Namespace, o.Spec.ParentRefs)
	default:
		return nil
	}
}

func parentGatewaysFromParentRefs(routeNamespace string, refs []gatewayv1.ParentReference) *types.NamespacedName {
	for i := range refs {
		ref := &refs[i]
		kind := dns.ResourceKindGateway
		if ref.Kind != nil && string(*ref.Kind) != "" {
			kind = string(*ref.Kind)
		}
		if kind != dns.ResourceKindGateway {
			continue
		}
		group := gatewayv1.GroupName
		if ref.Group != nil && string(*ref.Group) != "" {
			group = string(*ref.Group)
		}
		if group != gatewayv1.GroupName {
			continue
		}
		if ref.Name == "" {
			continue
		}
		ns := routeNamespace
		if ref.Namespace != nil && string(*ref.Namespace) != "" {
			ns = string(*ref.Namespace)
		}
		return &types.NamespacedName{Namespace: ns, Name: string(ref.Name)}
	}
	return nil
}

func routeParentGatewayIndexFunc(obj client.Object) []string {
	parent := parentGatewaysFromRouteObject(obj)
	if parent == nil {
		return []string{}
	}
	return []string{parent.String()}
}

func getHTTPRouteReference(hr *gatewayv1.HTTPRoute) *dns.ResourceRef {
	return &dns.ResourceRef{
		Kind:   dns.ResourceKindHTTPRoute,
		Object: hr.GetObjectMeta(),
	}
}

func getGRPCRouteReference(gr *gatewayv1.GRPCRoute) *dns.ResourceRef {
	return &dns.ResourceRef{
		Kind:   dns.ResourceKindGRPCRoute,
		Object: gr.GetObjectMeta(),
	}
}

func getTLSRouteReference(tr *gatewayv1.TLSRoute) *dns.ResourceRef {
	return &dns.ResourceRef{
		Kind:   dns.ResourceKindTLSRoute,
		Object: tr.GetObjectMeta(),
	}
}

var (
	listHTTPRouteItems = func(l *gatewayv1.HTTPRouteList) []gatewayv1.HTTPRoute { return l.Items }
	listGRPCRouteItems = func(l *gatewayv1.GRPCRouteList) []gatewayv1.GRPCRoute { return l.Items }
	listTLSRouteItems  = func(l *gatewayv1.TLSRouteList) []gatewayv1.TLSRoute { return l.Items }
	getHTTPRouteInfo   = func(rt *gatewayv1.HTTPRoute) (*metav1.ObjectMeta, []gatewayv1.Hostname, *dns.ResourceRef) {
		return &rt.ObjectMeta, rt.Spec.Hostnames, getHTTPRouteReference(rt)
	}
	getGRPCRouteInfo = func(rt *gatewayv1.GRPCRoute) (*metav1.ObjectMeta, []gatewayv1.Hostname, *dns.ResourceRef) {
		return &rt.ObjectMeta, rt.Spec.Hostnames, getGRPCRouteReference(rt)
	}
	getTLSRouteInfo = func(rt *gatewayv1.TLSRoute) (*metav1.ObjectMeta, []gatewayv1.Hostname, *dns.ResourceRef) {
		return &rt.ObjectMeta, rt.Spec.Hostnames, getTLSRouteReference(rt)
	}
	checkHTTPRouteParentReady = func(rt *gatewayv1.HTTPRoute, nn types.NamespacedName) bool {
		return extdnssrc.HTTPRouteParentReadyForGateway(rt, nn)
	}
	checkGRPCRouteParentReady = func(rt *gatewayv1.GRPCRoute, nn types.NamespacedName) bool {
		return extdnssrc.GRPCRouteParentReadyForGateway(rt, nn)
	}
	checkTLSRouteParentReady = func(rt *gatewayv1.TLSRoute, nn types.NamespacedName) bool {
		return extdnssrc.TLSRouteParentReadyForGateway(rt, nn)
	}
	getHTTPRouteParentStatus = func(hr *gatewayv1.HTTPRoute) []gatewayv1.RouteParentStatus {
		return hr.Status.Parents
	}
	getGRPCRouteParentStatus = func(hr *gatewayv1.GRPCRoute) []gatewayv1.RouteParentStatus {
		return hr.Status.Parents
	}
	getTLSRouteParentStatus = func(hr *gatewayv1.TLSRoute) []gatewayv1.RouteParentStatus {
		return hr.Status.Parents
	}
)

// collectRouteEndpoints is a generic helper that processes various Gateway API Route types
// (HTTPRoute, GRPCRoute, TLSRoute) to generate DNS OwnerEndpoints.
// It performs the following steps:
// 1. Lists routes associated with the parent Gateway using a field index.
// 2. Sorts routes by Namespace and Name to ensure deterministic DNS record ownership.
// 3. Filters routes based on their status (Accepted and Programmed) using ExternalDNS logic.
// 4. Extracts desired hostnames, respecting both Spec and external-dns annotations.
// 5. Cross-references Route hostnames against allowed filters from Gateway Listeners and ListenerSets.
func collectRouteEndpoints[T any, L any, PL interface {
	client.ObjectList
	*L
}](
	ctx context.Context,
	r *GatewayReconciler,
	gwRef *dns.ResourceRef,
	targets extdns.Targets,
	gwNSType common.NameSpaceType,
	allowedFilters []string,
	seenFQDNs sets.Set[string],
	batches *[]*dns.OwnerEndpoints,
	getItems func(*L) []T,
	getRouteInfo func(*T) (*metav1.ObjectMeta, []gatewayv1.Hostname, *dns.ResourceRef),
	isReady func(*T, types.NamespacedName) bool,
) error {
	gwNN := types.NamespacedName{Namespace: gwRef.GetNamespace(), Name: gwRef.GetName()}
	list := PL(new(L))
	if err := r.Client.List(ctx, list, client.MatchingFields{routeParentGatewayIndex: gwNN.String()}); err != nil {
		return err
	}
	items := getItems((*L)(list))

	slices.SortFunc(items, func(a, b T) int {
		metaA, _, _ := getRouteInfo(&a)
		metaB, _, _ := getRouteInfo(&b)
		if c := cmp.Compare(metaA.GetNamespace(), metaB.GetNamespace()); c != 0 {
			return c
		}
		return cmp.Compare(metaA.GetName(), metaB.GetName())
	})

	for i := range items {
		route := &items[i]

		if !isReady(route, gwNN) {
			continue
		}

		meta, specHostnames, routeRef := getRouteInfo(route)

		desiredHostnames, err := extdnssrc.RouteHostnames(meta, extdnssrc.RouteSpecHostnames(specHostnames), false)
		if err != nil {
			continue
		}

		if err = appendOwnerEndpointsForRoute(seenFQDNs, batches, gwRef, targets, gwNSType, meta,
			desiredHostnames, routeRef, allowedFilters); err != nil {
			return err
		}
	}
	return nil
}

// updateRouteParentDNSCondition is a generic helper to update the DNS status condition
// for a specific parent Gateway of a Route (HTTPRoute, GRPCRoute, or TLSRoute).
// It uses a retry-on-conflict strategy to ensure reliable status updates.
func updateRouteParentDNSCondition[T any, PT interface {
	client.Object
	*T
}](
	ctx context.Context,
	r *GatewayReconciler,
	gwNN, routeKey types.NamespacedName,
	cond metav1.Condition,
	getParents func(PT) []gatewayv1.RouteParentStatus,
) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		route := PT(new(T))
		if err := r.Client.Get(ctx, routeKey, route); err != nil {
			return err
		}

		cond.ObservedGeneration = route.GetGeneration()
		parents := getParents(route)

		found := false
		for i := range parents {
			if !extdnssrc.ParentRefMatchesGateway(&parents[i].ParentRef, route.GetNamespace(), gwNN) {
				continue
			}
			parents[i].Conditions = mergeDNSReadyCondition(parents[i].Conditions, cond)
			found = true
			break
		}

		if !found {
			return nil
		}

		return r.Client.Status().Update(ctx, route)
	})
}

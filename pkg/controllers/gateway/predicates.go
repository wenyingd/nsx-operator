/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package gateway

import (
	"context"
	"maps"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	extdnssrc "github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/source"
)

// isManagedRouteForDNS checks if the Route is associated with any Gateway
// that uses a managed GatewayClass.
func (r *GatewayReconciler) isManagedRouteForDNS(ctx context.Context, obj client.Object) bool {
	parent := parentGatewaysFromRouteObject(obj)
	gw := &gatewayv1.Gateway{}
	if err := r.Client.Get(ctx, *parent, gw); err != nil {
		return false
	}
	if shouldProcessGateway(gw) {
		return true
	}
	return false
}

// isListenerSetUnderManagedGateway checks if the ListenerSet belongs to a managed Gateway.
func (r *GatewayReconciler) isListenerSetUnderManagedGateway(ctx context.Context, ls *gatewayv1.ListenerSet) bool {
	parent := findParentGatewayFromListenerSet(ls)
	if parent == nil {
		return false
	}
	gw := &gatewayv1.Gateway{}
	if err := r.Client.Get(ctx, *parent, gw); err != nil {
		return false
	}
	return shouldProcessGateway(gw)
}

func (r *GatewayReconciler) predicateFuncsHTTPRoute() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			hr := e.Object.(*gatewayv1.HTTPRoute)
			meta := routeObjectMeta(e.Object)
			if meta == nil {
				return false
			}
			h, err := extdnssrc.RouteHostnames(meta, extdnssrc.RouteSpecHostnames(hr.Spec.Hostnames), false)
			if err != nil || len(extdnssrc.NormalizeHostnameStrings(h)) == 0 {
				return false
			}
			return r.isManagedRouteForDNS(context.Background(), e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldObj := e.ObjectOld.(*gatewayv1.HTTPRoute)
			newObj := e.ObjectNew.(*gatewayv1.HTTPRoute)

			managed := r.isManagedRouteForDNS(context.Background(), newObj)
			if !managed && !r.isManagedRouteForDNS(context.Background(), oldObj) {
				return false
			}

			// Trigger if DNS annotations changed
			if !maps.Equal(oldObj.GetAnnotations(), newObj.GetAnnotations()) {
				return true
			}
			// Trigger if Spec hostnames or ParentRefs changed
			if !reflect.DeepEqual(oldObj.Spec.Hostnames, newObj.Spec.Hostnames) ||
				!reflect.DeepEqual(oldObj.Spec.ParentRefs, newObj.Spec.ParentRefs) {
				return true
			}
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return r.isManagedRouteForDNS(context.Background(), e.Object)
		},
	}
}

// predicateFuncsGRPCRoute defines filtering logic for GRPCRoute events.
func (r *GatewayReconciler) predicateFuncsGRPCRoute() predicate.Funcs {
	return r.genericRoutePredicate(func(o client.Object) []gatewayv1.Hostname {
		return o.(*gatewayv1.GRPCRoute).Spec.Hostnames
	})
}

// predicateFuncsTLSRoute defines filtering logic for TLSRoute events.
func (r *GatewayReconciler) predicateFuncsTLSRoute() predicate.Funcs {
	return r.genericRoutePredicate(func(o client.Object) []gatewayv1.Hostname {
		return o.(*gatewayv1.TLSRoute).Spec.Hostnames
	})
}

func routeObjectMeta(o client.Object) *metav1.ObjectMeta {
	switch t := o.(type) {
	case *gatewayv1.HTTPRoute:
		return &t.ObjectMeta
	case *gatewayv1.GRPCRoute:
		return &t.ObjectMeta
	case *gatewayv1.TLSRoute:
		return &t.ObjectMeta
	default:
		return nil
	}
}

// genericRoutePredicate provides a unified way to handle different Route types
func (r *GatewayReconciler) genericRoutePredicate(getHostnames func(client.Object) []gatewayv1.Hostname) predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			meta := routeObjectMeta(e.Object)
			if meta == nil {
				return false
			}
			h, err := extdnssrc.RouteHostnames(meta, extdnssrc.RouteSpecHostnames(getHostnames(e.Object)), false)
			if err != nil || len(extdnssrc.NormalizeHostnameStrings(h)) == 0 {
				return false
			}
			return r.isManagedRouteForDNS(context.Background(), e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			if !maps.Equal(e.ObjectOld.GetAnnotations(), e.ObjectNew.GetAnnotations()) {
				return true
			}
			// Fallback to deep equal for spec changes if needed, but annotations are primary for DNS
			return r.isManagedRouteForDNS(context.Background(), e.ObjectNew)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return r.isManagedRouteForDNS(context.Background(), e.Object)
		},
	}
}

// listenerSetDNSRelevantSpecEqual reports whether two ListenerSets are equivalent for DNS:
// same parent reference and the same multiset of listener hostnames (order-insensitive).
func listenerSetDNSRelevantSpecEqual(a, b *gatewayv1.ListenerSet) bool {
	if !reflect.DeepEqual(a.Spec.ParentRef, b.Spec.ParentRef) {
		return false
	}
	return sortedStringSliceEqual(extdnssrc.ListenerSetEntryHostnames(a.Spec.Listeners), extdnssrc.ListenerSetEntryHostnames(b.Spec.Listeners))
}

// predicateFuncsListenerSet defines filtering logic for ListenerSet events.
func (r *GatewayReconciler) predicateFuncsListenerSet() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			ls := e.Object.(*gatewayv1.ListenerSet)
			if len(collectHostnamesFromListenerSet(*ls)) == 0 {
				return false
			}
			return r.isListenerSetUnderManagedGateway(context.Background(), ls)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldObj := e.ObjectOld.(*gatewayv1.ListenerSet)
			newObj := e.ObjectNew.(*gatewayv1.ListenerSet)
			if !r.isListenerSetUnderManagedGateway(context.Background(), newObj) {
				return false
			}
			// DNS-relevant spec: parent ref + multiset of listener hostnames (order-insensitive).
			// Ignore Listener entry order-only permutations and other non-DNS spec noise.
			return !listenerSetDNSRelevantSpecEqual(oldObj, newObj) ||
				!maps.Equal(oldObj.GetAnnotations(), newObj.GetAnnotations())
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return r.isListenerSetUnderManagedGateway(context.Background(), e.Object.(*gatewayv1.ListenerSet))
		},
	}
}

// predicateFuncsGateway defines filtering logic for Gateway events.
var predicateFuncsGateway = predicate.Funcs{
	CreateFunc: func(e event.CreateEvent) bool {
		gw := e.Object.(*gatewayv1.Gateway)
		return shouldProcessGateway(gw) && hasUsableGatewayIP(gw)
	},
	UpdateFunc: func(e event.UpdateEvent) bool {
		oldObj := e.ObjectOld.(*gatewayv1.Gateway)
		newObj := e.ObjectNew.(*gatewayv1.Gateway)

		// 1. GatewayClass transition (managed <-> unmanaged)
		if oldObj.Spec.GatewayClassName != newObj.Spec.GatewayClassName {
			return shouldProcessGateway(oldObj) || shouldProcessGateway(newObj)
		}

		if !shouldProcessGateway(newObj) {
			return false
		}

		// 2. DNS relevant Annotations changed
		if !maps.Equal(oldObj.GetAnnotations(), newObj.GetAnnotations()) {
			return true
		}

		// 3. Listeners changed (may change allowed DNS filters)
		if !reflect.DeepEqual(oldObj.Spec.Listeners, newObj.Spec.Listeners) {
			return true
		}

		// 4. IP Addresses changed
		return !reflect.DeepEqual(oldObj.Status.Addresses, newObj.Status.Addresses)
	},
	DeleteFunc: func(e event.DeleteEvent) bool {
		return shouldProcessGateway(e.Object.(*gatewayv1.Gateway))
	},
}

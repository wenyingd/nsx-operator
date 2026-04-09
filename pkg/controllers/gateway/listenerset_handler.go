/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package gateway

import (
	"context"
	"slices"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/dns"
)

// enqueueManagedGatewayForListenerSet maps ListenerSet events to reconcile requests for parent
// Gateways that use a managed GatewayClass. On Update, the old and new parent references are
// considered independently: each is enqueued only if that Gateway is managed. If ParentRef moves
// from a managed Gateway to an unmanaged one, only the former is queued so DNS under the old
// Gateway can be released; the unmanaged parent is intentionally not queued.
type enqueueManagedGatewayForListenerSet struct {
	Client client.Client
}

var _ handler.EventHandler = (*enqueueManagedGatewayForListenerSet)(nil)

func (r *GatewayReconciler) listenerSetEnqueueHandler() handler.EventHandler {
	return &enqueueManagedGatewayForListenerSet{Client: r.Client}
}

func (e *enqueueManagedGatewayForListenerSet) Create(ctx context.Context, evt event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	e.enqueue(ctx, evt.Object, q)
}

func (e *enqueueManagedGatewayForListenerSet) Update(ctx context.Context, evt event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	e.enqueue(ctx, evt.ObjectOld, q)
	e.enqueue(ctx, evt.ObjectNew, q)
}

func (e *enqueueManagedGatewayForListenerSet) Delete(ctx context.Context, evt event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	e.enqueue(ctx, evt.Object, q)
}

func (e *enqueueManagedGatewayForListenerSet) Generic(ctx context.Context, evt event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	e.enqueue(ctx, evt.Object, q)
}

func (e *enqueueManagedGatewayForListenerSet) enqueue(ctx context.Context, obj client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	parentGateway := findParentGatewayFromListenerSet(obj)
	if parentGateway == nil {
		return
	}
	gw := &gatewayv1.Gateway{}
	if err := e.Client.Get(ctx, *parentGateway, gw); err != nil {
		log.Error(err, "Failed to fetch the parent Gateway", "Gateway", parentGateway)
		return
	}
	if !shouldProcessGateway(gw) {
		return
	}
	q.Add(reconcile.Request{NamespacedName: *parentGateway})
}

func findParentGatewayFromListenerSet(obj client.Object) *types.NamespacedName {
	ls, ok := obj.(*gatewayv1.ListenerSet)
	if !ok || ls == nil {
		return nil
	}

	parent := ls.Spec.ParentRef
	// Check if the ListenerSet is referring to a Gateway
	if parent.Kind != nil && string(*parent.Kind) != dns.ResourceKindGateway {
		return nil
	}
	if parent.Group != nil && string(*parent.Group) != gatewayv1.GroupName {
		return nil
	}
	// Ignore the ListenerSet if it does not refer to any K8s Gateway resource.
	// This is for security purpose.
	if parent.Name == "" {
		return nil
	}

	// Parse the parent Gateway's namespace. If the Namespace is not specified in the parent reference,
	// use the ListenerSet's namespace.
	gwNS := ls.Namespace
	if parent.Namespace != nil && string(*parent.Namespace) != "" {
		gwNS = string(*parent.Namespace)
	}

	return &types.NamespacedName{Namespace: gwNS, Name: string(parent.Name)}
}

func collectHostnamesFromListenerSet(ls gatewayv1.ListenerSet) []string {
	var hostnames []string
	for _, l := range ls.Spec.Listeners {
		if l.Hostname != nil {
			h := strings.TrimSpace(string(*l.Hostname))
			if h != "" {
				hostnames = append(hostnames, h)
			}
		}
	}
	return hostnames
}

func listenerSetParentGatewayIndexFunc(obj client.Object) []string {
	parentGateway := findParentGatewayFromListenerSet(obj)
	if parentGateway == nil {
		return []string{}
	}
	return []string{parentGateway.String()}
}

var predicateFuncsListenerSet = predicate.Funcs{
	CreateFunc: func(e event.CreateEvent) bool {
		ls := e.Object.(*gatewayv1.ListenerSet)
		return len(collectHostnamesFromListenerSet(*ls)) > 0
	},
	UpdateFunc: func(e event.UpdateEvent) bool {
		oldObj := e.ObjectOld.(*gatewayv1.ListenerSet)
		newObj := e.ObjectNew.(*gatewayv1.ListenerSet)
		oldHostnames := collectHostnamesFromListenerSet(*oldObj)
		oldGateway := findParentGatewayFromListenerSet(oldObj)
		newHostnames := collectHostnamesFromListenerSet(*newObj)
		newGateway := findParentGatewayFromListenerSet(newObj)
		if sliceEquals(oldHostnames, newHostnames) && gatewayEquals(oldGateway, newGateway) {
			return false
		}
		return true
	},
	DeleteFunc: func(e event.DeleteEvent) bool {
		return true
	},
}

func gatewayEquals(old, new *types.NamespacedName) bool {
	if old == nil || new == nil {
		return old == new
	}
	return old.String() == new.String()
}

func sliceEquals(old, new []string) bool {
	if len(old) != len(new) {
		return false
	}
	oldCopy := append([]string(nil), old...)
	newCopy := append([]string(nil), new...)

	sort.Strings(oldCopy)
	sort.Strings(newCopy)

	return slices.Equal(oldCopy, newCopy)
}

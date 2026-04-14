/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package gateway

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/dns"
	extdnssrc "github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/source"
)

func (r *GatewayReconciler) listenerSetEnqueueHandler() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(r.listenerSetToGatewayMapFunc)
}

func (r *GatewayReconciler) listenerSetToGatewayMapFunc(ctx context.Context, obj client.Object) []reconcile.Request {
	var requests []reconcile.Request
	ls, ok := obj.(*gatewayv1.ListenerSet)
	if !ok {
		return requests
	}

	parent := findParentGatewayFromListenerSet(ls)
	if parent == nil {
		return requests
	}

	gw := &gatewayv1.Gateway{}
	if err := r.Client.Get(ctx, *parent, gw); err != nil {
		log.Error(err, "Failed to fetch the parent Gateway", "Gateway", parent.String())
		return requests
	}

	if !shouldProcessGateway(gw) {
		log.Debug("Skipping ListenerSet enqueue: Parent Gateway is not managed", "Gateway", parent.String(),
			"ListenerSet", fmt.Sprintf("%s/%s", ls.Namespace, ls.Name))
		return requests
	}
	log.Debug("ListenerSet enqueue: reconcile parent Gateway", "Gateway", parent.String(),
		"ListenerSet", fmt.Sprintf("%s/%s", ls.Namespace, ls.Name))

	return append(requests, reconcile.Request{NamespacedName: *parent})
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
	return extdnssrc.GetDesiredHostnames(&ls, extdnssrc.ListenerSetEntryHostnames(ls.Spec.Listeners))
}

func listenerSetParentGatewayIndexFunc(obj client.Object) []string {
	parentGateway := findParentGatewayFromListenerSet(obj)
	if parentGateway == nil {
		return []string{}
	}
	return []string{parentGateway.String()}
}

func gatewayEquals(old, new *types.NamespacedName) bool {
	if old == nil || new == nil {
		return old == new
	}
	return old.String() == new.String()
}

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
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func Test_sliceEquals(t *testing.T) {
	for _, tt := range []struct {
		name string
		a, b []string
		want bool
	}{
		{"order independent", []string{"b", "a"}, []string{"a", "b"}, true},
		{"length", []string{"a"}, []string{"a", "b"}, false},
		{"nil and empty", nil, []string{}, true},
		{"same", []string{"x", "y"}, []string{"x", "y"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sliceEquals(tt.a, tt.b))
		})
	}
}

func Test_predicateFuncsListenerSet(t *testing.T) {
	oldMulti := newTestListenerSetMulti("ls1", "ns1", "gw1", "a.example.com", "b.example.com")
	newOrder := oldMulti.DeepCopy()
	newOrder.Spec.Listeners = []gatewayv1.ListenerEntry{
		{Name: "l2", Hostname: ptr(gatewayv1.Hostname("b.example.com"))},
		{Name: "l1", Hostname: ptr(gatewayv1.Hostname("a.example.com"))},
	}

	for _, tt := range []struct {
		name string
		want bool
		fn   func() bool
	}{
		{"Create/with hostname", true, func() bool {
			return predicateFuncsListenerSet.Create(event.CreateEvent{Object: newTestListenerSet("ls1", "ns1", "gw1", "h.example.com")})
		}},
		{"Create/no hostname", false, func() bool {
			ls := newTestListenerSet("ls1", "ns1", "gw1", "h.example.com")
			ls.Spec.Listeners[0].Hostname = nil
			return predicateFuncsListenerSet.Create(event.CreateEvent{Object: ls})
		}},
		{"Update/parent changed", true, func() bool {
			o := newTestListenerSet("ls1", "ns1", "gw-a", "h.example.com")
			n := o.DeepCopy()
			n.Spec.ParentRef.Name = gatewayv1.ObjectName("gw-b")
			return predicateFuncsListenerSet.Update(event.UpdateEvent{ObjectOld: o, ObjectNew: n})
		}},
		{"Update/hostname changed", true, func() bool {
			return predicateFuncsListenerSet.Update(event.UpdateEvent{
				ObjectOld: newTestListenerSet("ls1", "ns1", "gw1", "a.example.com"),
				ObjectNew: newTestListenerSet("ls1", "ns1", "gw1", "b.example.com"),
			})
		}},
		{"Update/hostname order only", false, func() bool {
			return predicateFuncsListenerSet.Update(event.UpdateEvent{ObjectOld: oldMulti, ObjectNew: newOrder})
		}},
		{"Update/no-op", false, func() bool {
			ls := newTestListenerSet("ls1", "ns1", "gw1", "h.example.com")
			return predicateFuncsListenerSet.Update(event.UpdateEvent{ObjectOld: ls, ObjectNew: ls.DeepCopy()})
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.fn())
		})
	}
}

func Test_enqueueManagedGatewayForListenerSet_Update_parentRef(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns1"}}
	lsForParent := func(parent string) *gatewayv1.ListenerSet {
		return newTestListenerSet("ls1", "ns1", parent, "app.example.com")
	}

	for _, tt := range []struct {
		name      string
		objects   []client.Object
		oldParent string
		newParent string
		want      []types.NamespacedName
	}{
		{
			name: "managed to managed",
			objects: []client.Object{
				ns,
				newTestGateway("gwa", "ns1", "10.0.0.1", false, nil),
				newTestGateway("gwb", "ns1", "10.0.0.2", false, nil),
			},
			oldParent: "gwa", newParent: "gwb",
			want: []types.NamespacedName{{Namespace: "ns1", Name: "gwa"}, {Namespace: "ns1", Name: "gwb"}},
		},
		{
			name: "managed to unmanaged new parent",
			objects: []client.Object{
				ns,
				newTestGateway("gwa", "ns1", "10.0.0.1", false, nil),
				newTestGateway("gwb", "ns1", "10.0.0.2", false, func(g *gatewayv1.Gateway) {
					g.Spec.GatewayClassName = gatewayv1.ObjectName("other")
				}),
			},
			oldParent: "gwa", newParent: "gwb",
			want: []types.NamespacedName{{Namespace: "ns1", Name: "gwa"}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fc := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.objects...).
				WithStatusSubresource(&gatewayv1.Gateway{}).
				Build()
			oldLS := lsForParent(tt.oldParent)
			newLS := lsForParent(tt.newParent)
			h := &enqueueManagedGatewayForListenerSet{Client: fc}
			q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
			h.Update(context.Background(), event.UpdateEvent{ObjectOld: oldLS, ObjectNew: newLS}, q)
			got := drainQueueNN(t, q)
			assert.ElementsMatch(t, tt.want, got)
		})
	}
}

func drainQueueNN(t *testing.T, q workqueue.TypedRateLimitingInterface[reconcile.Request]) []types.NamespacedName {
	t.Helper()
	var out []types.NamespacedName
	for q.Len() > 0 {
		req, shutdown := q.Get()
		require.False(t, shutdown)
		out = append(out, req.NamespacedName)
		q.Done(req)
	}
	return out
}

package gateway

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/vmware-tanzu/nsx-operator/pkg/apis/vpc/v1alpha1"
	mockclient "github.com/vmware-tanzu/nsx-operator/pkg/mock/controller-runtime/client"
	servicecommon "github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
)

func TestPredicateFuncsRouteDNS(t *testing.T) {
	gr := &genericRouteReconciler[*HTTPRoute, gatewayv1.HTTPRoute, *gatewayv1.HTTPRoute]{kind: "HTTPRoute"}
	p := gr.predicateFuncsRouteDNS()

	httpObj1 := &gatewayv1.HTTPRoute{
		Spec: gatewayv1.HTTPRouteSpec{
			Hostnames: []gatewayv1.Hostname{"a.com"},
		},
	}
	httpObj2 := &gatewayv1.HTTPRoute{
		Spec: gatewayv1.HTTPRouteSpec{
			Hostnames: []gatewayv1.Hostname{"b.com"},
		},
	}

	assert.True(t, p.Create(event.CreateEvent{Object: httpObj1}))
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: httpObj1, ObjectNew: httpObj2}))
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: httpObj1, ObjectNew: httpObj1})) // No change
	assert.True(t, p.Delete(event.DeleteEvent{Object: httpObj1}))
}

func TestPredicateNetworkInfoAllowedDNSDomainsChanged(t *testing.T) {
	p := predicateNetworkInfoAllowedDNSDomainsChanged()

	ni1 := &v1alpha1.NetworkInfo{
		VPCs: []v1alpha1.VPCState{},
		// We'll mock the object and manually check logic or just use a basic struct if fields missing
	}
	ni2 := &v1alpha1.NetworkInfo{
		VPCs: []v1alpha1.VPCState{},
	}
	ni3 := &v1alpha1.NetworkInfo{
		VPCs: []v1alpha1.VPCState{}, // Same
	}

	assert.False(t, p.Create(event.CreateEvent{Object: ni1}))
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: ni1, ObjectNew: ni2}))
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: ni1, ObjectNew: ni3}))
	assert.False(t, p.Delete(event.DeleteEvent{Object: ni1}))
}

func ptrGatewayAddressType(t gatewayv1.AddressType) *gatewayv1.AddressType {
	return &t
}

func TestNetworkInfoToGatewayDNSRequests(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	c := mockclient.NewMockClient(ctrl)
	r := &GatewayReconciler{Client: c}

	c.EXPECT().List(ctx, gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
		gwList := list.(*gatewayv1.GatewayList)
		gwList.Items = []gatewayv1.Gateway{
			{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "ns1",
					Name:      "gw1",
					Annotations: map[string]string{
						servicecommon.AnnotationDNSHostnameKey: "a.com",
					},
				},
				Spec: gatewayv1.GatewaySpec{GatewayClassName: "avi-lb"},
				Status: gatewayv1.GatewayStatus{
					Addresses: []gatewayv1.GatewayStatusAddress{
						{
							Type:  ptrGatewayAddressType(gatewayv1.IPAddressType),
							Value: "1.1.1.1",
						},
					},
				},
			},
		}
		return nil
	})

	ni := &v1alpha1.NetworkInfo{ObjectMeta: metav1.ObjectMeta{Namespace: "ns1"}}
	reqs := r.networkInfoToGatewayDNSRequests(ctx, ni)
	if assert.Len(t, reqs, 1) {
		assert.Equal(t, types.NamespacedName{Namespace: "ns1", Name: "gw1"}, reqs[0].NamespacedName)
	}

	reqs2 := r.networkInfoToGatewayDNSRequests(ctx, &corev1.Pod{})
	assert.Empty(t, reqs2)
}

func TestNetworkInfoToRouteDNSRequests(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	c := mockclient.NewMockClient(ctrl)

	c.EXPECT().List(ctx, gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
		httpList := list.(*gatewayv1.HTTPRouteList)
		httpList.Items = []gatewayv1.HTTPRoute{
			{ObjectMeta: metav1.ObjectMeta{Namespace: "ns1", Name: "r1"}},
		}
		return nil
	})

	ni := &v1alpha1.NetworkInfo{ObjectMeta: metav1.ObjectMeta{Namespace: "ns1"}}

	reqs := networkInfoToRouteDNSRequests[gatewayv1.HTTPRoute, *gatewayv1.HTTPRoute](ctx, c, ni, "HTTPRoute", &gatewayv1.HTTPRouteList{})
	if assert.Len(t, reqs, 1) {
		assert.Equal(t, types.NamespacedName{Namespace: "ns1", Name: "r1"}, reqs[0].NamespacedName)
	}

	reqs2 := networkInfoToRouteDNSRequests[gatewayv1.HTTPRoute, *gatewayv1.HTTPRoute](ctx, c, &corev1.Pod{}, "HTTPRoute", &gatewayv1.HTTPRouteList{})
	assert.Empty(t, reqs2)
}

func TestGatewayNamespacedNameFromListenerParentRef(t *testing.T) {
	gwGroup := gatewayv1.Group("gateway.networking.k8s.io")
	gwKind := gatewayv1.Kind("Gateway")
	ns := gatewayv1.Namespace("test-ns")

	t.Run("Valid", func(t *testing.T) {
		ref := &gatewayv1.ParentGatewayReference{
			Group:     &gwGroup,
			Kind:      &gwKind,
			Name:      "gw1",
			Namespace: &ns,
		}
		nn, ok := gatewayNamespacedNameFromListenerParentRef(ref, "default")
		assert.True(t, ok)
		assert.Equal(t, "test-ns", nn.Namespace)
		assert.Equal(t, "gw1", nn.Name)
	})

	t.Run("Valid no namespace", func(t *testing.T) {
		ref := &gatewayv1.ParentGatewayReference{
			Group: &gwGroup,
			Kind:  &gwKind,
			Name:  "gw1",
		}
		nn, ok := gatewayNamespacedNameFromListenerParentRef(ref, "default")
		assert.True(t, ok)
		assert.Equal(t, "default", nn.Namespace)
		assert.Equal(t, "gw1", nn.Name)
	})

	t.Run("Invalid Group", func(t *testing.T) {
		badGroup := gatewayv1.Group("bad.group")
		ref := &gatewayv1.ParentGatewayReference{
			Group: &badGroup,
			Kind:  &gwKind,
			Name:  "gw1",
		}
		_, ok := gatewayNamespacedNameFromListenerParentRef(ref, "default")
		assert.False(t, ok)
	})

	t.Run("Invalid Kind", func(t *testing.T) {
		badKind := gatewayv1.Kind("BadKind")
		ref := &gatewayv1.ParentGatewayReference{
			Group: &gwGroup,
			Kind:  &badKind,
			Name:  "gw1",
		}
		_, ok := gatewayNamespacedNameFromListenerParentRef(ref, "default")
		assert.False(t, ok)
	})
}

func TestFindParentGatewayFromListenerSet(t *testing.T) {
	gwGroup := gatewayv1.Group("gateway.networking.k8s.io")
	gwKind := gatewayv1.Kind("Gateway")

	t.Run("Valid", func(t *testing.T) {
		ls := &gatewayv1.ListenerSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
			Spec: gatewayv1.ListenerSetSpec{
				ParentRef: gatewayv1.ParentGatewayReference{Group: &gwGroup, Kind: &gwKind, Name: "gw1"},
			},
		}
		nn := findParentGatewayFromListenerSet(ls)
		assert.NotNil(t, nn)
		assert.Equal(t, "default", nn.Namespace)
		assert.Equal(t, "gw1", nn.Name)
	})

	t.Run("Invalid Object", func(t *testing.T) {
		gw := &gatewayv1.Gateway{}
		nn := findParentGatewayFromListenerSet(gw)
		assert.Nil(t, nn)
	})

	t.Run("No Valid Parent", func(t *testing.T) {
		badGroup := gatewayv1.Group("bad.group")
		ls := &gatewayv1.ListenerSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
			Spec: gatewayv1.ListenerSetSpec{
				ParentRef: gatewayv1.ParentGatewayReference{Group: &badGroup, Kind: &gwKind, Name: "gw1"},
			},
		}
		nn := findParentGatewayFromListenerSet(ls)
		assert.Nil(t, nn)
	})
}

func TestListenerSetParentGatewayIndexFunc(t *testing.T) {
	gwGroup := gatewayv1.Group("gateway.networking.k8s.io")
	gwKind := gatewayv1.Kind("Gateway")

	ls := &gatewayv1.ListenerSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
		Spec: gatewayv1.ListenerSetSpec{
			ParentRef: gatewayv1.ParentGatewayReference{Group: &gwGroup, Kind: &gwKind, Name: "gw1"},
		},
	}
	res := listenerSetParentGatewayIndexFunc(ls)
	assert.Len(t, res, 1)
	assert.Equal(t, "default/gw1", res[0])

	gw := &gatewayv1.Gateway{}
	res = listenerSetParentGatewayIndexFunc(gw)
	assert.Len(t, res, 0)
}

func TestListenersSortedByName(t *testing.T) {
	l1 := gatewayv1.Listener{Name: "b"}
	l2 := gatewayv1.Listener{Name: "a"}
	l3 := gatewayv1.Listener{Name: "c"}

	sorted := listenersSortedByName([]gatewayv1.Listener{l1, l2, l3})
	assert.Len(t, sorted, 3)
	assert.Equal(t, gatewayv1.SectionName("a"), sorted[0].Name)
	assert.Equal(t, gatewayv1.SectionName("b"), sorted[1].Name)
	assert.Equal(t, gatewayv1.SectionName("c"), sorted[2].Name)
}

func TestReconcileRequestsFromRouteList(t *testing.T) {
	list := &gatewayv1.HTTPRouteList{
		Items: []gatewayv1.HTTPRoute{
			{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "r1"}},
			{ObjectMeta: metav1.ObjectMeta{Namespace: "ns2", Name: "r2"}},
		},
	}

	reqs := reconcileRequestsFromRouteList(list)
	assert.Len(t, reqs, 2)
	assert.Equal(t, "default", reqs[0].Namespace)
	assert.Equal(t, "r1", reqs[0].Name)
	assert.Equal(t, "ns2", reqs[1].Namespace)
	assert.Equal(t, "r2", reqs[1].Name)

	grpcList := &gatewayv1.GRPCRouteList{
		Items: []gatewayv1.GRPCRoute{
			{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "r3"}},
		},
	}
	reqs = reconcileRequestsFromRouteList(grpcList)
	assert.Len(t, reqs, 1)
	assert.Equal(t, "default", reqs[0].Namespace)
	assert.Equal(t, "r3", reqs[0].Name)

	tlsList := &gatewayv1.TLSRouteList{
		Items: []gatewayv1.TLSRoute{
			{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "r4"}},
		},
	}
	reqs = reconcileRequestsFromRouteList(tlsList)
	assert.Len(t, reqs, 1)
	assert.Equal(t, "default", reqs[0].Namespace)
	assert.Equal(t, "r4", reqs[0].Name)

	reqs = reconcileRequestsFromRouteList(&gatewayv1.GatewayList{})
	assert.Nil(t, reqs)
}

func TestListenerSetToGatewayMapFunc(t *testing.T) {
	ctx := context.Background()
	ctrlMock := gomock.NewController(t)
	defer ctrlMock.Finish()

	c := mockclient.NewMockClient(ctrlMock)
	r := &GatewayReconciler{Client: c}

	gwGroup := gatewayv1.Group("gateway.networking.k8s.io")
	gwKind := gatewayv1.Kind("Gateway")

	ls := &gatewayv1.ListenerSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
		Spec: gatewayv1.ListenerSetSpec{
			ParentRef: gatewayv1.ParentGatewayReference{Group: &gwGroup, Kind: &gwKind, Name: "gw1"},
		},
	}

	c.EXPECT().Get(ctx, types.NamespacedName{Namespace: "default", Name: "gw1"}, gomock.Any()).DoAndReturn(func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		gw := obj.(*gatewayv1.Gateway)
		gw.Spec.GatewayClassName = "avi-lb"
		return nil
	}).Times(1)

	reqs := r.listenerSetToGatewayMapFunc(ctx, ls)
	assert.Len(t, reqs, 1)
	assert.Equal(t, "default", reqs[0].Namespace)
	assert.Equal(t, "gw1", reqs[0].Name)

	// Test invalid object
	gw := &gatewayv1.Gateway{}
	reqs = r.listenerSetToGatewayMapFunc(ctx, gw)
	assert.Len(t, reqs, 0)
}

func TestRouteDNSRequestsForListenerSet(t *testing.T) {
	ctx := context.Background()
	ctrlMock := gomock.NewController(t)
	defer ctrlMock.Finish()

	c := mockclient.NewMockClient(ctrlMock)
	sub := &genericRouteReconciler[*HTTPRoute, gatewayv1.HTTPRoute, *gatewayv1.HTTPRoute]{
		client: c,
		kind:   "HTTPRoute",
	}

	ls := &gatewayv1.ListenerSet{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "ls1"}}

	c.EXPECT().List(ctx, gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
		l := list.(*gatewayv1.HTTPRouteList)
		l.Items = []gatewayv1.HTTPRoute{
			{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "r1"}},
		}
		return nil
	}).Times(1)

	fn := sub.routeDNSRequestsForListenerSet(func() client.ObjectList { return &gatewayv1.HTTPRouteList{} })
	reqs := fn(ctx, ls)
	assert.Len(t, reqs, 1)
	assert.Equal(t, "default", reqs[0].Namespace)
	assert.Equal(t, "r1", reqs[0].Name)

	// Invalid obj
	gw := &gatewayv1.Gateway{}
	reqs = fn(ctx, gw)
	assert.Len(t, reqs, 0)

	// List error
	c.EXPECT().List(ctx, gomock.Any(), gomock.Any()).Return(errors.New("list error")).Times(1)
	reqs = fn(ctx, ls)
	assert.Len(t, reqs, 0)
}

func TestRouteParentGatewayAcceptedByKey(t *testing.T) {
	gwName := gatewayv1.ObjectName("gw")
	ns := gatewayv1.Namespace("ns")

	parents := []gatewayv1.RouteParentStatus{
		{
			ParentRef: gatewayv1.ParentReference{
				Name:      gwName,
				Namespace: &ns,
			},
			Conditions: []metav1.Condition{
				{
					Type:   string(gatewayv1.RouteConditionAccepted),
					Status: metav1.ConditionTrue,
				},
			},
		},
		{
			ParentRef: gatewayv1.ParentReference{
				Name: gatewayv1.ObjectName("gw2"),
			},
			Conditions: []metav1.Condition{
				{
					Type:   "OtherCondition",
					Status: metav1.ConditionTrue,
				},
			},
		},
		{
			ParentRef: gatewayv1.ParentReference{
				Kind: func(k string) *gatewayv1.Kind { x := gatewayv1.Kind(k); return &x }("OtherKind"),
				Name: gatewayv1.ObjectName("gw3"),
			},
		},
	}

	out := routeParentAcceptedByKey("ns", parents)
	assert.Equal(t, map[string]bool{
		"Gateway:ns/gw":    true,
		"Gateway:ns/gw2":   false,
		"OtherKind:ns/gw3": false,
	}, out)
}

func TestConvertToRouteObject(t *testing.T) {
	r1 := (&genericRouteReconciler[*HTTPRoute, gatewayv1.HTTPRoute, *gatewayv1.HTTPRoute]{})
	httpRoute := &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "r1"}}
	obj1, ok := r1.convertToRouteObject(httpRoute)
	assert.True(t, ok)
	assert.NotNil(t, obj1)

	r2 := (&genericRouteReconciler[*GRPCRoute, gatewayv1.GRPCRoute, *gatewayv1.GRPCRoute]{})
	grpcRoute := &gatewayv1.GRPCRoute{ObjectMeta: metav1.ObjectMeta{Name: "r2"}}
	obj2, ok := r2.convertToRouteObject(grpcRoute)
	assert.True(t, ok)
	assert.NotNil(t, obj2)

	r3 := (&genericRouteReconciler[*TLSRoute, gatewayv1.TLSRoute, *gatewayv1.TLSRoute]{})
	tlsRoute := &gatewayv1.TLSRoute{ObjectMeta: metav1.ObjectMeta{Name: "r3"}}
	obj3, ok := r3.convertToRouteObject(tlsRoute)
	assert.True(t, ok)
	assert.NotNil(t, obj3)

	_, ok = r1.convertToRouteObject(&gatewayv1.Gateway{})
	assert.False(t, ok)

	_, ok = r1.convertToRouteObject(nil)
	assert.False(t, ok)
}

package gateway

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery/fake"
	k8stesting "k8s.io/client-go/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/vmware-tanzu/nsx-operator/pkg/controllers/common"
	mockclient "github.com/vmware-tanzu/nsx-operator/pkg/mock/controller-runtime/client"
	mockdns "github.com/vmware-tanzu/nsx-operator/pkg/mock/dnsrecordprovider"
	mockgateway "github.com/vmware-tanzu/nsx-operator/pkg/mock/gateway"
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/dns"
	extdns "github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/endpoint"
)

func TestGatewayReconciler_ipsToTargets(t *testing.T) {
	gw := []net.IP{
		net.ParseIP("1.1.1.1"),
		net.ParseIP("2.2.2.2"),
	}
	targets := ipsToTargets(gw)
	assert.Len(t, targets, 2)
	assert.Contains(t, targets, "1.1.1.1")
	assert.Contains(t, targets, "2.2.2.2")
}

func TestGatewayReconciler_buildDNSRecordReadyCondition(t *testing.T) {
	c := buildDNSRecordReadyCondition(nil)
	assert.Equal(t, conditionTypeDNSRecordReady, c.Type)
	assert.Equal(t, metav1.ConditionTrue, c.Status)
	assert.Equal(t, reasonDNSRecordConfigured, c.Reason)

	err := errors.New("test error")
	cErr := buildDNSRecordReadyCondition(err)
	assert.Equal(t, conditionTypeDNSRecordReady, cErr.Type)
	assert.Equal(t, metav1.ConditionFalse, cErr.Status)
	assert.Equal(t, reasonDNSRecordFailed, cErr.Reason)
	assert.Equal(t, "test error", cErr.Message)
}

func TestGatewayReconciler_updateGatewayDNSReadyCondition(t *testing.T) {
	ctx := context.Background()
	ctrlMock := gomock.NewController(t)
	defer ctrlMock.Finish()

	c := mockclient.NewMockClient(ctrlMock)
	r := &GatewayReconciler{Client: c}

	gwNN := types.NamespacedName{Namespace: "default", Name: "gw"}

	// Should ignore NotFound
	c.EXPECT().Get(ctx, gwNN, gomock.Any()).Return(apierrors.NewNotFound(schema.GroupResource{}, "gw")).Times(1)
	err := r.updateGatewayDNSReadyCondition(ctx, gwNN, nil)
	assert.NoError(t, err)

	// Should update condition
	// It's hard to mock client.SubResourceWriter, so we skip the exact update call test

	// No condition change needed
	c.EXPECT().Get(ctx, gwNN, gomock.Any()).DoAndReturn(func(ctx context.Context, key types.NamespacedName, obj *gatewayv1.Gateway, opts ...client.GetOption) error {
		obj.Status.Conditions = []metav1.Condition{buildDNSRecordReadyCondition(nil)}
		return nil
	}).Times(1)
	err = r.updateGatewayDNSReadyCondition(ctx, gwNN, nil)
	assert.NoError(t, err)
}

func TestGatewayReconciler_removeGatewayDNSConfigCondition(t *testing.T) {
	ctx := context.Background()
	ctrlMock := gomock.NewController(t)
	defer ctrlMock.Finish()

	c := mockclient.NewMockClient(ctrlMock)
	r := &GatewayReconciler{Client: c}

	gwNN := types.NamespacedName{Namespace: "default", Name: "gw"}

	// Should ignore NotFound
	c.EXPECT().Get(ctx, gwNN, gomock.Any()).Return(apierrors.NewNotFound(schema.GroupResource{}, "gw")).Times(1)
	err := r.removeGatewayDNSConfigCondition(ctx, gwNN)
	assert.NoError(t, err)
}

func TestGatewayReconciler_refreshGatewayIPCache(t *testing.T) {
	r := &GatewayReconciler{ipCache: NewGatewayIPCache()}

	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "gw"},
		Status: gatewayv1.GatewayStatus{
			Addresses: []gatewayv1.GatewayStatusAddress{
				{Type: ptrGatewayAddressType(gatewayv1.IPAddressType), Value: "1.1.1.1"},
			},
		},
	}

	lsList := []gatewayv1.ListenerSet{} // no listeners

	_, changed := r.refreshGatewayIPCache(gw, lsList)
	assert.True(t, changed)

	entry, ok := r.ipCache.get(types.NamespacedName{Namespace: "default", Name: "gw"})
	assert.True(t, ok)
	assert.Equal(t, extdns.Targets{"1.1.1.1"}, entry.IPs)

	// Try same update, shouldn't change
	_, changed = r.refreshGatewayIPCache(gw, lsList)
	assert.False(t, changed)
}

func TestGatewayReconciler_hasUsableGatewayIP(t *testing.T) {
	gw := &gatewayv1.Gateway{
		Status: gatewayv1.GatewayStatus{
			Addresses: []gatewayv1.GatewayStatusAddress{
				{Type: ptrGatewayAddressType(gatewayv1.IPAddressType), Value: "1.1.1.1"},
			},
		},
	}
	assert.True(t, hasUsableGatewayIP(gw))

	gw.Status.Addresses = []gatewayv1.GatewayStatusAddress{
		{Type: ptrGatewayAddressType(gatewayv1.HostnameAddressType), Value: "a.com"},
	}
	assert.False(t, hasUsableGatewayIP(gw))

	gw.Status.Addresses = nil
	assert.False(t, hasUsableGatewayIP(gw))
}
func TestGatewayReconciler_enqueueAttachedRoutesForGatewayDNSFromAPI_ListError(t *testing.T) {
	ctx := context.Background()
	ctrlMock := gomock.NewController(t)
	defer ctrlMock.Finish()
	c := mockclient.NewMockClient(ctrlMock)
	r := &GatewayReconciler{Client: c}
	r.apiResources.listenerSetEnabled = true

	gwNN := types.NamespacedName{Namespace: "default", Name: "gw1"}
	c.EXPECT().List(ctx, gomock.Any(), gomock.Any()).Return(errors.New("list error"))

	// Should not panic, logs error and proceeds
	r.enqueueAttachedRoutesForGatewayDNSFromAPI(ctx, gwNN)
}

func TestGatewayReconciler_enqueueAttachedRoutesForGatewayDNSFromAPI(t *testing.T) {
	ctx := context.Background()
	ctrlMock := gomock.NewController(t)
	defer ctrlMock.Finish()

	c := mockclient.NewMockClient(ctrlMock)
	r := &GatewayReconciler{
		Client: c,
		apiResources: gatewayAPIResources{
			listenerSetEnabled: true,
		},
	}

	c.EXPECT().List(ctx, gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
		switch l := list.(type) {
		case *gatewayv1.HTTPRouteList:
			l.Items = []gatewayv1.HTTPRoute{{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "r1"}}}
		case *gatewayv1.GRPCRouteList:
			l.Items = []gatewayv1.GRPCRoute{{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "r1"}}}
		case *gatewayv1.TLSRouteList:
			l.Items = []gatewayv1.TLSRoute{{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "r1"}}}
		case *gatewayv1.ListenerSetList:
			l.Items = []gatewayv1.ListenerSet{{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "ls1"}}}
		}
		return nil
	}).Times(1) // 1 for ListenerSets

	r.enqueueAttachedRoutesForGatewayDNSFromAPI(ctx, types.NamespacedName{Namespace: "default", Name: "gw"})
}

func TestGatewayReconciler_listSortedListenerSetsForGateway(t *testing.T) {
	ctx := context.Background()
	ctrlMock := gomock.NewController(t)
	defer ctrlMock.Finish()

	c := mockclient.NewMockClient(ctrlMock)
	r := &GatewayReconciler{Client: c}

	c.EXPECT().List(ctx, gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
		lsList := list.(*gatewayv1.ListenerSetList)
		lsList.Items = []gatewayv1.ListenerSet{
			{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "ls2"}},
			{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "ls1"}},
			{ObjectMeta: metav1.ObjectMeta{Namespace: "ns2", Name: "ls1"}},
		}
		return nil
	}).Times(1)

	list, err := r.listSortedListenerSetsForGateway(ctx, types.NamespacedName{Namespace: "default", Name: "gw"})
	assert.NoError(t, err)
	assert.Len(t, list, 3)
	assert.Equal(t, "default", list[0].Namespace)
	assert.Equal(t, "ls1", list[0].Name)
	assert.Equal(t, "default", list[1].Namespace)
	assert.Equal(t, "ls2", list[1].Name)
	assert.Equal(t, "ns2", list[2].Namespace)
	assert.Equal(t, "ls1", list[2].Name)
}

func TestGatewayReconciler_routeParentListenerSetIndexFunc(t *testing.T) {
	gwGroup := gatewayv1.Group("gateway.networking.k8s.io")
	gwKind := gatewayv1.Kind("ListenerSet")

	testCases := []struct {
		name string
		obj  client.Object
		want []string
	}{
		{
			name: "HTTPRoute",
			obj: &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: gatewayv1.HTTPRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{{Group: &gwGroup, Kind: &gwKind, Name: "ls1"}},
					},
				},
			},
			want: []string{"default/ls1"},
		},
		{
			name: "GRPCRoute",
			obj: &gatewayv1.GRPCRoute{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: gatewayv1.GRPCRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{{Group: &gwGroup, Kind: &gwKind, Name: "ls1"}},
					},
				},
			},
			want: []string{"default/ls1"},
		},
		{
			name: "TLSRoute",
			obj: &gatewayv1.TLSRoute{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: gatewayv1.TLSRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{{Group: &gwGroup, Kind: &gwKind, Name: "ls1"}},
					},
				},
			},
			want: []string{"default/ls1"},
		},
		{
			name: "Unknown",
			obj:  &gatewayv1.Gateway{},
			want: nil,
		},
		{
			name: "Empty Parents",
			obj:  &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Namespace: "default"}},
			want: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var keys []string
			switch tc.name {
			case "HTTPRoute", "Empty Parents":
				keys = (&genericRouteReconciler[*HTTPRoute, gatewayv1.HTTPRoute, *gatewayv1.HTTPRoute]{}).routeParentListenerSetIndexFunc(tc.obj)
			case "GRPCRoute":
				keys = (&genericRouteReconciler[*GRPCRoute, gatewayv1.GRPCRoute, *gatewayv1.GRPCRoute]{}).routeParentListenerSetIndexFunc(tc.obj)
			case "TLSRoute":
				keys = (&genericRouteReconciler[*TLSRoute, gatewayv1.TLSRoute, *gatewayv1.TLSRoute]{}).routeParentListenerSetIndexFunc(tc.obj)
			default:
				keys = (&genericRouteReconciler[*HTTPRoute, gatewayv1.HTTPRoute, *gatewayv1.HTTPRoute]{}).routeParentListenerSetIndexFunc(tc.obj)
			}
			assert.Equal(t, tc.want, keys)
		})
	}
}

func TestGatewayReconciler_filterUsableGatewayCRs(t *testing.T) {
	gwList := &gatewayv1.GatewayList{
		Items: []gatewayv1.Gateway{
			{Spec: gatewayv1.GatewaySpec{GatewayClassName: "unmanaged"}}, // should skip
			{Spec: gatewayv1.GatewaySpec{GatewayClassName: "avi-lb"}},    // should skip (no usable IP)
			{
				Spec: gatewayv1.GatewaySpec{GatewayClassName: "avi-lb"},
				Status: gatewayv1.GatewayStatus{
					Addresses: []gatewayv1.GatewayStatusAddress{
						{Type: ptrGatewayAddressType(gatewayv1.IPAddressType), Value: "1.1.1.1"},
					},
				},
			}, // should process
		},
	}

	processed := 0
	err := filterUsableGatewayCRs(gwList, func(gw *gatewayv1.Gateway) error {
		processed++
		return nil
	})
	assert.NoError(t, err)
	assert.Equal(t, 1, processed)
}

func TestGatewayReconciler_checkGatewayCRDs_Full(t *testing.T) {
	ctrlMock := gomock.NewController(t)
	defer ctrlMock.Finish()
	c := mockclient.NewMockClient(ctrlMock)
	r := &GatewayReconciler{Client: c}

	fakeDiscovery := &fake.FakeDiscovery{Fake: &k8stesting.Fake{}}
	fakeDiscovery.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: gatewayAPIGroupVersion,
			APIResources: []metav1.APIResource{
				{Name: "gateways"},
				{Name: "listenersets"},
				{Name: "httproutes"},
				{Name: "grpcroutes"},
				{Name: "tlsroutes"},
			},
		},
	}
	r.discoveryClient = fakeDiscovery

	mgr := setupMockManager(ctrlMock, c)
	err := r.checkGatewayCRDs(mgr)
	assert.NoError(t, err)

	assert.True(t, r.apiResources.gatewayEnabled)
	assert.True(t, r.apiResources.listenerSetEnabled)
}

func TestGatewayReconciler_checkGatewayCRDs_NotFound(t *testing.T) {
	ctrlMock := gomock.NewController(t)
	defer ctrlMock.Finish()
	c := mockclient.NewMockClient(ctrlMock)
	r := &GatewayReconciler{Client: c}

	fakeDiscovery := &fake.FakeDiscovery{Fake: &k8stesting.Fake{}}
	fakeDiscovery.Resources = []*metav1.APIResourceList{}
	r.discoveryClient = fakeDiscovery

	mgr := setupMockManager(ctrlMock, c)
	err := r.checkGatewayCRDs(mgr)
	assert.NoError(t, err)

	assert.False(t, r.apiResources.gatewayEnabled)
}

func TestGatewayReconciler_checkGatewayCRDs_NilResources(t *testing.T) {
	ctrlMock := gomock.NewController(t)
	defer ctrlMock.Finish()
	c := mockclient.NewMockClient(ctrlMock)
	r := &GatewayReconciler{Client: c}

	fakeDiscovery := &fake.FakeDiscovery{Fake: &k8stesting.Fake{}}
	fakeDiscovery.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: gatewayAPIGroupVersion,
			APIResources: nil, // Note this difference
		},
	}
	r.discoveryClient = fakeDiscovery

	mgr := setupMockManager(ctrlMock, c)
	err := r.checkGatewayCRDs(mgr)
	assert.NoError(t, err)

	assert.False(t, r.apiResources.gatewayEnabled)
}

func TestGatewayReconciler_StartController_GatewayNotEnabled(t *testing.T) {
	ctrlMock := gomock.NewController(t)
	defer ctrlMock.Finish()
	c := mockclient.NewMockClient(ctrlMock)
	r := &GatewayReconciler{Client: c}

	fakeDiscovery := &fake.FakeDiscovery{Fake: &k8stesting.Fake{}}
	fakeDiscovery.Resources = []*metav1.APIResourceList{} // API group absent
	r.discoveryClient = fakeDiscovery

	mgr := setupMockManager(ctrlMock, c)

	err := r.StartController(mgr, nil)
	assert.NoError(t, err)
}

func TestGatewayReconciler_warmGatewayIPCacheOnStartup_NoGateway(t *testing.T) {
	ctx := context.Background()
	ctrlMock := gomock.NewController(t)
	defer ctrlMock.Finish()
	c := mockclient.NewMockClient(ctrlMock)
	r := &GatewayReconciler{Client: c}
	r.apiResources.gatewayEnabled = false
	err := r.warmGatewayIPCacheOnStartup(ctx)
	assert.NoError(t, err)
}

func TestGatewayReconciler_warmGatewayIPCacheOnStartup_ListError(t *testing.T) {
	ctx := context.Background()
	ctrlMock := gomock.NewController(t)
	defer ctrlMock.Finish()
	c := mockclient.NewMockClient(ctrlMock)
	r := &GatewayReconciler{Client: c}
	r.apiResources.gatewayEnabled = true

	c.EXPECT().List(ctx, gomock.Any()).Return(errors.New("list error"))

	err := r.warmGatewayIPCacheOnStartup(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "list error")
}

func TestGatewayReconciler_StartController_CheckGatewayCRDsError(t *testing.T) {
	ctrlMock := gomock.NewController(t)
	defer ctrlMock.Finish()
	c := mockclient.NewMockClient(ctrlMock)
	r := &GatewayReconciler{Client: c}

	fakeDiscovery := &fake.FakeDiscovery{Fake: &k8stesting.Fake{}}
	fakeDiscovery.PrependReactor("get", "resource", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		return true, nil, errors.New("discovery error")
	})
	// force error by mocking discovery client ServerResourcesForGroupVersion directly if possible, or using fake config
	// Since we mock mgr.GetConfig to nil, it already fails:
	mgr := setupMockManager(ctrlMock, c)
	err := r.StartController(mgr, nil)
	assert.Error(t, err)
}

func TestGatewayReconciler_StartController_SetupWithManagerError(t *testing.T) {
	ctrlMock := gomock.NewController(t)
	defer ctrlMock.Finish()
	c := mockclient.NewMockClient(ctrlMock)
	r := &GatewayReconciler{Client: c}
	r.apiResources.gatewayEnabled = true // Bypass skip

	// Setup fake discovery
	fakeDiscovery := &fake.FakeDiscovery{Fake: &k8stesting.Fake{}}
	fakeDiscovery.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: gatewayAPIGroupVersion,
			APIResources: []metav1.APIResource{
				{Name: "gateways"},
				{Name: "listenersets"},
			},
		},
	}
	r.discoveryClient = fakeDiscovery

	mgr := setupMockManager(ctrlMock, c)
	idx := mockclient.NewMockFieldIndexer(ctrlMock)
	mgr.EXPECT().GetFieldIndexer().Return(idx).AnyTimes()
	idx.EXPECT().IndexField(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(errors.New("idx err")).Times(1)

	err := r.StartController(mgr, nil)
	assert.Error(t, err)
}

func TestGatewayReconciler_Reconcile(t *testing.T) {
	gwReq := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "gw1"}}

	tests := []struct {
		name      string
		setupMock func(*mockclient.MockClient, *mockdns.MockDNSRecordProvider, *mockgateway.MockStatusUpdater)
		wantRes   ctrl.Result
		wantErr   bool
	}{
		{
			name: "delete",
			setupMock: func(c *mockclient.MockClient, d *mockdns.MockDNSRecordProvider, s *mockgateway.MockStatusUpdater) {
				c.EXPECT().Get(gomock.Any(), gwReq.NamespacedName, gomock.Any()).Return(apierrors.NewNotFound(schema.GroupResource{}, "gw1")).AnyTimes()
				d.EXPECT().DeleteRecordByOwnerNN(gomock.Any(), dns.ResourceKindGateway, "default", "gw1").Return(true, nil).AnyTimes()
				c.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				s.EXPECT().IncreaseSyncTotal().AnyTimes()
				s.EXPECT().IncreaseDeleteTotal().AnyTimes()
				s.EXPECT().DeleteSuccess(gomock.Any(), gomock.Any()).AnyTimes()
			},
			wantRes: ctrl.Result{},
		},
		{
			name: "unmanaged gateway",
			setupMock: func(c *mockclient.MockClient, d *mockdns.MockDNSRecordProvider, s *mockgateway.MockStatusUpdater) {
				c.EXPECT().Get(gomock.Any(), gwReq.NamespacedName, gomock.Any()).DoAndReturn(func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					gw := obj.(*gatewayv1.Gateway)
					gw.Spec.GatewayClassName = "unmanaged"
					gw.Name = "gw1"
					gw.Namespace = "default"
					return nil
				}).AnyTimes()
				d.EXPECT().DeleteRecordByOwnerNN(gomock.Any(), dns.ResourceKindGateway, "default", "gw1").Return(true, nil).Times(1)
				c.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				s.EXPECT().IncreaseSyncTotal().AnyTimes()
				s.EXPECT().IncreaseDeleteTotal().AnyTimes()
				s.EXPECT().DeleteSuccess(gomock.Any(), gomock.Any()).AnyTimes()
			},
			wantRes: ctrl.Result{},
		},
		{
			name: "managed gateway with hostnames",
			setupMock: func(c *mockclient.MockClient, d *mockdns.MockDNSRecordProvider, s *mockgateway.MockStatusUpdater) {
				c.EXPECT().Get(gomock.Any(), gwReq.NamespacedName, gomock.Any()).DoAndReturn(func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					gw := obj.(*gatewayv1.Gateway)
					gw.Spec.GatewayClassName = "avi-lb"
					gw.Name = "gw1"
					gw.Namespace = "default"
					hostname := gatewayv1.Hostname("a.com")
					gw.Spec.Listeners = []gatewayv1.Listener{{Hostname: &hostname, Name: "l1"}}
					gw.Status.Addresses = []gatewayv1.GatewayStatusAddress{{Type: ptrGatewayAddressType(gatewayv1.IPAddressType), Value: "1.1.1.1"}}
					return nil
				}).AnyTimes()
				c.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				d.EXPECT().ValidateEndpointsByZone(gomock.Any(), gomock.Any(), gomock.Any()).Return([]dns.EndpointRow{{Endpoint: &extdns.Endpoint{DNSName: "a.com"}}}, map[string]string{"a.com": ""}, nil).AnyTimes()
				d.EXPECT().CreateOrUpdateRecords(gomock.Any(), gomock.Any()).Return(true, nil).AnyTimes()
				d.EXPECT().DeleteRecordByOwnerNN(gomock.Any(), dns.ResourceKindGateway, "default", "gw1").Return(true, nil).AnyTimes()
				s.EXPECT().IncreaseSyncTotal().AnyTimes()
				s.EXPECT().IncreaseUpdateTotal().AnyTimes()
				s.EXPECT().UpdateSuccess(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
				s.EXPECT().DeleteSuccess(gomock.Any(), gomock.Any()).AnyTimes()
			},
			wantRes: ctrl.Result{},
		},
		{
			name: "client get error",
			setupMock: func(c *mockclient.MockClient, d *mockdns.MockDNSRecordProvider, s *mockgateway.MockStatusUpdater) {
				c.EXPECT().Get(gomock.Any(), gwReq.NamespacedName, gomock.Any()).Return(errors.New("get error"))
				s.EXPECT().IncreaseSyncTotal().AnyTimes()
			},
			wantRes: common.ResultRequeueAfter10sec,
		},
		{
			name: "not found delete error",
			setupMock: func(c *mockclient.MockClient, d *mockdns.MockDNSRecordProvider, s *mockgateway.MockStatusUpdater) {
				c.EXPECT().Get(gomock.Any(), gwReq.NamespacedName, gomock.Any()).Return(apierrors.NewNotFound(schema.GroupResource{}, ""))
				d.EXPECT().DeleteRecordByOwnerNN(gomock.Any(), dns.ResourceKindGateway, "default", "gw1").Return(false, errors.New("delete error"))
				s.EXPECT().IncreaseSyncTotal().AnyTimes()
				s.EXPECT().IncreaseDeleteTotal().AnyTimes()
				s.EXPECT().DeleteFail(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
			},
			wantRes: common.ResultRequeueAfter10sec,
		},
		{
			name: "deletion timestamp delete error",
			setupMock: func(c *mockclient.MockClient, d *mockdns.MockDNSRecordProvider, s *mockgateway.MockStatusUpdater) {
				c.EXPECT().Get(gomock.Any(), gwReq.NamespacedName, gomock.Any()).DoAndReturn(func(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
					gw := obj.(*gatewayv1.Gateway)
					gw.Name = "gw1"
					gw.Namespace = "default"
					gw.DeletionTimestamp = &metav1.Time{Time: time.Now()}
					return nil
				})
				d.EXPECT().DeleteRecordByOwnerNN(gomock.Any(), dns.ResourceKindGateway, "default", "gw1").Return(false, errors.New("delete error"))
				s.EXPECT().IncreaseSyncTotal().AnyTimes()
				s.EXPECT().IncreaseDeleteTotal().AnyTimes()
				s.EXPECT().DeleteFail(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
			},
			wantRes: common.ResultRequeueAfter10sec,
		},
		{
			name: "not managed delete error",
			setupMock: func(c *mockclient.MockClient, d *mockdns.MockDNSRecordProvider, s *mockgateway.MockStatusUpdater) {
				c.EXPECT().Get(gomock.Any(), gwReq.NamespacedName, gomock.Any()).DoAndReturn(func(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
					gw := obj.(*gatewayv1.Gateway)
					gw.Name = "gw1"
					gw.Namespace = "default"
					gw.Spec.GatewayClassName = "other-class"
					return nil
				})
				d.EXPECT().DeleteRecordByOwnerNN(gomock.Any(), dns.ResourceKindGateway, "default", "gw1").Return(false, errors.New("delete error"))
				s.EXPECT().IncreaseSyncTotal().AnyTimes()
				s.EXPECT().IncreaseDeleteTotal().AnyTimes()
				s.EXPECT().DeleteFail(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
			},
			wantRes: common.ResultRequeueAfter10sec,
		},
		{
			name: "no usable ip delete error",
			setupMock: func(c *mockclient.MockClient, d *mockdns.MockDNSRecordProvider, s *mockgateway.MockStatusUpdater) {
				c.EXPECT().Get(gomock.Any(), gwReq.NamespacedName, gomock.Any()).DoAndReturn(func(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
					gw := obj.(*gatewayv1.Gateway)
					gw.Name = "gw1"
					gw.Namespace = "default"
					gw.Spec.GatewayClassName = "nsx"
					gw.Status.Addresses = []gatewayv1.GatewayStatusAddress{}
					return nil
				})
				d.EXPECT().DeleteRecordByOwnerNN(gomock.Any(), dns.ResourceKindGateway, "default", "gw1").Return(false, errors.New("delete error"))
				s.EXPECT().IncreaseSyncTotal().AnyTimes()
				s.EXPECT().IncreaseDeleteTotal().AnyTimes()
				s.EXPECT().DeleteFail(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
			},
			wantRes: common.ResultRequeueAfter10sec,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			ctrlMock := gomock.NewController(t)
			defer ctrlMock.Finish()
			c := mockclient.NewMockClient(ctrlMock)
			d := mockdns.NewMockDNSRecordProvider(ctrlMock)
			s := mockgateway.NewMockStatusUpdater(ctrlMock)
			r := &GatewayReconciler{
				Client:        c,
				DNS:           d,
				StatusUpdater: s,
				ipCache:       NewGatewayIPCache(),
			}

			if tc.setupMock != nil {
				tc.setupMock(c, d, s)
			}

			res, err := r.Reconcile(ctx, gwReq)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.wantRes, res)
			}
		})
	}
}

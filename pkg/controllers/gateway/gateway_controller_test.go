/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package gateway

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"sync"
	"testing"

	"github.com/agiledragon/gomonkey/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt-mp/nsx/model"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/discovery"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	crmanager "sigs.k8s.io/controller-runtime/pkg/manager"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/vmware-tanzu/nsx-operator/pkg/config"
	"github.com/vmware-tanzu/nsx-operator/pkg/controllers/common"
	servicecommon "github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/dns"
	extdns "github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/endpoint"
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = gatewayv1.Install(scheme)
}

var scheme = runtime.NewScheme()

type MockManager struct {
	ctrl.Manager
	client  client.Client
	scheme  *runtime.Scheme
	indexer client.FieldIndexer
}

func newMockManager(c client.Client, s *runtime.Scheme) *MockManager {
	return &MockManager{client: c, scheme: s}
}
func (m *MockManager) GetClient() client.Client                        { return m.client }
func (m *MockManager) GetScheme() *runtime.Scheme                      { return m.scheme }
func (m *MockManager) GetEventRecorderFor(string) record.EventRecorder { return nil }
func (m *MockManager) Add(crmanager.Runnable) error                    { return nil }
func (m *MockManager) Start(context.Context) error                     { return nil }
func (m *MockManager) GetFieldIndexer() client.FieldIndexer {
	if m.indexer != nil {
		return m.indexer
	}
	return &mockFieldIndexer{}
}

type mockFieldIndexer struct{ err error }

func (f *mockFieldIndexer) IndexField(_ context.Context, _ client.Object, _ string, _ client.IndexerFunc) error {
	return f.err
}

type fakeDiscoveryClient struct {
	discovery.DiscoveryInterface
	resources *metav1.APIResourceList
	err       error
}

func (f *fakeDiscoveryClient) ServerResourcesForGroupVersion(_ string) (*metav1.APIResourceList, error) {
	return f.resources, f.err
}

func createFakeManagerAndClient(objs ...client.Object) (ctrl.Manager, client.Client) {
	b := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&gatewayv1.Gateway{}, &gatewayv1.ListenerSet{}, &gatewayv1.HTTPRoute{}, &gatewayv1.GRPCRoute{}, &gatewayv1.TLSRoute{}).
		WithIndex(&gatewayv1.ListenerSet{}, listenerSetParentGatewayIndex, listenerSetParentGatewayIndexFunc).
		WithIndex(&gatewayv1.HTTPRoute{}, routeParentGatewayIndex, routeParentGatewayIndexFunc).
		WithIndex(&gatewayv1.GRPCRoute{}, routeParentGatewayIndex, routeParentGatewayIndexFunc).
		WithIndex(&gatewayv1.TLSRoute{}, routeParentGatewayIndex, routeParentGatewayIndexFunc)
	if len(objs) > 0 {
		b.WithObjects(objs...)
	}
	fc := b.Build()
	return newMockManager(fc, scheme), fc
}

func fakeGatewayReconciler(t *testing.T, objs ...client.Object) (*GatewayReconciler, client.Client) {
	t.Helper()
	mgr, fc := createFakeManagerAndClient(objs...)
	svc := servicecommon.Service{
		Client:    fc,
		NSXConfig: &config.NSXOperatorConfig{CoeConfig: &config.CoeConfig{}},
	}
	dnsService := &dns.DNSRecordService{Service: svc, DNSRecordStore: dns.BuildDNSRecordStore()}
	r := NewGatewayReconciler(mgr, dnsService)
	r.StatusUpdater = newMockStatusUpdater()
	return r, fc
}

func fakeClientForGatewayTests(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&gatewayv1.Gateway{}, &gatewayv1.ListenerSet{}, &gatewayv1.HTTPRoute{}, &gatewayv1.GRPCRoute{}, &gatewayv1.TLSRoute{}).
		WithIndex(&gatewayv1.ListenerSet{}, listenerSetParentGatewayIndex, listenerSetParentGatewayIndexFunc).
		WithIndex(&gatewayv1.HTTPRoute{}, routeParentGatewayIndex, routeParentGatewayIndexFunc).
		WithIndex(&gatewayv1.GRPCRoute{}, routeParentGatewayIndex, routeParentGatewayIndexFunc).
		WithIndex(&gatewayv1.TLSRoute{}, routeParentGatewayIndex, routeParentGatewayIndexFunc).
		Build()
}

const (
	callIncreaseSyncTotal          = "IncreaseSyncTotal"
	callIncreaseUpdateTotal        = "IncreaseUpdateTotal"
	callIncreaseDeleteTotal        = "IncreaseDeleteTotal"
	callIncreaseDeleteSuccessTotal = "IncreaseDeleteSuccessTotal"
	callIncreaseDeleteFailTotal    = "IncreaseDeleteFailTotal"
	callUpdateSuccess              = "UpdateSuccess"
	callUpdateFail                 = "UpdateFail"
	callDeleteSuccess              = "DeleteSuccess"
	callDeleteFail                 = "DeleteFail"
)

var ipType = gatewayv1.IPAddressType

type mockStatusUpdater struct {
	mu    sync.RWMutex
	calls []string
}

func (u *mockStatusUpdater) record(status string) {
	u.mu.Lock()
	u.calls = append(u.calls, status)
	u.mu.Unlock()
}
func (u *mockStatusUpdater) UpdateSuccess(_ context.Context, _ client.Object, _ common.UpdateSuccessStatusFn, _ ...interface{}) {
	u.record(callUpdateSuccess)
}
func (u *mockStatusUpdater) UpdateFail(_ context.Context, _ client.Object, _ error, _ string, _ common.UpdateFailStatusFn, _ ...interface{}) {
	u.record(callUpdateFail)
}
func (u *mockStatusUpdater) DeleteSuccess(_ types.NamespacedName, _ client.Object) {
	u.record(callDeleteSuccess)
}
func (u *mockStatusUpdater) IncreaseSyncTotal()          { u.record(callIncreaseSyncTotal) }
func (u *mockStatusUpdater) IncreaseUpdateTotal()        { u.record(callIncreaseUpdateTotal) }
func (u *mockStatusUpdater) IncreaseDeleteTotal()        { u.record(callIncreaseDeleteTotal) }
func (u *mockStatusUpdater) IncreaseDeleteSuccessTotal() { u.record(callIncreaseDeleteSuccessTotal) }
func (u *mockStatusUpdater) IncreaseDeleteFailTotal()    { u.record(callIncreaseDeleteFailTotal) }
func (u *mockStatusUpdater) DeleteFail(_ types.NamespacedName, _ client.Object, _ error) {
	u.record(callDeleteFail)
}
func (u *mockStatusUpdater) validateCalls(t *testing.T, wantCalls []string) {
	t.Helper()
	u.mu.RLock()
	defer u.mu.RUnlock()
	cp := make([]string, len(u.calls))
	copy(cp, u.calls)
	assert.Equal(t, wantCalls, cp, "statusUpdater call sequence mismatch")
}
func (u *mockStatusUpdater) getCalls() []string {
	u.mu.RLock()
	defer u.mu.RUnlock()
	cp := make([]string, len(u.calls))
	copy(cp, u.calls)
	return cp
}
func newMockStatusUpdater() statusUpdater { return &mockStatusUpdater{calls: make([]string, 0)} }

func dnsRecordOwnedByGateway(id, gwUID, gwNs, gwName string) *dns.DNSRecord {
	return &dns.DNSRecord{
		Id: ptr(id),
		Tags: []model.Tag{
			{Scope: ptr(servicecommon.TagScopeDNSRecordFor), Tag: ptr(servicecommon.TagValueDNSRecordForGateway)},
			{Scope: ptr(servicecommon.TagScopeGatewayUID), Tag: ptr(gwUID)},
			{Scope: ptr(servicecommon.TagScopeGatewayNamespace), Tag: ptr(gwNs)},
			{Scope: ptr(servicecommon.TagScopeGatewayName), Tag: ptr(gwName)},
		},
	}
}

func newTestGateway(name, ns, ip string, deleting bool, mutateFn func(*gatewayv1.Gateway)) *gatewayv1.Gateway {
	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID("uid-" + name)},
		Spec:       gatewayv1.GatewaySpec{GatewayClassName: gatewayv1.ObjectName(common.ManagedK8sGatewayClassIstio)},
	}
	if ip != "" {
		gw.Status.Addresses = []gatewayv1.GatewayStatusAddress{{Type: &ipType, Value: ip}}
	}
	if deleting {
		now := metav1.Now()
		gw.DeletionTimestamp = &now
		gw.Finalizers = []string{"dns.temp/finalizer"}
	}
	if mutateFn != nil {
		mutateFn(gw)
	}
	return gw
}

func newTestListenerSet(name, ns, gwName, hostname string) *gatewayv1.ListenerSet {
	gwKind := gatewayv1.Kind("Gateway")
	gwGroup := gatewayv1.Group(gatewayv1.GroupName)
	return &gatewayv1.ListenerSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID("uid-" + name)},
		Spec: gatewayv1.ListenerSetSpec{
			ParentRef: gatewayv1.ParentGatewayReference{Kind: &gwKind, Group: &gwGroup, Name: gatewayv1.ObjectName(gwName)},
			Listeners: []gatewayv1.ListenerEntry{{Name: "dns-l1", Hostname: ptr(gatewayv1.Hostname(hostname))}},
		},
	}
}

// newTestListenerSetMulti builds a ListenerSet with multiple listener hostnames (names l1, l2, …).
// newTestHTTPRouteReady returns an HTTPRoute attached to a Gateway with Accepted+Programmed parent status.
func newTestHTTPRouteReady(name, ns, gwName string, hostnames ...gatewayv1.Hostname) *gatewayv1.HTTPRoute {
	gwGroup := gatewayv1.Group(gatewayv1.GroupName)
	gwKind := gatewayv1.Kind("Gateway")
	parent := gatewayv1.ParentReference{
		Group: &gwGroup,
		Kind:  &gwKind,
		Name:  gatewayv1.ObjectName(gwName),
	}
	return &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID("uid-" + name)},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{parent},
			},
			Hostnames: hostnames,
			Rules:     []gatewayv1.HTTPRouteRule{{}},
		},
		Status: gatewayv1.HTTPRouteStatus{
			RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{
					{
						ParentRef: parent,
						Conditions: []metav1.Condition{
							{Type: string(gatewayv1.RouteConditionAccepted), Status: metav1.ConditionTrue},
							{Type: "Programmed", Status: metav1.ConditionTrue},
						},
					},
				},
			},
		},
	}
}

func newTestListenerSetMulti(name, ns, gwName string, hostnames ...string) *gatewayv1.ListenerSet {
	gwKind := gatewayv1.Kind("Gateway")
	gwGroup := gatewayv1.Group(gatewayv1.GroupName)
	entries := make([]gatewayv1.ListenerEntry, len(hostnames))
	for i, h := range hostnames {
		entries[i] = gatewayv1.ListenerEntry{
			Name:     gatewayv1.SectionName(fmt.Sprintf("l%d", i+1)),
			Hostname: ptr(gatewayv1.Hostname(h)),
		}
	}
	return &gatewayv1.ListenerSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID("uid-" + name)},
		Spec: gatewayv1.ListenerSetSpec{
			ParentRef: gatewayv1.ParentGatewayReference{Kind: &gwKind, Group: &gwGroup, Name: gatewayv1.ObjectName(gwName)},
			Listeners: entries,
		},
	}
}

func ptr[T any](v T) *T { return &v }

// patchCreateOrUpdateDNSRecordsNoOp stubs NSX create/update success for tests that only care about reconcile flow.
func patchCreateOrUpdateDNSRecordsNoOp(t *testing.T, svc *dns.DNSRecordService) {
	t.Helper()
	p := gomonkey.ApplyMethod(reflect.TypeOf(svc), "CreateOrUpdateDNSRecords",
		func(_ *dns.DNSRecordService, _ context.Context, _ *dns.OwnerEndpoints) error { return nil })
	t.Cleanup(p.Reset)
}

func reconcilerForFakeClient(t *testing.T, listenerSetEnabled bool, objs ...client.Object) (*GatewayReconciler, client.Client, *mockStatusUpdater) {
	t.Helper()
	fc := fakeClientForGatewayTests(objs...)
	u := newMockStatusUpdater().(*mockStatusUpdater)
	r := &GatewayReconciler{
		Client: fc, Scheme: scheme, Service: &dns.DNSRecordService{DNSRecordStore: dns.BuildDNSRecordStore()},
		StatusUpdater: u, Recorder: record.NewFakeRecorder(10), listenerSetEnabled: listenerSetEnabled,
	}
	return r, fc, u
}

// --- tests ---

func Test_shouldProcessGateway_and_hasUsableGatewayIP(t *testing.T) {
	t.Run("shouldProcessGateway", func(t *testing.T) {
		for _, tt := range []struct {
			class string
			want  bool
		}{
			{common.ManagedK8sGatewayClassIstio, true},
			{"other", false},
			{"", false},
		} {
			t.Run(tt.class, func(t *testing.T) {
				gw := &gatewayv1.Gateway{Spec: gatewayv1.GatewaySpec{GatewayClassName: gatewayv1.ObjectName(tt.class)}}
				assert.Equal(t, tt.want, shouldProcessGateway(gw))
			})
		}
	})
	t.Run("hasUsableGatewayIP", func(t *testing.T) {
		for _, tt := range []struct {
			name string
			gw   *gatewayv1.Gateway
			want bool
		}{
			{"no addresses", &gatewayv1.Gateway{}, false},
			{"valid IP", &gatewayv1.Gateway{Status: gatewayv1.GatewayStatus{
				Addresses: []gatewayv1.GatewayStatusAddress{{Type: &ipType, Value: "192.168.1.1"}},
			}}, true},
			{"invalid IP", &gatewayv1.Gateway{Status: gatewayv1.GatewayStatus{
				Addresses: []gatewayv1.GatewayStatusAddress{{Type: &ipType, Value: "not-an-ip"}},
			}}, false},
		} {
			t.Run(tt.name, func(t *testing.T) {
				assert.Equal(t, tt.want, hasUsableGatewayIP(tt.gw))
			})
		}
	})
}

func Test_collectIPsFromGateway(t *testing.T) {
	hostname := gatewayv1.HostnameAddressType
	for _, tt := range []struct {
		name    string
		gw      *gatewayv1.Gateway
		wantLen int
		wantIPs []net.IP
	}{
		{"no addresses", &gatewayv1.Gateway{}, 0, nil},
		{"IPv4", &gatewayv1.Gateway{Status: gatewayv1.GatewayStatus{
			Addresses: []gatewayv1.GatewayStatusAddress{{Type: &ipType, Value: "10.0.0.1"}},
		}}, 1, []net.IP{net.ParseIP("10.0.0.1")}},
		{"nil addr type as IP", &gatewayv1.Gateway{Status: gatewayv1.GatewayStatus{
			Addresses: []gatewayv1.GatewayStatusAddress{{Type: nil, Value: "10.0.0.2"}},
		}}, 1, []net.IP{net.ParseIP("10.0.0.2")}},
		{"hostname type skipped", &gatewayv1.Gateway{Status: gatewayv1.GatewayStatus{
			Addresses: []gatewayv1.GatewayStatusAddress{{Type: &hostname, Value: "foo.example.com"}},
		}}, 0, nil},
		{"invalid IP skipped", &gatewayv1.Gateway{Status: gatewayv1.GatewayStatus{
			Addresses: []gatewayv1.GatewayStatusAddress{{Type: &ipType, Value: "bad"}},
		}}, 0, nil},
		{"dual stack", &gatewayv1.Gateway{Status: gatewayv1.GatewayStatus{
			Addresses: []gatewayv1.GatewayStatusAddress{
				{Type: &ipType, Value: "10.0.0.1"},
				{Type: &ipType, Value: "2001:db8::1"},
			},
		}}, 2, []net.IP{net.ParseIP("10.0.0.1"), net.ParseIP("2001:db8::1")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := collectIPsFromGateway(tt.gw)
			require.Len(t, got, tt.wantLen)
			if tt.wantIPs != nil {
				assert.ElementsMatch(t, tt.wantIPs, got)
			}
		})
	}
}

func Test_mergeDNSReadyCondition(t *testing.T) {
	cond := metav1.Condition{Type: conditionTypeDNSReady, Status: metav1.ConditionTrue, Reason: reasonDNSRecordConfigured}
	for _, tt := range []struct {
		name   string
		exist  []metav1.Condition
		wantN  int
		checkF func([]metav1.Condition)
	}{
		{"append nil", nil, 1, func(out []metav1.Condition) { assert.Equal(t, metav1.ConditionTrue, out[0].Status) }},
		{"update DNSReady in place", []metav1.Condition{{Type: conditionTypeDNSReady, Status: metav1.ConditionFalse}}, 1,
			func(out []metav1.Condition) { assert.Equal(t, metav1.ConditionTrue, out[0].Status) }},
		{"preserve other", []metav1.Condition{{Type: "Other", Status: metav1.ConditionTrue}}, 2,
			func(out []metav1.Condition) { assert.Equal(t, conditionTypeDNSReady, out[1].Type) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := mergeDNSReadyCondition(tt.exist, cond)
			require.Len(t, out, tt.wantN)
			tt.checkF(out)
		})
	}
}

func Test_RestoreReconcile(t *testing.T) {
	r, _ := fakeGatewayReconciler(t)
	assert.NoError(t, r.RestoreReconcile())
}

func Test_takeNewHostnames_wildcardOverlap(t *testing.T) {
	seen := sets.New[string]()
	out1 := takeNewHostnames(seen, []string{"*.example.com"})
	assert.Equal(t, []string{"*.example.com"}, out1)
	out2 := takeNewHostnames(seen, []string{"app.example.com"})
	assert.Empty(t, out2, "concrete host overlapping existing wildcard should not be claimed again")

	seen2 := sets.New[string]()
	out3 := takeNewHostnames(seen2, []string{"app.example.com"})
	assert.Equal(t, []string{"app.example.com"}, out3)
	out4 := takeNewHostnames(seen2, []string{"*.example.com"})
	assert.Empty(t, out4, "wildcard overlapping existing concrete host should not be claimed again")
}

func Test_takeNewHostnames_orderStableExactMatch(t *testing.T) {
	seen := sets.New[string]()
	assert.Equal(t, []string{"a.example.com"}, takeNewHostnames(seen, []string{"a.example.com"}))
	assert.Empty(t, takeNewHostnames(seen, []string{"a.example.com"}))
}

func Test_buildOwnerEndpointsForGateway_listenerSetOwner(t *testing.T) {
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns1"}}
	gw := newTestGateway("gw1", "ns1", "10.0.0.1", false, nil)
	ls := newTestListenerSet("ls1", "ns1", "gw1", "app.example.com")
	hr := newTestHTTPRouteReady("hr1", "ns1", "gw1", gatewayv1.Hostname("app.example.com"))
	fc := fakeClientForGatewayTests(ns, gw, ls, hr)
	r := &GatewayReconciler{Client: fc, listenerSetEnabled: true, httpRouteEnabled: true}
	batches, err := r.buildOwnerEndpointsForGateway(ctx, gw)
	require.NoError(t, err)
	require.Len(t, batches, 1)
	assert.Equal(t, dns.ResourceKindHTTPRoute, batches[0].Owner.Kind)
	var names []string
	for _, ep := range batches[0].Endpoints {
		names = append(names, ep.DNSName)
	}
	assert.Contains(t, names, "app.example.com")
}

func Test_Reconcile_gatewayClassTransitions(t *testing.T) {
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns1"}}
	gw := newTestGateway("gw1", "ns1", "10.0.0.1", false, func(g *gatewayv1.Gateway) {
		g.Spec.GatewayClassName = "other"
		g.Spec.Listeners = []gatewayv1.Listener{{Name: "l1", Hostname: ptr(gatewayv1.Hostname("svc.example.com")), Port: 80, Protocol: gatewayv1.HTTPProtocolType}}
	})
	fc := fakeClientForGatewayTests(ns, gw)
	dnsSvc := &dns.DNSRecordService{DNSRecordStore: dns.BuildDNSRecordStore()}
	updater := newMockStatusUpdater().(*mockStatusUpdater)
	r := &GatewayReconciler{Client: fc, Scheme: scheme, Service: dnsSvc, StatusUpdater: updater, Recorder: record.NewFakeRecorder(10)}
	patchCreateOrUpdateDNSRecordsNoOp(t, r.Service)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: "gw1"}}
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	updater.validateCalls(t, []string{callIncreaseSyncTotal, callIncreaseDeleteTotal, callDeleteSuccess})

	latest := &gatewayv1.Gateway{}
	require.NoError(t, fc.Get(ctx, req.NamespacedName, latest))
	latest.Spec.GatewayClassName = gatewayv1.ObjectName(common.ManagedK8sGatewayClassIstio)
	require.NoError(t, fc.Update(ctx, latest))
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	updater.validateCalls(t, []string{
		callIncreaseSyncTotal, callIncreaseDeleteTotal, callDeleteSuccess,
		callIncreaseSyncTotal, callIncreaseUpdateTotal, callUpdateSuccess,
	})

	latest = &gatewayv1.Gateway{}
	require.NoError(t, fc.Get(ctx, req.NamespacedName, latest))
	latest.Spec.GatewayClassName = "other"
	require.NoError(t, fc.Update(ctx, latest))
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	updater.validateCalls(t, []string{
		callIncreaseSyncTotal, callIncreaseDeleteTotal, callDeleteSuccess,
		callIncreaseSyncTotal, callIncreaseUpdateTotal, callUpdateSuccess,
		callIncreaseSyncTotal, callIncreaseDeleteTotal, callDeleteSuccess,
	})
}

func Test_Reconcile_DNS_recordsTrackAddressAndHostnames(t *testing.T) {
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns1"}}
	gw := newTestGateway("gw1", "ns1", "10.0.0.1", false, func(g *gatewayv1.Gateway) {
		g.Spec.Listeners = []gatewayv1.Listener{{Name: "l1", Hostname: ptr(gatewayv1.Hostname("a.example.com")), Port: 80, Protocol: gatewayv1.HTTPProtocolType}}
	})
	hr := newTestHTTPRouteReady("hr1", "ns1", "gw1", gatewayv1.Hostname("a.example.com"))
	fc := fakeClientForGatewayTests(ns, gw, hr)
	r := &GatewayReconciler{
		Client: fc, Scheme: scheme, Service: &dns.DNSRecordService{DNSRecordStore: dns.BuildDNSRecordStore()},
		StatusUpdater: newMockStatusUpdater(), Recorder: record.NewFakeRecorder(10), httpRouteEnabled: true,
	}
	var seen []*dns.OwnerEndpoints
	p := gomonkey.ApplyMethod(reflect.TypeOf(r.Service), "CreateOrUpdateDNSRecords",
		func(_ *dns.DNSRecordService, _ context.Context, batch *dns.OwnerEndpoints) error {
			seen = append(seen, batch)
			return nil
		})
	t.Cleanup(p.Reset)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: "gw1"}}

	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Len(t, seen, 1)
	require.NotEmpty(t, seen[0].Endpoints)
	assert.Equal(t, "a.example.com", seen[0].Endpoints[0].DNSName)
	assert.Contains(t, []string(seen[0].Endpoints[0].Targets), "10.0.0.1")
	assert.Equal(t, "external-dns", seen[0].Endpoints[0].Labels[extdns.HeritageLabelKey])
	assert.Equal(t, extdns.DefaultSourceOwnerID, seen[0].Endpoints[0].Labels[extdns.OwnerLabelKey])

	latest := &gatewayv1.Gateway{}
	require.NoError(t, fc.Get(ctx, req.NamespacedName, latest))
	latest.Status.Addresses = []gatewayv1.GatewayStatusAddress{{Type: &ipType, Value: "10.0.0.2"}}
	require.NoError(t, fc.Status().Update(ctx, latest))
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Len(t, seen, 2)
	assert.Contains(t, []string(seen[1].Endpoints[0].Targets), "10.0.0.2")

	require.NoError(t, fc.Get(ctx, req.NamespacedName, latest))
	h := gatewayv1.Hostname("b.example.com")
	latest.Spec.Listeners = []gatewayv1.Listener{{Name: "l1", Hostname: &h, Port: 80, Protocol: gatewayv1.HTTPProtocolType}}
	require.NoError(t, fc.Update(ctx, latest))
	latestHR := &gatewayv1.HTTPRoute{}
	require.NoError(t, fc.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: "hr1"}, latestHR))
	latestHR.Spec.Hostnames = []gatewayv1.Hostname{"b.example.com"}
	require.NoError(t, fc.Update(ctx, latestHR))
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Len(t, seen, 3)
	assert.Equal(t, "b.example.com", seen[2].Endpoints[0].DNSName)
}

func Test_Reconcile_listenerSetParentRefChanges(t *testing.T) {
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns1"}}

	for _, tt := range []struct {
		name                       string
		lsName                     string
		lsHost                     string
		mutateGwB                  func(*gatewayv1.Gateway)
		reconcileAfterParentChange []types.NamespacedName
	}{
		{
			name:                       "managed to managed",
			lsName:                     "ls1",
			lsHost:                     "ls.example.com",
			reconcileAfterParentChange: []types.NamespacedName{{Namespace: "ns1", Name: "gwa"}, {Namespace: "ns1", Name: "gwb"}},
		},
		{
			name:   "managed to unmanaged new parent only old gateway reconciled",
			lsName: "ls2",
			lsHost: "ls2.example.com",
			mutateGwB: func(g *gatewayv1.Gateway) {
				g.Spec.GatewayClassName = gatewayv1.ObjectName("other")
			},
			reconcileAfterParentChange: []types.NamespacedName{{Namespace: "ns1", Name: "gwa"}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gwA := newTestGateway("gwa", "ns1", "10.0.0.1", false, nil)
			gwB := newTestGateway("gwb", "ns1", "10.0.0.2", false, tt.mutateGwB)
			ls := newTestListenerSet(tt.lsName, "ns1", "gwa", tt.lsHost)
			r, fc, updater := reconcilerForFakeClient(t, true, ns, gwA, gwB, ls)
			patchCreateOrUpdateDNSRecordsNoOp(t, r.Service)

			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: "gwa"}})
			require.NoError(t, err)
			latestLS := &gatewayv1.ListenerSet{}
			require.NoError(t, fc.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: tt.lsName}, latestLS))
			latestLS.Spec.ParentRef.Name = gatewayv1.ObjectName("gwb")
			require.NoError(t, fc.Update(ctx, latestLS))
			for _, nn := range tt.reconcileAfterParentChange {
				_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: nn})
				require.NoError(t, err)
			}
			assert.Contains(t, updater.getCalls(), callUpdateSuccess)
		})
	}
}

func Test_Reconcile_listenerSetHostnameChange(t *testing.T) {
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns1"}}
	gw := newTestGateway("gw1", "ns1", "10.0.0.1", false, nil)
	ls := newTestListenerSet("ls1", "ns1", "gw1", "old.example.com")
	hr := newTestHTTPRouteReady("hr1", "ns1", "gw1", gatewayv1.Hostname("old.example.com"))
	r, fc, _ := reconcilerForFakeClient(t, true, ns, gw, ls, hr)
	r.httpRouteEnabled = true
	var seen []string
	p := gomonkey.ApplyMethod(reflect.TypeOf(r.Service), "CreateOrUpdateDNSRecords",
		func(_ *dns.DNSRecordService, _ context.Context, batch *dns.OwnerEndpoints) error {
			if batch.Owner.Kind == dns.ResourceKindHTTPRoute && len(batch.Endpoints) > 0 {
				seen = append(seen, batch.Endpoints[0].DNSName)
			}
			return nil
		})
	t.Cleanup(p.Reset)

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: "gw1"}})
	require.NoError(t, err)
	require.Equal(t, []string{"old.example.com"}, seen)

	latestLS := &gatewayv1.ListenerSet{}
	require.NoError(t, fc.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: "ls1"}, latestLS))
	latestLS.Spec.Listeners[0].Hostname = ptr(gatewayv1.Hostname("new.example.com"))
	require.NoError(t, fc.Update(ctx, latestLS))
	latestHR := &gatewayv1.HTTPRoute{}
	require.NoError(t, fc.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: "hr1"}, latestHR))
	latestHR.Spec.Hostnames = []gatewayv1.Hostname{"new.example.com"}
	require.NoError(t, fc.Update(ctx, latestHR))
	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: "gw1"}})
	require.NoError(t, err)
	require.Equal(t, []string{"old.example.com", "new.example.com"}, seen)
}

func TestGatewayReconciler_Reconcile(t *testing.T) {
	for _, tt := range []struct {
		name               string
		existingObjs       []client.Object
		reqName            string
		mockDNSFail        bool
		listenerSetEnabled bool
		expectResult       ctrl.Result
		expectStatus       []string
		expectErr          bool
	}{
		{
			name: "success Gateway+ListenerSet",
			existingObjs: []client.Object{
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns1"}},
				newTestGateway("gw1", "ns1", "10.10.1.1", false, func(g *gatewayv1.Gateway) {
					g.Spec.Listeners = []gatewayv1.Listener{{Name: "dns-l1", Hostname: ptr(gatewayv1.Hostname("svc.example.com"))}}
				}),
				newTestListenerSet("ls1", "ns1", "gw1", "app.example.com"),
				newTestHTTPRouteReady("hr1", "ns1", "gw1", gatewayv1.Hostname("svc.example.com"), gatewayv1.Hostname("app.example.com")),
			},
			reqName: "gw1", listenerSetEnabled: true,
			expectStatus: []string{callIncreaseSyncTotal, callIncreaseUpdateTotal, callUpdateSuccess},
			expectResult: ResultNormal,
		},
		{
			name:         "deletion cleanup",
			existingObjs: []client.Object{newTestGateway("gw1", "ns1", "10.10.1.1", true, nil)},
			reqName:      "gw1",
			expectStatus: []string{callIncreaseSyncTotal, callIncreaseDeleteTotal, callDeleteSuccess},
			expectResult: ResultNormal,
		},
		{
			name: "DNS UpdateFail",
			existingObjs: []client.Object{
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns1"}},
				newTestGateway("gw1", "ns1", "10.10.1.1", false, func(g *gatewayv1.Gateway) {
					g.Spec.Listeners = []gatewayv1.Listener{{Name: "dns-l1", Hostname: ptr(gatewayv1.Hostname("svc.example.com"))}}
				}),
				newTestHTTPRouteReady("hr1", "ns1", "gw1", gatewayv1.Hostname("svc.example.com")),
			},
			reqName: "gw1", mockDNSFail: true,
			expectStatus: []string{callIncreaseSyncTotal, callIncreaseUpdateTotal, callUpdateFail},
			expectErr:    true, expectResult: common.ResultRequeueAfter10sec,
		},
		{
			name: "namespace missing",
			existingObjs: []client.Object{
				newTestGateway("gw1", "ns1", "10.10.1.1", false, func(g *gatewayv1.Gateway) {
					g.Spec.Listeners = []gatewayv1.Listener{{Name: "dns-l1", Hostname: ptr(gatewayv1.Hostname("svc.example.com"))}}
				}),
				newTestHTTPRouteReady("hr1", "ns1", "gw1", gatewayv1.Hostname("svc.example.com")),
			},
			reqName:      "gw1",
			expectStatus: []string{callIncreaseSyncTotal, callIncreaseUpdateTotal},
			expectErr:    true, expectResult: common.ResultRequeueAfter10sec,
		},
		{
			name:         "no IP deletes DNS",
			existingObjs: []client.Object{newTestGateway("gw1", "ns1", "", false, nil)},
			reqName:      "gw1",
			expectStatus: []string{callIncreaseSyncTotal, callIncreaseDeleteTotal, callDeleteSuccess},
			expectResult: ResultNormal,
		},
		{
			name: "Gateway not found", existingObjs: nil, reqName: "non-existent",
			expectStatus: []string{callIncreaseSyncTotal, callIncreaseDeleteTotal, callDeleteSuccess},
			expectResult: ResultNormal,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			objs := tt.existingObjs
			if objs == nil {
				objs = []client.Object{}
			}
			fc := fakeClientForGatewayTests(objs...)
			dnsSvc := &dns.DNSRecordService{DNSRecordStore: dns.BuildDNSRecordStore()}
			updater := newMockStatusUpdater().(*mockStatusUpdater)
			r := &GatewayReconciler{
				Client: fc, Scheme: scheme, Service: dnsSvc, StatusUpdater: updater,
				Recorder: record.NewFakeRecorder(10), listenerSetEnabled: tt.listenerSetEnabled,
				httpRouteEnabled: true,
			}
			p := gomonkey.ApplyMethod(reflect.TypeOf(r.Service), "CreateOrUpdateDNSRecords",
				func(_ *dns.DNSRecordService, _ context.Context, _ *dns.OwnerEndpoints) error {
					if tt.mockDNSFail {
						return fmt.Errorf("dns provider unreachable")
					}
					return nil
				})
			t.Cleanup(p.Reset)

			res, err := r.Reconcile(context.TODO(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: tt.reqName}})
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expectResult, res)
			assert.ElementsMatch(t, tt.expectStatus, updater.getCalls())
		})
	}
}

func Test_predicateFuncsGateway(t *testing.T) {
	withIP := &gatewayv1.Gateway{
		Spec:   gatewayv1.GatewaySpec{GatewayClassName: gatewayv1.ObjectName(common.ManagedK8sGatewayClassIstio)},
		Status: gatewayv1.GatewayStatus{Addresses: []gatewayv1.GatewayStatusAddress{{Type: &ipType, Value: "10.0.0.1"}}},
	}
	noIP := &gatewayv1.Gateway{Spec: gatewayv1.GatewaySpec{GatewayClassName: gatewayv1.ObjectName(common.ManagedK8sGatewayClassIstio)}}
	unmanaged := &gatewayv1.Gateway{Spec: gatewayv1.GatewaySpec{GatewayClassName: "other"}}
	h := gatewayv1.Hostname("new.example.com")
	newListeners := withIP.DeepCopy()
	newListeners.Spec.Listeners = []gatewayv1.Listener{{Name: "l1", Hostname: &h, Port: 80, Protocol: gatewayv1.HTTPProtocolType}}

	for _, tt := range []struct {
		name string
		want bool
		fn   func() bool
	}{
		{"Create/managed+IP", true, func() bool { return predicateFuncsGateway.Create(event.CreateEvent{Object: withIP}) }},
		{"Create/managed+noIP", false, func() bool { return predicateFuncsGateway.Create(event.CreateEvent{Object: noIP}) }},
		{"Create/unmanaged", false, func() bool { return predicateFuncsGateway.Create(event.CreateEvent{Object: unmanaged}) }},
		{"Update/both unmanaged", false, func() bool {
			return predicateFuncsGateway.Update(event.UpdateEvent{ObjectOld: unmanaged, ObjectNew: unmanaged})
		}},
		{"Update/no change", false, func() bool {
			return predicateFuncsGateway.Update(event.UpdateEvent{ObjectOld: withIP, ObjectNew: withIP})
		}},
		{"Update/addresses changed", true, func() bool {
			n := withIP.DeepCopy()
			n.Status.Addresses = nil
			return predicateFuncsGateway.Update(event.UpdateEvent{ObjectOld: withIP, ObjectNew: n})
		}},
		{"Update/listeners changed", true, func() bool {
			return predicateFuncsGateway.Update(event.UpdateEvent{ObjectOld: withIP, ObjectNew: newListeners})
		}},
		{"Delete/managed", true, func() bool { return predicateFuncsGateway.Delete(event.DeleteEvent{Object: withIP}) }},
		{"Delete/unmanaged", false, func() bool { return predicateFuncsGateway.Delete(event.DeleteEvent{Object: unmanaged}) }},
		{"Update/class managed→unmanaged", true, func() bool {
			n := withIP.DeepCopy()
			n.Spec.GatewayClassName = "other"
			return predicateFuncsGateway.Update(event.UpdateEvent{ObjectOld: withIP, ObjectNew: n})
		}},
		{"Update/class unmanaged→managed", true, func() bool {
			o := withIP.DeepCopy()
			o.Spec.GatewayClassName = "other"
			return predicateFuncsGateway.Update(event.UpdateEvent{ObjectOld: o, ObjectNew: withIP})
		}},
		{"Update/class other→other2", false, func() bool {
			n := unmanaged.DeepCopy()
			n.Spec.GatewayClassName = "other2"
			return predicateFuncsGateway.Update(event.UpdateEvent{ObjectOld: unmanaged, ObjectNew: n})
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.fn())
		})
	}
}

func Test_updateDNSRecordCondition(t *testing.T) {
	testGw := newTestGateway("gw1", "ns1", "10.10.1.1", false, nil)
	ls := newTestListenerSet("ls1", "ns1", "gw1", "unused.example.com")
	r, _ := fakeGatewayReconciler(t, testGw, ls)
	ctx := context.Background()

	for _, tt := range []struct {
		name   string
		owner  *dns.ResourceRef
		getKey types.NamespacedName
		obj    client.Object
	}{
		{"Gateway", &dns.ResourceRef{Kind: dns.ResourceKindGateway, Object: testGw.GetObjectMeta()},
			types.NamespacedName{Namespace: "ns1", Name: "gw1"}, &gatewayv1.Gateway{}},
		{"ListenerSet", &dns.ResourceRef{Kind: dns.ResourceKindListenerSet, Object: ls.GetObjectMeta()},
			types.NamespacedName{Namespace: "ns1", Name: "ls1"}, &gatewayv1.ListenerSet{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r.updateDNSRecordCondition(ctx, types.NamespacedName{Namespace: "ns1", Name: "gw1"}, tt.owner, nil)
			require.NoError(t, r.Client.Get(ctx, tt.getKey, tt.obj))
			var conds []metav1.Condition
			switch o := tt.obj.(type) {
			case *gatewayv1.Gateway:
				conds = o.Status.Conditions
			case *gatewayv1.ListenerSet:
				conds = o.Status.Conditions
			}
			var found bool
			for _, c := range conds {
				if c.Type == conditionTypeDNSReady {
					assert.Equal(t, metav1.ConditionTrue, c.Status)
					found = true
				}
			}
			assert.True(t, found)
		})
	}
	t.Run("unknown kind skipped", func(t *testing.T) {
		r.updateDNSRecordCondition(ctx, types.NamespacedName{Namespace: "ns1", Name: "gw1"}, &dns.ResourceRef{Kind: "Unknown", Object: &metav1.ObjectMeta{Namespace: "ns1", Name: "gw1"}}, nil)
	})
}

func Test_CollectGarbage(t *testing.T) {
	h := gatewayv1.Hostname("svc.example.com")
	for _, tt := range []struct {
		name         string
		k8sObjects   []client.Object
		seedRecords  []*dns.DNSRecord
		patchService func(*GatewayReconciler) func()
		wantErr      bool
	}{
		{name: "empty cache"},
		{
			name:        "orphan delete success",
			seedRecords: []*dns.DNSRecord{dnsRecordOwnedByGateway("rec-gc-1", "uid-gw1", "ns1", "gw1")},
		},
		{
			name:        "orphan delete fail",
			seedRecords: []*dns.DNSRecord{dnsRecordOwnedByGateway("rec-gc-2", "uid-gw1", "ns1", "gw1")},
			patchService: func(r *GatewayReconciler) func() {
				p := gomonkey.ApplyMethod(reflect.TypeOf(r.Service), "DeleteAllDNSRecordsInGateway",
					func(_ *dns.DNSRecordService, _ context.Context, _, _ string) error {
						return fmt.Errorf("gc delete error")
					})
				return p.Reset
			},
			wantErr: true,
		},
		{
			name:        "gateway still exists",
			seedRecords: []*dns.DNSRecord{dnsRecordOwnedByGateway("rec-gc-3", "uid-gw1", "ns1", "gw1")},
			k8sObjects: []client.Object{
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns1"}},
				&gatewayv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Namespace: "ns1", Name: "gw1"},
					Spec: gatewayv1.GatewaySpec{
						GatewayClassName: gatewayv1.ObjectName(common.ManagedK8sGatewayClassIstio),
						Listeners:        []gatewayv1.Listener{{Name: "l1", Hostname: &h, Port: 80, Protocol: gatewayv1.HTTPProtocolType}},
					},
					Status: gatewayv1.GatewayStatus{Addresses: []gatewayv1.GatewayStatusAddress{{Type: &ipType, Value: "10.0.0.1"}}},
				},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := fakeGatewayReconciler(t, tt.k8sObjects...)
			r.crdReady = true
			if len(tt.seedRecords) > 0 {
				require.NoError(t, r.Service.DNSRecordStore.Apply(tt.seedRecords))
			}
			if tt.patchService != nil {
				reset := tt.patchService(r)
				defer reset()
			}
			err := r.CollectGarbage(context.Background())
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func Test_CollectGarbage_crdNotReady(t *testing.T) {
	r, _ := fakeGatewayReconciler(t)
	assert.False(t, r.crdReady)
	assert.NoError(t, r.CollectGarbage(context.Background()))
	r.StatusUpdater.(*mockStatusUpdater).validateCalls(t, []string{})
}

func Test_findParentGatewayFromListenerSet(t *testing.T) {
	ns := gatewayv1.Namespace("other-ns")
	gwKind := gatewayv1.Kind("Gateway")
	gwGroup := gatewayv1.Group(gatewayv1.GroupName)
	wrongKind := gatewayv1.Kind("Service")
	wrongGroup := gatewayv1.Group("wrong.io")
	for _, tt := range []struct {
		name    string
		obj     client.Object
		wantNil bool
		wantNN  types.NamespacedName
	}{
		{"non-ListenerSet", &gatewayv1.Gateway{}, true, types.NamespacedName{}},
		{"wrong Kind", &gatewayv1.ListenerSet{Spec: gatewayv1.ListenerSetSpec{ParentRef: gatewayv1.ParentGatewayReference{Kind: &wrongKind, Name: "gw1"}}}, true, types.NamespacedName{}},
		{"wrong Group", &gatewayv1.ListenerSet{Spec: gatewayv1.ListenerSetSpec{ParentRef: gatewayv1.ParentGatewayReference{Group: &wrongGroup, Name: "gw1"}}}, true, types.NamespacedName{}},
		{"empty Name", &gatewayv1.ListenerSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns1"},
			Spec:       gatewayv1.ListenerSetSpec{ParentRef: gatewayv1.ParentGatewayReference{Name: ""}},
		}, true, types.NamespacedName{}},
		{"default namespace", &gatewayv1.ListenerSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns1"},
			Spec:       gatewayv1.ListenerSetSpec{ParentRef: gatewayv1.ParentGatewayReference{Name: "gw1"}},
		}, false, types.NamespacedName{Namespace: "ns1", Name: "gw1"}},
		{"parent namespace", &gatewayv1.ListenerSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns1"},
			Spec:       gatewayv1.ListenerSetSpec{ParentRef: gatewayv1.ParentGatewayReference{Name: "gw1", Namespace: &ns}},
		}, false, types.NamespacedName{Namespace: "other-ns", Name: "gw1"}},
		{"explicit kind/group", &gatewayv1.ListenerSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns1"},
			Spec:       gatewayv1.ListenerSetSpec{ParentRef: gatewayv1.ParentGatewayReference{Kind: &gwKind, Group: &gwGroup, Name: "gw1"}},
		}, false, types.NamespacedName{Namespace: "ns1", Name: "gw1"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := findParentGatewayFromListenerSet(tt.obj)
			if tt.wantNil {
				assert.Nil(t, got)
			} else {
				require.NotNil(t, got)
				assert.Equal(t, tt.wantNN, *got)
			}
		})
	}
}

func Test_listenerSetParentGatewayIndexFunc(t *testing.T) {
	for _, tt := range []struct {
		name string
		obj  client.Object
		want []string
	}{
		{"non-ListenerSet", &gatewayv1.Gateway{}, []string{}},
		{"valid", &gatewayv1.ListenerSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns1"},
			Spec:       gatewayv1.ListenerSetSpec{ParentRef: gatewayv1.ParentGatewayReference{Name: "gw1"}},
		}, []string{"ns1/gw1"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, listenerSetParentGatewayIndexFunc(tt.obj))
		})
	}
}

func Test_checkGatewayCRDs(t *testing.T) {
	for _, tt := range []struct {
		name         string
		resources    *metav1.APIResourceList
		discoveryErr error
		wantGW       bool
		wantLS       bool
		wantHTTP     bool
		wantGRPC     bool
		wantTLS      bool
		wantErr      bool
	}{
		{"both CRDs", &metav1.APIResourceList{APIResources: []metav1.APIResource{{Name: "gateways"}, {Name: "listenersets"}}}, nil, true, true, false, false, false, false},
		{"gateway only", &metav1.APIResourceList{APIResources: []metav1.APIResource{{Name: "gateways"}}}, nil, true, false, false, false, false, false},
		{"empty list", &metav1.APIResourceList{}, nil, false, false, false, false, false, false},
		{"nil list", nil, nil, false, false, false, false, false, false},
		{"404", nil, apierrors.NewNotFound(schema.GroupResource{Group: "gateway.k8s.io", Resource: "v1"}, ""), false, false, false, false, false, false},
		{"other error", nil, fmt.Errorf("refused"), false, false, false, false, false, true},
		{"extra resources", &metav1.APIResourceList{APIResources: []metav1.APIResource{{Name: "gateways"}, {Name: "httproutes"}, {Name: "grpcroutes"}, {Name: "tlsroutes"}}}, nil, true, false, true, true, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &GatewayReconciler{discoveryClient: &fakeDiscoveryClient{resources: tt.resources, err: tt.discoveryErr}}
			got, err := r.checkGatewayCRDs(nil)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantGW, got.Gateway)
			assert.Equal(t, tt.wantLS, got.ListenerSet)
			assert.Equal(t, tt.wantHTTP, got.HTTPRoute)
			assert.Equal(t, tt.wantGRPC, got.GRPCRoute)
			assert.Equal(t, tt.wantTLS, got.TLSRoute)
		})
	}
}

func Test_StartController(t *testing.T) {
	for _, tt := range []struct {
		name                     string
		resources                *metav1.APIResourceList
		discoveryErr             error
		stubSetupWithManagerNil bool
		wantErr                  bool
		wantCrdReady             bool
		wantLSEnabled            bool
	}{
		{name: "no gateway CRD", resources: &metav1.APIResourceList{}},
		{name: "discovery error", discoveryErr: fmt.Errorf("down"), wantErr: true},
		{
			name: "gateway only", resources: &metav1.APIResourceList{APIResources: []metav1.APIResource{{Name: "gateways"}}},
			stubSetupWithManagerNil: true, wantCrdReady: true,
		},
		{
			name: "gateway+listenerset", resources: &metav1.APIResourceList{APIResources: []metav1.APIResource{{Name: "gateways"}, {Name: "listenersets"}}},
			stubSetupWithManagerNil: true, wantCrdReady: true, wantLSEnabled: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := fakeGatewayReconciler(t)
			r.discoveryClient = &fakeDiscoveryClient{resources: tt.resources, err: tt.discoveryErr}
			mgr := &MockManager{client: r.Client, scheme: scheme}
			if tt.stubSetupWithManagerNil {
				p := gomonkey.ApplyPrivateMethod(reflect.TypeOf(r), "setupWithManager",
					func(_ *GatewayReconciler, _ ctrl.Manager) error { return nil })
				t.Cleanup(p.Reset)
			}
			err := r.StartController(mgr, nil)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.wantCrdReady, r.crdReady)
			assert.Equal(t, tt.wantLSEnabled, r.listenerSetEnabled)
		})
	}
}

// Test_StartController_IndexFieldError is isolated from Test_StartController so gomonkey patches
// on setupWithManager from other table cases cannot affect this scenario.
func Test_StartController_IndexFieldError(t *testing.T) {
	r, _ := fakeGatewayReconciler(t)
	r.discoveryClient = &fakeDiscoveryClient{resources: &metav1.APIResourceList{APIResources: []metav1.APIResource{
		{Name: "gateways"}, {Name: "listenersets"},
	}}}
	mgr := &MockManager{client: r.Client, scheme: scheme, indexer: &mockFieldIndexer{err: fmt.Errorf("index failed")}}
	err := r.StartController(mgr, nil)
	assert.Error(t, err)
	assert.True(t, r.crdReady)
	assert.True(t, r.listenerSetEnabled)
}


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
	"time"

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
		WithStatusSubresource(&gatewayv1.Gateway{}, &gatewayv1.ListenerSet{}).
		WithIndex(&gatewayv1.ListenerSet{}, listenerSetParentGatewayIndex, listenerSetParentGatewayIndexFunc)
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
	dnsService.GetPermittedZonesFunc = testPermittedZonesFunc("example.com")
	r := NewGatewayReconciler(mgr, dnsService)
	r.StatusUpdater = newMockStatusUpdater()
	return r, fc
}

func fakeClientForGatewayTests(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&gatewayv1.Gateway{}, &gatewayv1.ListenerSet{}).
		WithIndex(&gatewayv1.ListenerSet{}, listenerSetParentGatewayIndex, listenerSetParentGatewayIndexFunc).
		Build()
}

// testPermittedZonesFunc returns a zone list so CreateOrUpdateDNSRecords FQDN validation passes in unit tests without NSX.
func testPermittedZonesFunc(domain string) dns.GetPermittedZonesFunc {
	return func(_ context.Context, _ string) ([]dns.ZoneConfig, error) {
		return []dns.ZoneConfig{{Path: "/infra/dns-forwarder-zones/test", Domain: domain}}, nil
	}
}

// newGatewayTestDNSRecordService builds a DNSRecordService with a default example.com permitted zone.
func newGatewayTestDNSRecordService() *dns.DNSRecordService {
	s := &dns.DNSRecordService{DNSRecordStore: dns.BuildDNSRecordStore()}
	s.GetPermittedZonesFunc = testPermittedZonesFunc("example.com")
	return s
}

// fakeGatewayReconcilerWithPermittedZone overrides the permitted DNS zone domain (e.g. for E2E negative FQDN cases).
func fakeGatewayReconcilerWithPermittedZone(t *testing.T, domain string, objs ...client.Object) (*GatewayReconciler, client.Client) {
	t.Helper()
	r, fc := fakeGatewayReconciler(t, objs...)
	r.Service.GetPermittedZonesFunc = testPermittedZonesFunc(domain)
	return r, fc
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
		func(_ *dns.DNSRecordService, _ context.Context, _ *dns.Record) error { return nil })
	t.Cleanup(p.Reset)
}

func reconcilerForFakeClient(t *testing.T, listenerSetEnabled bool, objs ...client.Object) (*GatewayReconciler, client.Client, *mockStatusUpdater) {
	t.Helper()
	fc := fakeClientForGatewayTests(objs...)
	u := newMockStatusUpdater().(*mockStatusUpdater)
	r := &GatewayReconciler{
		Client: fc, Scheme: scheme, Service: newGatewayTestDNSRecordService(),
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
			{"nil", nil, false},
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
		{"nil", nil, 0, nil},
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

func Test_buildDNSRecordsForGateway_listenerSetOwner(t *testing.T) {
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns1"}}
	gw := newTestGateway("gw1", "ns1", "10.0.0.1", false, nil)
	ls := newTestListenerSet("ls1", "ns1", "gw1", "app.example.com")
	fc := fakeClientForGatewayTests(ns, gw, ls)
	r := &GatewayReconciler{Client: fc, listenerSetEnabled: true}
	recs, err := r.buildDNSRecordsForGateway(ctx, gw)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	assert.Equal(t, dns.ResourceKindListenerSet, recs[0].Owner.Kind)
	assert.Equal(t, []string{"app.example.com"}, recs[0].Hostnames)
}

func Test_Reconcile_gatewayClassTransitions(t *testing.T) {
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns1"}}
	gw := newTestGateway("gw1", "ns1", "10.0.0.1", false, func(g *gatewayv1.Gateway) {
		g.Spec.GatewayClassName = "other"
		g.Spec.Listeners = []gatewayv1.Listener{{Name: "l1", Hostname: ptr(gatewayv1.Hostname("svc.example.com")), Port: 80, Protocol: gatewayv1.HTTPProtocolType}}
	})
	fc := fakeClientForGatewayTests(ns, gw)
	dnsSvc := newGatewayTestDNSRecordService()
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
	fc := fakeClientForGatewayTests(ns, gw)
	r := &GatewayReconciler{
		Client: fc, Scheme: scheme, Service: newGatewayTestDNSRecordService(),
		StatusUpdater: newMockStatusUpdater(), Recorder: record.NewFakeRecorder(10),
	}
	var seen []*dns.Record
	p := gomonkey.ApplyMethod(reflect.TypeOf(r.Service), "CreateOrUpdateDNSRecords",
		func(_ *dns.DNSRecordService, _ context.Context, rec *dns.Record) error {
			seen = append(seen, rec)
			return nil
		})
	t.Cleanup(p.Reset)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: "gw1"}}

	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Len(t, seen, 1)
	assert.True(t, seen[0].Addresses[0].Equal(net.ParseIP("10.0.0.1")))
	assert.Equal(t, []string{"a.example.com"}, seen[0].Hostnames)

	latest := &gatewayv1.Gateway{}
	require.NoError(t, fc.Get(ctx, req.NamespacedName, latest))
	latest.Status.Addresses = []gatewayv1.GatewayStatusAddress{{Type: &ipType, Value: "10.0.0.2"}}
	require.NoError(t, fc.Status().Update(ctx, latest))
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Len(t, seen, 2)
	assert.True(t, seen[1].Addresses[0].Equal(net.ParseIP("10.0.0.2")))

	require.NoError(t, fc.Get(ctx, req.NamespacedName, latest))
	h := gatewayv1.Hostname("b.example.com")
	latest.Spec.Listeners = []gatewayv1.Listener{{Name: "l1", Hostname: &h, Port: 80, Protocol: gatewayv1.HTTPProtocolType}}
	require.NoError(t, fc.Update(ctx, latest))
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Len(t, seen, 3)
	assert.Equal(t, []string{"b.example.com"}, seen[2].Hostnames)
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
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns1", UID: types.UID("uid-ns1")}}
	gw := newTestGateway("gw1", "ns1", "10.0.0.1", false, nil)
	ls := newTestListenerSet("ls1", "ns1", "gw1", "old.example.com")
	r, fc, _ := reconcilerForFakeClient(t, true, ns, gw, ls)

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: "gw1"}})
	require.NoError(t, err)
	lsRecs := r.Service.DNSRecordStore.GetByOwnerResourceUID(dns.ResourceKindListenerSet, string(ls.UID))
	require.NotEmpty(t, lsRecs, "ListenerSet DNS records after first reconcile")
	assert.True(t, hasFQDNInRecords(lsRecs, "old.example.com"))

	latestLS := &gatewayv1.ListenerSet{}
	require.NoError(t, fc.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: "ls1"}, latestLS))
	latestLS.Spec.Listeners[0].Hostname = ptr(gatewayv1.Hostname("new.example.com"))
	require.NoError(t, fc.Update(ctx, latestLS))
	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: "gw1"}})
	require.NoError(t, err)
	lsRecs = r.Service.DNSRecordStore.GetByOwnerResourceUID(dns.ResourceKindListenerSet, string(ls.UID))
	require.NotEmpty(t, lsRecs, "ListenerSet DNS records after hostname update")
	assert.True(t, hasFQDNInRecords(lsRecs, "new.example.com"))
	assert.False(t, hasFQDNInRecords(lsRecs, "old.example.com"))
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
			dnsSvc := newGatewayTestDNSRecordService()
			updater := newMockStatusUpdater().(*mockStatusUpdater)
			r := &GatewayReconciler{
				Client: fc, Scheme: scheme, Service: dnsSvc, StatusUpdater: updater,
				Recorder: record.NewFakeRecorder(10), listenerSetEnabled: tt.listenerSetEnabled,
			}
			p := gomonkey.ApplyMethod(reflect.TypeOf(r.Service), "CreateOrUpdateDNSRecords",
				func(_ *dns.DNSRecordService, _ context.Context, _ *dns.Record) error {
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
			r.updateDNSRecordCondition(ctx, tt.owner, nil)
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
		r.updateDNSRecordCondition(ctx, &dns.ResourceRef{Kind: "Unknown", Object: &metav1.ObjectMeta{Namespace: "ns1", Name: "gw1"}}, nil)
	})
}

func Test_CollectGarbage(t *testing.T) {
	h := gatewayv1.Hostname("svc.example.com")
	for _, tt := range []struct {
		name         string
		k8sObjects   []client.Object
		seedRecords  []*dns.DNSRecord
		patchService func(*GatewayReconciler) func()
		wantCalls    []string
		wantErr      bool
	}{
		{name: "empty cache", wantCalls: []string{}},
		{
			name:        "orphan delete success",
			seedRecords: []*dns.DNSRecord{dnsRecordOwnedByGateway("rec-gc-1", "uid-gw1", "ns1", "gw1")},
			wantCalls:   []string{callDeleteSuccess},
		},
		{
			name:        "gateway still exists",
			seedRecords: []*dns.DNSRecord{dnsRecordOwnedByGateway("rec-gc-3", "uid-gw1", "ns1", "gw1")},
			k8sObjects: []client.Object{&gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{Namespace: "ns1", Name: "gw1"},
				Spec: gatewayv1.GatewaySpec{
					GatewayClassName: gatewayv1.ObjectName(common.ManagedK8sGatewayClassIstio),
					Listeners:        []gatewayv1.Listener{{Name: "l1", Hostname: &h, Port: 80, Protocol: gatewayv1.HTTPProtocolType}},
				},
				Status: gatewayv1.GatewayStatus{Addresses: []gatewayv1.GatewayStatusAddress{{Type: &ipType, Value: "10.0.0.1"}}},
			}},
			wantCalls: []string{},
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
			r.StatusUpdater.(*mockStatusUpdater).validateCalls(t, tt.wantCalls)
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
		wantErr      bool
	}{
		{"both CRDs", &metav1.APIResourceList{APIResources: []metav1.APIResource{{Name: "gateways"}, {Name: "listenersets"}}}, nil, true, true, false},
		{"gateway only", &metav1.APIResourceList{APIResources: []metav1.APIResource{{Name: "gateways"}}}, nil, true, false, false},
		{"empty list", &metav1.APIResourceList{}, nil, false, false, false},
		{"nil list", nil, nil, false, false, false},
		{"404", nil, apierrors.NewNotFound(schema.GroupResource{Group: "gateway.k8s.io", Resource: "v1"}, ""), false, false, false},
		{"other error", nil, fmt.Errorf("refused"), false, false, true},
		{"extra resources", &metav1.APIResourceList{APIResources: []metav1.APIResource{{Name: "gateways"}, {Name: "httproutes"}}}, nil, true, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &GatewayReconciler{discoveryClient: &fakeDiscoveryClient{resources: tt.resources, err: tt.discoveryErr}}
			gotGW, gotLS, err := r.checkGatewayCRDs(nil)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantGW, gotGW)
			assert.Equal(t, tt.wantLS, gotLS)
		})
	}
}

func Test_StartController(t *testing.T) {
	for _, tt := range []struct {
		name                    string
		resources               *metav1.APIResourceList
		discoveryErr            error
		indexFieldErr           error
		stubSetupWithManagerNil bool
		wantErr                 bool
		wantCrdReady            bool
		wantLSEnabled           bool
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
		{
			name: "IndexField error", resources: &metav1.APIResourceList{APIResources: []metav1.APIResource{{Name: "gateways"}, {Name: "listenersets"}}},
			indexFieldErr: fmt.Errorf("index failed"), wantErr: true, wantCrdReady: true, wantLSEnabled: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := fakeGatewayReconciler(t)
			r.discoveryClient = &fakeDiscoveryClient{resources: tt.resources, err: tt.discoveryErr}
			mgr := &MockManager{client: r.Client, scheme: scheme}
			if tt.indexFieldErr != nil {
				mgr.indexer = &mockFieldIndexer{err: tt.indexFieldErr}
			}
			if tt.stubSetupWithManagerNil {
				origGC := startPeriodicGatewayGC
				startPeriodicGatewayGC = func(chan bool, time.Duration, func(context.Context) error) {}
				t.Cleanup(func() { startPeriodicGatewayGC = origGC })
				setupWithManagerTestHook = func(_ *GatewayReconciler, _ ctrl.Manager) error { return nil }
				t.Cleanup(func() { setupWithManagerTestHook = nil })
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

func Test_NewGatewayReconciler(t *testing.T) {
	t.Run("nil service produces nil StatusUpdater", func(t *testing.T) {
		mgr, _ := createFakeManagerAndClient()
		r := NewGatewayReconciler(mgr, nil)
		assert.Nil(t, r.StatusUpdater)
	})
	t.Run("service with NSXConfig sets StatusUpdater", func(t *testing.T) {
		mgr, fc := createFakeManagerAndClient()
		svc := servicecommon.Service{
			Client:    fc,
			NSXConfig: &config.NSXOperatorConfig{CoeConfig: &config.CoeConfig{}},
		}
		// Avoid InitializeDNSRecordService here: it starts async NSX store init; wiring is the same as production uses after init completes.
		dnsService := &dns.DNSRecordService{Service: svc, DNSRecordStore: dns.BuildDNSRecordStore()}
		r := NewGatewayReconciler(mgr, dnsService)
		require.NotNil(t, r.StatusUpdater)
		su, ok := r.StatusUpdater.(*common.StatusUpdater)
		require.True(t, ok, "StatusUpdater should be *common.StatusUpdater when service is provided")
		assert.NotNil(t, su.NSXConfig)
	})
}

// --------------------------------------------------------------------------
// E2E test helpers
// --------------------------------------------------------------------------

const (
	e2eGWNamespace      = "ns1"
	e2eGWName           = "gw1"
	e2ePermittedDNSZone = "example.com"
)

func e2eTestNamespace() *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: e2eGWNamespace}}
}

func e2eGatewayRequest() ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: e2eGWNamespace, Name: e2eGWName}}
}

// e2eTypeMetaFor derives the canonical APIVersion and Kind for obj from the test scheme.
func e2eTypeMetaFor(obj runtime.Object) metav1.TypeMeta {
	gvks, _, _ := scheme.ObjectKinds(obj)
	if len(gvks) == 0 {
		return metav1.TypeMeta{}
	}
	return metav1.TypeMeta{
		APIVersion: gvks[0].GroupVersion().String(),
		Kind:       gvks[0].Kind,
	}
}

// e2eGateway creates a managed Gateway in e2eGWNamespace/e2eGWName.
// listenerHostnames → Spec.Listeners; statusIPs → Status.Addresses.
// Optional mutateMeta funcs run last to allow e.g. setting DeletionTimestamp.
func e2eGateway(uid string, listenerHostnames, statusIPs []string, mutateMeta ...func(*gatewayv1.Gateway)) *gatewayv1.Gateway {
	gwIPType := gatewayv1.IPAddressType
	gw := &gatewayv1.Gateway{
		TypeMeta: e2eTypeMetaFor(&gatewayv1.Gateway{}),
		ObjectMeta: metav1.ObjectMeta{
			Namespace: e2eGWNamespace,
			Name:      e2eGWName,
			UID:       types.UID(uid),
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: gatewayv1.ObjectName(common.ManagedK8sGatewayClassIstio),
		},
	}
	for i, hostname := range listenerHostnames {
		hh := gatewayv1.Hostname(hostname)
		gw.Spec.Listeners = append(gw.Spec.Listeners, gatewayv1.Listener{
			Name:     gatewayv1.SectionName(fmt.Sprintf("l%d", i+1)),
			Hostname: &hh,
			Port:     80,
			Protocol: gatewayv1.HTTPProtocolType,
		})
	}
	for _, ip := range statusIPs {
		gw.Status.Addresses = append(gw.Status.Addresses, gatewayv1.GatewayStatusAddress{
			Type:  &gwIPType,
			Value: ip,
		})
	}
	for _, f := range mutateMeta {
		f(gw)
	}
	return gw
}

// e2eListenerSet creates a ListenerSet in e2eGWNamespace with a ParentRef pointing to gwName.
func e2eListenerSet(uid, lsName, gwName string, listenerHostnames []string) *gatewayv1.ListenerSet {
	gwTM := e2eTypeMetaFor(&gatewayv1.Gateway{})
	gwKind := gatewayv1.Kind(gwTM.Kind)
	gwGroup := gatewayv1.Group(gatewayv1.GroupName)
	gwNS := gatewayv1.Namespace(e2eGWNamespace)
	ls := &gatewayv1.ListenerSet{
		TypeMeta: e2eTypeMetaFor(&gatewayv1.ListenerSet{}),
		ObjectMeta: metav1.ObjectMeta{
			Namespace: e2eGWNamespace,
			Name:      lsName,
			UID:       types.UID(uid),
		},
		Spec: gatewayv1.ListenerSetSpec{
			ParentRef: gatewayv1.ParentGatewayReference{
				Kind:      &gwKind,
				Group:     &gwGroup,
				Name:      gatewayv1.ObjectName(gwName),
				Namespace: &gwNS,
			},
		},
	}
	for i, hostname := range listenerHostnames {
		hh := gatewayv1.Hostname(hostname)
		ls.Spec.Listeners = append(ls.Spec.Listeners, gatewayv1.ListenerEntry{
			Name:     gatewayv1.SectionName(fmt.Sprintf("l%d", i+1)),
			Hostname: &hh,
		})
	}
	return ls
}

// e2eClientObjects builds the fake-client seed list: Namespace + gw + optional ls.
func e2eClientObjects(gw *gatewayv1.Gateway, ls *gatewayv1.ListenerSet) []client.Object {
	objs := []client.Object{e2eTestNamespace(), gw}
	if ls != nil {
		objs = append(objs, ls)
	}
	return objs
}

// hasFQDNInRecords reports whether any record in records has the given FQDN.
func hasFQDNInRecords(records []*dns.DNSRecord, fqdn string) bool {
	for _, r := range records {
		if r.Fqdn != nil && *r.Fqdn == fqdn {
			return true
		}
	}
	return false
}

// getGatewayDNSReadyCondition returns the DNSReady condition from the Gateway's current status.
func getGatewayDNSReadyCondition(ctx context.Context, r *GatewayReconciler, key types.NamespacedName) *metav1.Condition {
	gw := &gatewayv1.Gateway{}
	if err := r.Client.Get(ctx, key, gw); err != nil {
		return nil
	}
	for _, c := range gw.Status.Conditions {
		if c.Type == conditionTypeDNSReady {
			cc := c
			return &cc
		}
	}
	return nil
}

// getListenerSetDNSReadyCondition returns the DNSReady condition from the ListenerSet's current status.
func getListenerSetDNSReadyCondition(ctx context.Context, r *GatewayReconciler, key types.NamespacedName) *metav1.Condition {
	ls := &gatewayv1.ListenerSet{}
	if err := r.Client.Get(ctx, key, ls); err != nil {
		return nil
	}
	for _, c := range ls.Status.Conditions {
		if c.Type == conditionTypeDNSReady {
			cc := c
			return &cc
		}
	}
	return nil
}

// e2eResourceRef builds a ResourceRef for use in seeding DNS records directly.
func e2eResourceRef(kind, namespace, name, uid string) *dns.ResourceRef {
	return &dns.ResourceRef{
		Kind:   kind,
		Object: &metav1.ObjectMeta{Namespace: namespace, Name: name, UID: types.UID(uid)},
	}
}

// --------------------------------------------------------------------------
// Test_Reconcile_E2E — end-to-end reconcile verification against DNSRecordStore
// --------------------------------------------------------------------------

func Test_Reconcile_E2E(t *testing.T) {
	ctx := context.Background()

	type reconcileE2ECase struct {
		name            string
		gw              *gatewayv1.Gateway
		listenerSet     *gatewayv1.ListenerSet
		gwSecond        *gatewayv1.Gateway
		lsSecond        *gatewayv1.ListenerSet
		seedFn          func(r *GatewayReconciler)
		wantErrSecond   bool
		verify          func(t *testing.T, r *GatewayReconciler)
		wantStatusCalls []string
	}

	tests := []reconcileE2ECase{
		{
			name: "create_dns_records_in_store",
			gw:   e2eGateway("gw-uid-1", []string{"svc.example.com"}, []string{"10.0.0.1"}),
			verify: func(t *testing.T, r *GatewayReconciler) {
				records := r.Service.DNSRecordStore.GetByOwnerResourceUID(dns.ResourceKindGateway, "gw-uid-1")
				require.NotEmpty(t, records, "expected DNS records in store for Gateway owner")
				assert.True(t, hasFQDNInRecords(records, "svc.example.com"))
				assert.True(t, r.Service.ListGatewayNamespacedName().Has(
					types.NamespacedName{Namespace: e2eGWNamespace, Name: e2eGWName}))
			},
			wantStatusCalls: []string{callIncreaseSyncTotal, callIncreaseUpdateTotal, callUpdateSuccess},
		},
		{
			name:        "gateway_and_listener_set_distinct_hostnames",
			gw:          e2eGateway("gw-uid-2", []string{"gw-listener.example.com"}, []string{"10.0.0.1"}),
			listenerSet: e2eListenerSet("ls-uid-2", "ls1", e2eGWName, []string{"ls-listener.example.com"}),
			verify: func(t *testing.T, r *GatewayReconciler) {
				gwRecs := r.Service.DNSRecordStore.GetByOwnerResourceUID(dns.ResourceKindGateway, "gw-uid-2")
				require.NotEmpty(t, gwRecs, "expected Gateway DNS records")
				assert.True(t, hasFQDNInRecords(gwRecs, "gw-listener.example.com"))

				lsRecs := r.Service.DNSRecordStore.GetByOwnerResourceUID(dns.ResourceKindListenerSet, "ls-uid-2")
				require.NotEmpty(t, lsRecs, "expected ListenerSet DNS records")
				assert.True(t, hasFQDNInRecords(lsRecs, "ls-listener.example.com"))
			},
			wantStatusCalls: []string{callIncreaseSyncTotal, callIncreaseUpdateTotal, callUpdateSuccess},
		},
		{
			name:     "delete_dns_records_when_gateway_has_no_ip",
			gw:       e2eGateway("gw-uid-3", []string{"svc.example.com"}, []string{"10.0.0.1"}),
			gwSecond: e2eGateway("gw-uid-3", []string{"svc.example.com"}, nil),
			verify: func(t *testing.T, r *GatewayReconciler) {
				assert.Empty(t, r.Service.DNSRecordStore.GetByOwnerResourceUID(dns.ResourceKindGateway, "gw-uid-3"),
					"store should be empty after gateway loses its IP")
			},
			wantStatusCalls: []string{
				callIncreaseSyncTotal, callIncreaseUpdateTotal, callUpdateSuccess,
				callIncreaseSyncTotal, callIncreaseDeleteTotal, callDeleteSuccess,
			},
		},
		{
			name: "gateway_deleting_removes_records_from_store",
			gw: e2eGateway("gw-uid-4", []string{"svc.example.com"}, []string{"10.0.0.1"}, func(gw *gatewayv1.Gateway) {
				now := metav1.Now()
				gw.DeletionTimestamp = &now
				gw.Finalizers = []string{"x"}
			}),
			seedFn: func(r *GatewayReconciler) {
				gwRef := e2eResourceRef(dns.ResourceKindGateway, e2eGWNamespace, e2eGWName, "gw-uid-4")
				rec := &dns.Record{
					Addresses:       []net.IP{net.ParseIP("10.0.0.1")},
					Hostnames:       []string{"svc.example.com"},
					Owner:           gwRef,
					AddressProvider: gwRef,
				}
				require.NoError(t, r.Service.CreateOrUpdateDNSRecords(context.Background(), rec))
				require.NotEmpty(t, r.Service.DNSRecordStore.GetByOwnerResourceUID(dns.ResourceKindGateway, "gw-uid-4"),
					"precondition: store must be non-empty before reconcile")
			},
			verify: func(t *testing.T, r *GatewayReconciler) {
				assert.Empty(t, r.Service.DNSRecordStore.GetByOwnerResourceUID(dns.ResourceKindGateway, "gw-uid-4"),
					"store should be empty after gateway deletion reconcile")
			},
			wantStatusCalls: []string{callIncreaseSyncTotal, callIncreaseDeleteTotal, callDeleteSuccess},
		},
		{
			name:     "update_dns_records_in_store",
			gw:       e2eGateway("gw-uid-5", []string{"svc.example.com"}, []string{"10.0.0.1"}),
			gwSecond: e2eGateway("gw-uid-5", []string{"svc.example.com"}, []string{"10.0.0.2"}),
			verify: func(t *testing.T, r *GatewayReconciler) {
				records := r.Service.DNSRecordStore.GetByOwnerResourceUID(dns.ResourceKindGateway, "gw-uid-5")
				require.NotEmpty(t, records, "store should have updated records")
				found10001, found10002 := false, false
				for _, rec := range records {
					if rec.IpAddress != nil {
						switch *rec.IpAddress {
						case "10.0.0.1":
							found10001 = true
						case "10.0.0.2":
							found10002 = true
						}
					}
				}
				assert.False(t, found10001, "old IP 10.0.0.1 must be removed after update")
				assert.True(t, found10002, "new IP 10.0.0.2 must be present after update")
			},
			wantStatusCalls: []string{
				callIncreaseSyncTotal, callIncreaseUpdateTotal, callUpdateSuccess,
				callIncreaseSyncTotal, callIncreaseUpdateTotal, callUpdateSuccess,
			},
		},
		{
			name:          "gateway_hostname_update_invalid_zone_clears_dns_and_dnsready_false",
			gw:            e2eGateway("gw-uid-6", []string{"svc.example.com"}, []string{"10.0.0.1"}),
			gwSecond:      e2eGateway("gw-uid-6", []string{"invalid.notourzone.com"}, []string{"10.0.0.1"}),
			wantErrSecond: true,
			verify: func(t *testing.T, r *GatewayReconciler) {
				assert.Empty(t, r.Service.DNSRecordStore.GetByOwnerResourceUID(dns.ResourceKindGateway, "gw-uid-6"),
					"store should be empty after invalid FQDN update")
				cond := getGatewayDNSReadyCondition(ctx, r,
					types.NamespacedName{Namespace: e2eGWNamespace, Name: e2eGWName})
				require.NotNil(t, cond, "DNSReady condition must be set on Gateway")
				assert.Equal(t, metav1.ConditionFalse, cond.Status)
				assert.Equal(t, reasonDNSRecordFailed, cond.Reason)
				assert.Contains(t, cond.Message, "invalid FQDNs are detected")
			},
			wantStatusCalls: []string{
				callIncreaseSyncTotal, callIncreaseUpdateTotal, callUpdateSuccess,
				callIncreaseSyncTotal, callIncreaseUpdateTotal, callUpdateFail,
			},
		},
		{
			name:          "listenerset_hostname_update_invalid_zone_clears_dns_and_dnsready_false",
			gw:            e2eGateway("gw-uid-7", nil, []string{"10.0.0.1"}),
			listenerSet:   e2eListenerSet("ls-uid-7", "ls1", e2eGWName, []string{"ls-good.example.com"}),
			lsSecond:      e2eListenerSet("ls-uid-7", "ls1", e2eGWName, []string{"ls-app.notourzone.com"}),
			wantErrSecond: true,
			verify: func(t *testing.T, r *GatewayReconciler) {
				assert.Empty(t, r.Service.DNSRecordStore.GetByOwnerResourceUID(dns.ResourceKindListenerSet, "ls-uid-7"),
					"LS DNS records should be cleared after invalid FQDN")
				cond := getListenerSetDNSReadyCondition(ctx, r,
					types.NamespacedName{Namespace: e2eGWNamespace, Name: "ls1"})
				require.NotNil(t, cond, "ListenerSet DNSReady condition must be set")
				assert.Equal(t, metav1.ConditionFalse, cond.Status)
				assert.Equal(t, reasonDNSRecordFailed, cond.Reason)
				assert.Contains(t, cond.Message, "invalid FQDNs are detected")
			},
			wantStatusCalls: []string{
				callIncreaseSyncTotal, callIncreaseUpdateTotal, callUpdateSuccess,
				callIncreaseSyncTotal, callIncreaseUpdateTotal, callUpdateFail,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := fakeGatewayReconcilerWithPermittedZone(t, e2ePermittedDNSZone, e2eClientObjects(tc.gw, tc.listenerSet)...)
			r.listenerSetEnabled = true

			if tc.seedFn != nil {
				tc.seedFn(r)
			}

			needSecond := tc.gwSecond != nil || tc.lsSecond != nil

			if !needSecond {
				// Single reconcile
				result, err := r.Reconcile(ctx, e2eGatewayRequest())
				require.NoError(t, err)
				assert.False(t, result.Requeue)
			} else {
				// First reconcile (skip if we only seeded the store)
				if tc.seedFn == nil {
					result, err := r.Reconcile(ctx, e2eGatewayRequest())
					require.NoError(t, err, "first reconcile should succeed")
					assert.False(t, result.Requeue)
				}

				// Build second reconciler with the updated objects, sharing the same service
				gwForSecond := tc.gwSecond
				if gwForSecond == nil {
					gwForSecond = tc.gw
				}
				lsForSecond := tc.lsSecond
				if lsForSecond == nil {
					lsForSecond = tc.listenerSet
				}
				r2, _ := fakeGatewayReconcilerWithPermittedZone(t, e2ePermittedDNSZone, e2eClientObjects(gwForSecond, lsForSecond)...)
				r2.Service = r.Service
				r2.StatusUpdater = r.StatusUpdater
				r2.listenerSetEnabled = true

				result2, err2 := r2.Reconcile(ctx, e2eGatewayRequest())
				if tc.wantErrSecond {
					assert.Error(t, err2, "second reconcile should return an error")
					assert.True(t, result2.Requeue || result2.RequeueAfter > 0)
				} else {
					require.NoError(t, err2)
					assert.False(t, result2.Requeue)
				}
				r = r2
			}

			if tc.verify != nil {
				tc.verify(t, r)
			}
			if tc.wantStatusCalls != nil {
				r.StatusUpdater.(*mockStatusUpdater).validateCalls(t, tc.wantStatusCalls)
			}
		})
	}
}

// --------------------------------------------------------------------------
// Test_CollectGarbage_E2E — end-to-end GC verification against DNSRecordStore
// --------------------------------------------------------------------------

func Test_CollectGarbage_E2E(t *testing.T) {
	ctx := context.Background()

	type collectGarbageE2ECase struct {
		name       string
		k8sObjects []client.Object
		seedFn     func(t *testing.T, r *GatewayReconciler)
		verify     func(t *testing.T, r *GatewayReconciler)
	}

	tests := []collectGarbageE2ECase{
		{
			name:       "removes_dns_records_when_gateway_not_in_cluster",
			k8sObjects: []client.Object{e2eTestNamespace()},
			seedFn: func(t *testing.T, r *GatewayReconciler) {
				ref := e2eResourceRef(dns.ResourceKindGateway, e2eGWNamespace, "gone-gw", "uid-gone-gw")
				require.NoError(t, r.Service.CreateOrUpdateDNSRecords(ctx, &dns.Record{
					Addresses:       []net.IP{net.ParseIP("10.0.0.1")},
					Hostnames:       []string{"gone.example.com"},
					Owner:           ref,
					AddressProvider: ref,
				}))
			},
			verify: func(t *testing.T, r *GatewayReconciler) {
				assert.Empty(t, r.Service.DNSRecordStore.GetByOwnerResourceUID(dns.ResourceKindGateway, "uid-gone-gw"),
					"GC should clear records for Gateway not in cluster")
				assert.False(t, r.Service.ListGatewayNamespacedName().Has(
					types.NamespacedName{Namespace: e2eGWNamespace, Name: "gone-gw"}))
			},
		},
		{
			name: "removes_orphan_listener_set_dns_records_keeps_gateway_records",
			k8sObjects: e2eClientObjects(
				e2eGateway("gw-uid-gc1", []string{"gw.example.com"}, []string{"10.0.0.1"}),
				nil,
			),
			seedFn: func(t *testing.T, r *GatewayReconciler) {
				gwRef := e2eResourceRef(dns.ResourceKindGateway, e2eGWNamespace, e2eGWName, "gw-uid-gc1")
				// GW owner record
				require.NoError(t, r.Service.CreateOrUpdateDNSRecords(ctx, &dns.Record{
					Addresses: []net.IP{net.ParseIP("10.0.0.1")},
					Hostnames: []string{"gw.example.com"},
					Owner:     gwRef, AddressProvider: gwRef,
				}))
				// Orphaned LS record (LS does not exist in K8s)
				require.NoError(t, r.Service.CreateOrUpdateDNSRecords(ctx, &dns.Record{
					Addresses:       []net.IP{net.ParseIP("10.0.0.1")},
					Hostnames:       []string{"orphan-ls.example.com"},
					Owner:           e2eResourceRef(dns.ResourceKindListenerSet, e2eGWNamespace, "orphan-ls", "uid-orphan-ls"),
					AddressProvider: gwRef,
				}))
			},
			verify: func(t *testing.T, r *GatewayReconciler) {
				assert.NotEmpty(t, r.Service.DNSRecordStore.GetByOwnerResourceUID(dns.ResourceKindGateway, "gw-uid-gc1"),
					"Gateway owner records should survive GC")
				assert.Empty(t, r.Service.DNSRecordStore.GetByOwnerResourceUID(dns.ResourceKindListenerSet, "uid-orphan-ls"),
					"orphaned LS records should be removed by GC")
			},
		},
		{
			name: "removes_deleted_listener_set_dns_records_keeps_existing_listener_set_records",
			k8sObjects: e2eClientObjects(
				e2eGateway("gw-uid-gc2", []string{"gw.example.com"}, []string{"10.0.0.1"}),
				e2eListenerSet("ls-uid-kept", "ls-kept", e2eGWName, []string{"kept-ls.example.com"}),
			),
			seedFn: func(t *testing.T, r *GatewayReconciler) {
				gwRef := e2eResourceRef(dns.ResourceKindGateway, e2eGWNamespace, e2eGWName, "gw-uid-gc2")
				require.NoError(t, r.Service.CreateOrUpdateDNSRecords(ctx, &dns.Record{
					Addresses: []net.IP{net.ParseIP("10.0.0.1")}, Hostnames: []string{"gw.example.com"},
					Owner: gwRef, AddressProvider: gwRef,
				}))
				require.NoError(t, r.Service.CreateOrUpdateDNSRecords(ctx, &dns.Record{
					Addresses:       []net.IP{net.ParseIP("10.0.0.1")},
					Hostnames:       []string{"kept-ls.example.com"},
					Owner:           e2eResourceRef(dns.ResourceKindListenerSet, e2eGWNamespace, "ls-kept", "ls-uid-kept"),
					AddressProvider: gwRef,
				}))
				require.NoError(t, r.Service.CreateOrUpdateDNSRecords(ctx, &dns.Record{
					Addresses:       []net.IP{net.ParseIP("10.0.0.1")},
					Hostnames:       []string{"removed-ls.example.com"},
					Owner:           e2eResourceRef(dns.ResourceKindListenerSet, e2eGWNamespace, "ls-removed", "ls-uid-removed"),
					AddressProvider: gwRef,
				}))
			},
			verify: func(t *testing.T, r *GatewayReconciler) {
				assert.NotEmpty(t, r.Service.DNSRecordStore.GetByOwnerResourceUID(dns.ResourceKindGateway, "gw-uid-gc2"),
					"GW records should survive GC")
				assert.NotEmpty(t, r.Service.DNSRecordStore.GetByOwnerResourceUID(dns.ResourceKindListenerSet, "ls-uid-kept"),
					"kept LS records should survive GC")
				assert.Empty(t, r.Service.DNSRecordStore.GetByOwnerResourceUID(dns.ResourceKindListenerSet, "ls-uid-removed"),
					"removed LS records should be cleared by GC")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := fakeGatewayReconcilerWithPermittedZone(t, e2ePermittedDNSZone, tc.k8sObjects...)
			r.crdReady = true
			r.listenerSetEnabled = true
			if tc.seedFn != nil {
				tc.seedFn(t, r)
			}
			require.NoError(t, r.CollectGarbage(ctx))
			if tc.verify != nil {
				tc.verify(t, r)
			}
		})
	}
}

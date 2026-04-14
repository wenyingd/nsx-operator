/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package gateway

import (
	"cmp"
	"context"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apimachineryruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/vmware-tanzu/nsx-operator/pkg/controllers/common"
	"github.com/vmware-tanzu/nsx-operator/pkg/logger"
	servicecommon "github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/dns"
	extdns "github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/endpoint"
	extdnssrc "github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/source"
)

const (
	listenerSetParentGatewayIndex = "listenerSetParentGateway"
	// DNS condition type and reasons for Gateway/ListenerSet status.
	conditionTypeDNSReady     = "DNSReady"
	reasonDNSRecordConfigured = "DNSRecordConfigured"
	reasonDNSRecordFailed     = "DNSRecordFailed"
	// Gateway API group version used for CRD presence checks via discovery.
	gatewayAPIGroupVersion = "gateway.networking.k8s.io/v1"
)

var (
	log                    = logger.Log
	ResultNormal           = common.ResultNormal
	filteredGatewayClasses = sets.New[string](common.ManagedK8sGatewayClassIstio)
)

// statusUpdater is an interface for test
type statusUpdater interface {
	UpdateSuccess(ctx context.Context, obj client.Object, setStatusFn common.UpdateSuccessStatusFn, args ...interface{})
	UpdateFail(ctx context.Context, obj client.Object, err error, msg string, setStatusFn common.UpdateFailStatusFn, args ...interface{})
	DeleteSuccess(namespacedName types.NamespacedName, obj client.Object)
	IncreaseSyncTotal()
	IncreaseUpdateTotal()
	IncreaseDeleteTotal()
	IncreaseDeleteSuccessTotal()
	IncreaseDeleteFailTotal()
	DeleteFail(namespacedName types.NamespacedName, obj client.Object, err error)
}

// GatewayReconciler watches Gateway API resources and reconciles Gateways.
// ListenerSet events are mapped back to their parent Gateways via parentRefs.
type GatewayReconciler struct {
	Client        client.Client
	Scheme        *apimachineryruntime.Scheme
	Recorder      record.EventRecorder
	Service       *dns.DNSRecordService
	StatusUpdater statusUpdater

	// listenerSetEnabled is true when the ListenerSet CRD is installed in the cluster.
	listenerSetEnabled bool
	// httpRouteEnabled, grpcRouteEnabled, tlsRouteEnabled gate watches and reconcile paths when CRDs exist.
	httpRouteEnabled bool
	grpcRouteEnabled bool
	tlsRouteEnabled  bool
	// crdReady is true when at least the Gateway CRD is installed in the cluster.
	// CollectGarbage is a no-op until crdReady becomes true.
	crdReady bool
	// discoveryClient is used to check whether the Gateway API CRDs are installed.
	// When nil, StartController creates one from the manager's REST config.
	discoveryClient discovery.DiscoveryInterface
}

// Reconcile is the main reconciliation loop for Gateway objects.
// It fetches the Gateway referenced by req, validates whether it should be processed, and then applies the desired state.
// Existing DNS records on the Gateway is deleted when
// - the Gateway has a non-zero DeletionTimestamp or the Gateway is not found.
// - the Gateway has no valid IP address
// - the Gateway is updated to a different class which is not managed.
func (r *GatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	startTime := time.Now()
	defer func() {
		log.Debug("Finished reconciling Gateway", "Gateway", req.NamespacedName, "duration(ms)", time.Since(startTime).Milliseconds())
	}()

	log.Debug("Reconcile started", "Gateway", req.NamespacedName)
	r.StatusUpdater.IncreaseSyncTotal()
	gw := &gatewayv1.Gateway{}
	if err := r.Client.Get(ctx, req.NamespacedName, gw); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Gateway not found", "Gateway", req.NamespacedName)
			gw.SetName(req.Name)
			gw.SetNamespace(req.Namespace)
			return r.deleteAllDNSRecords(ctx, gw, req)
		}
		log.Error(err, "Failed to fetch Gateway", "Gateway", req.NamespacedName)
		return common.ResultRequeueAfter10sec, err
	}

	log.Debug("Gateway loaded", "Gateway", req.NamespacedName, "class", gw.Spec.GatewayClassName,
		"hasDeletionTimestamp", !gw.DeletionTimestamp.IsZero(), "addressCount", len(gw.Status.Addresses), "listenerCount", len(gw.Spec.Listeners))

	if !shouldProcessGateway(gw) {
		// Gateway is no longer in a managed GatewayClass (e.g. class changed away from managed).
		// The update predicate still fires when old was managed, so we must clean up any DNS records
		// that were created before the class change.
		log.Info("Gateway is no longer managed, deleting DNS records", "Gateway", req.NamespacedName)
		return r.deleteAllDNSRecords(ctx, gw, req)
	}

	if !gw.DeletionTimestamp.IsZero() {
		log.Info("Reconciling Gateway delete", "Gateway", req.NamespacedName)
		return r.deleteAllDNSRecords(ctx, gw, req)
	}

	if !hasUsableGatewayIP(gw) {
		log.Info("Gateway has no valid address, DNS records should be deleted", "Gateway", req.NamespacedName)
		return r.deleteAllDNSRecords(ctx, gw, req)
	}

	r.StatusUpdater.IncreaseUpdateTotal()
	desiredBatches, err := r.buildOwnerEndpointsForGateway(ctx, gw)
	if err != nil {
		log.Error(err, "Failed to build DNS endpoints for Gateway", "Gateway", req.NamespacedName.String())
		return common.ResultRequeueAfter10sec, err
	}
	existingOwners := make([]*dns.ResourceRef, 0)
	var lastErr error
	for _, batch := range desiredBatches {
		existingOwners = append(existingOwners, batch.Owner)
		updateErr := r.Service.CreateOrUpdateDNSRecords(ctx, batch)
		if updateErr != nil {
			switch batch.Owner.Kind {
			case dns.ResourceKindListenerSet, dns.ResourceKindHTTPRoute, dns.ResourceKindGRPCRoute, dns.ResourceKindTLSRoute:
				log.Error(updateErr, fmt.Sprintf("Failed to configure DNS records for %s", batch.Owner.Kind), "Gateway", req.NamespacedName.String(),
					batch.Owner.Kind, batch.Owner.GetNamespace()+"/"+batch.Owner.GetName())
			default:
				log.Error(updateErr, fmt.Sprintf("Failed to configure DNS records for %s", batch.Owner.Kind), "Gateway", req.NamespacedName.String())
			}
			lastErr = updateErr
		}

		log.Debug("DNS endpoint batch upsert", "Gateway", req.NamespacedName, "ownerKind", batch.Owner.Kind,
			"owner", batch.Owner.GetNamespace()+"/"+batch.Owner.GetName(), "err", updateErr, "endpoints", len(batch.Endpoints))

		// Update resource conditions.
		r.updateDNSRecordCondition(ctx, types.NamespacedName{Namespace: gw.Namespace, Name: gw.Name}, batch.Owner, updateErr)
	}

	// Delete the existing DNS records on the current Gateway but the owner resource does not exist.
	log.Debug("Deleting orphaned DNS records not owned by existing owners", "Gateway", req.NamespacedName)
	delErr := r.Service.DeleteOrphanedDNSRecordsInGateway(ctx, gw.Namespace, gw.Name, existingOwners)
	if delErr != nil {
		log.Error(delErr, "Failed to delete the orphaned DNS records")
	}

	if lastErr != nil || delErr != nil {
		if lastErr != nil {
			r.StatusUpdater.UpdateFail(ctx, gw, lastErr, "DNS record create/update failed", nil)
		}
		return common.ResultRequeueAfter10sec, lastErr
	}

	r.StatusUpdater.UpdateSuccess(ctx, gw, nil)
	log.Info("Reconciling Gateway", "Gateway", req.NamespacedName, "generation", gw.Generation, "dnsOwnerBatches", len(desiredBatches))
	return ResultNormal, nil
}

// deleteAllDNSRecords removes all DNS records for gw, updating metrics regardless of outcome.
func (r *GatewayReconciler) deleteAllDNSRecords(ctx context.Context, gw *gatewayv1.Gateway, req ctrl.Request) (ctrl.Result, error) {
	r.StatusUpdater.IncreaseDeleteTotal()
	if err := r.Service.DeleteAllDNSRecordsInGateway(ctx, gw.Namespace, gw.Name); err != nil {
		r.StatusUpdater.DeleteFail(req.NamespacedName, gw, err)
		log.Error(err, "Failed to delete DNS records for Gateway", "Gateway", req.NamespacedName)
		return common.ResultRequeueAfter10sec, err
	}
	r.StatusUpdater.DeleteSuccess(req.NamespacedName, gw)
	return ResultNormal, nil
}

// buildDNSReadyCondition returns a metav1.Condition for DNSReady from CreateOrUpdateDNSRecords result.
func buildDNSReadyCondition(err error) metav1.Condition {
	cond := metav1.Condition{
		Type:               conditionTypeDNSReady,
		LastTransitionTime: metav1.Now(),
	}
	if err != nil {
		cond.Status = metav1.ConditionFalse
		cond.Reason = reasonDNSRecordFailed
		cond.Message = err.Error()
	} else {
		cond.Status = metav1.ConditionTrue
		cond.Reason = reasonDNSRecordConfigured
	}
	return cond
}

// updateDNSRecordCondition sets the DNSReady condition on the resource that owns the DNS record.
// If CreateOrUpdateDNSRecords returned an error, status is False and message is the error string;
// otherwise status is True.
func (r *GatewayReconciler) updateDNSRecordCondition(ctx context.Context, gwNN types.NamespacedName, owner *dns.ResourceRef, err error) {
	cond := buildDNSReadyCondition(err)
	ownerKey := types.NamespacedName{Namespace: owner.GetNamespace(), Name: owner.GetName()}
	switch owner.Kind {
	case dns.ResourceKindGateway:
		if uerr := r.updateGatewayStatusCondition(ctx, ownerKey, cond); uerr != nil {
			log.Error(uerr, "Failed to update Gateway DNSReady condition", "Gateway", ownerKey)
		}
	case dns.ResourceKindListenerSet:
		if uerr := r.updateListenerSetStatusCondition(ctx, ownerKey, cond); uerr != nil {
			log.Error(uerr, "Failed to update ListenerSet DNSReady condition", "ListenerSet", ownerKey)
		}
	case dns.ResourceKindHTTPRoute:
		if uerr := updateRouteParentDNSCondition(ctx, r, gwNN, ownerKey, cond, getHTTPRouteParentStatus); uerr != nil {
			log.Error(uerr, "Failed to update HTTPRoute DNSReady condition", "HTTPRoute", ownerKey)
		}
	case dns.ResourceKindGRPCRoute:
		if uerr := updateRouteParentDNSCondition(ctx, r, gwNN, ownerKey, cond, getGRPCRouteParentStatus); uerr != nil {
			log.Error(uerr, "Failed to update GRPCRoute DNSReady condition", "GRPCRoute", ownerKey)
		}
	case dns.ResourceKindTLSRoute:
		if uerr := updateRouteParentDNSCondition(ctx, r, gwNN, ownerKey, cond, getTLSRouteParentStatus); uerr != nil {
			log.Error(uerr, "Failed to update TLSRoute DNSReady condition", "TLSRoute", ownerKey)
		}
	default:
		log.Warn("updateDNSRecordCondition: unsupported owner kind, skipping", "kind", owner.Kind, "owner", owner.GetNamespace()+"/"+owner.GetName())
	}
}

func (r *GatewayReconciler) updateGatewayStatusCondition(ctx context.Context, key types.NamespacedName, cond metav1.Condition) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &gatewayv1.Gateway{}
		if err := r.Client.Get(ctx, key, latest); err != nil {
			return err
		}
		cond.ObservedGeneration = latest.Generation
		latest.Status.Conditions = mergeDNSReadyCondition(latest.Status.Conditions, cond)
		return r.Client.Status().Update(ctx, latest)
	})
}

func (r *GatewayReconciler) updateListenerSetStatusCondition(ctx context.Context, key types.NamespacedName, cond metav1.Condition) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		ls := &gatewayv1.ListenerSet{}
		if err := r.Client.Get(ctx, key, ls); err != nil {
			return err
		}
		cond.ObservedGeneration = ls.Generation
		ls.Status.Conditions = mergeDNSReadyCondition(ls.Status.Conditions, cond)
		return r.Client.Status().Update(ctx, ls)
	})
}

// mergeDNSReadyCondition updates or appends the DNSReady condition per gateway-api merge rules.
func mergeDNSReadyCondition(conditions []metav1.Condition, newCond metav1.Condition) []metav1.Condition {
	for i := range conditions {
		if (conditions)[i].Type == conditionTypeDNSReady {
			(conditions)[i].Status = newCond.Status
			(conditions)[i].Reason = newCond.Reason
			(conditions)[i].Message = newCond.Message
			(conditions)[i].LastTransitionTime = metav1.Now()
			(conditions)[i].ObservedGeneration = newCond.ObservedGeneration
			return conditions
		}
	}
	conditions = append(conditions, newCond)
	return conditions
}

// shouldProcessGateway returns true if the given Gateway uses a GatewayClassName
// defined in the filteredGatewayClasses set.
func shouldProcessGateway(gw *gatewayv1.Gateway) bool {
	return filteredGatewayClasses.Has(string(gw.Spec.GatewayClassName))
}

func getGatewayReference(gw *gatewayv1.Gateway) *dns.ResourceRef {
	return &dns.ResourceRef{
		Kind:   dns.ResourceKindGateway,
		Object: gw.GetObjectMeta(),
	}
}

func ipsToTargets(ips []net.IP) extdns.Targets {
	s := make([]string, 0, len(ips))
	for _, ip := range ips {
		if ip == nil {
			continue
		}
		s = append(s, ip.String())
	}
	return extdns.NewTargets(s...)
}

func dnsResourceLabel(ref *dns.ResourceRef) string {
	return strings.ToLower(ref.Kind) + "/" + ref.GetNamespace() + "/" + ref.GetName()
}

func extendEndpointsForHostnames(out *[]*extdns.Endpoint, hostnames []string, targets extdns.Targets, resourceKey string) {
	ttl := extdns.TTL(0)
	for _, h := range hostnames {
		if h == "" {
			continue
		}
		for _, ep := range extdns.EndpointsForHostname(h, targets, ttl, nil, "", resourceKey) {
			extdns.ApplyExternalDNSSourceLabels(ep, "")
			*out = append(*out, ep)
		}
	}
}

// buildOwnerEndpointsForGateway builds ExternalDNS-style Endpoint batches for routes attached to the Gateway.
// Gateway and ListenerSet listener hostnames define admission only (CollectAdmissionHostnameFilters +
// RouteHostnamesMatchingAdmission); DNS names come from route spec/annotations, not from Gateway/ListenerSet
// hostnames alone. HTTPRoute/GRPCRoute/TLSRoute are included only when extdnssrc HTTP/GRPC/TLS
// ParentReadyForGateway is true (Accepted=True and Programmed=True for the parent Gateway ref).
func (r *GatewayReconciler) buildOwnerEndpointsForGateway(ctx context.Context, gw *gatewayv1.Gateway) ([]*dns.OwnerEndpoints, error) {
	ips := collectIPsFromGateway(gw)
	if len(ips) == 0 {
		return nil, nil
	}
	targets := ipsToTargets(ips)
	gwRef := getGatewayReference(gw)
	gwNN := types.NamespacedName{Namespace: gw.Namespace, Name: gw.Name}

	gwNSType, err := r.getNamespaceType(ctx, gw.Namespace)
	if err != nil {
		return nil, err
	}

	seenFQDNs := sets.New[string]()
	var batches []*dns.OwnerEndpoints

	var listenerSets []gatewayv1.ListenerSet
	if r.listenerSetEnabled {
		var err error
		listenerSets, err = r.listSortedListenerSetsForGateway(ctx, gwNN)
		if err != nil {
			return nil, err
		}
	}
	allowed := extdnssrc.CollectAdmissionHostnameFilters(gw, listenerSets)

	// Collect HTTPRoute Endpoints.
	if r.httpRouteEnabled {
		if err = collectRouteEndpoints(ctx, r, gwRef, targets, gwNSType, allowed,
			seenFQDNs, &batches, listHTTPRouteItems, getHTTPRouteInfo, checkHTTPRouteParentReady,
		); err != nil {
			return nil, err
		}
	}

	// Collect GRPCRoute Endpoints.
	if r.grpcRouteEnabled {
		if err = collectRouteEndpoints(ctx, r, gwRef, targets, gwNSType, allowed,
			seenFQDNs, &batches, listGRPCRouteItems, getGRPCRouteInfo, checkGRPCRouteParentReady,
		); err != nil {
			return nil, err
		}
	}

	// Collect TLSRoute Endpoints.
	if r.tlsRouteEnabled {
		if err = collectRouteEndpoints(ctx, r, gwRef, targets, gwNSType, allowed,
			seenFQDNs, &batches, listTLSRouteItems, getTLSRouteInfo, checkTLSRouteParentReady,
		); err != nil {
			return nil, err
		}
	}

	return batches, nil
}

// takeNewHostnames returns hostnames from desired that are not already assigned to another owner
// for this Gateway (including wildcard overlap per ExternalDNS GwMatchingHost), and registers
// claimed names in seen.
func takeNewHostnames(seen sets.Set[string], desired []string) []string {
	if len(desired) == 0 {
		return nil
	}
	var out []string
	for _, h := range desired {
		if h == "" {
			continue
		}
		if hostnameOverlapsSeenFQDNs(seen, h) {
			continue
		}
		seen.Insert(h)
		out = append(out, h)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// hostnameOverlapsSeenFQDNs reports whether h overlaps any name already claimed for this Gateway,
// using the same GwMatchingHost semantics as external-dns/source/gateway.go (listener vs route host).
func hostnameOverlapsSeenFQDNs(seen sets.Set[string], h string) bool {
	for s := range seen {
		if _, ok := extdnssrc.GwMatchingHost(s, h); ok {
			return true
		}
	}
	return false
}

// appendOwnerEndpointsForRoute applies ExternalDNS-style hostname resolution, takeNewHostnames, and endpoint build
// for one route (shared by HTTP/GRPC/TLS). Generics do not simplify this in Go: the route types do not share a
// constraint exposing Spec without a large custom interface; listing/parent-ready checks stay per-type.
func appendOwnerEndpointsForRoute(seenFQDNs sets.Set[string], batches *[]*dns.OwnerEndpoints, gwRef *dns.ResourceRef,
	targets extdns.Targets, gwNSType common.NameSpaceType, meta *metav1.ObjectMeta, rawHosts []string,
	owner *dns.ResourceRef, allowedAdmissionHostnames []string) error {
	filtered, err := extdnssrc.RouteHostnamesMatchingAdmission(allowedAdmissionHostnames, meta, rawHosts)
	if err != nil {
		return err
	}
	routeHostnames := takeNewHostnames(seenFQDNs, filtered)
	if len(routeHostnames) == 0 {
		return nil
	}
	var eps []*extdns.Endpoint
	extendEndpointsForHostnames(&eps, routeHostnames, targets, dnsResourceLabel(owner))
	if len(eps) == 0 {
		return nil
	}
	*batches = append(*batches, &dns.OwnerEndpoints{
		AddressProvider: gwRef,
		Owner:           owner,
		ForSVService:    gwNSType == common.SVServiceNs,
		Endpoints:       eps,
	})
	return nil
}

func (r *GatewayReconciler) getNamespaceType(ctx context.Context, namespace string) (common.NameSpaceType, error) {
	obj := &v1.Namespace{}
	ns := types.NamespacedName{Name: namespace}
	if err := r.Client.Get(ctx, ns, obj); err != nil {
		log.Error(err, "Unable to fetch Namespace", "Namespace", ns)
		return common.NormalNs, err
	}
	return common.GetNamespaceType(obj, nil), nil
}

func (r *GatewayReconciler) listSortedListenerSetsForGateway(
	ctx context.Context,
	gwNamespacedName types.NamespacedName,
) ([]gatewayv1.ListenerSet, error) {
	lsList := &gatewayv1.ListenerSetList{}
	if err := r.Client.List(
		ctx,
		lsList,
		client.MatchingFields{listenerSetParentGatewayIndex: gwNamespacedName.String()},
	); err != nil {
		return nil, err
	}
	listenerSets := lsList.Items
	slices.SortFunc(listenerSets, func(a, b gatewayv1.ListenerSet) int {
		if c := cmp.Compare(a.Namespace, b.Namespace); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return listenerSets, nil
}

func collectIPsFromGateway(gw *gatewayv1.Gateway) []net.IP {
	var ips []net.IP
	gwNamespaceName := types.NamespacedName{Namespace: gw.Namespace, Name: gw.Name}
	for _, addr := range gw.Status.Addresses {
		if addr.Type == nil || *addr.Type == gatewayv1.IPAddressType {
			value := strings.TrimSpace(addr.Value)
			if ip := net.ParseIP(value); ip != nil {
				ips = append(ips, ip)
			} else {
				log.Warn("Invalid Gateway address for DNS records (parse failed)", "Gateway", gwNamespaceName.String(), "value", value)
			}
		} else {
			log.Info("Ignore the unsupported K8s Gateway address type for DNS records", "Gateway", gwNamespaceName.String(), "address type", *addr.Type)
		}
	}
	return ips
}

// hasUsableGatewayIP returns true if the Gateway has at least one parseable IP address (IPAddressType).
// Used to decide whether we can create DNS records (need IP) or should delete existing ones.
func hasUsableGatewayIP(gw *gatewayv1.Gateway) bool {
	return len(collectIPsFromGateway(gw)) > 0
}

func (r *GatewayReconciler) setupWithManager(mgr ctrl.Manager) error {
	if r.listenerSetEnabled {
		// Register the ListenerSet→Gateway field index only when the CRD is present;
		if err := mgr.GetFieldIndexer().IndexField(context.TODO(), &gatewayv1.ListenerSet{}, listenerSetParentGatewayIndex, listenerSetParentGatewayIndexFunc); err != nil {
			log.Error(err, "Failed to register ListenerSet cache indexer", "controller", "Gateway")
			return err
		}
	} else {
		log.Info("ListenerSet CRD is not installed, Gateway controller will not process ListenerSet resources")
	}

	if r.httpRouteEnabled {
		if err := mgr.GetFieldIndexer().IndexField(context.TODO(), &gatewayv1.HTTPRoute{}, routeParentGatewayIndex, routeParentGatewayIndexFunc); err != nil {
			log.Error(err, "Failed to register HTTPRoute cache indexer", "controller", "Gateway")
			return err
		}
	}
	if r.grpcRouteEnabled {
		if err := mgr.GetFieldIndexer().IndexField(context.TODO(), &gatewayv1.GRPCRoute{}, routeParentGatewayIndex, routeParentGatewayIndexFunc); err != nil {
			log.Error(err, "Failed to register GRPCRoute cache indexer", "controller", "Gateway")
			return err
		}
	}
	if r.tlsRouteEnabled {
		if err := mgr.GetFieldIndexer().IndexField(context.TODO(), &gatewayv1.TLSRoute{}, routeParentGatewayIndex, routeParentGatewayIndexFunc); err != nil {
			log.Error(err, "Failed to register TLSRoute cache indexer", "controller", "Gateway")
			return err
		}
	}

	b := ctrl.NewControllerManagedBy(mgr).For(&gatewayv1.Gateway{}, builder.WithPredicates(predicateFuncsGateway))
	if r.listenerSetEnabled {
		b = b.Watches(&gatewayv1.ListenerSet{}, r.listenerSetEnqueueHandler(), builder.WithPredicates(r.predicateFuncsListenerSet()))
	}
	if r.httpRouteEnabled {
		b = b.Watches(&gatewayv1.HTTPRoute{}, r.routeEnqueueHandler(), builder.WithPredicates(r.predicateFuncsHTTPRoute()))
	}
	if r.grpcRouteEnabled {
		b = b.Watches(&gatewayv1.GRPCRoute{}, r.routeEnqueueHandler(), builder.WithPredicates(r.predicateFuncsGRPCRoute()))
	}
	if r.tlsRouteEnabled {
		b = b.Watches(&gatewayv1.TLSRoute{}, r.routeEnqueueHandler(), builder.WithPredicates(r.predicateFuncsTLSRoute()))
	}

	return b.WithOptions(controller.Options{MaxConcurrentReconciles: common.NumReconcile()}).
		Complete(r)
}

func (r *GatewayReconciler) RestoreReconcile() error {
	return nil
}

func (r *GatewayReconciler) CollectGarbage(ctx context.Context) error {
	if !r.crdReady {
		return nil
	}
	cachedGatewaySet := r.Service.ListGatewayNamespacedName()
	log.Debug("Gateway CollectGarbage started", "cachedGateways", len(cachedGatewaySet), "listenerSetEnabled", r.listenerSetEnabled)
	gwList := gatewayv1.GatewayList{}
	err := r.Client.List(ctx, &gwList)
	if err != nil {
		log.Error(err, "failed to list K8s Gateways CR")
		return err
	}

	CRGatewayMap := make(map[types.NamespacedName]gatewayv1.Gateway, 0)
	for i := range gwList.Items {
		gw := gwList.Items[i]
		if !shouldProcessGateway(&gw) {
			continue
		}
		CRGatewayMap[types.NamespacedName{Namespace: gw.Namespace, Name: gw.Name}] = gw
	}

	var errList []error
	for elem := range cachedGatewaySet {
		gwCR, found := CRGatewayMap[elem]
		// Delete all the DNS records if the corresponding Gateway does not exist.
		if !found {
			log.Info("GC collected nsx DNS records for Gateway", "Gateway", elem.String())
			if err = r.Service.DeleteAllDNSRecordsInGateway(ctx, elem.Namespace, elem.Name); err != nil {
				log.Error(err, "Failed to delete nsx DNS records for Gateway", "Gateway", elem.String())
				errList = append(errList, err)
			}
		} else {
			desiredBatches, err := r.buildOwnerEndpointsForGateway(ctx, &gwCR)
			if err != nil {
				log.Error(err, "Failed to build desired DNS endpoints for GC", "Gateway", elem.String())
				errList = append(errList, err)
				continue
			}
			existingOwners := make([]*dns.ResourceRef, 0, len(desiredBatches))
			for i := range desiredBatches {
				existingOwners = append(existingOwners, desiredBatches[i].Owner)
			}

			// Delete the DNS records configured on the given Gateway but owner does not exist.
			log.Debug("GC deleting orphaned DNS records", "Gateway", elem.String())
			if err := r.Service.DeleteOrphanedDNSRecordsInGateway(ctx, gwCR.Namespace, gwCR.Name, existingOwners); err != nil {
				log.Error(err, "Failed to delete nsx DNS records attached to Gateway without owner", "Gateway", elem)
				errList = append(errList, err)
			}
		}
	}

	if len(errList) > 0 {
		return fmt.Errorf("errors found in K8s Gateway garbage collection: %s", errList)
	}
	return nil
}

// gatewayAPIResources reports which gateway.networking.k8s.io/v1 resources exist in the cluster.
type gatewayAPIResources struct {
	Gateway     bool
	ListenerSet bool
	HTTPRoute   bool
	GRPCRoute   bool
	TLSRoute    bool
}

// checkGatewayCRDs uses the discovery API to determine whether Gateway API CRDs are installed under gateway.networking.k8s.io/v1.
func (r *GatewayReconciler) checkGatewayCRDs(mgr ctrl.Manager) (gatewayAPIResources, error) {
	var out gatewayAPIResources
	if r.discoveryClient == nil {
		var err error
		r.discoveryClient, err = discovery.NewDiscoveryClientForConfig(mgr.GetConfig())
		if err != nil {
			log.Error(err, "Failed to create discovery client", "controller", "Gateway")
			return out, err
		}
	}
	resourceList, err := r.discoveryClient.ServerResourcesForGroupVersion(gatewayAPIGroupVersion)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return out, nil
		}
		return out, err
	}
	if resourceList == nil {
		return out, nil
	}
	for _, res := range resourceList.APIResources {
		switch res.Name {
		case "gateways":
			out.Gateway = true
		case "listenersets":
			out.ListenerSet = true
		case "httproutes":
			out.HTTPRoute = true
		case "grpcroutes":
			out.GRPCRoute = true
		case "tlsroutes":
			out.TLSRoute = true
		}
	}
	return out, nil
}

func (r *GatewayReconciler) StartController(mgr ctrl.Manager, _ webhook.Server) error {
	feat, err := r.checkGatewayCRDs(mgr)
	if err != nil {
		log.Error(err, "Failed to check Gateway API CRDs", "controller", "Gateway")
		return err
	}

	if !feat.Gateway {
		log.Info("Gateway API CRDs are not installed in the cluster, skipping Gateway controller start")
		return nil
	}

	r.listenerSetEnabled = feat.ListenerSet
	r.httpRouteEnabled = feat.HTTPRoute
	r.grpcRouteEnabled = feat.GRPCRoute
	r.tlsRouteEnabled = feat.TLSRoute

	r.crdReady = true
	log.Debug("Gateway StartController: Gateway API present", "listenerSetEnabled", feat.ListenerSet,
		"httpRouteEnabled", feat.HTTPRoute, "grpcRouteEnabled", feat.GRPCRoute, "tlsRouteEnabled", feat.TLSRoute)
	if err = r.setupWithManager(mgr); err != nil {
		log.Error(err, "Failed to create controller", "controller", "Gateway")
		return err
	}
	go common.GenericGarbageCollector(make(chan bool), servicecommon.GCInterval, r.CollectGarbage)
	return nil
}

func NewGatewayReconciler(mgr ctrl.Manager, service *dns.DNSRecordService) *GatewayReconciler {
	r := &GatewayReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("gateway-controller"),
		Service:  service,
	}
	if service != nil && service.NSXConfig != nil {
		updater := common.NewStatusUpdater(
			r.Client,
			service.NSXConfig,
			r.Recorder,
			common.MetricResTypeGateway,
			"DNSRecord",
			"Gateway",
		)
		r.StatusUpdater = &updater
	}
	return r
}

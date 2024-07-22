/* Copyright © 2023 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package networkinfo

import (
	"context"
	"time"

	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt/model"
	corev1 "k8s.io/api/core/v1"
	apimachineryruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/vmware-tanzu/nsx-operator/pkg/apis/crd.nsx.vmware.com/v1alpha1"
	"github.com/vmware-tanzu/nsx-operator/pkg/controllers/common"
	"github.com/vmware-tanzu/nsx-operator/pkg/logger"
	"github.com/vmware-tanzu/nsx-operator/pkg/metrics"
	_ "github.com/vmware-tanzu/nsx-operator/pkg/nsx/ratelimiter"
	commonservice "github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/vpc"
)

const (
	preCreatedVPCPollInterval = time.Minute * 5
)

var (
	log           = &logger.Log
	MetricResType = common.MetricResTypeNetworkInfo
)

// NetworkInfoReconciler NetworkInfoReconcile reconciles a NetworkInfo object
// Actually it is more like a shell, which is used to manage nsx VPC
type NetworkInfoReconciler struct {
	Client   client.Client
	Scheme   *apimachineryruntime.Scheme
	Service  *vpc.VPCService
	Recorder record.EventRecorder
}

func (r *NetworkInfoReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	obj := &v1alpha1.NetworkInfo{}
	log.Info("reconciling NetworkInfo CR", "NetworkInfo", req.NamespacedName)
	metrics.CounterInc(r.Service.NSXConfig, metrics.ControllerSyncTotal, common.MetricResTypeNetworkInfo)

	if err := r.Client.Get(ctx, req.NamespacedName, obj); err != nil {
		log.Error(err, "unable to fetch NetworkInfo CR", "req", req.NamespacedName)
		return common.ResultNormal, client.IgnoreNotFound(err)
	}

	if obj.ObjectMeta.DeletionTimestamp.IsZero() {
		metrics.CounterInc(r.Service.NSXConfig, metrics.ControllerUpdateTotal, common.MetricResTypeNetworkInfo)
		if !controllerutil.ContainsFinalizer(obj, commonservice.NetworkInfoFinalizerName) {
			controllerutil.AddFinalizer(obj, commonservice.NetworkInfoFinalizerName)
			if err := r.Client.Update(ctx, obj); err != nil {
				log.Error(err, "add finalizer", "NetworkInfo", req.NamespacedName)
				updateFail(r, &ctx, obj, &err, r.Client, nil)
				return common.ResultRequeue, err
			}
			log.V(1).Info("added finalizer on NetworkInfo CR", "NetworkInfo", req.NamespacedName)
		}

		createdVpc, nc, err := r.Service.CreateOrUpdateVPC(obj)
		if err != nil {
			log.Error(err, "create vpc failed, would retry exponentially", "VPC", req.NamespacedName)
			updateFail(r, &ctx, obj, &err, r.Client, nil)
			return common.ResultRequeueAfter10sec, err
		}

		privateIPs := nc.PrivateIPv4CIDRs
		if vpc.IsPreCreatedVPC(*nc) {
			privateIPs = createdVpc.PrivateIps
		} else {
			// We don't support the case a Namespace is configured with a pre-created VPC and
			// is sharing the VPC to other Namespaces by now.
			isShared, err := r.Service.IsSharedVPCNamespaceByNS(obj.GetNamespace())
			if err != nil {
				log.Error(err, "failed to check if namespace is shared", "Namespace", obj.GetNamespace())
				return common.ResultRequeue, err
			}
			if r.Service.NSXConfig.NsxConfig.UseAVILoadBalancer && !isShared {
				// Create AVI Rules inside the VPC only if it is not a pre-created VPC nor a shared VPC.
				err = r.Service.CreateOrUpdateAVIRule(createdVpc, obj.Namespace)
				if err != nil {
					state := &v1alpha1.VPCState{
						Name:                    *createdVpc.DisplayName,
						VPCPath:                 *createdVpc.Path,
						DefaultSNATIP:           "",
						LoadBalancerIPAddresses: "",
						PrivateIPv4CIDRs:        privateIPs,
					}
					log.Error(err, "update avi rule failed, would retry exponentially", "NetworkInfo", req.NamespacedName)
					updateFail(r, &ctx, obj, &err, r.Client, state)
					return common.ResultRequeueAfter10sec, err
				}
			}
		}

		state, lbsPath, err := r.getVPCState(createdVpc)
		if err != nil {
			updateFail(r, &ctx, obj, &err, r.Client, state)
			return common.ResultRequeueAfter10sec, err
		}
		updateSuccess(r, &ctx, obj, r.Client, state, nc.Name, lbsPath, r.Service.GetNSXLBSPath(*createdVpc.Id))
	} else {
		if controllerutil.ContainsFinalizer(obj, commonservice.NetworkInfoFinalizerName) {
			metrics.CounterInc(r.Service.NSXConfig, metrics.ControllerDeleteTotal, common.MetricResTypeNetworkInfo)
			isShared, err := r.Service.IsSharedVPCNamespaceByNS(obj.GetNamespace())
			if err != nil {
				log.Error(err, "failed to check if namespace is shared", "Namespace", obj.GetNamespace())
				return common.ResultRequeue, err
			}
			vpcs := r.Service.GetVPCsByNamespace(obj.GetNamespace())
			// if nsx resource do not exist, continue to remove finalizer, or the crd can not be removed
			if len(vpcs) == 0 {
				// when nsx vpc not found in vpc store, skip deleting NSX VPC
				log.Info("can not find VPC in store, skip deleting NSX VPC, remove finalizer from NetworkInfo CR")
			} else if !isShared {
				vpc := vpcs[0]
				// first delete vpc and then ipblock or else it will fail arguing it is being referenced by other objects
				if err := r.Service.DeleteVPC(*vpc.Path); err != nil {
					log.Error(err, "failed to delete nsx VPC, would retry exponentially", "NetworkInfo", req.NamespacedName)
					deleteFail(r, &ctx, obj, &err, r.Client)
					return common.ResultRequeueAfter10sec, err
				}
				if err := r.Service.DeleteIPBlockInVPC(*vpc); err != nil {
					log.Error(err, "failed to delete private ip blocks for VPC", "VPC", req.NamespacedName)
					return common.ResultRequeueAfter10sec, err
				}
			}

			controllerutil.RemoveFinalizer(obj, commonservice.NetworkInfoFinalizerName)
			if err := r.Client.Update(ctx, obj); err != nil {
				deleteFail(r, &ctx, obj, &err, r.Client)
				return common.ResultRequeue, err
			}
			log.V(1).Info("removed finalizer", "NetworkInfo", req.NamespacedName)
			deleteSuccess(r, &ctx, obj)
		} else {
			// only print a message because it's not a normal case
			log.Info("finalizers cannot be recognized", "NetworkInfo", req.NamespacedName)
		}
	}
	return common.ResultNormal, nil
}

func (r *NetworkInfoReconciler) setupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.NetworkInfo{}).
		WithEventFilter(predicate.Funcs{
			UpdateFunc: func(e event.UpdateEvent) bool {
				return false
			},
		}).
		WithOptions(
			controller.Options{
				MaxConcurrentReconciles: common.NumReconcile(),
			}).
		Watches(
			// For created/removed network config, add/remove from vpc network config cache.
			// For modified network config, currently only support appending ips to public ip blocks,
			// update network config in cache and update nsx vpc object.
			&v1alpha1.VPCNetworkConfiguration{},
			&VPCNetworkConfigurationHandler{
				Client:     mgr.GetClient(),
				vpcService: r.Service,
			},
			builder.WithPredicates(VPCNetworkConfigurationPredicate)).
		Complete(r)
}

// Start setup manager and launch GC
func (r *NetworkInfoReconciler) Start(mgr ctrl.Manager) error {
	// Start a separate goroutine to poll pre-created VPC from NSX, and update its state into the corresponding
	// NetworkInfo CR.
	if err := wait.PollUntilContextCancel(context.Background(), preCreatedVPCPollInterval, true, func(ctx context.Context) (done bool, err error) {
		r.SyncPreCreatedVPCState(ctx)
		return false, nil
	}); err != nil {
		log.Error(err, "Failed to start goroutine to periodically poll NSX pre-created VPCs")
		return err
	}
	err := r.setupWithManager(mgr)
	if err != nil {
		return err
	}
	return nil
}

// CollectGarbage logic for nsx-vpc is that:
// 1. list all current existing namespace in kubernetes
// 2. list all the nsx-vpc in vpcStore
// 3. loop all the nsx-vpc to get its namespace, check if the namespace still exist
// 4. if ns do not exist anymore, delete the nsx-vpc resource
// it implements the interface GarbageCollector method.
func (r *NetworkInfoReconciler) CollectGarbage(ctx context.Context) {
	log.Info("VPC garbage collector started")
	// read all nsx-vpc from vpc store
	nsxVPCList := r.Service.ListVPC()
	if len(nsxVPCList) == 0 {
		return
	}

	// read all namespaces from k8s
	namespaces := &corev1.NamespaceList{}
	err := r.Client.List(ctx, namespaces)
	if err != nil {
		log.Error(err, "failed to list k8s namespaces")
		return
	}
	nsSet := sets.NewString()
	for _, ns := range namespaces.Items {
		nsSet.Insert(ns.Name)
	}

	for i := len(nsxVPCList) - 1; i >= 0; i-- {
		nsxVPCNamespace := getNamespaceFromNSXVPC(&nsxVPCList[i])
		if nsSet.Has(nsxVPCNamespace) {
			continue
		}
		elem := nsxVPCList[i]
		log.Info("GC collected nsx VPC object", "ID", elem.Id, "Namespace", nsxVPCNamespace)
		metrics.CounterInc(r.Service.NSXConfig, metrics.ControllerDeleteTotal, common.MetricResTypeNetworkInfo)
		err = r.Service.DeleteVPC(*elem.Path)
		if err != nil {
			metrics.CounterInc(r.Service.NSXConfig, metrics.ControllerDeleteFailTotal, common.MetricResTypeNetworkInfo)
		} else {
			metrics.CounterInc(r.Service.NSXConfig, metrics.ControllerDeleteSuccessTotal, common.MetricResTypeNetworkInfo)
			if err := r.Service.DeleteIPBlockInVPC(elem); err != nil {
				log.Error(err, "failed to delete private ip blocks for VPC", "VPC", *elem.DisplayName)
			}
			log.Info("deleted private ip blocks for VPC", "VPC", *elem.DisplayName)
		}
	}
}

func (r *NetworkInfoReconciler) getVPCState(vpcModel *model.Vpc) (*v1alpha1.VPCState, string, error) {
	var err error
	snatIP, path, cidr := "", "", ""
	// currently, auto snat is not exposed, and use default value True
	// checking autosnat to support future extension in vpc configuration
	if vpcModel.ServiceGateway != nil && vpcModel.ServiceGateway.AutoSnat != nil && *vpcModel.ServiceGateway.AutoSnat {
		snatIP, err = r.Service.GetDefaultSNATIP(*vpcModel)
		if err != nil {
			log.Error(err, "failed to read default SNAT ip from VPC", "VPC", vpcModel.Id)
			return &v1alpha1.VPCState{
				Name:                    *vpcModel.DisplayName,
				VPCPath:                 *vpcModel.Path,
				DefaultSNATIP:           "",
				LoadBalancerIPAddresses: "",
				PrivateIPv4CIDRs:        vpcModel.PrivateIps,
			}, "", err
		}
	}

	// if lb vpc enabled, read avi subnet path and cidr
	// nsx bug, if set LoadBalancerVpcEndpoint.Enabled to false, when read this vpc back,
	// LoadBalancerVpcEndpoint.Enabled will become a nil pointer.
	if r.Service.NSXConfig.NsxConfig.UseAVILoadBalancer && vpcModel.LoadBalancerVpcEndpoint.Enabled != nil && *vpcModel.LoadBalancerVpcEndpoint.Enabled {
		path, cidr, err = r.Service.GetAVISubnetInfo(*vpcModel)
		if err != nil {
			log.Error(err, "failed to read lb subnet path and cidr", "VPC", vpcModel.Id)
			return &v1alpha1.VPCState{
				Name:                    *vpcModel.DisplayName,
				VPCPath:                 *vpcModel.Path,
				DefaultSNATIP:           snatIP,
				LoadBalancerIPAddresses: "",
				PrivateIPv4CIDRs:        vpcModel.PrivateIps,
			}, path, err
		}
	}

	return &v1alpha1.VPCState{
		Name:                    *vpcModel.DisplayName,
		VPCPath:                 *vpcModel.Path,
		DefaultSNATIP:           snatIP,
		LoadBalancerIPAddresses: cidr,
		PrivateIPv4CIDRs:        vpcModel.PrivateIps,
	}, path, nil
}

func (r *NetworkInfoReconciler) SyncPreCreatedVPCState(ctx context.Context) {
	for vpcPath, namespaces := range r.Service.ListNamespacesWithPreCreatedVPC() {
		preVPC, err := r.Service.GetVPCFromNSXByPath(vpcPath)
		if err != nil {
			log.Error(err, "failed to read VPC from NSX")
			continue
		}
		state, _, err := r.getVPCState(preVPC)
		if err != nil {
			log.Error(err, "failed to get the state on pre-created VPC", "vpcPath", vpcPath)
			continue
		}
		for _, ns := range namespaces {
			updateNetworkInfoVPCStates(r, ctx, ns, state)
		}
	}
}

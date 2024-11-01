package subnetset

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/vmware-tanzu/nsx-operator/pkg/apis/vpc/v1alpha1"
	"github.com/vmware-tanzu/nsx-operator/pkg/controllers/common"
	servicecommon "github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
)

func requeueSubnetSetByBindingMapUpdate(ctx context.Context, c client.Client, _, objNew client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	bindingMap := objNew.(*v1alpha1.SubnetConnectionBindingMap)
	if bindingMap.Spec.TargetSubnetSetName == "" {
		return
	}
	needFinalizer := common.IsObjectReady(bindingMap.Status.Conditions)
	err := enqueue(ctx, c, bindingMap.Namespace, bindingMap.Spec.TargetSubnetSetName, needFinalizer, q)
	if err != nil {
		log.Error(err, "Failed to requeue SubnetSet", "Namespace", bindingMap.Namespace, "Name", bindingMap.Spec.TargetSubnetSetName)
		return
	}
}

func enqueue(ctx context.Context, c client.Client, namespace, name string, needFinalizer bool, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	subnetSetCR := &v1alpha1.SubnetSet{}
	subnetKey := types.NamespacedName{Namespace: namespace, Name: name}
	err := c.Get(ctx, subnetKey, subnetSetCR)
	if err != nil {
		log.Error(err, "Failed to get Subnet CR", "key", subnetKey.String())
		return err
	}
	addedFinalizer := controllerutil.ContainsFinalizer(subnetSetCR, servicecommon.SubnetSetFinalizerName)
	if addedFinalizer != needFinalizer {
		q.Add(reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			},
		})
	}
	log.V(1).Info("Requeue SubnetSet", "key", subnetKey.String())
	return nil
}

func requeueSubnetSetByBindingMapDelete(ctx context.Context, c client.Client, obj client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	bindingMap := obj.(*v1alpha1.SubnetConnectionBindingMap)
	if bindingMap.Spec.TargetSubnetSetName == "" {
		return
	}
	err := enqueue(ctx, c, bindingMap.Namespace, bindingMap.Spec.TargetSubnetSetName, false, q)
	if err != nil {
		log.Error(err, "Failed to requeue SubnetSet", "Namespace", bindingMap.Namespace, "Name", bindingMap.Spec.TargetSubnetSetName)
		return
	}

	//ns := obj.GetNamespace()
	//subnetSetList := &v1alpha1.SubnetSetList{}
	//err := r.Client.List(ctx, subnetSetList, client.InNamespace(ns))
	//if err != nil {
	//	log.Error(err, "Failed to list SubnetSets", "Namespace", ns)
	//	return
	//}
	//
	//for i := range subnetSetList.Items {
	//	s := subnetSetList.Items[i]
	//	// Ignore the SubnetSet if it has no changes on the subnet connection binding maps after the CR deletion.
	//	if !controllerutil.ContainsFinalizer(&s, servicecommon.SubnetSetFinalizerName) {
	//		continue
	//	}
	//
	//	key := types.NamespacedName{
	//		Name:      s.Name,
	//		Namespace: s.Namespace,
	//	}
	//
	//	if bindingCRs := r.subnetSetHasBindings(string(s.UID)); len(bindingCRs) > 0 {
	//		log.Info("Requeue SubnetSet which has no subnet connection binding maps", "key", key.String())
	//		q.Add(reconcile.Request{
	//			NamespacedName: key,
	//		})
	//	}
	//}
}

func (r *SubnetSetReconciler) subnetSetHasBindings(subnetSetCRUID string) []*v1alpha1.SubnetConnectionBindingMap {
	vpcSubnets := r.SubnetService.ListSubnetCreatedBySubnetSet(subnetSetCRUID)
	if len(vpcSubnets) == 0 {
		return nil
	}

	for _, vpcSubnet := range vpcSubnets {
		bindings := r.BindingService.GetSubnetConnectionBindingMapCRsBySubnet(vpcSubnet)
		if len(bindings) > 0 {
			return bindings
		}
	}
	return nil
}

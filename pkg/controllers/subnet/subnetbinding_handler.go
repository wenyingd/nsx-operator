package subnet

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

func requeueSubnetByBindingMapUpdate(ctx context.Context, c client.Client, _ client.Object, objNew client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	bindingMap := objNew.(*v1alpha1.SubnetConnectionBindingMap)
	needFinalizer := common.IsObjectReady(bindingMap.Status.Conditions)
	enqueueSubnets(ctx, c, bindingMap, needFinalizer, q)
}

func enqueueSubnets(ctx context.Context, c client.Client, bindingMap *v1alpha1.SubnetConnectionBindingMap, needFinalizer bool, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	err := enqueue(ctx, c, bindingMap.Namespace, bindingMap.Spec.SubnetName, needFinalizer, q)
	if err != nil {
		log.Error(err, "Failed to requeue Subnet", "Namespace", bindingMap.Namespace, "Name", bindingMap.Spec.SubnetName)
		return
	}

	if bindingMap.Spec.TargetSubnetName == "" {
		return
	}

	err = enqueue(ctx, c, bindingMap.Namespace, bindingMap.Spec.TargetSubnetName, needFinalizer, q)
	if err != nil {
		log.Error(err, "Failed to requeue Subnet", "Namespace", bindingMap.Namespace, "Name", bindingMap.Spec.TargetSubnetName)
		return
	}
}

func enqueue(ctx context.Context, c client.Client, namespace, name string, needFinalizer bool, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	subnetCR := &v1alpha1.Subnet{}
	subnetKey := types.NamespacedName{Namespace: namespace, Name: name}
	err := c.Get(ctx, subnetKey, subnetCR)
	if err != nil {
		log.Error(err, "Failed to get Subnet CR", "key", subnetKey.String())
		return err
	}
	addedFinalizer := controllerutil.ContainsFinalizer(subnetCR, servicecommon.SubnetFinalizerName)
	if addedFinalizer != needFinalizer {
		log.V(1).Info("Requeue subnet", "key", subnetKey.String())
		q.Add(reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			},
		})
	}
	return nil
}

func requeueSubnetByBindingMapDelete(ctx context.Context, c client.Client, obj client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	bindingMap := obj.(*v1alpha1.SubnetConnectionBindingMap)
	enqueueSubnets(ctx, c, bindingMap, false, q)
	//ns := obj.GetNamespace()
	//subnetList := &v1alpha1.SubnetList{}
	//err := r.Client.List(ctx, subnetList, client.InNamespace(ns))
	//if err != nil {
	//	log.Error(err, "Failed to list Subnet CR", "Namespace", ns)
	//	return
	//}
	//
	//for i := range subnetList.Items {
	//	s := subnetList.Items[i]
	//	// Ignore the Subnet if it has no changes on the subnet connection binding maps after the CR deletion.
	//	if !controllerutil.ContainsFinalizer(&s, servicecommon.SubnetFinalizerName) {
	//		continue
	//	}
	//
	//	key := types.NamespacedName{
	//		Name:      s.Name,
	//		Namespace: s.Namespace,
	//	}
	//
	//	if bindingCRs := r.subnetHasBindings(string(s.UID)); len(bindingCRs) == 0 {
	//		log.Info("Requeue subnet which has no subnet connection binding maps", "key", key.String())
	//		q.Add(reconcile.Request{
	//			NamespacedName: key,
	//		})
	//	}
	//}
}

func (r *SubnetReconciler) subnetHasBindings(subnetCRUID string) []*v1alpha1.SubnetConnectionBindingMap {
	vpcSubnets := r.SubnetService.ListSubnetCreatedBySubnet(subnetCRUID)
	if len(vpcSubnets) == 0 {
		return nil
	}

	bindingMaps := make([]*v1alpha1.SubnetConnectionBindingMap, 0)
	for _, vpcSubnet := range vpcSubnets {
		bindings := r.BindingService.GetSubnetConnectionBindingMapCRsBySubnet(vpcSubnet)
		if len(bindings) > 0 {
			bindingMaps = append(bindingMaps, bindingMaps...)
		}
	}
	return bindingMaps
}

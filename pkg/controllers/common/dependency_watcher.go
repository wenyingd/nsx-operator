package common

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/vmware-tanzu/nsx-operator/pkg/apis/vpc/v1alpha1"
)

type RequeueObjectByEvent func(ctx context.Context, c client.Client, obj client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request])
type RequeueObjectsByUpdate func(ctx context.Context, c client.Client, objOld client.Object, objNew client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request])

type EnqueueRequestForDependency struct {
	Client          client.Client
	ResourceType    string
	RequeueByCreate RequeueObjectByEvent
	RequeueByDelete RequeueObjectByEvent
	RequeueByUpdate RequeueObjectsByUpdate
}

func (e *EnqueueRequestForDependency) Create(ctx context.Context, ev event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	obj := ev.Object
	log.V(1).Info(fmt.Sprintf("%s create event", e.ResourceType), "Namespace", obj.GetNamespace(), "Name", obj.GetName())
	if e.RequeueByCreate != nil {
		e.RequeueByCreate(ctx, e.Client, obj, q)
	}
}

func (e *EnqueueRequestForDependency) Delete(ctx context.Context, ev event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	obj := ev.Object
	log.V(1).Info(fmt.Sprintf("%s delete event", e.ResourceType), "Namespace", obj.GetNamespace(), "Name", obj.GetName())
	if e.RequeueByDelete != nil {
		e.RequeueByDelete(ctx, e.Client, obj, q)
	}
}

func (e *EnqueueRequestForDependency) Generic(_ context.Context, _ event.GenericEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	log.V(1).Info(fmt.Sprintf("%s generic event, do nothing", e.ResourceType))
}

func (e *EnqueueRequestForDependency) Update(ctx context.Context, ev event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	objNew := ev.ObjectNew
	log.V(1).Info(fmt.Sprintf("%s update event", e.ResourceType), "Namespace", objNew.GetNamespace(), "Name", objNew.GetName())
	if e.RequeueByUpdate != nil {
		objOld := ev.ObjectOld
		e.RequeueByUpdate(ctx, e.Client, objOld, objNew, q)
	}
}

func IsObjectUpdateToReady(oldConditions []v1alpha1.Condition, newConditions []v1alpha1.Condition) bool {
	return !IsObjectReady(oldConditions) && IsObjectReady(newConditions)
}

func IsObjectReady(conditions []v1alpha1.Condition) bool {
	for _, con := range conditions {
		if con.Type == v1alpha1.Ready && con.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

var PredicateFuncsWithBindingMapUpdateDelete = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		oldBindingMap, _ := e.ObjectOld.(*v1alpha1.SubnetConnectionBindingMap)
		newBindingMap, _ := e.ObjectNew.(*v1alpha1.SubnetConnectionBindingMap)
		if IsObjectReady(oldBindingMap.Status.Conditions) != IsObjectReady(newBindingMap.Status.Conditions) {
			return true
		}
		return false
	},
	CreateFunc: func(e event.CreateEvent) bool {
		return false
	},
	DeleteFunc: func(e event.DeleteEvent) bool { return true },
	GenericFunc: func(e event.GenericEvent) bool {
		return false
	},
}

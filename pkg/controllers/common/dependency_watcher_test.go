package common

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
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/vmware-tanzu/nsx-operator/pkg/apis/vpc/v1alpha1"
)

func TestEnqueueRequestForBindingMap(t *testing.T) {
	myQueue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	defer myQueue.ShutDown()

	requeueByCreate := func(ctx context.Context, _ client.Client, obj client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
		q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: "create", Namespace: "default"}})
	}
	requeueByUpdate := func(ctx context.Context, _ client.Client, _, _ client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
		q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: "update", Namespace: "default"}})
	}
	requeueByDelete := func(ctx context.Context, _ client.Client, obj client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
		q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: "delete", Namespace: "default"}})
	}

	obj := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "test",
		},
	}
	obj2 := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "test",
			Annotations: map[string]string{
				"update": "true",
			},
		},
	}
	enqueueRequest := &EnqueueRequestForDependency{
		ResourceType:    "create",
		RequeueByCreate: requeueByCreate,
		RequeueByDelete: requeueByDelete,
		RequeueByUpdate: requeueByUpdate,
	}
	createEvent := event.CreateEvent{
		Object: obj,
	}
	updateEvent := event.UpdateEvent{
		ObjectOld: obj,
		ObjectNew: obj2,
	}
	deleteEvent := event.DeleteEvent{
		Object: obj,
	}
	genericEvent := event.GenericEvent{
		Object: obj,
	}

	ctx := context.Background()
	enqueueRequest.Create(ctx, createEvent, myQueue)
	require.Equal(t, 1, myQueue.Len())
	item, _ := myQueue.Get()
	assert.Equal(t, "create", item.Name)
	myQueue.Done(item)

	enqueueRequest.Update(ctx, updateEvent, myQueue)
	require.Equal(t, 1, myQueue.Len())
	item, _ = myQueue.Get()
	assert.Equal(t, "update", item.Name)
	myQueue.Done(item)

	enqueueRequest.Delete(ctx, deleteEvent, myQueue)
	require.Equal(t, 1, myQueue.Len())
	item, _ = myQueue.Get()
	assert.Equal(t, "delete", item.Name)
	myQueue.Done(item)

	enqueueRequest.Generic(ctx, genericEvent, myQueue)
	require.Equal(t, 0, myQueue.Len())
}

func TestPredicateFuncsBindingMap(t *testing.T) {
	readyBM := &v1alpha1.SubnetConnectionBindingMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bm1",
			Namespace: "default",
		},
		Spec: v1alpha1.SubnetConnectionBindingMapSpec{
			VLANTrafficTag: 201,
		},
		Status: v1alpha1.SubnetConnectionBindingMapStatus{
			Conditions: []v1alpha1.Condition{
				{
					Type:   v1alpha1.Ready,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}
	readyBM2 := &v1alpha1.SubnetConnectionBindingMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bm1",
			Namespace: "default",
		},
		Spec: v1alpha1.SubnetConnectionBindingMapSpec{
			VLANTrafficTag: 202,
		},
		Status: v1alpha1.SubnetConnectionBindingMapStatus{
			Conditions: []v1alpha1.Condition{
				{
					Type:   v1alpha1.Ready,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}
	unreadyBM := &v1alpha1.SubnetConnectionBindingMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bm1",
			Namespace: "default",
		},
		Spec: v1alpha1.SubnetConnectionBindingMapSpec{
			VLANTrafficTag: 201,
		},
		Status: v1alpha1.SubnetConnectionBindingMapStatus{
			Conditions: []v1alpha1.Condition{
				{
					Type:   v1alpha1.Ready,
					Status: corev1.ConditionFalse,
				},
			},
		},
	}
	createEvent := event.CreateEvent{
		Object: readyBM,
	}
	assert.False(t, PredicateFuncsWithBindingMapUpdateDelete.CreateFunc(createEvent))

	updateEvent1 := event.UpdateEvent{
		ObjectOld: unreadyBM,
		ObjectNew: readyBM,
	}
	assert.True(t, PredicateFuncsWithBindingMapUpdateDelete.Update(updateEvent1))
	updateEvent2 := event.UpdateEvent{
		ObjectOld: readyBM,
		ObjectNew: unreadyBM,
	}
	assert.True(t, PredicateFuncsWithBindingMapUpdateDelete.Update(updateEvent2))
	updateEvent3 := event.UpdateEvent{
		ObjectOld: readyBM,
		ObjectNew: readyBM2,
	}
	assert.False(t, PredicateFuncsWithBindingMapUpdateDelete.Update(updateEvent3))
	deleteEvent := event.DeleteEvent{
		Object: readyBM,
	}
	assert.True(t, PredicateFuncsWithBindingMapUpdateDelete.Delete(deleteEvent))
	genericEvent := event.GenericEvent{
		Object: readyBM,
	}
	assert.False(t, PredicateFuncsWithBindingMapUpdateDelete.GenericFunc(genericEvent))
}

func TestIsObjectUpdateToReady(t *testing.T) {
	oldConditions := []v1alpha1.Condition{
		{
			Status: corev1.ConditionFalse,
			Type:   v1alpha1.Ready,
		},
	}
	newConditions := []v1alpha1.Condition{
		{
			Status: corev1.ConditionTrue,
			Type:   v1alpha1.Ready,
		},
	}
	assert.True(t, IsObjectUpdateToReady(oldConditions, newConditions))
	assert.False(t, IsObjectUpdateToReady(newConditions, newConditions))
	assert.False(t, IsObjectUpdateToReady(newConditions, oldConditions))
}

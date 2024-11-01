package subnet

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/vmware-tanzu/nsx-operator/pkg/apis/vpc/v1alpha1"
	servicecommon "github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
)

var (
	bm1 = &v1alpha1.SubnetConnectionBindingMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "binding1",
			Namespace: "default",
		},
		Spec: v1alpha1.SubnetConnectionBindingMapSpec{
			SubnetName:       "child",
			TargetSubnetName: "parent",
			VLANTrafficTag:   101,
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

	bm2 = &v1alpha1.SubnetConnectionBindingMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "binding1",
			Namespace: "default",
		},
		Spec: v1alpha1.SubnetConnectionBindingMapSpec{
			SubnetName:          "child2",
			TargetSubnetSetName: "parentSet",
			VLANTrafficTag:      102,
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

	subnet1 = &v1alpha1.Subnet{
		ObjectMeta: metav1.ObjectMeta{Name: "child", Namespace: "default"},
	}
	subnet2 = &v1alpha1.Subnet{
		ObjectMeta: metav1.ObjectMeta{Name: "parent", Namespace: "default", Finalizers: []string{servicecommon.SubnetFinalizerName}},
	}
	subnet3 = &v1alpha1.Subnet{
		ObjectMeta: metav1.ObjectMeta{Name: "child2", Namespace: "default", Finalizers: []string{servicecommon.SubnetFinalizerName}},
	}
	req1 = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "child",
			Namespace: "default",
		},
	}
	req2 = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "parent",
			Namespace: "default",
		},
	}
	req3 = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "child2",
			Namespace: "default",
		},
	}
)

func TestRequeueSubnetByBindingMap(t *testing.T) {
	myQueue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	defer myQueue.ShutDown()

	ctx := context.TODO()
	newScheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(newScheme))
	utilruntime.Must(v1alpha1.AddToScheme(newScheme))

	fakeClient := fake.NewClientBuilder().WithScheme(newScheme).WithObjects(subnet1, subnet2, subnet3).Build()

	requeueSubnetByBindingMapUpdate(ctx, fakeClient, bm1, bm1, myQueue)
	require.Equal(t, 1, myQueue.Len())
	item, _ := myQueue.Get()
	assert.Equal(t, req1, item)
	myQueue.Done(item)

	requeueSubnetByBindingMapUpdate(ctx, fakeClient, bm2, bm2, myQueue)
	require.Equal(t, 1, myQueue.Len())
	item, _ = myQueue.Get()
	assert.Equal(t, req3, item)
	myQueue.Done(item)

	requeueSubnetByBindingMapDelete(ctx, fakeClient, bm1, myQueue)
	require.Equal(t, 1, myQueue.Len())
	item, _ = myQueue.Get()
	assert.Equal(t, req2, item)
	myQueue.Done(item)

	requeueSubnetByBindingMapDelete(ctx, fakeClient, bm2, myQueue)
	require.Equal(t, 1, myQueue.Len())
	item, _ = myQueue.Get()
	assert.Equal(t, req3, item)
	myQueue.Done(item)
}

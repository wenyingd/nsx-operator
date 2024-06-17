package childsubnet

import (
	"context"
	"github.com/vmware-tanzu/nsx-operator/pkg/apis/v1alpha1"
	"github.com/vmware-tanzu/nsx-operator/pkg/controllers/common"
	"github.com/vmware-tanzu/nsx-operator/pkg/logger"
	"github.com/vmware-tanzu/nsx-operator/pkg/metrics"
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/childsubnet"
	apimachineryruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"time"
)

var (
	log                     = logger.Log
	ResultNormal            = common.ResultNormal
	ResultRequeue           = common.ResultRequeue
	ResultRequeueAfter5mins = common.ResultRequeueAfter5mins
	MetricResTypeSubnet     = common.MetricResTypeChildSubnet
)

// ChildSubnetReconciler reconciles a ChildSubnet object
type ChildSubnetReconciler struct {
	Client  client.Client
	Scheme  *apimachineryruntime.Scheme
	Service *childsubnet.ChildSubnetService
}

func (r *ChildSubnetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {

}

func (r *ChildSubnetReconciler) GarbageCollector(cancel chan bool, timeout time.Duration) {
	ctx := context.Background()
	log.Info("childSubnet garbage collector started")
	for {
		select {
		case <-cancel:
			return
		case <-time.After(timeout):
		}
		nsxSubnetList := r.Service.ListSubnetCreatedByCR()
		if len(nsxSubnetList) == 0 {
			continue
		}

		crdChildSubnetList := &v1alpha1.ChildSubnetList{}
		err := r.Client.List(ctx, crdChildSubnetList)
		if err != nil {
			log.Error(err, "failed to list subnet CR")
			continue
		}

		crdSubnetIDs := sets.NewString()
		for _, cs := range crdChildSubnetList.Items {
			crdSubnetIDs.Insert(string(cs.UID))
		}

		for _, elem := range nsxSubnetList {
			if crdSubnetIDs.Has(*elem.Id) {
				continue
			}

			log.Info("GC collected Subnet CR", "UID", elem)
			metrics.CounterInc(r.Service.NSXConfig, metrics.ControllerDeleteTotal, common.MetricResTypeSubnet)
			err = r.Service.DeleteSubnet(elem)
			if err != nil {
				metrics.CounterInc(r.Service.NSXConfig, metrics.ControllerDeleteFailTotal, common.MetricResTypeSubnet)
			} else {
				metrics.CounterInc(r.Service.NSXConfig, metrics.ControllerDeleteSuccessTotal, common.MetricResTypeSubnet)
			}
		}
	}
}

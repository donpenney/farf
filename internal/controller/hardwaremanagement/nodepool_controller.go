/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package hardwaremanagement

import (
	"context"
	"log/slog"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	hwmgmtv1alpha1 "github.com/openshift-kni/oran-o2ims/api/hardwaremanagement/v1alpha1"
)

// NodePoolReconciler reconciles a NodePool object
type NodePoolReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Logger *slog.Logger
}

func doNotRequeue() ctrl.Result {
	return ctrl.Result{Requeue: false}
}

func requeueWithError(err error) (ctrl.Result, error) {
	// can not be fixed by user during reconcile
	return ctrl.Result{}, err
}

func requeueWithLongInterval() ctrl.Result {
	return requeueWithCustomInterval(5 * time.Minute)
}

func requeueWithMediumInterval() ctrl.Result {
	return requeueWithCustomInterval(1 * time.Minute)
}

func requeueWithCustomInterval(interval time.Duration) ctrl.Result {
	return ctrl.Result{RequeueAfter: interval}
}

//+kubebuilder:rbac:groups=hardwaremanagement.oran.openshift.io,resources=nodepools,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=hardwaremanagement.oran.openshift.io,resources=nodepools/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=hardwaremanagement.oran.openshift.io,resources=nodepools/finalizers,verbs=update
//+kubebuilder:rbac:groups=hardwaremanagement.oran.openshift.io,resources=nodes,verbs=get;create;list;watch;update;patch;delete
//+kubebuilder:rbac:groups=hardwaremanagement.oran.openshift.io,resources=nodes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=hardwaremanagement.oran.openshift.io,resources=nodes/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the NodePool object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.16.3/pkg/reconcile
func (r *NodePoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	_ = log.FromContext(ctx)
	result = doNotRequeue()

	// Fetch the object:
	object := &hwmgmtv1alpha1.NodePool{}
	if err = r.Client.Get(ctx, req.NamespacedName, object); err != nil {
		if errors.IsNotFound(err) {
			// The NodePool could have been deleted
			r.Logger.ErrorContext(ctx, "NodePool not found... deleted? "+req.Name)
			err = nil
			return
		}
		r.Logger.ErrorContext(
			ctx,
			"Unable to fetch NodePool",
			slog.String("error", err.Error()),
		)
		return
	}

	r.Logger.InfoContext(ctx, "[NodePool] "+object.Name)

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodePoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hwmgmtv1alpha1.NodePool{}).
		Complete(r)
}

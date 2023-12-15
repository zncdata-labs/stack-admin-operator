/*
Copyright 2023 zncdata-labs.

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

package redis

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-logr/logr"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	stackv1alpha1 "github.com/zncdata-labs/zncdata-stack-operator/api/v1alpha1"
)

// RedisConnectionReconciler reconciles a RedisConnection object
type RedisConnectionReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

//+kubebuilder:rbac:groups=stack.zncdata.net,resources=redisconnections,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=stack.zncdata.net,resources=redisconnections/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=stack.zncdata.net,resources=redisconnections/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the RedisConnection object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.1/pkg/reconcile
func (r *RedisConnectionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	redisConnection := &stackv1alpha1.RedisConnection{}
	if err := r.Get(ctx, req.NamespacedName, redisConnection); err != nil {
		if client.IgnoreNotFound(err) != nil {
			r.Log.Error(err, "unable to fetch instance")
			return ctrl.Result{}, err
		}
		r.Log.Info("RedisConnection resource not found. Ignoring since object must be deleted")
		return ctrl.Result{}, nil
	}

	//// Get the status condition, if it exists and its generation is not the
	////same as the redisConnection's generation, reset the status conditions
	readCondition := apimeta.FindStatusCondition(redisConnection.Status.Conditions, stackv1alpha1.ConditionTypeProgressing)
	if readCondition == nil || readCondition.ObservedGeneration != redisConnection.GetGeneration() {
		redisConnection.InitStatusConditions()
		if err := r.UpdateStatus(ctx, redisConnection); err != nil {
			return ctrl.Result{}, err
		}
	}

	r.Log.Info("redisConnection is found", "Name", redisConnection.Name)

	connector := NewRedisConnector(&Config{
		Addr:     fmt.Sprintf("%s:%s", redisConnection.Spec.Host, redisConnection.Spec.Port),
		Password: redisConnection.Spec.Password,
	})
	connection, err := connector.CheckConnection()
	if err != nil {
		r.Log.Error(err, "connect redis", "err: ", err)
		return ctrl.Result{}, err
	}
	if !connection {
		return ctrl.Result{}, errors.New("connect redis failed")
	}

	if redisConnection.IsAvailable() {
		return ctrl.Result{}, err
	}

	redisConnection.SetStatusCondition(metav1.Condition{
		Type:               stackv1alpha1.ConditionTypeAvailable,
		Status:             metav1.ConditionTrue,
		Reason:             stackv1alpha1.ConditionReasonRunning,
		Message:            "DatabaseConnection is running",
		ObservedGeneration: redisConnection.GetGeneration(),
	})

	if err := r.UpdateStatus(ctx, redisConnection); err != nil {
		r.Log.Error(err, "Failed to update status")
		return ctrl.Result{}, err
	}

	r.Log.Info("Successfully updated status")

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RedisConnectionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&stackv1alpha1.RedisConnection{}).
		Complete(r)
}

func (r *RedisConnectionReconciler) UpdateStatus(ctx context.Context, instance *stackv1alpha1.RedisConnection) error {
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		return r.Status().Update(ctx, instance)
	})
	if retryErr != nil {
		r.Log.Error(retryErr, "Failed to update vfm status after retries")
		return retryErr
	}
	r.Log.V(1).Info("Successfully patched object status")
	return nil
}

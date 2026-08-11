/*
Copyright 2026.

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

package controller

import (
	"context"
	"fmt"
	"slices"

	glbv1alpha1 "github.com/itsthatdude/k8s-glb-operator/api/v1alpha1"
	"github.com/itsthatdude/k8s-glb-operator/internal/haproxy"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

type DeploymentImages struct {
	WaitForConfig string
	HAProxy       string
}

// GlobalLoadBalancerReconciler reconciles a GlobalLoadBalancer object
type GlobalLoadBalancerReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	images       DeploymentImages
	StartingPort int32
}

// +kubebuilder:rbac:groups=glb.antware.xyz,namespace=k8s-glb-operator-system,resources=globalloadbalancers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=glb.antware.xyz,namespace=k8s-glb-operator-system,resources=globalloadbalancers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=glb.antware.xyz,namespace=k8s-glb-operator-system,resources=globalloadbalancers/finalizers,verbs=update

func (r *GlobalLoadBalancerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = logf.FromContext(ctx)

	loadBalancer := &glbv1alpha1.GlobalLoadBalancer{}

	if err := r.Get(ctx, req.NamespacedName, loadBalancer); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	requeue, err := r.EnsureHAProxyStatefulSet(ctx, loadBalancer)
	if err != nil || requeue {
		return ctrl.Result{}, err
	}

	if err := r.ReconcileFrontends(ctx, loadBalancer); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.ReconcileConfig(ctx, loadBalancer); err != nil {

	}

	return ctrl.Result{}, nil
}

func (r *GlobalLoadBalancerReconciler) ReconcileFrontends(ctx context.Context, loadBalancer *glbv1alpha1.GlobalLoadBalancer) error {
	log := logf.FromContext(ctx)

	selector, err := metav1.LabelSelectorAsSelector(&loadBalancer.Spec.FrontendSelector)
	if err != nil {
		return fmt.Errorf("invalid backend selector on GlobalLoadBalancer: %w", err)
	}

	frontends := &glbv1alpha1.FrontendList{}
	if err := r.List(ctx, frontends, &client.ListOptions{
		LabelSelector: selector,
	}); err != nil {
		return err
	}

	nextPort := r.StartingPort

	if len(frontends.Items) > 0 {
		nextPort = GetNextPort(frontends.Items, r.StartingPort)
	}

	for _, frontend := range frontends.Items {
		updated := false
		if frontend.Status.LoadBalancer == nil {
			frontend.Status.LoadBalancer = &glbv1alpha1.LoadBalancerReference{
				Name:      loadBalancer.Name,
				Namespace: loadBalancer.Namespace,
			}
			updated = true
		}

		if frontend.Status.Port == nil {
			frontend.Status.Port = ptr.To(nextPort)
			updated = true
		}

		if updated {
			if err := r.Client.Status().Update(ctx, &frontend); err != nil {
				log.Error(err, "failed to update status for frontend", "frontend", frontend.Name)
				continue
			}
		}
		nextPort = GetNextPort(frontends.Items, r.StartingPort)
	}

	return nil
}

func (r *GlobalLoadBalancerReconciler) ReconcileConfig(ctx context.Context, loadBalancer *glbv1alpha1.GlobalLoadBalancer) error {
	configBuilder := haproxy.ConfigBuilder{}

	configMap, err := configBuilder.BuildConfig(loadBalancer)
	if err != nil {
		return err
	}

	for podName, config := range configMap {
		cfgMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-config", podName),
				Namespace: loadBalancer.Namespace,
			},
		}

		_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cfgMap, func() error {
			cfgMap.Data = map[string]string{
				"haproxy.cfg": config,
			}

			return nil
		})

		if err != nil {
			return err
		}
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *GlobalLoadBalancerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&glbv1alpha1.GlobalLoadBalancer{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&glbv1alpha1.Frontend{}).
		Owns(&glbv1alpha1.Backend{}).
		Named("globalloadbalancer").
		Complete(r)
}

func GetNextPort(items []glbv1alpha1.Frontend, minPort int32) int32 {
	if len(items) == 0 {
		return minPort
	}

	ports := make([]int32, len(items))
	for i, item := range items {
		if item.Status.Port != nil {
			ports[i] = *item.Status.Port
		}
	}

	slices.Sort(ports)

	if ports[0] > minPort {
		return minPort
	}

	for i := 0; i < len(ports)-1; i++ {
		if ports[i+1]-ports[i] > 1 {
			return ports[i] + 1
		}
	}

	return ports[len(ports)-1] + 1
}

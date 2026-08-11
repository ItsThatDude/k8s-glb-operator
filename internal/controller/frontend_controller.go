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

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/source"

	glbv1alpha1 "github.com/itsthatdude/k8s-glb-operator/api/v1alpha1"
	"github.com/itsthatdude/k8s-glb-operator/internal/haproxy"
)

// FrontendReconciler reconciles a Frontend object
type FrontendReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	healthCache *haproxy.HealthCache
}

// +kubebuilder:rbac:groups=glb.antware.xyz,namespace=k8s-glb-operator-system,resources=frontends,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=glb.antware.xyz,namespace=k8s-glb-operator-system,resources=frontends/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=glb.antware.xyz,namespace=k8s-glb-operator-system,resources=frontends/finalizers,verbs=update

// +kubebuilder:rbac:groups=core,namespace=k8s-glb-operator-system,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,namespace=k8s-glb-operator-system,resources=services/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,namespace=k8s-glb-operator-system,resources=services/finalizers,verbs=update

// +kubebuilder:rbac:groups=discovery,namespace=k8s-glb-operator-system,resources=endpointslices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=discovery,namespace=k8s-glb-operator-system,resources=endpointslices/restricted,verbs=create;update;patch;delete
// +kubebuilder:rbac:groups=discovery,namespace=k8s-glb-operator-system,resources=endpointslices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=discovery,namespace=k8s-glb-operator-system,resources=endpointslices/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Frontend object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.24.1/pkg/reconcile
func (r *FrontendReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = logf.FromContext(ctx)

	frontend := &glbv1alpha1.Frontend{}

	if err := r.Get(ctx, req.NamespacedName, frontend); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := r.ReconcileService(ctx, req, frontend); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *FrontendReconciler) ReconcileService(ctx context.Context, req ctrl.Request, frontend *glbv1alpha1.Frontend) error {
	if frontend.Status.Port == nil {
		return nil
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      frontend.Name,
			Namespace: frontend.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		if err := controllerutil.SetControllerReference(frontend, svc, r.Scheme); err != nil {
			return err
		}

		svc.Annotations = frontend.Spec.Service.Annotations

		svc.Spec.Ports = []corev1.ServicePort{
			{
				Name:       "frontend",
				Protocol:   corev1.ProtocolTCP,
				Port:       frontend.Spec.Service.Port,
				TargetPort: intstr.FromInt32(*frontend.Status.Port),
			},
		}

		return nil
	})

	if err != nil {
		return err
	}

	es := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svc.Name,
			Namespace: svc.Namespace,
			Labels: map[string]string{
				discoveryv1.LabelServiceName: svc.Name,
			},
		},
	}

	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, es, func() error {
		if err := controllerutil.SetControllerReference(svc, es, r.Scheme); err != nil {
			return err
		}

		es.Labels = map[string]string{
			discoveryv1.LabelServiceName: svc.Name,
		}
		es.AddressType = discoveryv1.AddressTypeIPv4

		endpointHealth := r.healthCache.Get(req.NamespacedName)
		epsEndpoints := make([]discoveryv1.Endpoint, 0, len(endpointHealth))
		for _, ep := range endpointHealth {
			ready := ep.Healthy
			epsEndpoints = append(epsEndpoints, discoveryv1.Endpoint{
				Addresses: []string{ep.IPAddress},
				Conditions: discoveryv1.EndpointConditions{
					Ready: &ready,
				},
				NodeName: &ep.NodeName,
			})
		}

		es.Endpoints = epsEndpoints
		es.Ports = []discoveryv1.EndpointPort{{
			Name:     ptr.To("frontend"),
			Port:     frontend.Status.Port,
			Protocol: ptr.To(corev1.ProtocolTCP),
		}}

		return nil
	})

	return err
}

// SetupWithManager sets up the controller with the Manager.
func (r *FrontendReconciler) SetupWithManager(mgr ctrl.Manager, healthEvents chan event.GenericEvent) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&glbv1alpha1.Frontend{}).
		WatchesRawSource(
			source.Channel(
				healthEvents,
				&handler.EnqueueRequestForObject{},
			),
		).
		Named("frontend").
		Complete(r)
}

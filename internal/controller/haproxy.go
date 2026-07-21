package controller

import (
	"context"
	"fmt"

	"github.com/ItsThatDude/k8s-glb-operator/api/v1alpha1"
	"github.com/ItsThatDude/k8s-glb-operator/version"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	componentLabel = "app.kubernetes.io/component"
	nameLabel      = "app.kubernetes.io/name"
	versionLabel   = "app.kubernetes.io/version"
)

var haproxyLabels = map[string]string{
	componentLabel: "haproxy",
	nameLabel:      "global-load-balancer",
}

func (r *GlobalLoadBalancerReconciler) EnsureHAProxyStatefulSet(ctx context.Context, cluster v1alpha1.GlobalLoadBalancer) error {
	log := logf.FromContext(ctx)
	deploymentName := fmt.Sprintf("%s-haproxy", cluster.Name)

	log.Info("Reconciling HAProxy StatefulSet", "cluster", cluster.Name, "namespace", cluster.Namespace)

	deployment := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: cluster.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		deployment.ObjectMeta = metav1.ObjectMeta{
			Name:            deploymentName,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{}},
			Labels: map[string]string{
				componentLabel: haproxyLabels[componentLabel],
				nameLabel:      haproxyLabels[nameLabel],
				versionLabel:   version.Version,
			},
		}

		configVolSize := resource.MustParse("100Mi")
		runVolSize := resource.MustParse("50Mi")

		deployment.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					componentLabel: haproxyLabels[componentLabel],
					nameLabel:      haproxyLabels[nameLabel],
					versionLabel:   version.Version,
				},
			},
			Spec: corev1.PodSpec{
				SecurityContext: &corev1.PodSecurityContext{
					RunAsNonRoot: ptr.To(true),
				},
				InitContainers: []corev1.Container{
					{
						Name:    "config-gen",
						Image:   r.images.ConfigGenerator,
						Command: []string{"/usr/local/bin/config-gen"},
						Args: func() []string {
							args := []string{
								"--name", cluster.Name,
							}
							return args
						}(),
						RestartPolicy: ptr.To(corev1.ContainerRestartPolicyAlways),
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "haproxy-config",
								MountPath: "/etc/haproxy",
								ReadOnly:  false,
							},
						},
					},
					{
						Name:    "wait-for-config",
						Image:   r.images.ConfigGenerator,
						Command: []string{"/usr/local/bin/wait-for-config.sh"},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "haproxy-config",
								MountPath: "/etc/haproxy",
								ReadOnly:  true,
							},
						},
					},
				},
				Containers: []corev1.Container{
					{
						Name:  "haproxy",
						Image: r.images.HAProxy,
						Args: func() []string {
							args := []string{
								"-W",
								"-f", "/etc/haproxy/haproxy.cfg",
								"-f", "/etc/haproxy/peers.cfg",
								"-f", "/etc/haproxy/maps",
								"-f", "/etc/haproxy/lists",
								"-f", "/etc/haproxy/frontends",
								"-f", "/etc/haproxy/backends",
								"-p", "/var/run/haproxy/haproxy.pid",
							}
							return args
						}(),
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "haproxy-config",
								MountPath: "/etc/haproxy",
								ReadOnly:  true,
							},
							{
								Name:      "haproxy-run",
								MountPath: "/var/run/haproxy",
								ReadOnly:  false,
							},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: "haproxy-config",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{
								SizeLimit: &configVolSize,
							},
						},
					},
					{
						Name: "haproxy-run",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{
								SizeLimit: &runVolSize,
							},
						},
					},
				},
			},
		}

		// Set immutable properties
		if deployment.CreationTimestamp.IsZero() {
			deployment.Spec.Selector = &metav1.LabelSelector{
				MatchLabels: haproxyLabels,
			}
		}

		return nil
	})

	if err != nil {
		return err
	}

	return nil
}

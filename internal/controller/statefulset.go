package controller

import (
	"context"
	"fmt"

	"github.com/itsthatdude/k8s-glb-operator/api/v1alpha1"
	"github.com/itsthatdude/k8s-glb-operator/version"
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

	haproxyRunPath       = "/var/run/haproxy"
	haproxyRunVolumeName = "haproxy-run"

	haproxyConfigPath       = "/etc/haproxy"
	haproxyConfigVolumeName = "haproxy-config"

	haproxyConfigMapVolumeName = "haproxy-static-config"
	haproxyConfigMapName       = "haproxy-config"
	haproxyConfigMapPath       = "/etc/haproxy/static"
)

var haproxyLabels = map[string]string{
	componentLabel: "haproxy",
	nameLabel:      "global-load-balancer",
}

func (r *GlobalLoadBalancerReconciler) EnsureHAProxyStatefulSet(ctx context.Context, cluster *v1alpha1.GlobalLoadBalancer) error {
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
			Name:      deploymentName,
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				componentLabel: haproxyLabels[componentLabel],
				nameLabel:      haproxyLabels[nameLabel],
				versionLabel:   version.Version,
			},
		}

		deployment.Spec.Replicas = &cluster.Spec.Replicas

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
				Containers: []corev1.Container{
					{
						Name:  "haproxy",
						Image: cluster.Spec.Image,
						Args: []string{
							"-W",
							"-f", "/etc/haproxy/static/$(POD_NAME).cfg",
							"-p", "/var/run/haproxy/haproxy.pid",
						},
						Env: []corev1.EnvVar{
							{
								Name: "POD_NAME",
								ValueFrom: &corev1.EnvVarSource{
									FieldRef: &corev1.ObjectFieldSelector{
										FieldPath: "metadata.name",
									},
								},
							},
						},
						Ports: []corev1.ContainerPort{
							{
								Name:          "stats",
								Protocol:      "TCP",
								ContainerPort: 8404,
							},
							{
								Name:          "metrics",
								Protocol:      "TCP",
								ContainerPort: 8405,
							},
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      haproxyConfigVolumeName,
								MountPath: haproxyConfigPath,
								ReadOnly:  false,
							},
							{
								Name:      haproxyConfigMapVolumeName,
								MountPath: haproxyConfigMapPath,
								ReadOnly:  true,
							},
							{
								Name:      haproxyRunVolumeName,
								MountPath: haproxyRunPath,
								ReadOnly:  false,
							},
						},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptr.To(false),
							RunAsNonRoot:             ptr.To(true),
							Capabilities: &corev1.Capabilities{
								Drop: []corev1.Capability{
									"ALL",
								},
							},
							SeccompProfile: &corev1.SeccompProfile{
								Type: corev1.SeccompProfileTypeRuntimeDefault,
							},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: haproxyConfigVolumeName,
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{
								SizeLimit: &configVolSize,
							},
						},
					},
					{
						Name: haproxyConfigMapVolumeName,
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: fmt.Sprintf("%s-%s", cluster.Name, haproxyConfigMapName),
								},
							},
						},
					},
					{
						Name: haproxyRunVolumeName,
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

			if err := controllerutil.SetControllerReference(cluster, deployment, r.Scheme); err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		return err
	}

	return nil
}

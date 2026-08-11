package haproxy

import (
	"fmt"

	glbv1alpha1 "github.com/itsthatdude/k8s-glb-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ConfigBuilder struct {
	Client client.Client
}

func (b *ConfigBuilder) BuildConfig(loadBalancer *glbv1alpha1.GlobalLoadBalancer) (map[string]string, error) {
	configMap := make(map[string]string)

	for i := range loadBalancer.Spec.Replicas {
		podName := fmt.Sprintf("%s-haproxy-%d", loadBalancer.Name, i)
		configMap[podName] = ""
	}

	return configMap, nil
}

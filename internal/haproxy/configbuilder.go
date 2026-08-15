package haproxy

import (
	"context"
	"fmt"
	"strings"

	glbv1alpha1 "github.com/itsthatdude/k8s-glb-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

type ConfigBuilder struct {
	Client client.Client
}

func (b *ConfigBuilder) BuildConfig(loadBalancer *glbv1alpha1.GlobalLoadBalancer, frontends []glbv1alpha1.Frontend, backends map[string][]glbv1alpha1.Backend) (map[string]string, error) {
	log := logf.FromContext(context.Background())
	configMap := make(map[string]string)

	var loadBalancerConfig strings.Builder

	for _, frontend := range frontends {
		fmt.Fprintf(&loadBalancerConfig, `
frontend %s
  bind *:%d
  mode %s
  option tcplog

  tcp-request inspect-delay 5s
  tcp-request content accept if { req_ssl_hello_type 1 }

`, frontend.Name, frontend.Spec.Service.Port, "tcp")
		for _, backend := range backends[frontend.Name] {
			fmt.Fprintf(&loadBalancerConfig, `
  use_backend %s if { req_ssl_sni -i %s }
`, backend.Name, backend.Spec.Host)
		}

		for _, backend := range backends[frontend.Name] {
			fmt.Fprintf(&loadBalancerConfig, `
backend %s
  mode %s
`, backend.Name, "tcp")
			for _, server := range backend.Spec.Servers {
				fmt.Fprintf(&loadBalancerConfig, `
  server %s %s:%d check
`, server.Name, server.Address, server.Port)
			}
		}
	}

	for i := range loadBalancer.Spec.Replicas {
		podName := fmt.Sprintf("%s-haproxy-%d", loadBalancer.Name, i)
		log.Info("Building config for replica", "replica", podName)
		configMap[podName] = `
global
  log stdout format raw local0

defaults
  log global
  option dontlognull
  timeout connect 5s
  timeout client 50s
  timeout client-fin 50s
  timeout server 50s
  timeout tunnel 1h

frontend stats
  mode http
  bind :8404
  stats enable
  stats refresh 10s
  stats uri /stats
  stats show-modules

frontend prometheus
  bind :8405
  mode http
  http-request use-service prometheus-exporter
  no log
`
		configMap[podName] += loadBalancerConfig.String()
	}

	return configMap, nil
}

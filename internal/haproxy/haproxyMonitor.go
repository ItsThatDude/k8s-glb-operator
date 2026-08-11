package haproxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	v1alpha1 "github.com/itsthatdude/k8s-glb-operator/api/v1alpha1"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// +kubebuilder:rbac:groups=core,namespace=k8s-glb-operator-system,resources=services,verbs=get;list;watch

type ScrapeTarget struct {
	UID       types.UID
	Name      string
	Namespace string
	NodeName  string
	IP        string
}

type HAProxyMonitor struct {
	mgr manager.Manager
	client.Client
	log logr.Logger

	healthMgr *HealthManager

	namespace string
	selector  labels.Selector

	stateMu sync.RWMutex
	pods    map[types.UID]*corev1.Pod

	scrapeTargets map[types.UID]ScrapeTarget
	// map[BackendName][PodUID]IsUp
	endpointStates map[string]map[types.UID]bool

	metricsPort int32
	metricsPath string
	interval    time.Duration
}

func NewHAProxyMonitor(
	mgr manager.Manager,
	healthMgr *HealthManager,
	namespace string,
	metricsPort int32,
	metricsPath string,
	interval time.Duration,
) (*HAProxyMonitor, error) {
	req, err := labels.NewRequirement("app.kubernetes.io/name", selection.In, []string{"glb.antware.xyz"})
	if err != nil {
		return nil, err
	}

	selector := labels.NewSelector().Add(*req)

	return &HAProxyMonitor{
		mgr:            mgr,
		Client:         mgr.GetClient(),
		log:            log.Log.WithName("haproxy-monitor"),
		healthMgr:      healthMgr,
		namespace:      namespace,
		selector:       selector,
		pods:           make(map[types.UID]*corev1.Pod),
		endpointStates: make(map[string]map[types.UID]bool),
		metricsPort:    metricsPort,
		metricsPath:    metricsPath,
		interval:       interval,
	}, nil
}

func (w *HAProxyMonitor) SetupWithManager(mgr manager.Manager) error {
	return mgr.Add(w)
}

func (w *HAProxyMonitor) NeedLeaderElection() bool {
	return true
}

func (w *HAProxyMonitor) Start(ctx context.Context) error {
	informer, err := w.mgr.GetCache().GetInformer(ctx, &corev1.Pod{})
	if err != nil {
		return fmt.Errorf("failed to get pod informer: %w", err)
	}

	_, err = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    w.filter(ctx, w.onAdd),
		UpdateFunc: w.filterUpdate(ctx, w.onUpdate),
		DeleteFunc: w.filter(ctx, w.onDelete),
	})
	if err != nil {
		return fmt.Errorf("failed to add event handler: %w", err)
	}

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:

			w.stateMu.RLock()
			targets := make(map[types.UID]ScrapeTarget)
			for uid, pod := range w.pods {
				if pod.Status.PodIP != "" {
					targets[pod.UID] = ScrapeTarget{
						UID:       uid,
						Name:      pod.Name,
						Namespace: pod.Namespace,
						NodeName:  pod.Spec.NodeName,
						IP:        pod.Status.PodIP,
					}
				}
			}
			w.scrapeTargets = targets
			w.stateMu.RUnlock()

			var wg sync.WaitGroup
			for _, target := range targets {
				wg.Add(1)
				go func(target ScrapeTarget) {
					defer wg.Done()
					if err := w.scrapeMetrics(target); err != nil {
						w.log.Error(err, "Failed to scrape HAProxy pod", "podName", target.Name)
					}
				}(target)
			}
			wg.Wait()

			if err := w.updateHealthManager(ctx); err != nil {
				// return err
				continue
			}
		}
	}
}

func (w *HAProxyMonitor) scrapeMetrics(target ScrapeTarget) error {
	httpClient := &http.Client{Timeout: 5 * time.Second}

	u := &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(target.IP, strconv.Itoa(int(w.metricsPort))),
		Path:   "/" + strings.TrimPrefix(w.metricsPath, "/"),
	}

	resp, err := httpClient.Get(u.String())
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code %d for %s", resp.StatusCode, u.String())
	}

	dec := expfmt.NewDecoder(resp.Body, expfmt.NewFormat(expfmt.TypeProtoText))

	for {
		var mf dto.MetricFamily
		if err := dec.Decode(&mf); err != nil {
			if err.Error() == "EOF" {
				break
			}
			return err
		}

		if mf.GetName() == "haproxy_backend_server_up" {
			for _, metric := range mf.Metric {
				var backendName string
				for _, lp := range metric.GetLabel() {
					if lp.GetName() == "proxy" || lp.GetName() == "backend" {
						backendName = lp.GetValue()
					}
				}

				w.stateMu.Lock()
				if _, exists := w.endpointStates[backendName]; !exists {
					w.endpointStates[backendName] = make(map[types.UID]bool)
				}
				w.endpointStates[backendName][target.UID] = metric.GetGauge().GetValue() == 1.0
				w.stateMu.Unlock()
			}
		}
	}
	return nil
}

func (w *HAProxyMonitor) updateHealthManager(ctx context.Context) error {
	var backendList v1alpha1.BackendList
	if err := w.List(ctx, &backendList, client.InNamespace(w.namespace)); err != nil {
		return fmt.Errorf("failed to list backend CRDs: %w", err)
	}

	var frontendList v1alpha1.FrontendList
	if err := w.List(ctx, &frontendList, client.InNamespace(w.namespace)); err != nil {
		return fmt.Errorf("failed to list frontend CRDs: %w", err)
	}

	w.stateMu.RLock()
	consolidatedMetrics := make(map[string]map[types.UID]bool)
	for backendName, backendMap := range w.endpointStates {
		for uid, isUp := range backendMap {
			if _, exists := consolidatedMetrics[backendName]; !exists {
				consolidatedMetrics[backendName] = make(map[types.UID]bool)
			}
			consolidatedMetrics[backendName][uid] = consolidatedMetrics[backendName][uid] || isUp
		}
	}

	frontendEndpointStates := make(map[types.NamespacedName][]Endpoint)
	for _, backendCR := range backendList.Items {
		backendName := backendCR.Name
		uidStates, dataExists := consolidatedMetrics[backendName]
		if !dataExists {
			continue
		}

		backendLabels := labels.Set(backendCR.Labels)
		for _, frontendCR := range frontendList.Items {
			key := types.NamespacedName{
				Namespace: frontendCR.GetNamespace(),
				Name:      frontendCR.GetName(),
			}
			selector, err := metav1.LabelSelectorAsSelector(&frontendCR.Spec.BackendSelector)
			if err != nil {
				w.log.Error(err, "Invalid backendSelector formatting on Frontend", "frontend", frontendCR.Name)
				continue
			}

			if selector.Matches(backendLabels) {
				if _, exists := frontendEndpointStates[key]; !exists {
					frontendEndpointStates[key] = make([]Endpoint, 0)
				}
				for uid, isUp := range uidStates {
					ep := w.scrapeTargets[uid]
					frontendEndpointStates[key] = append(frontendEndpointStates[key], Endpoint{
						NodeName:  ep.NodeName,
						IPAddress: ep.IP,
						Healthy:   isUp,
					})
				}
			}
		}
	}
	w.stateMu.RUnlock()

	for frontend, states := range frontendEndpointStates {
		w.healthMgr.Update(frontend, states)
	}

	return nil
}

func (w *HAProxyMonitor) filter(ctx context.Context, handler func(ctx context.Context, pod *corev1.Pod)) func(any) {
	return func(obj any) {
		if pod, ok := obj.(*corev1.Pod); ok {
			if w.selector.Matches(labels.Set(pod.Labels)) {
				handler(ctx, pod)
			}
		}
	}
}

func (w *HAProxyMonitor) filterUpdate(ctx context.Context, handler func(ctx context.Context, old, new *corev1.Pod)) func(any, any) {
	return func(oldObj, newObj any) {
		oldPod, okOld := oldObj.(*corev1.Pod)
		newPod, okNew := newObj.(*corev1.Pod)
		if okOld && okNew {
			if w.selector.Matches(labels.Set(newPod.Labels)) {
				handler(ctx, oldPod, newPod)
			}
		}
	}
}

func (w *HAProxyMonitor) onAdd(ctx context.Context, pod *corev1.Pod) {
	w.log.Info(fmt.Sprintf("[Watcher] Pod added: %s/%s\n", pod.Namespace, pod.Name))

	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	w.pods[pod.UID] = pod
}

func (w *HAProxyMonitor) onUpdate(ctx context.Context, oldPod, newPod *corev1.Pod) {
	w.log.Info(fmt.Sprintf("[Watcher] Pod updated: %s/%s\n", newPod.Namespace, newPod.Name))

	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	w.pods[oldPod.UID] = newPod
}

func (w *HAProxyMonitor) onDelete(ctx context.Context, pod *corev1.Pod) {
	w.log.Info(fmt.Sprintf("[Watcher] Pod deleted: %s/%s\n", pod.Namespace, pod.Name))

	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	delete(w.pods, pod.UID)
}

package watchers

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	v1alpha1 "github.com/itsthatdude/k8s-glb-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

type PeerServiceWatcher struct {
	mgr manager.Manager
	log logr.Logger

	selector labels.Selector
}

func NewPeerServiceWatcher(mgr manager.Manager, labelKey string) (*PeerServiceWatcher, error) {
	req, err := labels.NewRequirement(labelKey, selection.Exists, nil)
	if err != nil {
		return nil, err
	}

	selector := labels.NewSelector().Add(*req)

	return &PeerServiceWatcher{
		mgr:      mgr,
		log:      log.Log.WithName("peer-svc-watcher"),
		selector: selector,
	}, nil
}

func (w *PeerServiceWatcher) SetupWithManager(mgr manager.Manager) error {
	return mgr.Add(w)
}

func (w *PeerServiceWatcher) NeedLeaderElection() bool {
	return true
}

func (w *PeerServiceWatcher) Start(ctx context.Context) error {
	informer, err := w.mgr.GetCache().GetInformer(ctx, &corev1.Service{})
	if err != nil {
		return fmt.Errorf("failed to get service informer: %w", err)
	}

	_, err = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    w.filter(ctx, w.onAdd),
		UpdateFunc: w.filterUpdate(ctx, w.onUpdate),
		DeleteFunc: w.filter(ctx, w.onDelete),
	})
	if err != nil {
		return fmt.Errorf("failed to add event handler: %w", err)
	}

	<-ctx.Done()
	return nil
}

func (w *PeerServiceWatcher) filter(ctx context.Context, handler func(ctx context.Context, svc *corev1.Service)) func(any) {
	return func(obj any) {
		if svc, ok := obj.(*corev1.Service); ok {
			if w.selector.Matches(labels.Set(svc.Labels)) {
				handler(ctx, svc)
			}
		}
	}
}

func (w *PeerServiceWatcher) filterUpdate(ctx context.Context, handler func(ctx context.Context, old, new *corev1.Service)) func(any, any) {
	return func(oldObj, newObj any) {
		oldSvc, okOld := oldObj.(*corev1.Service)
		newSvc, okNew := newObj.(*corev1.Service)
		if okOld && okNew {
			if w.selector.Matches(labels.Set(newSvc.Labels)) {
				handler(ctx, oldSvc, newSvc)
			}
		}
	}
}

func (w *PeerServiceWatcher) onAdd(ctx context.Context, svc *corev1.Service) {
	w.log.Info(fmt.Sprintf("[Watcher] Service added: %s/%s\n", svc.Namespace, svc.Name))

	peer := &v1alpha1.Peer{}
	err := w.mgr.GetCache().Get(ctx, client.ObjectKey{
		Namespace: svc.Namespace,
		Name:      svc.Name,
	}, peer)

	if client.IgnoreNotFound(err) != nil {
		return
	}

	if errors.IsNotFound(err) {
		peer = &v1alpha1.Peer{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: svc.Namespace,
				Name:      svc.Name,
			},
		}

		if err := w.mgr.GetClient().Create(ctx, peer); err != nil {
			w.log.Error(err, "failed to create Peer for Service")
			return
		}
	}

	peerCopy := peer.DeepCopy()

	peerCopy.Status.LocalIP = svc.Spec.ClusterIP
	for _, ingress := range svc.Status.LoadBalancer.Ingress {
		peerCopy.Status.RemoteIP = ingress.IP
		break
	}

	err = w.mgr.GetClient().Status().Update(ctx, peerCopy)
	if err != nil {
		w.log.Error(err, "failed to update the related Peer resource")
		return
	}
}

func (w *PeerServiceWatcher) onUpdate(ctx context.Context, oldSvc, newSvc *corev1.Service) {
	w.log.Info(fmt.Sprintf("[Watcher] Service updated: %s/%s\n", newSvc.Namespace, newSvc.Name))
}

func (w *PeerServiceWatcher) onDelete(ctx context.Context, svc *corev1.Service) {
	w.log.Info(fmt.Sprintf("[Watcher] Service deleted: %s/%s\n", svc.Namespace, svc.Name))
}

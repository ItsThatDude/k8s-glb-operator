package haproxy

import (
	"github.com/itsthatdude/k8s-glb-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

type HealthManager struct {
	cache  *HealthCache
	events chan event.GenericEvent
}

func NewHealthManager(cache *HealthCache, eventsChan chan event.GenericEvent) *HealthManager {
	return &HealthManager{
		cache:  cache,
		events: eventsChan,
	}
}

func (m *HealthManager) Update(frontend types.NamespacedName, endpoints []Endpoint) {
	changed := m.cache.Update(frontend, endpoints)

	if !changed {
		return
	}

	select {
	case m.events <- event.GenericEvent{
		Object: &v1alpha1.Frontend{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: frontend.Namespace,
				Name:      frontend.Name,
			},
		},
	}:
	default:
	}
}

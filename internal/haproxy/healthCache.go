package haproxy

import (
	"sync"

	"k8s.io/apimachinery/pkg/types"
)

type Endpoint struct {
	NodeName  string
	IPAddress string
	Healthy   bool
}

type HealthCache struct {
	mu        sync.RWMutex
	endpoints map[types.NamespacedName][]Endpoint
}

func NewHealthCache() *HealthCache {
	return &HealthCache{
		endpoints: make(map[types.NamespacedName][]Endpoint),
	}
}

func (c *HealthCache) Get(frontend types.NamespacedName) []Endpoint {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return append([]Endpoint(nil), c.endpoints[frontend]...)
}

func (c *HealthCache) GetHealthy(frontend types.NamespacedName) []Endpoint {
	c.mu.RLock()
	defer c.mu.RUnlock()

	healthy := make([]Endpoint, 0, len(c.endpoints[frontend]))
	for _, ep := range c.endpoints[frontend] {
		if ep.Healthy {
			healthy = append(healthy, ep)
		}
	}
	return healthy
}

func (c *HealthCache) Update(frontend types.NamespacedName, endpoints []Endpoint) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	val, ok := c.endpoints[frontend]
	if !ok {
		c.endpoints[frontend] = endpoints
		return true
	}

	if len(val) != len(endpoints) {
		c.endpoints[frontend] = endpoints
		return true
	}

	existingMap := make(map[string]bool, len(val))
	for _, ep := range val {
		existingMap[ep.IPAddress] = ep.Healthy
	}

	for _, ep := range endpoints {
		healthy, exists := existingMap[ep.IPAddress]
		if !exists || healthy != ep.Healthy {
			c.endpoints[frontend] = append([]Endpoint(nil), endpoints...)
			return true
		}
	}

	return false
}

package crdmem

import (
	"context"
	"sync"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Memory struct {
	mu        sync.RWMutex
	data      map[string]cozyv1alpha1.ApplicationDefinition
	primed    bool
	primeOnce sync.Once
}

func New() *Memory {
	return &Memory{data: make(map[string]cozyv1alpha1.ApplicationDefinition)}
}

var (
	global     *Memory
	globalOnce sync.Once
)

func Global() *Memory {
	globalOnce.Do(func() { global = New() })
	return global
}

func (m *Memory) Upsert(obj *cozyv1alpha1.ApplicationDefinition) {
	if obj == nil {
		return
	}
	m.mu.Lock()
	m.data[obj.Name] = *obj.DeepCopy()
	m.mu.Unlock()
}

func (m *Memory) Delete(name string) {
	m.mu.Lock()
	delete(m.data, name)
	m.mu.Unlock()
}

func (m *Memory) Snapshot() []cozyv1alpha1.ApplicationDefinition {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]cozyv1alpha1.ApplicationDefinition, 0, len(m.data))
	for _, v := range m.data {
		out = append(out, v)
	}
	return out
}

func (m *Memory) IsPrimed() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.primed
}

type runnable func(context.Context) error

func (r runnable) Start(ctx context.Context) error { return r(ctx) }

func (m *Memory) EnsurePrimingWithManager(mgr ctrl.Manager) error {
	var errOut error
	m.primeOnce.Do(func() {
		errOut = mgr.Add(runnable(func(ctx context.Context) error {
			if ok := mgr.GetCache().WaitForCacheSync(ctx); !ok {
				return nil
			}
			var list cozyv1alpha1.ApplicationDefinitionList
			if err := mgr.GetClient().List(ctx, &list); err == nil {
				for i := range list.Items {
					m.Upsert(&list.Items[i])
				}
				m.mu.Lock()
				m.primed = true
				m.mu.Unlock()
			}
			return nil
		}))
	})
	return errOut
}

func (m *Memory) ListFromCacheOrAPI(ctx context.Context, c client.Client) ([]cozyv1alpha1.ApplicationDefinition, error) {
	if m.IsPrimed() {
		return m.Snapshot(), nil
	}
	var list cozyv1alpha1.ApplicationDefinitionList
	if err := c.List(ctx, &list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

/*
Copyright 2026 The Cozystack Authors.

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
	"fmt"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllertest"
)

var vmiKind = schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachineInstance"}

// laterMapper maps a kind only from its served-th lookup on, as the
// controller's mapper does once KubeVirt's operator has installed the CRD.
type laterMapper struct {
	meta.RESTMapper
	served, calls int
}

func (m *laterMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	m.calls++
	if m.calls < m.served {
		return nil, &meta.NoKindMatchError{GroupKind: gk, SearchedVersions: versions}
	}
	return &meta.RESTMapping{GroupVersionKind: gk.WithVersion(versions[0])}, nil
}

func TestWaitForKindsWaitsForACRDInstalledLater(t *testing.T) {
	m := &laterMapper{served: 3}
	var waited []schema.GroupVersionKind
	err := WaitForKinds(context.Background(), m, time.Millisecond, func(k schema.GroupVersionKind, _ error) { waited = append(waited, k) }, vmiKind)
	if err != nil {
		t.Fatalf("WaitForKinds: %v", err)
	}
	if m.calls != 3 || len(waited) != 2 {
		t.Fatalf("looked up %d times and waited %d, want it to wait until the kind is mapped", m.calls, len(waited))
	}
}

func TestWaitForKindsStopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := WaitForKinds(ctx, &laterMapper{served: 1 << 30}, time.Millisecond, func(schema.GroupVersionKind, error) {}, vmiKind); err == nil {
		t.Fatal("want an error once the context ends")
	}
}

// Readiness gates webhook traffic and, through the packages that depend on
// cozystack-controller, the platform install, so it waits for the GPU
// informers that need no KubeVirt; the KubeVirt informers start in the
// background once KubeVirt serves their kinds.
func TestWarmupReadinessDoesNotWaitForKubeVirt(t *testing.T) {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	nodes := controllertest.NewFakeInformer()
	pods := controllertest.NewFakeInformer()
	informers := &informertest.FakeInformers{Scheme: s, InformersByGVK: map[schema.GroupVersionKind]toolscache.SharedIndexInformer{
		corev1.SchemeGroupVersion.WithKind("Node"): nodes,
		corev1.SchemeGroupVersion.WithKind("Pod"):  pods,
	}}
	mapper := &laterMapper{served: 1 << 30}
	w := &Warmup{
		Mapper: mapper, Informers: informers, Interval: time.Millisecond,
		OnWait: func(schema.GroupVersionKind, error) {}, Kinds: []schema.GroupVersionKind{vmiKind},
		Objects:         []client.Object{&corev1.Node{}},
		KubeVirtObjects: []client.Object{&corev1.Pod{}},
	}
	rec := &recordingInformers{FakeInformers: informers}
	w.Informers = rec
	if w.NeedLeaderElection() {
		t.Fatal("every replica serves the webhooks, so every replica warms up")
	}
	if w.Ready(nil) == nil {
		t.Fatal("ready before the informers have started")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- w.Start(ctx) }()
	deadline := time.Now().Add(time.Second)
	for w.synced.Load() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if w.synced.Load() == nil {
		t.Fatal("the informers did not start")
	}
	if w.Ready(nil) == nil {
		t.Fatal("ready before the informers have synced")
	}
	nodes.Synced()
	for w.Ready(nil) != nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := w.Ready(nil); err != nil {
		t.Fatalf("not ready while KubeVirt serves nothing, its other informers synced: %v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Start: %v", err)
	}
	if rec.requested("Pod") || mapper.calls == 0 {
		t.Fatal("a KubeVirt informer started before KubeVirt serves its kinds")
	}
}

type recordingInformers struct {
	*informertest.FakeInformers
	mu    sync.Mutex
	kinds []string
}

func (r *recordingInformers) GetInformer(ctx context.Context, obj client.Object, opts ...cache.InformerGetOption) (cache.Informer, error) {
	r.mu.Lock()
	r.kinds = append(r.kinds, fmt.Sprintf("%T", obj))
	r.mu.Unlock()
	return r.FakeInformers.GetInformer(ctx, obj, opts...)
}

func (r *recordingInformers) requested(kind string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range r.kinds {
		if k == "*v1."+kind {
			return true
		}
	}
	return false
}

func TestWarmupStartsTheKubeVirtInformersOnceTheirKindsAreServed(t *testing.T) {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	rec := &recordingInformers{FakeInformers: &informertest.FakeInformers{Scheme: s}}
	w := &Warmup{
		Mapper: &laterMapper{served: 3}, Informers: rec, Interval: time.Millisecond,
		OnWait: func(schema.GroupVersionKind, error) {}, Kinds: []schema.GroupVersionKind{vmiKind},
		Objects: []client.Object{&corev1.Node{}}, KubeVirtObjects: []client.Object{&corev1.Pod{}},
	}
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !rec.requested("Pod") {
		t.Fatal("the KubeVirt informers did not start once their kinds were served")
	}
}

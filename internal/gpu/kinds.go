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

package gpu

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// WaitForKinds blocks until mapper maps every kind, looking again every
// interval, and calls onWait for a kind it cannot map yet. An informer on a
// kind no CRD serves fails to start, and on a fresh install KubeVirt's
// operator can install its CRDs after cozystack-controller starts.
func WaitForKinds(ctx context.Context, mapper meta.RESTMapper, interval time.Duration,
	onWait func(schema.GroupVersionKind, error), kinds ...schema.GroupVersionKind) error {
	return wait.PollUntilContextCancel(ctx, interval, true, func(context.Context) (bool, error) {
		for _, k := range kinds {
			if _, err := mapper.RESTMapping(k.GroupKind(), k.Version); err != nil {
				onWait(k, err)
				return false, nil
			}
		}
		return true, nil
	})
}

// Warmup is a manager runnable that, on every replica, starts the
// informers the GPU webhooks read and serves Ready as a readiness check that
// fails until Objects have synced. Readiness gates webhook traffic and,
// through the packages that depend on cozystack-controller, the platform
// install, so it does not wait for KubeVirt: KubeVirtObjects start in the
// background once Kinds are served.
type Warmup struct {
	Mapper    meta.RESTMapper
	Informers interface {
		GetInformer(ctx context.Context, obj client.Object, opts ...cache.InformerGetOption) (cache.Informer, error)
	}
	Objects         []client.Object
	Kinds           []schema.GroupVersionKind
	KubeVirtObjects []client.Object
	Interval        time.Duration
	OnWait          func(schema.GroupVersionKind, error)

	synced atomic.Pointer[[]func() bool]
}

// NeedLeaderElection is false: every replica serves the webhooks.
func (w *Warmup) NeedLeaderElection() bool { return false }

// Start starts Objects' informers, then KubeVirtObjects' once Kinds are
// served.
func (w *Warmup) Start(ctx context.Context) error {
	synced := make([]func() bool, 0, len(w.Objects))
	for _, obj := range w.Objects {
		informer, err := w.Informers.GetInformer(ctx, obj, cache.BlockUntilSynced(false))
		if err != nil {
			return err
		}
		synced = append(synced, informer.HasSynced)
	}
	w.synced.Store(&synced)
	if err := WaitForKinds(ctx, w.Mapper, w.Interval, w.OnWait, w.Kinds...); err != nil {
		return nil
	}
	for _, obj := range w.KubeVirtObjects {
		if _, err := w.Informers.GetInformer(ctx, obj, cache.BlockUntilSynced(false)); err != nil {
			return err
		}
	}
	return nil
}

// Ready fails until Objects' informers have synced.
func (w *Warmup) Ready(*http.Request) error {
	synced := w.synced.Load()
	if synced == nil {
		return errors.New("the GPU informers have not started")
	}
	for _, hasSynced := range *synced {
		if !hasSynced() {
			return errors.New("the GPU caches have not synced")
		}
	}
	return nil
}

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

package reservation

import (
	"context"
	"fmt"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// InstancetypeGVR is the cluster-scoped KubeVirt instance type a VMInstance
// names in spec.instanceType.
var InstancetypeGVR = schema.GroupVersionResource{
	Group:    "instancetype.kubevirt.io",
	Version:  "v1beta1",
	Resource: "virtualmachineclusterinstancetypes",
}

const (
	defaultInstancetypeTTL = time.Minute
	// A name missing from the snapshot triggers a refetch, at most this
	// often, so that a type created a moment ago is found without letting a
	// stream of orders for a nonexistent type list the cluster on each one.
	instancetypeMissRefresh = 5 * time.Second
	instancetypeListTimeout = 10 * time.Second
	// A failed list is answered from memory for this long, so that callers
	// queued behind it do not each wait out their own list against an API
	// server that just failed one.
	instancetypeFailureHold = 5 * time.Second
)

type classified struct {
	list ResourceList
	err  error
}

// ClusterEnvironment resolves instance types from the cluster and presets
// from a fixed table.
//
// Instance types are listed in one call and kept for a minute, without an
// informer: the admission path only needs them when a declaration names one,
// and the set changes with a platform upgrade, not with tenant traffic. A
// type that does not classify fails only the applications naming it.
type ClusterEnvironment struct {
	dyn     dynamic.Interface
	presets map[string]ResourceList
	ttl     time.Duration
	now     func() time.Time

	mu      sync.Mutex
	fetched time.Time
	types   map[string]classified
	failed  time.Time
	failure error
}

var _ Environment = (*ClusterEnvironment)(nil)

// NewClusterEnvironment returns an Environment over the cluster's
// VirtualMachineClusterInstancetypes and the given preset table.
func NewClusterEnvironment(dyn dynamic.Interface, presets map[string]ResourceList) *ClusterEnvironment {
	return &ClusterEnvironment{
		dyn:     dyn,
		presets: presets,
		ttl:     defaultInstancetypeTTL,
		now:     time.Now,
	}
}

func (e *ClusterEnvironment) Instancetype(name string) (ResourceList, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	age := e.now().Sub(e.fetched)
	if e.types == nil || age >= e.ttl {
		if err := e.refresh(); err != nil {
			return nil, err
		}
	} else if _, ok := e.types[name]; !ok && age >= instancetypeMissRefresh {
		if err := e.refresh(); err != nil {
			return nil, err
		}
	}

	c, ok := e.types[name]
	if !ok {
		return nil, fmt.Errorf("instance type %q: %w", name, ErrNotFound)
	}
	return c.list, c.err
}

func (e *ClusterEnvironment) Preset(name string) (ResourceList, error) {
	rl, ok := e.presets[name]
	if !ok {
		return nil, fmt.Errorf("resource preset %q: %w", name, ErrNotFound)
	}
	return rl, nil
}

// refresh replaces the snapshot. It must be called with e.mu held. A failed
// list leaves the snapshot as it was, so an expired one stays unusable, and
// is returned again without listing until instancetypeFailureHold passes.
func (e *ClusterEnvironment) refresh() error {
	if e.failure != nil && e.now().Sub(e.failed) < instancetypeFailureHold {
		return e.failure
	}
	ctx, cancel := context.WithTimeout(context.Background(), instancetypeListTimeout)
	defer cancel()
	list, err := e.dyn.Resource(InstancetypeGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		e.failure = fmt.Errorf("list %s: %w: %w", InstancetypeGVR.GroupResource(), ErrUnavailable, err)
		e.failed = e.now()
		return e.failure
	}
	e.failure = nil
	types := make(map[string]classified, len(list.Items))
	for i := range list.Items {
		obj := list.Items[i]
		rl, err := ClassifyInstancetype(obj.Object)
		if err != nil {
			err = fmt.Errorf("instance type %q: %w", obj.GetName(), err)
		}
		types[obj.GetName()] = classified{list: rl, err: err}
	}
	e.types = types
	e.fetched = e.now()
	return nil
}

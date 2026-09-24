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

// Package registrytest holds test helpers shared by the aggregated storages.
package registrytest

import (
	"context"
	"reflect"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RawRecordingWatch records the raw options of each backing watch request, per
// list type, and passes the call through. The controller-runtime fake client
// ignores most raw options, so their effect cannot be read off the event stream.
type RawRecordingWatch struct {
	client.WithWatch

	mu  sync.Mutex
	raw map[reflect.Type]*metav1.ListOptions
}

func (s *RawRecordingWatch) Watch(ctx context.Context, list client.ObjectList, opts ...client.ListOption) (watch.Interface, error) {
	lo := &client.ListOptions{}
	lo.ApplyOptions(opts)
	s.mu.Lock()
	if s.raw == nil {
		s.raw = map[reflect.Type]*metav1.ListOptions{}
	}
	s.raw[reflect.TypeOf(list)] = lo.Raw
	s.mu.Unlock()
	return s.WithWatch.Watch(ctx, list, opts...)
}

// RawFor returns the raw options of the last watch on list's type, or nil.
func (s *RawRecordingWatch) RawFor(list client.ObjectList) *metav1.ListOptions {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.raw[reflect.TypeOf(list)]
}

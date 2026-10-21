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
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/event"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/cozystack/cozystack/internal/gpu"
)

// Setup wires the watches without reaching an API server, with the feature
// on and off.
func TestSetupBuildsTheControllerEnabledAndDisabled(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		mgr, err := ctrl.NewManager(&rest.Config{Host: "https://127.0.0.1:1"}, ctrl.Options{
			Scheme:     testScheme(enabled),
			Metrics:    metricsserver.Options{BindAddress: "0"},
			Controller: config.Controller{SkipNameValidation: ptr.To(true)},
		})
		if err != nil {
			t.Fatalf("manager: %v", err)
		}
		if err := Setup(mgr, gpu.Config{Enabled: enabled}); err != nil {
			t.Fatalf("Setup(enabled=%v): %v", enabled, err)
		}
	}
}

func TestPodEventsAreFilteredToExtendedResources(t *testing.T) {
	plain := launcherPod("web", "", "n1", map[string]int64{"cpu": 1})
	if requestsExtendedResource(plain) {
		t.Fatal("a pod with only cpu must not trigger a pass")
	}
	if !requestsExtendedResource(launcherPod("gpu", "", "n1", map[string]int64{rnL40S: 1})) {
		t.Fatal("a pod requesting a GPU resource name must trigger a pass")
	}
	if requestsExtendedResource(&corev1.Node{}) {
		t.Fatal("a non-pod object must not match")
	}
}

// Node layouts arrive with the host layer; until then no node has a
// pending layout change, and this hook must be replaced, not extended.
func TestLayoutPendingIsAHookThatNeverFires(t *testing.T) {
	if layoutPending("n1") {
		t.Fatal("no node can have a pending layout before the host layer ships")
	}
}

func TestNodeUpdatesThatCannotChangePlacementAreDropped(t *testing.T) {
	base := node("n1", map[string]int64{rnL40S: 2})
	heartbeat := base.DeepCopy()
	heartbeat.Status.Conditions[0].LastHeartbeatTime = metav1.NewTime(t0)
	if nodePlacementChanged.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: heartbeat}) {
		t.Fatal("a heartbeat must not trigger a pass")
	}
	for name, change := range map[string]func(*corev1.Node){
		"cordon":      cordoned,
		"taint":       tainted("dedicated", corev1.TaintEffectNoSchedule),
		"label":       labelled("pool", "a"),
		"not ready":   notReadySince(t0),
		"allocatable": func(n *corev1.Node) { n.Status.Allocatable[rnL40S] = resource.MustParse("1") },
	} {
		changed := base.DeepCopy()
		change(changed)
		if !nodePlacementChanged.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: changed}) {
			t.Errorf("%s must trigger a pass", name)
		}
	}
}

func TestLauncherPodsWithOnlyKubeVirtDevicesDoNotTriggerAPass(t *testing.T) {
	if requestsExtendedResource(launcherPod("virt-launcher-x", "x", "n1", map[string]int64{"devices.kubevirt.io/kvm": 1})) {
		t.Fatal("every VM's launcher pod would trigger a pass")
	}
}

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

type recordingWatcher struct {
	srcs []source.TypedSource[reconcile.Request]
}

func (w *recordingWatcher) Watch(src source.TypedSource[reconcile.Request]) error {
	w.srcs = append(w.srcs, src)
	return nil
}

// A watch on a kind no CRD serves fails the controller's start, so the
// KubeVirt watches join it only once KubeVirt serves them.
func TestKubeVirtWatchesWaitForTheirKinds(t *testing.T) {
	m := &laterMapper{served: 4}
	w := &recordingWatcher{}
	srcs := []source.TypedSource[reconcile.Request]{nil, nil, nil}
	if err := watchWhenServed(context.Background(), m, time.Millisecond, w, srcs); err != nil {
		t.Fatalf("watchWhenServed: %v", err)
	}
	if m.calls < 4 || len(w.srcs) != 3 {
		t.Fatalf("looked up %d times, added %d watches; want every watch once the kinds are served", m.calls, len(w.srcs))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	w = &recordingWatcher{}
	if err := watchWhenServed(ctx, &laterMapper{served: 1 << 30}, time.Millisecond, w, srcs); err != nil || len(w.srcs) != 0 {
		t.Fatalf("a manager stopped before KubeVirt serves its kinds: err %v, %d watches", err, len(w.srcs))
	}
}

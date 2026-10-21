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
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/event"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

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

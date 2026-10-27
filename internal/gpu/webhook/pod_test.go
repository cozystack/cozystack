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

package webhook

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
)

var (
	dedicated = gpuv1alpha1.GPUClassToleration{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "gpu", Effect: corev1.TaintEffectNoSchedule}
	evictLate = gpuv1alpha1.GPUClassToleration{Key: "maintenance", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: ptr.To[int64](300)}
	vgpuOnly  = gpuv1alpha1.GPUClassToleration{Key: "vgpu", Operator: corev1.TolerationOpExists}
)

// gateCluster is a VMI holding a passthrough claim and a vGPU template,
// whose classes carry tolerations.
func gateCluster(t *testing.T, extra ...client.Object) (*unstructured.Unstructured, []client.Object) {
	t.Helper()
	v := vmi(t, vmiName, held("train-0"), tmpl("l40s", 1))
	objs := []client.Object{
		class("h100", gpuv1alpha1.GPUModePassthrough, h100RN, tolerates(dedicated, evictLate)),
		class("l40s", gpuv1alpha1.GPUModeVGPU, l40sRN, tolerates(dedicated, vgpuOnly)),
		attached(claim("train-0", "h100", 1, gpuv1alpha1.GPUClaimBound, nodeA), vmiName),
		v,
	}
	return v, append(objs, extra...)
}

func admitPod(t *testing.T, h *Handler, p *corev1.Pod) (admission.Response, *corev1.Pod) {
	t.Helper()
	resp := h.GatePod(context.Background(), createRequest(t, p, false))
	out := &corev1.Pod{}
	if resp.Allowed {
		if err := json.Unmarshal(applyPatch(t, p, resp), out); err != nil {
			t.Fatal(err)
		}
	}
	return resp, out
}

func gates(p *corev1.Pod) []string {
	var out []string
	for _, g := range p.Spec.SchedulingGates {
		out = append(out, g.Name)
	}
	return out
}

func TestPodGetsTheGateAndTheClassTolerations(t *testing.T) {
	v, objs := gateCluster(t)
	h := newHandler(newClient(t, objs...))
	p := launcherPod("virt-launcher-trainer-x", v)
	p.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: "example.com/other"}}
	p.Spec.Tolerations = []corev1.Toleration{{Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: ptr.To[int64](300)}}

	resp, got := admitPod(t, h, p)
	mustAllow(t, resp)
	if g := gates(got); !reflect.DeepEqual(g, []string{"example.com/other", gpu.SchedulingGateClaims}) {
		t.Fatalf("gates = %v", g)
	}
	want := append([]corev1.Toleration{p.Spec.Tolerations[0]}, dedicated.Toleration(), evictLate.Toleration(), vgpuOnly.Toleration())
	if !reflect.DeepEqual(got.Spec.Tolerations, want) {
		t.Fatalf("tolerations = %+v, want %+v", got.Spec.Tolerations, want)
	}
}

// The API server can call the webhook again; a second gate of one name
// fails pod validation, and tolerations must not pile up.
func TestPodGateIsIdempotent(t *testing.T) {
	v, objs := gateCluster(t)
	h := newHandler(newClient(t, objs...))
	resp, once := admitPod(t, h, launcherPod("virt-launcher-trainer-x", v))
	mustAllow(t, resp)
	again, twice := admitPod(t, h, once)
	mustAllow(t, again)
	if len(again.Patches) != 0 || !reflect.DeepEqual(once.Spec, twice.Spec) {
		t.Fatalf("a repeated call changed the pod: %v", again.Patches)
	}
}

// KubeVirt binds WaitForFirstConsumer volumes with a temporary launcher
// render; left ungated, it could bind a local volume on another node.
func TestPodGatesTheTemporaryVolumePod(t *testing.T) {
	v, objs := gateCluster(t)
	p := launcherPod("virt-launcher-trainer-tmp", v)
	p.Annotations = map[string]string{"kubevirt.io/ephemeral-provisioning": "true"}
	resp, got := admitPod(t, newHandler(newClient(t, objs...)), p)
	mustAllow(t, resp)
	if g := gates(got); !reflect.DeepEqual(g, []string{gpu.SchedulingGateClaims}) {
		t.Fatalf("gates = %v", g)
	}
}

func TestPodLeftAlone(t *testing.T) {
	v, objs := gateCluster(t)
	for name, mutate := range map[string]func(*corev1.Pod){
		// KubeVirt deletes a target pod still pending after 15 minutes.
		"a migration target": func(p *corev1.Pod) { p.Labels[labelMigrationJob] = "migration-uid" },
		"a pod without the reference label": func(p *corev1.Pod) {
			delete(p.Labels, gpu.LabelClaimReference)
		},
		"a pod that is not a virt-launcher": func(p *corev1.Pod) { p.Labels[labelKubeVirt] = "hotplug-disk" },
	} {
		t.Run(name, func(t *testing.T) {
			p := launcherPod("virt-launcher-trainer-x", v)
			mutate(p)
			resp, _ := admitPod(t, newHandler(newClient(t, objs...)), p)
			mustAllow(t, resp)
			if len(resp.Patches) != 0 {
				t.Fatalf("patched: %v", resp.Patches)
			}
		})
	}
}

// The webhook fails closed: a launcher pod that reached the scheduler
// ungated would run unpinned.
func TestPodRefusals(t *testing.T) {
	v, objs := gateCluster(t)
	for name, tt := range map[string]struct {
		mutate func(*corev1.Pod)
		denied string
	}{
		"no VMI owner":                    {func(p *corev1.Pod) { p.OwnerReferences = nil }, "no VirtualMachineInstance owner"},
		"a VMI that is gone":              {func(p *corev1.Pod) { p.OwnerReferences[0].Name = "gone" }, "VirtualMachineInstance gone"},
		"an earlier VMI of the same name": {func(p *corev1.Pod) { p.OwnerReferences[0].UID = "old-uid" }, "not the owner"},
	} {
		t.Run(name, func(t *testing.T) {
			p := launcherPod("virt-launcher-trainer-x", v)
			tt.mutate(p)
			resp, _ := admitPod(t, newHandler(newClient(t, objs...)), p)
			mustDeny(t, resp, tt.denied)
		})
	}
	t.Run("a VMI whose references do not parse", func(t *testing.T) {
		bad := vmi(t, "bad", held("x"))
		bad.SetAnnotations(map[string]string{gpu.AnnotationClaims: "[]"})
		resp, _ := admitPod(t, newHandler(newClient(t, bad)), launcherPod("virt-launcher-bad-x", bad))
		mustDeny(t, resp, gpu.AnnotationClaims)
	})
}

// A class or held claim gone since admission adds no tolerations, but the
// pod is still gated: the controller does not release a set it cannot
// complete.
func TestPodGatedWithoutAMissingClass(t *testing.T) {
	v := vmi(t, vmiName, held("gone"), tmpl("nope", 1))
	resp, got := admitPod(t, newHandler(newClient(t, v)), launcherPod("virt-launcher-trainer-x", v))
	mustAllow(t, resp)
	if g := gates(got); !reflect.DeepEqual(g, []string{gpu.SchedulingGateClaims}) || len(got.Spec.Tolerations) != 0 {
		t.Fatalf("gates = %v, tolerations = %v", g, got.Spec.Tolerations)
	}
}

// The VMI webhook attaches a claim past the cache and virt-controller
// creates the pod right after, possibly on a replica whose cache has not
// seen the claim yet; the pod must still get its class's tolerations.
func TestPodReadsClaimsPastTheCache(t *testing.T) {
	v, objs := gateCluster(t)
	h := &Handler{Client: newClient(t, v), Reader: newClient(t, objs...)}
	resp, got := admitPod(t, h, launcherPod("virt-launcher-trainer-x", v))
	mustAllow(t, resp)
	want := []corev1.Toleration{dedicated.Toleration(), evictLate.Toleration(), vgpuOnly.Toleration()}
	if !reflect.DeepEqual(got.Spec.Tolerations, want) {
		t.Fatalf("tolerations = %+v, want %+v", got.Spec.Tolerations, want)
	}
}

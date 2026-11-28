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
	"encoding/json"
	"reflect"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
)

const vmiUID = types.UID("vmi-uid")

var (
	tolerateGPU  = cozyv1alpha1.GPUClassToleration{Key: "nvidia.com/gpu", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}
	tolerateMain = cozyv1alpha1.GPUClassToleration{Key: "example.com/maintenance", Operator: corev1.TolerationOpEqual, Value: "planned", Effect: corev1.TaintEffectNoExecute}
)

func storedVMI(annotation string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: vmi(annotation, nil)}
	u.SetUID(vmiUID)
	return u
}

func launcher(tolerations ...corev1.Toleration) map[string]any {
	p := corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "virt-launcher-vm1-",
			Namespace:    tenantNS,
			Labels:       map[string]string{"kubevirt.io": "virt-launcher", gpu.LabelGPUReference: "true"},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance", Name: "vm1", UID: vmiUID,
			}},
		},
		Spec: corev1.PodSpec{Tolerations: tolerations},
	}
	raw, _ := json.Marshal(p)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

func tolerationsOf(t *testing.T, obj map[string]any) []corev1.Toleration {
	t.Helper()
	raw, _ := json.Marshal(obj)
	var p corev1.Pod
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p.Spec.Tolerations
}

func TestMutateLauncherAddsClassTolerations(t *testing.T) {
	existing := corev1.Toleration{Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute}
	h := newHandler(t,
		storedVMI(`[{"className":"h100","count":2},{"className":"a100"},{"className":"h100"}]`),
		class("h100", passthrough, h100RN, tolerates(tolerateGPU, tolerateMain)),
		class("a100", passthrough, l40sRN, tolerates(tolerateGPU)),
	)
	pod := launcher(existing)
	resp := h.MutateLauncher(ctx, request(t, admissionv1.Create, pod))
	expectAllowed(t, resp)
	got := tolerationsOf(t, patched(t, pod, resp))
	want := []corev1.Toleration{existing, tolerateGPU.Toleration(), tolerateMain.Toleration()}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tolerations = %v, want %v", got, want)
	}

	resp = h.MutateLauncher(ctx, request(t, admissionv1.Create, patched(t, pod, resp)))
	expectAllowed(t, resp)
	if len(resp.Patches) != 0 {
		t.Errorf("reinvocation patched %v", resp.Patches)
	}
}

func TestMutateLauncherOnAPodWithoutTolerations(t *testing.T) {
	h := newHandler(t, storedVMI(`[{"className":"h100"}]`), class("h100", passthrough, h100RN, tolerates(tolerateGPU)))
	pod := launcher()
	got := tolerationsOf(t, patched(t, pod, h.MutateLauncher(ctx, request(t, admissionv1.Create, pod))))
	if !reflect.DeepEqual(got, []corev1.Toleration{tolerateGPU.Toleration()}) {
		t.Errorf("tolerations = %v", got)
	}
}

func TestMutateLauncherRefusesAVanishedClass(t *testing.T) {
	h := newHandler(t, storedVMI(`[{"className":"h100"}]`))
	resp := h.MutateLauncher(ctx, request(t, admissionv1.Create, launcher()))
	expectDenied(t, resp, "GPUClass h100 no longer exists")
	if len(resp.Patches) != 0 {
		t.Errorf("partial patch: %v", resp.Patches)
	}
}

func TestMutateLauncherRefuses(t *testing.T) {
	noOwner := launcher()
	delete(noOwner["metadata"].(map[string]any), "ownerReferences")
	otherUID := storedVMI(`[{"className":"h100"}]`)
	otherUID.SetUID("another")
	tests := []struct {
		name    string
		objects []client.Object
		pod     map[string]any
		want    string
	}{
		{name: "no VMI owner", pod: noOwner, want: "no VirtualMachineInstance owner"},
		{name: "VMI gone", pod: launcher(), want: "VirtualMachineInstance vm1 of the pod does not exist"},
		{name: "VMI recreated", objects: []client.Object{otherUID}, pod: launcher(), want: "is not the owner the pod names"},
		{name: "VMI without entries", objects: []client.Object{storedVMI("")}, pod: launcher(), want: "annotation cozystack.io/gpus"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHandler(t, tc.objects...)
			expectDenied(t, h.MutateLauncher(ctx, request(t, admissionv1.Create, tc.pod)), tc.want)
		})
	}
}

func TestMutateLauncherLeavesOtherPodsAlone(t *testing.T) {
	h := newHandler(t)
	notLauncher := launcher()
	notLauncher["metadata"].(map[string]any)["labels"] = map[string]any{gpu.LabelGPUReference: "true"}
	unlabelled := launcher()
	unlabelled["metadata"].(map[string]any)["labels"] = map[string]any{"kubevirt.io": "virt-launcher"}
	for name, pod := range map[string]map[string]any{"not a launcher": notLauncher, "no reference label": unlabelled} {
		t.Run(name, func(t *testing.T) {
			expectAllowed(t, h.MutateLauncher(ctx, request(t, admissionv1.Create, pod)))
		})
	}
	expectAllowed(t, h.MutateLauncher(ctx, request(t, admissionv1.Update, launcher())))
}

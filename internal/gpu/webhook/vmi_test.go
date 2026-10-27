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
	"reflect"
	"slices"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

const vmiName = "vm-instance-trainer"

// cluster is two nodes serving h100, one serving l40s and a40, and a
// tenant, entitled unless extra carries its namespace.
func cluster(t *testing.T, extra ...client.Object) []client.Object {
	t.Helper()
	objs := []client.Object{
		class("h100", gpuv1alpha1.GPUModePassthrough, h100RN, maxCount(4)),
		class("l40s", gpuv1alpha1.GPUModeVGPU, l40sRN, maxCount(2)),
		class("a40", gpuv1alpha1.GPUModeVGPU, a40RN, maxCount(2)),
		class("pt", gpuv1alpha1.GPUModePassthrough, h100RN),
		node(nodeA, map[string]int64{h100RN: 4}),
		node(nodeB, map[string]int64{h100RN: 2}),
		node("gpu-v", map[string]int64{l40sRN: 4}),
	}
	if !slices.ContainsFunc(extra, func(o client.Object) bool { _, ok := o.(*corev1.Namespace); return ok }) {
		objs = append(objs, namespace(tenantNS, true))
	}
	return append(objs, extra...)
}

func gpusOf(t *testing.T, u *unstructured.Unstructured) []map[string]any {
	t.Helper()
	list, _, err := unstructured.NestedSlice(u.Object, "spec", "domain", "devices", "gpus")
	if err != nil {
		t.Fatal(err)
	}
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		out = append(out, e.(map[string]any))
	}
	return out
}

func entry(name, rn string) map[string]any { return map[string]any{"name": name, "deviceName": rn} }

func admitVMI(t *testing.T, h *Handler, u *unstructured.Unstructured, dryRun bool) admission.Response {
	t.Helper()
	return h.MutateVMI(context.Background(), createRequest(t, u.Object, dryRun))
}

func setGPUs(entries ...map[string]any) func(*unstructured.Unstructured) {
	return func(u *unstructured.Unstructured) {
		list := make([]any, 0, len(entries))
		for _, e := range entries {
			list = append(list, e)
		}
		_ = unstructured.SetNestedSlice(u.Object, list, "spec", "domain", "devices", "gpus")
	}
}

func TestVMIAttachesHeldClaimsAndWritesOneEntryPerUnit(t *testing.T) {
	c := newClient(t, cluster(t, claim("train-0", "h100", 2, gpuv1alpha1.GPUClaimBound, nodeA))...)
	u := vmi(t, vmiName, held("train-0"), tmpl("h100", 1))

	resp := admitVMI(t, newHandler(c), u, false)
	mustAllow(t, resp)

	got := patchedVMI(t, u, resp)
	want := []map[string]any{entry("gpuclaim-0-0", h100RN), entry("gpuclaim-0-1", h100RN), entry("gpuclaim-1-0", h100RN)}
	if g := gpusOf(t, got); !reflect.DeepEqual(g, want) {
		t.Fatalf("gpus = %v, want %v", g, want)
	}
	d := getClaim(t, c, "train-0")
	if d.Status.AttachedTo == nil || d.Status.AttachedTo.VMI != vmiName {
		t.Fatalf("attachment = %v", d.Status.AttachedTo)
	}
	if d.Status.Phase != gpuv1alpha1.GPUClaimBound || d.Status.NodeName != nodeA {
		t.Fatalf("the webhook moved or rebound the claim: %+v", d.Status)
	}
	if n := len(listClaims(t, c)); n != 1 {
		t.Fatalf("%d claims exist, want 1: the controller creates template claims", n)
	}
}

func TestVMIRefusals(t *testing.T) {
	busy := attached(claim("busy", "h100", 1, gpuv1alpha1.GPUClaimAttached, nodeA), "another-vmi")
	dying := claim("dying", "h100", 1, gpuv1alpha1.GPUClaimBound, nodeA)
	deleting(dying)
	deletedClass := class("gone", gpuv1alpha1.GPUModePassthrough, h100RN)
	deleting(deletedClass)
	orphan := claim("orphan", "nope", 1, gpuv1alpha1.GPUClaimPending, "")
	templateName := claim(model.TemplateClaimName(vmiName, 0), "h100", 1, gpuv1alpha1.GPUClaimBound, nodeA)
	otherTemplate := claim(model.TemplateClaimName("other-vm", 0), "h100", 1, gpuv1alpha1.GPUClaimBound, nodeA)
	otherTemplate.Labels = map[string]string{gpu.LabelTemplateClaim: gpu.LabelValueTrue}
	falseMark := claim("false-mark", "h100", 1, gpuv1alpha1.GPUClaimBound, nodeA)
	falseMark.Labels = map[string]string{gpu.LabelTemplateClaim: "false"}

	tests := []struct {
		name   string
		extra  []client.Object
		refs   []model.ClaimRef
		vmi    func(*unstructured.Unstructured)
		denied []string
	}{
		{name: "a missing claim", extra: []client.Object{fine()}, refs: []model.ClaimRef{held("fine"), held("nope")}, denied: []string{"GPUClaim nope", "does not exist"}},
		{name: "a claim being deleted", extra: []client.Object{dying, fine()}, refs: []model.ClaimRef{held("fine"), held("dying")}, denied: []string{"GPUClaim dying", "being deleted"}},
		{name: "a claim attached to another VMI", extra: []client.Object{busy, fine()}, refs: []model.ClaimRef{held("fine"), held("busy")}, denied: []string{"GPUClaim busy", "node " + nodeA, "another-vmi"}},
		{name: "a claim of a missing class", extra: []client.Object{orphan}, refs: []model.ClaimRef{held("orphan")}, denied: []string{"GPUClaim orphan", "GPUClass nope", "does not exist"}},
		{
			name:   "a held reference to a template claim name of the same VMI",
			extra:  []client.Object{templateName},
			refs:   []model.ClaimRef{tmpl("h100", 1), held(templateName.Name)},
			denied: []string{"GPUClaim " + templateName.Name, "claimTemplate 0"},
		},
		{
			// Created and not yet attached to its own VMI by the controller.
			name:   "another VMI's template claim",
			extra:  []client.Object{fine(), otherTemplate},
			refs:   []model.ClaimRef{held("fine"), held(otherTemplate.Name)},
			denied: []string{"GPUClaim " + otherTemplate.Name, "created from a claimTemplate"},
		},
		{
			name:   "a template claim whose mark is not true",
			extra:  []client.Object{falseMark},
			refs:   []model.ClaimRef{held("false-mark")},
			denied: []string{"GPUClaim false-mark", "created from a claimTemplate"},
		},
		{
			name: "vGPU claims of two classes",
			extra: []client.Object{
				claim("v1", "l40s", 1, gpuv1alpha1.GPUClaimBound, "gpu-v"),
				claim("v2", "a40", 1, gpuv1alpha1.GPUClaimBound, "gpu-v"),
			},
			refs:   []model.ClaimRef{held("v1"), held("v2")},
			denied: []string{"vGPU claims of one class", "l40s", "a40"},
		},
		{
			name:   "a vGPU template next to a vGPU claim of another class",
			extra:  []client.Object{claim("v1", "l40s", 1, gpuv1alpha1.GPUClaimBound, "gpu-v")},
			refs:   []model.ClaimRef{held("v1"), tmpl("a40", 1)},
			denied: []string{"vGPU claims of one class", "claimTemplate 1 (a40)"},
		},
		{
			name: "held and template units above maxCount",
			extra: []client.Object{
				claim("v1", "l40s", 1, gpuv1alpha1.GPUClaimBound, "gpu-v"),
				claim("v2", "l40s", 1, gpuv1alpha1.GPUClaimBound, "gpu-v"),
			},
			refs:   []model.ClaimRef{held("v1"), held("v2"), tmpl("l40s", 1)},
			denied: []string{"3 GPUs of GPUClass l40s", "maxCount 2"},
		},
		{
			name:   "a passthrough template without maxCount larger than any node",
			refs:   []model.ClaimRef{tmpl("pt", 5)},
			denied: []string{"5 GPUs of GPUClass pt", "no node has more than 4"},
		},
		{
			name:   "a passthrough template without maxCount larger than any node with the held claims",
			extra:  []client.Object{claim("p1", "pt", 3, gpuv1alpha1.GPUClaimBound, nodeA)},
			refs:   []model.ClaimRef{held("p1"), tmpl("pt", 2)},
			denied: []string{"5 GPUs of GPUClass pt", "no node has more than 4"},
		},
		{name: "a template of a missing class", refs: []model.ClaimRef{tmpl("nope", 1)}, denied: []string{"claimTemplate 0", "GPUClass nope", "does not exist"}},
		{name: "a template of a deleted class", extra: []client.Object{deletedClass}, refs: []model.ClaimRef{tmpl("gone", 1)}, denied: []string{"claimTemplate 0", "GPUClass gone", "being deleted"}},
		{
			name:   "a vGPU template without the entitlement",
			extra:  []client.Object{namespace(tenantNS, false)},
			refs:   []model.ClaimRef{tmpl("l40s", 1)},
			denied: []string{"claimTemplate 0", "GPUClass l40s", "not entitled to vGPU"},
		},
		{
			name:   "a gpus entry of the VMI's own",
			extra:  []client.Object{fine()},
			refs:   []model.ClaimRef{held("fine")},
			vmi:    setGPUs(entry("gpu1", "nvidia.com/anything")),
			denied: []string{"GPU entries of its own", "gpus gpu1"},
		},
		{
			name:   "a gpus entry posing as the webhook's",
			extra:  []client.Object{fine()},
			refs:   []model.ClaimRef{held("fine")},
			vmi:    setGPUs(entry("gpuclaim-0-0", h100RN), entry("gpuclaim-0-1", h100RN)),
			denied: []string{"GPU entries of its own", "gpus gpuclaim-0-1"},
		},
		{
			name:   "an entry with the webhook's name and another resource",
			extra:  []client.Object{fine()},
			refs:   []model.ClaimRef{held("fine")},
			vmi:    setGPUs(entry("gpuclaim-0-0", l40sRN)),
			denied: []string{"GPU entries of its own", "gpus gpuclaim-0-0"},
		},
		{
			name:  "a GPU resource name in the VMI's resources",
			extra: []client.Object{fine()},
			refs:  []model.ClaimRef{held("fine")},
			vmi: func(u *unstructured.Unstructured) {
				_ = unstructured.SetNestedField(u.Object, "1", "spec", "domain", "resources", "limits", l40sRN)
			},
			denied: []string{"GPU entries of its own", "resources " + l40sRN},
		},
		{
			name:  "a host device with a GPU resource name",
			extra: []client.Object{fine()},
			refs:  []model.ClaimRef{held("fine")},
			vmi: func(u *unstructured.Unstructured) {
				_ = unstructured.SetNestedSlice(u.Object, []any{entry("hd", a40RN)}, "spec", "domain", "devices", "hostDevices")
			},
			denied: []string{"GPU entries of its own", "hostDevices hd"},
		},
		{
			name:  "a host device with a GPU name only the KubeVirt table carries",
			extra: []client.Object{kubeVirt(h100RN, "nvidia.com/GA102GL_A10"), fine()},
			refs:  []model.ClaimRef{held("fine")},
			vmi: func(u *unstructured.Unstructured) {
				_ = unstructured.SetNestedSlice(u.Object, []any{entry("hd", "nvidia.com/GA102GL_A10")}, "spec", "domain", "devices", "hostDevices")
			},
			denied: []string{"GPU entries of its own", "hostDevices hd"},
		},
		{
			name:  "a host device with a GPU name only the hoster's list carries",
			extra: []client.Object{fine()},
			refs:  []model.ClaimRef{held("fine")},
			vmi: func(u *unstructured.Unstructured) {
				_ = unstructured.SetNestedSlice(u.Object, []any{entry("hd", "example.com/gpu")}, "spec", "domain", "devices", "hostDevices")
			},
			denied: []string{"GPU entries of its own", "hostDevices hd"},
		},
		{
			name:   "a class outside the KubeVirt permittedHostDevices table",
			extra:  []client.Object{kubeVirt(l40sRN), fine()},
			refs:   []model.ClaimRef{held("fine")},
			denied: []string{"GPUClass h100", h100RN, "permittedHostDevices"},
		},
		{
			name:   "a VMI without a name",
			refs:   []model.ClaimRef{held("fine")},
			vmi:    func(u *unstructured.Unstructured) { u.SetName(""); u.SetGenerateName("vm-") },
			denied: []string{"needs a name"},
		},
		{
			name: "malformed references",
			vmi: func(u *unstructured.Unstructured) {
				u.SetAnnotations(map[string]string{gpu.AnnotationClaims: `[{"claimSelector":{}}]`})
			},
			refs:   []model.ClaimRef{held("fine")},
			denied: []string{gpu.AnnotationClaims},
		},
		{
			name: "a reference label without references",
			vmi: func(u *unstructured.Unstructured) {
				u.SetAnnotations(nil)
			},
			refs:   []model.ClaimRef{held("fine")},
			denied: []string{gpu.AnnotationClaims},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClient(t, cluster(t, tt.extra...)...)
			h := newHandler(c)
			h.Config.ResourceNames = []string{"example.com/gpu"}
			u := vmi(t, vmiName, tt.refs...)
			if tt.vmi != nil {
				tt.vmi(u)
			}
			mustDeny(t, admitVMI(t, h, u, false), tt.denied...)
			for _, d := range listClaims(t, c) {
				if a := d.Status.AttachedTo; a != nil && a.VMI == vmiName {
					t.Fatalf("a refused VMI attached GPUClaim %s", d.Name)
				}
			}
		})
	}
}

// fine is a held claim that passes every check. Listed before a claim
// that must be refused, it shows the refusal comes before any write.
func fine() *gpuv1alpha1.GPUClaim {
	return claim("fine", "h100", 1, gpuv1alpha1.GPUClaimBound, nodeA)
}

func TestVMIPasses(t *testing.T) {
	notReady := node("gpu-down", map[string]int64{h100RN: 4})
	notReady.Status.Conditions[0].Status = corev1.ConditionFalse
	cordoned := node("gpu-cordoned", map[string]int64{h100RN: 4})
	cordoned.Spec.Unschedulable = true

	tests := []struct {
		name  string
		extra []client.Object
		refs  []model.ClaimRef
		vmi   func(*unstructured.Unstructured)
	}{
		{name: "a Pending claim is bound by the controller", extra: []client.Object{claim("p", "h100", 1, gpuv1alpha1.GPUClaimPending, "")}, refs: []model.ClaimRef{held("p")}},
		{name: "a Lost claim is rebound by the controller", extra: []client.Object{claim("l", "h100", 1, gpuv1alpha1.GPUClaimLost, nodeA)}, refs: []model.ClaimRef{held("l")}},
		{name: "a claim on a node that is gone is moved by the controller", extra: []client.Object{claim("g", "h100", 1, gpuv1alpha1.GPUClaimBound, "gone")}, refs: []model.ClaimRef{held("g")}},
		{name: "a claim on a NotReady node", extra: []client.Object{notReady, claim("n", "h100", 1, gpuv1alpha1.GPUClaimBound, "gpu-down")}, refs: []model.ClaimRef{held("n")}},
		{name: "a claim on a cordoned node", extra: []client.Object{cordoned, claim("c", "h100", 1, gpuv1alpha1.GPUClaimBound, "gpu-cordoned")}, refs: []model.ClaimRef{held("c")}},
		{name: "a passthrough template as large as the largest node", refs: []model.ClaimRef{tmpl("pt", 4)}},
		{
			// Held claims were judged when they were ordered; a node that
			// shrank since is the controller's to handle.
			name:  "held passthrough claims without maxCount above the largest node",
			extra: []client.Object{claim("p1", "pt", 3, gpuv1alpha1.GPUClaimBound, nodeA), claim("p2", "pt", 2, gpuv1alpha1.GPUClaimBound, nodeA)},
			refs:  []model.ClaimRef{held("p1"), held("p2")},
		},
		{
			name: "held passthrough claims above the largest node next to a template of another class",
			extra: []client.Object{
				class("pt2", gpuv1alpha1.GPUModePassthrough, l40sRN),
				claim("p1", "pt", 3, gpuv1alpha1.GPUClaimBound, nodeA), claim("p2", "pt", 2, gpuv1alpha1.GPUClaimBound, nodeA),
			},
			refs: []model.ClaimRef{held("p1"), held("p2"), tmpl("pt2", 1)},
		},
		{
			// Budgets and capacity are the controller's: a template that
			// does not fit keeps the VM waiting at start.
			name:  "a template over the tenant budget and the class's free units",
			extra: []client.Object{class("full", gpuv1alpha1.GPUModePassthrough, h100RN, freeUnits(0, 0)), tenantRelease(t, tenantNS, map[string]string{"gpu.cozystack.io/total": "0"})},
			refs:  []model.ClaimRef{tmpl("full", 1)},
		},
		{name: "vGPU claims of one class up to maxCount", extra: []client.Object{claim("v1", "l40s", 1, gpuv1alpha1.GPUClaimBound, "gpu-v")}, refs: []model.ClaimRef{held("v1"), tmpl("l40s", 1)}},
		{name: "a vGPU claim next to a passthrough claim", extra: []client.Object{claim("v1", "l40s", 1, gpuv1alpha1.GPUClaimBound, "gpu-v"), fine()}, refs: []model.ClaimRef{held("v1"), held("fine")}},
		{
			name:  "a NIC host device next to a GPU claim",
			extra: []client.Object{fine()},
			refs:  []model.ClaimRef{held("fine")},
			vmi: func(u *unstructured.Unstructured) {
				_ = unstructured.SetNestedSlice(u.Object, []any{entry("rdma", "nvidia.com/hostdev")}, "spec", "domain", "devices", "hostDevices")
			},
		},
		{name: "a class inside the permittedHostDevices table", extra: []client.Object{kubeVirt(h100RN), fine()}, refs: []model.ClaimRef{held("fine")}},
		{name: "a KubeVirt CR without a permittedHostDevices table", extra: []client.Object{kubeVirt(), fine()}, refs: []model.ClaimRef{held("fine")}},
		{name: "a claim already attached to this VMI", extra: []client.Object{attached(fine(), vmiName)}, refs: []model.ClaimRef{held("fine")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClient(t, cluster(t, tt.extra...)...)
			u := vmi(t, vmiName, tt.refs...)
			if tt.vmi != nil {
				tt.vmi(u)
			}
			mustAllow(t, admitVMI(t, newHandler(c), u, false))
		})
	}
}

func TestVMIWithoutReferenceLabelIsLeftAlone(t *testing.T) {
	u := vmi(t, vmiName, held("nope"))
	u.SetLabels(nil)
	resp := admitVMI(t, newHandler(newClient(t)), u, false)
	mustAllow(t, resp)
	if len(resp.Patches) != 0 {
		t.Fatalf("patched a VMI without the reference label: %v", resp.Patches)
	}
}

// The API server calls the webhook again after a later mutator changes
// the VMI; the second call finds its entries and its attachment in place.
func TestVMIReinvocationIsIdempotent(t *testing.T) {
	c := newClient(t, cluster(t, claim("train-0", "h100", 2, gpuv1alpha1.GPUClaimBound, nodeA))...)
	h := newHandler(c)
	u := vmi(t, vmiName, held("train-0"), tmpl("h100", 1))
	resp := admitVMI(t, h, u, false)
	mustAllow(t, resp)
	again := admitVMI(t, h, patchedVMI(t, u, resp), false)
	mustAllow(t, again)
	if len(again.Patches) != 0 {
		t.Fatalf("reinvocation patched again: %v", again.Patches)
	}
}

func TestVMIDryRunWritesNothing(t *testing.T) {
	c := newClient(t, cluster(t, fine())...)
	resp := admitVMI(t, newHandler(c), vmi(t, vmiName, held("fine")), true)
	mustAllow(t, resp)
	if len(resp.Patches) == 0 {
		t.Fatal("a dry run still returns the gpus entries")
	}
	if a := getClaim(t, c, "fine").Status.AttachedTo; a != nil {
		t.Fatalf("a dry run attached the claim: %v", a)
	}
}

func TestVMIDryRunStillRefuses(t *testing.T) {
	busy := attached(claim("busy", "h100", 1, gpuv1alpha1.GPUClaimAttached, nodeA), "another-vmi")
	dying := claim("dying", "h100", 1, gpuv1alpha1.GPUClaimBound, nodeA)
	deleting(dying)
	h := newHandler(newClient(t, cluster(t, busy, dying)...))
	mustDeny(t, admitVMI(t, h, vmi(t, vmiName, held("nope")), true), "GPUClaim nope", "does not exist")
	mustDeny(t, admitVMI(t, h, vmi(t, vmiName, held("busy")), true), "GPUClaim busy", "another-vmi")
	mustDeny(t, admitVMI(t, h, vmi(t, vmiName, held("dying")), true), "GPUClaim dying", "being deleted")
}

// Two VMIs whose sets share a claim start at once; at most one of them
// attaches it, and the loser leaves nothing behind.
func TestVMIConcurrentStartsShareNoClaim(t *testing.T) {
	for range 20 {
		c := newClient(t, cluster(t,
			claim("d1", "h100", 1, gpuv1alpha1.GPUClaimBound, nodeA),
			claim("d2", "h100", 1, gpuv1alpha1.GPUClaimBound, nodeA),
			claim("d3", "h100", 1, gpuv1alpha1.GPUClaimBound, nodeA),
		)...)
		h := newHandler(c)
		vmis := map[string][]model.ClaimRef{"vmi-x": {held("d1"), held("d2")}, "vmi-y": {held("d2"), held("d3")}}
		results := map[string]admission.Response{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for name, refs := range vmis {
			wg.Go(func() {
				r := admitVMI(t, h, vmi(t, name, refs...), false)
				mu.Lock()
				results[name] = r
				mu.Unlock()
			})
		}
		wg.Wait()
		if results["vmi-x"].Allowed && results["vmi-y"].Allowed {
			t.Fatal("both VMIs admitted with a shared claim")
		}
		winner := ""
		for name, r := range results {
			if r.Allowed {
				winner = name
			}
		}
		for _, d := range listClaims(t, c) {
			if a := d.Status.AttachedTo; a != nil && a.VMI != winner {
				t.Fatalf("refused VMI %s kept claim %s", a.VMI, d.Name)
			}
		}
	}
}

// A claim another VMI takes between the check and the write refuses the
// VMI and clears the attachments this call already wrote, but not one an
// earlier call made for the same VMI.
func TestVMILostRaceClearsOnlyItsOwnAttachments(t *testing.T) {
	base := newClient(t, cluster(t,
		attached(claim("earlier", "h100", 1, gpuv1alpha1.GPUClaimBound, nodeA), vmiName),
		claim("mine", "h100", 1, gpuv1alpha1.GPUClaimBound, nodeA),
		claim("raced", "h100", 1, gpuv1alpha1.GPUClaimBound, nodeA),
	)...)
	raced := false
	c := interceptor.NewClient(base, interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if obj.GetName() == "raced" && !raced {
				raced = true
				other := getClaim(t, base, "raced")
				attached(other, "other-vmi")
				if err := base.Status().Update(ctx, other); err != nil {
					t.Fatal(err)
				}
				return apierrors.NewConflict(schema.GroupResource{Group: gpu.Group, Resource: "gpuclaims"}, "raced", nil)
			}
			return cl.SubResource(sub).Update(ctx, obj, opts...)
		},
	})
	mustDeny(t, admitVMI(t, newHandler(c), vmi(t, vmiName, held("earlier"), held("mine"), held("raced")), false), "GPUClaim raced", "other-vmi")
	if a := getClaim(t, base, "mine").Status.AttachedTo; a != nil {
		t.Fatalf("the attachment this call wrote stays: %v", a)
	}
	if a := getClaim(t, base, "earlier").Status.AttachedTo; a == nil || a.VMI != vmiName {
		t.Fatalf("the earlier attachment was cleared: %v", a)
	}
}

// Clearing the tenant's vGPU flag keeps what it holds and refuses the
// next template claim.
func TestVMIClearedEntitlement(t *testing.T) {
	c := newClient(t, cluster(t, namespace(tenantNS, false), claim("v1", "l40s", 1, gpuv1alpha1.GPUClaimBound, "gpu-v"))...)
	h := newHandler(c)
	mustAllow(t, admitVMI(t, h, vmi(t, "held-only", held("v1")), false))
	mustDeny(t, admitVMI(t, h, vmi(t, "with-template", tmpl("l40s", 1)), false), "GPUClass l40s", "not entitled to vGPU")
}

// Every replica serves the webhook while only the leader binds, so what a
// refusal is judged by is read past the cache.
func TestVMIJudgesPastTheCache(t *testing.T) {
	ownEntry := func(u *unstructured.Unstructured) {
		_ = unstructured.SetNestedSlice(u.Object, []any{entry("hd", "example.com/new")}, "spec", "domain", "devices", "hostDevices")
	}
	agentNode := &gpuv1alpha1.GPUNode{ObjectMeta: metav1.ObjectMeta{Name: nodeA}, Status: gpuv1alpha1.GPUNodeStatus{ResourceNames: []string{"example.com/new"}}}
	for name, tt := range map[string]struct {
		cache, live []client.Object
		refs        []model.ClaimRef
		vmi         func(*unstructured.Unstructured)
		denied      string
	}{
		"a permittedHostDevices entry the cache still shows": {
			cache:  cluster(t, kubeVirt(h100RN), fine()),
			live:   cluster(t, kubeVirt(l40sRN), fine()),
			refs:   []model.ClaimRef{held("fine")},
			denied: "permittedHostDevices",
		},
		"a GPU resource name a node agent has just reported": {
			cache:  cluster(t, fine()),
			live:   cluster(t, fine(), agentNode),
			refs:   []model.ClaimRef{held("fine")},
			vmi:    ownEntry,
			denied: "hostDevices hd",
		},
		"an entitlement the cache still shows": {
			cache:  cluster(t),
			live:   cluster(t, namespace(tenantNS, false)),
			refs:   []model.ClaimRef{tmpl("l40s", 1)},
			denied: "not entitled to vGPU",
		},
		"a class the cache does not show deleted": {
			cache: cluster(t, class("gone", gpuv1alpha1.GPUModePassthrough, h100RN)),
			live: cluster(t, func() client.Object {
				c := class("gone", gpuv1alpha1.GPUModePassthrough, h100RN)
				deleting(c)
				return c
			}()),
			refs:   []model.ClaimRef{tmpl("gone", 1)},
			denied: "being deleted",
		},
		"a node the cache still shows larger": {
			cache:  cluster(t, node("big", map[string]int64{h100RN: 8})),
			live:   cluster(t),
			refs:   []model.ClaimRef{tmpl("pt", 8)},
			denied: "no node has more than 4",
		},
	} {
		t.Run(name, func(t *testing.T) {
			cached := newClient(t, tt.cache...)
			h := &Handler{Client: cached, Reader: newClient(t, tt.live...)}
			u := vmi(t, vmiName, tt.refs...)
			if tt.vmi != nil {
				tt.vmi(u)
			}
			mustDeny(t, admitVMI(t, h, u, false), tt.denied)
		})
	}
}

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
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

// member is one reference of a VMI: a held claim, or a claimTemplate
// entry whose claim the controller creates once the pod waits at the gate.
type member struct {
	// index is the reference's position; the gpus entries it gets are
	// named after it.
	index int
	class *gpuv1alpha1.GPUClass
	count int32
	// claim is the held claim, nil for a template.
	claim *gpuv1alpha1.GPUClaim
}

func (m *member) String() string {
	if m.claim != nil {
		return "GPUClaim " + m.claim.Name
	}
	return fmt.Sprintf("claimTemplate %d", m.index)
}

// MutateVMI resolves a VMI's claim references, refuses what section 3 of
// the GPUClaim design refuses without placing anything, records the
// attachment of each held claim and adds a gpus entry per GPU unit with
// the class's resource name. internal/gpu/doc.go carries the contract
// with the controller, which places the claims while the launcher pod
// waits at the scheduling gate.
func (h *Handler) MutateVMI(ctx context.Context, req admission.Request) admission.Response {
	if req.Operation != admissionv1.Create {
		return admission.Allowed("")
	}
	u := &unstructured.Unstructured{}
	if err := json.Unmarshal(req.Object.Raw, &u.Object); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if u.GetLabels()[gpu.LabelClaimReference] != gpu.LabelValueTrue {
		return admission.Allowed("")
	}
	if u.GetName() == "" {
		return admission.Denied("a VMI with claim references needs a name; generateName is not supported")
	}
	refs, err := model.ParseClaimRefs(u.GetAnnotations()[gpu.AnnotationClaims])
	if err != nil {
		return admission.Denied(fmt.Sprintf("annotation %s: %v", gpu.AnnotationClaims, err))
	}

	set, refusal, err := h.check(ctx, req.Namespace, u, refs)
	switch {
	case err != nil:
		return admission.Errored(http.StatusInternalServerError, err)
	case refusal != "":
		return admission.Denied(refusal)
	}
	if req.DryRun == nil || !*req.DryRun {
		refusal, err := h.attach(ctx, u.GetName(), set)
		switch {
		case err != nil:
			return admission.Errored(http.StatusInternalServerError, err)
		case refusal != "":
			return admission.Denied(refusal)
		}
	}
	if op := gpusPatch(u, desiredGPUs(set)); op != nil {
		return admission.Patched("", *op)
	}
	return admission.Allowed("")
}

// check resolves the references and runs every refusal check. It writes
// nothing.
func (h *Handler) check(ctx context.Context, ns string, u *unstructured.Unstructured, refs []model.ClaimRef) ([]*member, string, error) {
	classes, err := h.classes(ctx)
	if err != nil {
		return nil, "", err
	}
	entitled, err := h.entitled(ctx, ns)
	if err != nil {
		return nil, "", err
	}
	templateNames := map[string]int{}
	for i, r := range refs {
		if r.ClaimTemplate != nil {
			templateNames[model.TemplateClaimName(u.GetName(), i)] = i
		}
	}
	var set []*member
	var problems []string
	for i, r := range refs {
		var m *member
		var problem string
		if r.Claim != "" {
			if j, ok := templateNames[r.Claim]; ok {
				problems = append(problems, fmt.Sprintf("GPUClaim %s is the claim of claimTemplate %d and cannot be referenced as well", r.Claim, j))
				continue
			}
			m, problem, err = h.resolveHeld(ctx, ns, u.GetName(), i, r.Claim, classes)
		} else {
			m, problem = resolveTemplate(i, r.ClaimTemplate, classes, entitled)
		}
		if err != nil {
			return nil, "", err
		}
		if problem != "" {
			problems = append(problems, problem)
			continue
		}
		set = append(set, m)
	}
	if len(problems) > 0 {
		return nil, strings.Join(problems, "; "), nil
	}

	permitted, tableGPUs, err := h.permitted(ctx)
	if err != nil {
		return nil, "", err
	}
	if problem := setProblem(set, permitted); problem != "" {
		return nil, problem, nil
	}
	if problem, err := h.unitsFitANode(ctx, set); err != nil || problem != "" {
		return nil, problem, err
	}
	names, err := h.gpuNames(ctx, classes, tableGPUs)
	if err != nil {
		return nil, "", err
	}
	if own := ownGPUEntries(u, desiredGPUs(set), names); len(own) > 0 {
		return nil, "the VMI carries GPU entries of its own next to claim references: " + strings.Join(own, ", "), nil
	}
	return set, "", nil
}

func ident(d *gpuv1alpha1.GPUClaim) string {
	if d.Status.NodeName != "" {
		return fmt.Sprintf("GPUClaim %s (node %s)", d.Name, d.Status.NodeName)
	}
	return "GPUClaim " + d.Name
}

// resolveHeld reads a held claim past the cache, so a claim another VMI
// attached a moment ago is not taken for free. A Pending or Lost claim,
// or one on a node that cannot serve it, is not refused: the controller
// binds or moves it before it releases the pod.
func (h *Handler) resolveHeld(ctx context.Context, ns, vmiName string, index int, name string,
	classes map[string]*gpuv1alpha1.GPUClass) (*member, string, error) {
	d := &gpuv1alpha1.GPUClaim{}
	err := h.Reader.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, d)
	if apierrors.IsNotFound(err) {
		return nil, fmt.Sprintf("GPUClaim %s does not exist", name), nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("get GPUClaim %s: %w", name, err)
	}
	c := classes[d.Spec.ClassName]
	switch a := d.Status.AttachedTo; {
	case !d.DeletionTimestamp.IsZero():
		return nil, ident(d) + " is being deleted", nil
	case a != nil && a.VMI != vmiName:
		return nil, fmt.Sprintf("%s is attached to VMI %s", ident(d), a.VMI), nil
	case hasTemplateMark(d):
		// The controller attaches a template claim to its VMI only after it
		// creates it, so a claim entry could otherwise take it in between.
		return nil, fmt.Sprintf("%s was created from a claimTemplate and is taken only through it", ident(d)), nil
	case c == nil:
		return nil, fmt.Sprintf("%s: GPUClass %s does not exist", ident(d), d.Spec.ClassName), nil
	}
	return &member{index: index, class: c, count: d.Spec.Count, claim: d}, "", nil
}

// resolveTemplate checks a claimTemplate entry against its class and the
// vGPU entitlement. Budgets and capacity are the controller's when it
// creates the claim, so an entry that does not fit keeps the VM waiting
// at start instead of being refused here.
func resolveTemplate(index int, t *model.ClaimTemplate, classes map[string]*gpuv1alpha1.GPUClass, entitled bool) (*member, string) {
	c := classes[t.ClassName]
	switch {
	case c == nil:
		return nil, fmt.Sprintf("claimTemplate %d: GPUClass %s does not exist", index, t.ClassName)
	case !c.DeletionTimestamp.IsZero():
		return nil, fmt.Sprintf("claimTemplate %d: GPUClass %s is being deleted and takes no new orders", index, t.ClassName)
	case c.Spec.Mode == gpuv1alpha1.GPUModeVGPU && !entitled:
		return nil, fmt.Sprintf("claimTemplate %d: cannot order GPUClass %s: the tenant is not entitled to vGPU", index, t.ClassName)
	}
	return &member{index: index, class: c, count: t.Count}, ""
}

// setProblem refuses a set a VM may not take as a whole.
func setProblem(set []*member, permitted sets.Set[string]) string {
	vgpu := map[string][]string{}
	units := map[string]int64{}
	for _, m := range set {
		if m.class.Spec.Mode == gpuv1alpha1.GPUModeVGPU {
			vgpu[m.class.Name] = append(vgpu[m.class.Name], m.String())
		}
		units[m.class.Name] += int64(m.count)
	}
	if len(vgpu) > 1 {
		var parts []string
		for c, members := range vgpu {
			parts = append(parts, fmt.Sprintf("%s (%s)", strings.Join(members, ", "), c))
		}
		sort.Strings(parts)
		return "a VMI takes vGPU claims of one class only: " + strings.Join(parts, "; ")
	}
	for _, m := range set {
		if limit, ok := model.EffectiveMaxCount(&m.class.Spec); ok && units[m.class.Name] > int64(limit) {
			return fmt.Sprintf("%d GPUs of GPUClass %s exceed its maxCount %d per VM", units[m.class.Name], m.class.Name, limit)
		}
		if rn := resourceName(m.class); permitted != nil && !permitted.Has(rn) {
			return fmt.Sprintf("GPUClass %s: resource name %s is not in the KubeVirt permittedHostDevices table, so the VM could not start",
				m.class.Name, rn)
		}
	}
	return ""
}

// unitsFitANode applies maxCount where a passthrough class leaves it
// unset, which means the GPUs of one node: the set's units of such a class
// with a template entry must not exceed the most any node advertises. It
// also bounds the gpus entries desiredGPUs builds from a template's count.
// A set of held claims only is not judged, since its claims were judged
// when they were ordered.
func (h *Handler) unitsFitANode(ctx context.Context, set []*member) (string, error) {
	units := map[string]int64{}
	templated := sets.New[string]()
	for _, m := range set {
		if _, ok := model.EffectiveMaxCount(&m.class.Spec); ok {
			continue
		}
		units[m.class.Name] += int64(m.count)
		if m.claim == nil {
			templated.Insert(m.class.Name)
		}
	}
	if templated.Len() == 0 {
		return "", nil
	}
	nodes := &corev1.NodeList{}
	if err := h.Reader.List(ctx, nodes); err != nil {
		return "", fmt.Errorf("list nodes: %w", err)
	}
	for _, m := range set {
		if !templated.Has(m.class.Name) {
			continue
		}
		rn := corev1.ResourceName(resourceName(m.class))
		var most int64
		for i := range nodes.Items {
			q := nodes.Items[i].Status.Allocatable[rn]
			most = max(most, q.Value())
		}
		if units[m.class.Name] > most {
			return fmt.Sprintf("%d GPUs of GPUClass %s exceed its maxCount, the GPUs of one node: no node has more than %d",
				units[m.class.Name], m.class.Name, most), nil
		}
	}
	return "", nil
}

// desiredGPUs is one gpus entry per GPU unit of the set. The names carry
// the reference index, so a reinvocation finds exactly the entries it
// added.
func desiredGPUs(set []*member) []any {
	var out []any
	for _, m := range set {
		for j := int32(0); j < m.count; j++ {
			out = append(out, map[string]any{
				"name":       fmt.Sprintf("%s%d-%d", gpu.VMIGPUNamePrefix, m.index, j),
				"deviceName": resourceName(m.class),
			})
		}
	}
	return out
}

// ownGPUEntries lists the VMI's GPU entries that this webhook did not add:
// any gpus entry, a hostDevices entry or a spec.domain.resources key with a
// GPU resource name. Entries from an instance type are on the VMI already.
func ownGPUEntries(u *unstructured.Unstructured, desired []any, names sets.Set[string]) []string {
	ours := map[string]any{}
	for _, e := range desired {
		m := e.(map[string]any)
		ours[m["name"].(string)] = m["deviceName"]
	}
	var out []string
	gpus, _, _ := unstructured.NestedSlice(u.Object, "spec", "domain", "devices", "gpus")
	for _, e := range gpus {
		m, _ := e.(map[string]any)
		name, _ := m["name"].(string)
		if want, ok := ours[name]; ok && m["deviceName"] == want {
			continue
		}
		out = append(out, "gpus "+name)
	}
	hostDevices, _, _ := unstructured.NestedSlice(u.Object, "spec", "domain", "devices", "hostDevices")
	for _, e := range hostDevices {
		m, _ := e.(map[string]any)
		if dn, _ := m["deviceName"].(string); names.Has(dn) {
			name, _ := m["name"].(string)
			out = append(out, "hostDevices "+name)
		}
	}
	for _, kind := range []string{"requests", "limits"} {
		rl, _, _ := unstructured.NestedMap(u.Object, "spec", "domain", "resources", kind)
		keys := make([]string, 0, len(rl))
		for k := range rl {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if names.Has(k) {
				out = append(out, "resources "+k)
			}
		}
	}
	return out
}

// gpusPatch sets the VMI's gpus to the set's entries, or returns nil when
// a reinvocation finds them in place. Every other gpus entry was refused.
func gpusPatch(u *unstructured.Unstructured, desired []any) *jsonpatch.Operation {
	current, _, _ := unstructured.NestedSlice(u.Object, "spec", "domain", "devices", "gpus")
	if len(current) == len(desired) {
		return nil
	}
	if _, ok, _ := unstructured.NestedMap(u.Object, "spec", "domain", "devices"); !ok {
		op := jsonpatch.NewOperation("add", "/spec/domain/devices", map[string]any{"gpus": desired})
		return &op
	}
	op := jsonpatch.NewOperation("add", "/spec/domain/devices/gpus", desired)
	return &op
}

// errTaken is a claim another VMI attached between the check and the write.
type errTaken struct{ msg string }

func (e *errTaken) Error() string { return e.msg }

// attach records the attachment of every held claim of the set, with
// optimistic concurrency, so no other VMI and no background move can take
// one. On a refusal it clears the attachments this call wrote; what it
// cannot clear, the controller does after Config.AttachmentGrace.
func (h *Handler) attach(ctx context.Context, vmiName string, set []*member) (string, error) {
	var written []*gpuv1alpha1.GPUClaim
	for _, m := range set {
		if m.claim == nil {
			continue
		}
		wrote, err := h.attachOne(ctx, m.claim, vmiName)
		if wrote {
			written = append(written, m.claim)
		}
		if err == nil {
			continue
		}
		h.detach(ctx, vmiName, written)
		var taken *errTaken
		if errors.As(err, &taken) {
			return taken.msg, nil
		}
		return "", err
	}
	return "", nil
}

// attachOne reports whether it wrote the attachment: one already there
// for this VMI, from an earlier call, is left as it is.
func (h *Handler) attachOne(ctx context.Context, d *gpuv1alpha1.GPUClaim, vmiName string) (bool, error) {
	wrote := false
	first := true
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if !first {
			if err := h.Reader.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
				return err
			}
		}
		first = false
		switch a := d.Status.AttachedTo; {
		case !d.DeletionTimestamp.IsZero():
			return &errTaken{msg: ident(d) + " is being deleted"}
		case a != nil && a.VMI != vmiName:
			return &errTaken{msg: fmt.Sprintf("%s is attached to VMI %s", ident(d), a.VMI)}
		case a != nil:
			return nil
		}
		d.Status.AttachedTo = &gpuv1alpha1.GPUClaimAttachment{VMI: vmiName}
		if err := h.Client.Status().Update(ctx, d); err != nil {
			d.Status.AttachedTo = nil
			return err
		}
		wrote = true
		return nil
	})
	var taken *errTaken
	switch {
	case apierrors.IsNotFound(err):
		return wrote, &errTaken{msg: fmt.Sprintf("GPUClaim %s no longer exists", d.Name)}
	case err != nil && !errors.As(err, &taken):
		return wrote, fmt.Errorf("attach GPUClaim %s: %w", d.Name, err)
	}
	return wrote, err
}

func (h *Handler) detach(ctx context.Context, vmiName string, claims []*gpuv1alpha1.GPUClaim) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for _, w := range claims {
		d := w.DeepCopy()
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := h.Reader.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
				return err
			}
			if a := d.Status.AttachedTo; a == nil || a.VMI != vmiName {
				return nil
			}
			d.Status.AttachedTo = nil
			return h.Client.Status().Update(ctx, d)
		})
		if client.IgnoreNotFound(err) != nil {
			logf.FromContext(ctx).Error(err, "clearing the attachment of a refused VMI", "namespace", d.Namespace, "claim", d.Name, "vmi", vmiName)
		}
	}
}

func hasTemplateMark(d *gpuv1alpha1.GPUClaim) bool {
	_, ok := d.Labels[gpu.LabelTemplateClaim]
	return ok
}

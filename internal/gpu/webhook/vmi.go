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
	"slices"
	"sort"
	"strings"
	"time"

	"gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

// requestReuse is how old a Succeeded bind request for this VMI's name may
// be and still count as this create's own answer: the API server's default
// request timeout, which the API server's reinvocation of this webhook
// falls within. An older one was left by an earlier VMI of the same name,
// whose placement a gather may have to change.
const requestReuse = time.Minute

// member is one device of a VMI's set.
type member struct {
	device *gpuv1alpha1.GPUDevice
	class  *gpuv1alpha1.GPUClass
	// index is the device's position in the VMI's references; the gpus
	// entries it gets are named after it.
	index    int
	template bool
	// create is set for a template device that does not exist yet.
	create bool
}

// MutateVMI resolves a VMI's device references: it refuses what §3 of the
// GPUDevice design refuses, creates the template devices, attaches the
// whole set, has the binder place it on one node, and adds a gpus entry
// per GPU with the class's resource name. internal/gpu/doc.go carries the
// contract with the binder.
func (h *Handler) MutateVMI(ctx context.Context, req admission.Request) admission.Response {
	if req.Operation != admissionv1.Create {
		return admission.Allowed("")
	}
	u := &unstructured.Unstructured{}
	if err := json.Unmarshal(req.Object.Raw, &u.Object); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if u.GetLabels()[gpu.LabelDeviceReference] != gpu.LabelValueTrue {
		return admission.Allowed("")
	}
	if u.GetName() == "" {
		return admission.Denied("a VMI with device references needs a name; generateName is not supported")
	}
	refs, err := model.ParseDeviceRefs(u.GetAnnotations()[gpu.AnnotationDevices])
	if err != nil {
		return admission.Denied(fmt.Sprintf("annotation %s: %v", gpu.AnnotationDevices, err))
	}

	set, gpus, refusal, err := h.plan(ctx, req.Namespace, u, refs)
	switch {
	case err != nil:
		return admission.Errored(http.StatusInternalServerError, err)
	case refusal != "":
		return admission.Denied(refusal)
	}
	if req.DryRun == nil || !*req.DryRun {
		refusal, err := h.bind(ctx, req.Namespace, u.GetName(), set)
		switch {
		case err != nil:
			return admission.Errored(http.StatusInternalServerError, err)
		case refusal != "":
			return admission.Denied(refusal)
		}
	}
	if op := gpusPatch(u, gpus); op != nil {
		return admission.Patched("", *op)
	}
	return admission.Allowed("")
}

// plan runs every refusal check and returns the set and the gpus entries
// the VMI gets. It writes nothing.
func (h *Handler) plan(ctx context.Context, ns string, u *unstructured.Unstructured, refs []model.DeviceRef) ([]*member, []any, string, error) {
	classes, err := h.classes(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	all := &gpuv1alpha1.GPUDeviceList{}
	if err := h.Client.List(ctx, all); err != nil {
		return nil, nil, "", fmt.Errorf("list GPUDevices: %w", err)
	}
	var set []*member
	var problems []string
	for i, r := range refs {
		var m *member
		var problem string
		if r.Device != "" {
			m, problem, err = h.resolveHeld(ctx, ns, u.GetName(), i, r.Device, classes, all.Items)
		} else {
			m, problem, err = h.resolveTemplate(ctx, ns, u.GetName(), i, r.DeviceTemplate, classes)
		}
		if err != nil {
			return nil, nil, "", err
		}
		if problem != "" {
			problems = append(problems, problem)
			continue
		}
		set = append(set, m)
	}
	if len(problems) > 0 {
		return nil, nil, strings.Join(problems, "; "), nil
	}
	// The references can name one device twice: a template device's name
	// is predictable, so a held reference can name it as well.
	seen := sets.New[string]()
	for _, m := range set {
		if seen.Has(m.device.Name) {
			return nil, nil, fmt.Sprintf("GPUDevice %s is referenced twice", m.device.Name), nil
		}
		seen.Insert(m.device.Name)
	}

	permitted, tableGPUs, err := h.permitted(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	if problem := setProblem(u, set, permitted); problem != "" {
		return nil, nil, problem, nil
	}

	if problem, err := h.templatesFitANode(ctx, set); err != nil || problem != "" {
		return nil, nil, problem, err
	}
	gpus := desiredGPUs(set)
	names, err := h.gpuNames(ctx, classes, tableGPUs)
	if err != nil {
		return nil, nil, "", err
	}
	if own := ownGPUEntries(u, gpus, names); len(own) > 0 {
		return nil, nil, "the VMI carries GPU entries of its own next to device references: " + strings.Join(own, ", "), nil
	}

	problem, err := h.budgets(ctx, ns, set, classes)
	if err != nil || problem != "" {
		return nil, nil, problem, err
	}
	return set, gpus, "", nil
}

func ident(d *gpuv1alpha1.GPUDevice) string {
	if model.HoldsGPUs(d.Status.Phase) || d.Status.Phase == gpuv1alpha1.GPUDeviceLost {
		return fmt.Sprintf("GPUDevice %s (node %s)", d.Name, d.Status.NodeName)
	}
	return "GPUDevice " + d.Name
}

func (h *Handler) resolveHeld(ctx context.Context, ns, vmiName string, index int, name string,
	classes map[string]*gpuv1alpha1.GPUClass, all []gpuv1alpha1.GPUDevice) (*member, string, error) {
	d := &gpuv1alpha1.GPUDevice{}
	err := h.Reader.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, d)
	if apierrors.IsNotFound(err) {
		return nil, fmt.Sprintf("GPUDevice %s does not exist", name), nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("get GPUDevice %s: %w", name, err)
	}
	c := classes[d.Spec.ClassName]
	switch a := d.Status.AttachedTo; {
	case !d.DeletionTimestamp.IsZero():
		return nil, ident(d) + " is being deleted", nil
	case a != nil && a.VMI != vmiName:
		return nil, fmt.Sprintf("%s is attached to VMI %s", ident(d), a.VMI), nil
	case !model.HoldsGPUs(d.Status.Phase):
		return nil, fmt.Sprintf("%s is %s", ident(d), phaseOf(d)), nil
	case c == nil:
		return nil, fmt.Sprintf("%s: GPUClass %s does not exist", ident(d), d.Spec.ClassName), nil
	}
	problem, err := h.nodeProblem(ctx, d, c, classes, all)
	if err != nil || problem != "" {
		return nil, problem, err
	}
	return &member{device: d, class: c, index: index}, "", nil
}

// nodeProblem refuses a device whose node cannot serve it at all: gone,
// no longer serving the class, or lost capacity the device does not fit
// under. A cordoned, tainted or NotReady node is left to the binder, which
// gathers the set elsewhere.
func (h *Handler) nodeProblem(ctx context.Context, d *gpuv1alpha1.GPUDevice, c *gpuv1alpha1.GPUClass,
	classes map[string]*gpuv1alpha1.GPUClass, all []gpuv1alpha1.GPUDevice) (string, error) {
	n := &corev1.Node{}
	err := h.Client.Get(ctx, client.ObjectKey{Name: d.Status.NodeName}, n)
	if apierrors.IsNotFound(err) {
		return fmt.Sprintf("GPUDevice %s: node %s is gone", d.Name, d.Status.NodeName), nil
	}
	if err != nil {
		return "", fmt.Errorf("get node %s: %w", d.Status.NodeName, err)
	}
	snap := &model.Snapshot{Classes: map[string]model.Class{}, Nodes: []model.Node{model.NodeFrom(n, nil)}}
	for name, cl := range classes {
		snap.Classes[name] = model.ClassFrom(cl)
	}
	if !labels.SelectorFromSet(c.Spec.NodeSelector).Matches(labels.Set(n.Labels)) || slices.Contains(snap.Conflicts(n.Name), c.Name) {
		return fmt.Sprintf("GPUDevice %s: node %s no longer serves GPUClass %s", d.Name, n.Name, c.Name), nil
	}

	gn := &gpuv1alpha1.GPUNode{}
	err = h.Client.Get(ctx, client.ObjectKey{Name: n.Name}, gn)
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get GPUNode %s: %w", n.Name, err)
	}
	if !meta.IsStatusConditionTrue(gn.Status.Conditions, gpu.ConditionLostCapacity) {
		return "", nil
	}
	lost := fmt.Sprintf("GPUDevice %s: node %s has lost capacity", d.Name, n.Name)
	if !model.Ready(n) {
		return lost, nil
	}
	rn := corev1.ResourceName(resourceName(c))
	var held int64
	for i := range all {
		o := &all[i]
		if o.Status.NodeName == n.Name && model.HoldsGPUs(o.Status.Phase) && classes[o.Spec.ClassName] != nil &&
			corev1.ResourceName(resourceName(classes[o.Spec.ClassName])) == rn {
			held += int64(o.Spec.Count)
		}
	}
	if held > model.ServedUnits(n, gn.Status.Devices, rn) {
		return lost, nil
	}
	return "", nil
}

func (h *Handler) resolveTemplate(ctx context.Context, ns, vmiName string, index int, t *model.DeviceTemplate,
	classes map[string]*gpuv1alpha1.GPUClass) (*member, string, error) {
	name := model.TemplateDeviceName(vmiName, index)
	c := classes[t.ClassName]
	d := &gpuv1alpha1.GPUDevice{}
	err := h.Reader.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, d)
	create := apierrors.IsNotFound(err)
	switch {
	case create:
		d = &gpuv1alpha1.GPUDevice{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns,
				Labels: map[string]string{gpu.LabelTemplateDevice: gpu.LabelValueTrue}},
			Spec: gpuv1alpha1.GPUDeviceSpec{ClassName: t.ClassName, Count: t.Count},
		}
	case err != nil:
		return nil, "", fmt.Errorf("get GPUDevice %s: %w", name, err)
	case d.Labels[gpu.LabelTemplateDevice] != gpu.LabelValueTrue:
		return nil, fmt.Sprintf("GPUDevice %s exists and was not created from a deviceTemplate", name), nil
	case !d.DeletionTimestamp.IsZero():
		return nil, fmt.Sprintf("GPUDevice %s of an earlier VMI is still being deleted", name), nil
	case vmiOwner(d) != "":
		return nil, fmt.Sprintf("GPUDevice %s is still owned by an earlier VMI %s", name, vmiName), nil
	case d.Spec.ClassName != t.ClassName || d.Spec.Count != t.Count:
		return nil, fmt.Sprintf("GPUDevice %s (GPUClass %s, count %d) does not match deviceTemplate %d (GPUClass %s, count %d)",
			name, d.Spec.ClassName, d.Spec.Count, index, t.ClassName, t.Count), nil
	case d.Status.AttachedTo != nil && d.Status.AttachedTo.VMI != vmiName:
		return nil, fmt.Sprintf("GPUDevice %s is attached to VMI %s", name, d.Status.AttachedTo.VMI), nil
	}
	switch {
	case c == nil:
		return nil, fmt.Sprintf("deviceTemplate %d: GPUClass %s does not exist", index, t.ClassName), nil
	case !c.DeletionTimestamp.IsZero() && !model.HoldsGPUs(d.Status.Phase):
		return nil, fmt.Sprintf("deviceTemplate %d: GPUClass %s is being deleted and takes no new orders", index, t.ClassName), nil
	}
	return &member{device: d, class: c, index: index, template: true, create: create}, "", nil
}

// setProblem refuses a set a VM may not take as a whole.
func setProblem(u *unstructured.Unstructured, set []*member, permitted sets.Set[string]) string {
	if mode, ok := u.GetLabels()[labelMode]; ok {
		for _, m := range set {
			if string(m.class.Spec.Mode) != mode {
				return fmt.Sprintf("GPUDevice %s: GPUClass %s has mode %s, and the VMI's pool takes mode %s",
					m.device.Name, m.class.Name, m.class.Spec.Mode, mode)
			}
		}
	}
	vgpu := map[string][]string{}
	units := map[string]int64{}
	for _, m := range set {
		if m.class.Spec.Mode == gpuv1alpha1.GPUModeVGPU {
			vgpu[m.class.Name] = append(vgpu[m.class.Name], m.device.Name)
		}
		units[m.class.Name] += int64(m.device.Spec.Count)
	}
	if len(vgpu) > 1 {
		var parts []string
		for c, devices := range vgpu {
			parts = append(parts, fmt.Sprintf("%s (%s)", strings.Join(devices, ", "), c))
		}
		sort.Strings(parts)
		return "a VMI takes vGPU devices of one class only: " + strings.Join(parts, "; ")
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

// templatesFitANode refuses a template device larger than any node serves
// before desiredGPUs builds an entry per unit: a class without maxCount
// puts no other cap on the count. A held device is Bound, so it fits.
func (h *Handler) templatesFitANode(ctx context.Context, set []*member) (string, error) {
	var nodes *corev1.NodeList
	for _, m := range set {
		if !m.template || model.HoldsGPUs(m.device.Status.Phase) {
			continue
		}
		if nodes == nil {
			nodes = &corev1.NodeList{}
			if err := h.Client.List(ctx, nodes); err != nil {
				return "", fmt.Errorf("list nodes: %w", err)
			}
		}
		rn := corev1.ResourceName(resourceName(m.class))
		var most int64
		for i := range nodes.Items {
			q := nodes.Items[i].Status.Allocatable[rn]
			most = max(most, q.Value())
		}
		if int64(m.device.Spec.Count) > most {
			return fmt.Sprintf("deviceTemplate %d: no node has %d units of %s; the most one has is %d", m.index, m.device.Spec.Count, rn, most), nil
		}
	}
	return "", nil
}

// desiredGPUs is one gpus entry per GPU of the set. The names carry the
// reference index, so a reinvocation finds exactly the entries it added.
func desiredGPUs(set []*member) []any {
	var out []any
	for _, m := range set {
		for j := int32(0); j < m.device.Spec.Count; j++ {
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

// budgets checks the template devices that do not count against budgets
// yet as orders, each counting the ones before it. Held devices are not checked again: a
// cleared entitlement or a lowered budget keeps what a tenant holds.
func (h *Handler) budgets(ctx context.Context, ns string, set []*member, classes map[string]*gpuv1alpha1.GPUClass) (string, error) {
	var orders []*member
	for _, m := range set {
		if m.template && !model.CountsAgainstBudget(m.device.Status.Phase) {
			orders = append(orders, m)
		}
	}
	if len(orders) == 0 {
		return "", nil
	}
	snap, err := h.snapshot(ctx, ns, classes)
	if err != nil {
		return "", err
	}
	entitled, err := h.entitled(ctx, ns)
	if err != nil {
		return "", err
	}
	for _, m := range orders {
		d := m.device
		if err := snap.CheckOrder(ns, d.Spec.ClassName, int64(d.Spec.Count), entitled); err != nil {
			return fmt.Sprintf("deviceTemplate %d: %v", m.index, err), nil
		}
		admitted := model.DeviceFrom(d)
		admitted.Phase = gpuv1alpha1.GPUDeviceBound
		snap.Devices = append(snap.Devices, admitted)
	}
	return "", nil
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

// errTaken is a device another VMI attached between the check and the write.
type errTaken struct{ msg string }

func (e *errTaken) Error() string { return e.msg }

// write records what this call wrote to one device, for cleanup.
type write struct {
	device *gpuv1alpha1.GPUDevice
	// attached is set when this call wrote the attachment.
	attached bool
}

// bind creates the template devices, attaches the set, asks the binder to
// place it and waits for the answer. It returns a refusal message, having
// cleared what it wrote, or "" when the set is placed on one node.
func (h *Handler) bind(ctx context.Context, ns, vmiName string, set []*member) (string, error) {
	var created []*gpuv1alpha1.GPUDevice
	var written []write
	refuse := func(msg string, err error) (string, error) {
		h.cleanup(ctx, vmiName, written, created)
		return msg, err
	}
	for _, m := range set {
		if !m.create {
			continue
		}
		if err := h.Client.Create(ctx, m.device); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return refuse(fmt.Sprintf("GPUDevice %s was created by another admission at the same time", m.device.Name), nil)
			}
			return refuse("", fmt.Errorf("create GPUDevice %s: %w", m.device.Name, err))
		}
		created = append(created, m.device)
	}

	names := make([]string, 0, len(set))
	for _, m := range set {
		names = append(names, m.device.Name)
	}
	if answered(set, vmiName, names) {
		return "", nil
	}

	req := gpuv1alpha1.BindRequest{ID: string(uuid.NewUUID()), VMI: vmiName, Devices: names, RequestedAt: metav1.Now()}
	for _, m := range set {
		w, err := h.request(ctx, m.device, vmiName, req)
		if w.device != nil {
			written = append(written, w)
		}
		var taken *errTaken
		if errors.As(err, &taken) {
			return refuse(taken.msg, nil)
		}
		if err != nil {
			return refuse("", err)
		}
	}

	results, err := h.wait(ctx, set, req.ID)
	if err != nil {
		return refuse(fmt.Sprintf("the GPU binder did not answer within %s; KubeVirt retries the start", h.BindTimeout), nil)
	}
	nodes := sets.New[string]()
	for _, d := range results {
		if r := d.Status.BindRequest.Result; r != gpuv1alpha1.BindSucceeded {
			return refuse(d.Status.BindRequest.Message, nil)
		}
		if !model.HoldsGPUs(d.Status.Phase) {
			return refuse(fmt.Sprintf("the GPU binder answered, but %s holds no node", ident(d)), nil)
		}
		nodes.Insert(d.Status.NodeName)
	}
	if nodes.Len() != 1 {
		return refuse(fmt.Sprintf("the GPU binder placed the set on more than one node: %s", strings.Join(sets.List(nodes), ", ")), nil)
	}
	return "", nil
}

// answered reports a set that a recent request for this VMI has already
// placed on one node, as a reinvocation of the same create finds it.
func answered(set []*member, vmiName string, names []string) bool {
	node := ""
	for _, m := range set {
		d := m.device
		r := d.Status.BindRequest
		if d.Status.AttachedTo == nil || d.Status.AttachedTo.VMI != vmiName || r == nil || r.VMI != vmiName ||
			r.Result != gpuv1alpha1.BindSucceeded || time.Since(r.RequestedAt.Time) >= requestReuse ||
			!sets.New(r.Devices...).Equal(sets.New(names...)) || !model.HoldsGPUs(d.Status.Phase) {
			return false
		}
		if node != "" && d.Status.NodeName != node {
			return false
		}
		node = d.Status.NodeName
	}
	return true
}

// request attaches one device to the VMI and writes the bind request, with
// optimistic concurrency, so no other VMI can take the device meanwhile.
func (h *Handler) request(ctx context.Context, d *gpuv1alpha1.GPUDevice, vmiName string, req gpuv1alpha1.BindRequest) (write, error) {
	var w write
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
		}
		attaching := d.Status.AttachedTo == nil
		d.Status.AttachedTo = &gpuv1alpha1.GPUDeviceAttachment{VMI: vmiName}
		d.Status.BindRequest = req.DeepCopy()
		if err := h.Client.Status().Update(ctx, d); err != nil {
			return err
		}
		w = write{device: d, attached: attaching}
		return nil
	})
	var taken *errTaken
	switch {
	case apierrors.IsNotFound(err):
		return w, &errTaken{msg: fmt.Sprintf("GPUDevice %s no longer exists", d.Name)}
	case err != nil && !errors.As(err, &taken):
		return w, fmt.Errorf("attach GPUDevice %s: %w", d.Name, err)
	}
	return w, err
}

// wait polls the cache until the binder has answered the request on every
// device of the set.
func (h *Handler) wait(ctx context.Context, set []*member, id string) ([]*gpuv1alpha1.GPUDevice, error) {
	var out []*gpuv1alpha1.GPUDevice
	err := wait.PollUntilContextTimeout(ctx, h.PollInterval, h.BindTimeout, true, func(ctx context.Context) (bool, error) {
		out = out[:0]
		for _, m := range set {
			d := &gpuv1alpha1.GPUDevice{}
			if err := h.Client.Get(ctx, client.ObjectKeyFromObject(m.device), d); err != nil {
				return false, nil
			}
			if r := d.Status.BindRequest; r == nil || r.ID != id || r.Result == "" {
				return false, nil
			}
			out = append(out, d)
		}
		return true, nil
	})
	return out, err
}

// cleanup undoes this call's writes: its bind request and the attachments
// it made, and the template devices it created. What it cannot undo, the
// controller clears after the attachment grace period.
func (h *Handler) cleanup(ctx context.Context, vmiName string, written []write, created []*gpuv1alpha1.GPUDevice) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	log := logf.FromContext(ctx)
	isCreated := func(d *gpuv1alpha1.GPUDevice) bool {
		return slices.ContainsFunc(created, func(c *gpuv1alpha1.GPUDevice) bool { return c.Name == d.Name })
	}
	for _, w := range written {
		if isCreated(w.device) {
			continue
		}
		id := w.device.Status.BindRequest.ID
		d := w.device.DeepCopy()
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := h.Reader.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
				return err
			}
			changed := false
			if r := d.Status.BindRequest; r != nil && r.ID == id {
				d.Status.BindRequest = nil
				changed = true
			}
			if a := d.Status.AttachedTo; w.attached && a != nil && a.VMI == vmiName {
				d.Status.AttachedTo = nil
				changed = true
			}
			if !changed {
				return nil
			}
			return h.Client.Status().Update(ctx, d)
		})
		if client.IgnoreNotFound(err) != nil {
			log.Error(err, "clearing the attachment of a refused VMI", "namespace", d.Namespace, "device", d.Name, "vmi", vmiName)
		}
	}
	for _, d := range created {
		err := h.Client.Delete(ctx, d, client.Preconditions{UID: &d.UID})
		if client.IgnoreNotFound(err) != nil {
			log.Error(err, "deleting the template device of a refused VMI", "namespace", d.Namespace, "device", d.Name, "vmi", vmiName)
		}
	}
}

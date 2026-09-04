// SPDX-License-Identifier: Apache-2.0

package migrationcontroller

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	migrationv1alpha1 "github.com/cozystack/cozystack/api/migration/v1alpha1"
)

// taskOwnership returns the labels and owner reference every piece of a task's
// Forklift scaffolding carries. The labels let the controller watch Migrations
// and map them back without trusting a label an unrelated object might set; the
// owner reference is what makes deleting the task take the scaffolding with it.
func taskOwnership(task *migrationv1alpha1.VMImportTask) (map[string]string, metav1.OwnerReference) {
	return map[string]string{
			migrationv1alpha1.ManagedByLabel:           migrationv1alpha1.ManagedByValue,
			migrationv1alpha1.OwningTaskNameLabel:      task.Name,
			migrationv1alpha1.OwningTaskNamespaceLabel: task.Namespace,
		}, ownerRef(
			migrationv1alpha1.GroupVersion.WithKind("VMImportTask"), task.Name, task.UID)
}

// outputMarkers are the labels every object a task produces carries: which
// task made it, and for which source VM. Labels rather than owner references,
// because outputs are meant to outlive the task that created them.
func outputMarkers(task *migrationv1alpha1.VMImportTask, vmID string) map[string]string {
	return map[string]string{
		migrationv1alpha1.OutputTaskUIDLabel: string(task.UID),
		migrationv1alpha1.OutputVMIDLabel:    sanitizeName(vmID),
	}
}

// stampOutput writes the markers onto an object being created, preserving any
// labels already set on it.
func stampOutput(obj metav1.Object, task *migrationv1alpha1.VMImportTask, vmID string) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	for k, v := range outputMarkers(task, vmID) {
		labels[k] = v
	}
	obj.SetLabels(labels)
}

// isOwnOutput reports whether an existing object is this task's own earlier
// output for this source VM, rather than something that merely shares its name.
// Absence of the marker is the answer that matters: it means a tenant object is
// standing where an output would go, which is a collision and never a resume.
func isOwnOutput(obj metav1.Object, task *migrationv1alpha1.VMImportTask, vmID string) bool {
	labels := obj.GetLabels()
	if labels == nil {
		return false
	}
	return labels[migrationv1alpha1.OutputTaskUIDLabel] == string(task.UID) &&
		labels[migrationv1alpha1.OutputVMIDLabel] == sanitizeName(vmID)
}

// providerRefs is the source/destination pair every Forklift object in a task
// points at. Both live in the task's namespace, because a Source does.
func providerRefs(task *migrationv1alpha1.VMImportTask, src *migrationv1alpha1.VMImportSource) map[string]interface{} {
	return map[string]interface{}{
		"source": map[string]interface{}{
			"name":      sourceProviderName(src.Name),
			"namespace": task.Namespace,
		},
		"destination": map[string]interface{}{
			"name":      destinationProviderName(src.Name),
			"namespace": task.Namespace,
		},
	}
}

// ensureMaps creates the NetworkMap and StorageMap a Plan requires. Both start
// empty — Forklift will not validate a Plan without them — and populateMaps
// fills them in from what the inventory says each VM actually uses, one VM at a
// time as its reconcile reaches this point.
func (r *VMImportTaskReconciler) ensureMaps(
	ctx context.Context,
	task *migrationv1alpha1.VMImportTask,
	src *migrationv1alpha1.VMImportSource,
	storageClass string,
) error {
	labels, owner := taskOwnership(task)
	name := mapName(task.Name)

	for _, gvk := range []struct {
		gvk schema.GroupVersionKind
	}{{networkMapGVK}, {storageMapGVK}} {
		existing := newObject(gvk.gvk)
		err := r.Get(ctx, types.NamespacedName{Namespace: task.Namespace, Name: name}, existing)
		if err == nil {
			continue
		}
		if !apierrors.IsNotFound(err) {
			return err
		}
		obj := newObject(gvk.gvk)
		obj.SetName(name)
		obj.SetNamespace(task.Namespace)
		obj.SetLabels(labels)
		obj.SetOwnerReferences([]metav1.OwnerReference{owner})
		spec := map[string]interface{}{
			"provider": providerRefs(task, src),
			"map":      []interface{}{},
		}
		if err := unstructured.SetNestedMap(obj.Object, spec, "spec"); err != nil {
			return err
		}
		if err := r.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	}

	// The storage class is not part of the map until a datastore is known, but
	// it must be recorded somewhere the next pass can read it: the map's own
	// annotation is the natural place, and it keeps the class immutable for the
	// task's lifetime even if the cluster default changes mid-import.
	sm := newObject(storageMapGVK)
	if err := r.Get(ctx, types.NamespacedName{Namespace: task.Namespace, Name: name}, sm); err != nil {
		return err
	}
	annotations := sm.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	if annotations[storageClassAnnotation] == storageClass {
		return nil
	}
	annotations[storageClassAnnotation] = storageClass
	sm.SetAnnotations(annotations)
	return r.Update(ctx, sm)
}

// storageClassAnnotation records the resolved StorageClass on the task's
// StorageMap, so mappings learned later all land on the same class.
const storageClassAnnotation = "forklift.cozystack.io/storage-class"

// forkliftPowerStateOff is Forklift's `off` target power state
// (plan.TargetPowerStateOff upstream). Spelled out rather than imported: the
// controller deliberately builds Forklift objects as unstructured data and
// takes no compile-time dependency on the Forklift API module.
const forkliftPowerStateOff = "off"

// createPlan renders the Forklift Plan that migrates one VM.
//
// Raw copy (skipGuestConversion) is the only mode v1 offers, and it is the one
// that needs no privileged pod anywhere: Forklift emits a CDI DataVolume with a
// VDDK source and CDI's own importer — which satisfies the restricted Pod
// Security Standard — moves the bytes. Guest conversion needs a node-level
// seccomp profile and arrives as a separate, additive option.
func (r *VMImportTaskReconciler) createPlan(
	ctx context.Context,
	task *migrationv1alpha1.VMImportTask,
	src *migrationv1alpha1.VMImportSource,
	req *migrationv1alpha1.VMImportRequest,
) error {
	labels, owner := taskOwnership(task)
	obj := newObject(planGVK)
	obj.SetName(planName(task.Name, req.ID))
	obj.SetNamespace(task.Namespace)
	obj.SetLabels(labels)
	obj.SetOwnerReferences([]metav1.OwnerReference{owner})

	spec := map[string]interface{}{
		"skipGuestConversion": true,
		"provider":            providerRefs(task, src),
		"map": map[string]interface{}{
			"network": map[string]interface{}{
				"name":      mapName(task.Name),
				"namespace": task.Namespace,
			},
			"storage": map[string]interface{}{
				"name":      mapName(task.Name),
				"namespace": task.Namespace,
			},
		},
		// The destination is always the task's own namespace. There is no
		// field for anything else: an import lands where the object that
		// asked for it lives, which removes the whole class of
		// cross-namespace coercion bugs.
		"targetNamespace": task.Namespace,
		"vms": []interface{}{
			map[string]interface{}{
				"id": req.ID,
				// Keep the machine Forklift builds powered off. Left unset,
				// Forklift matches the source's pre-migration power state, so
				// migrating a running VM — every VM in a production cutover —
				// yields a target that boots the moment the transfer finishes.
				// That is a duplicate of a live machine on the network, and it
				// starts a guest on the very volume the handoff is about to
				// re-point. The VMInstance this controller builds is what
				// starts the imported guest, on the tenant's terms.
				"targetPowerState": forkliftPowerStateOff,
			},
		},
	}
	if err := unstructured.SetNestedMap(obj.Object, spec, "spec"); err != nil {
		return err
	}
	if err := r.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// ensureMigration creates the Migration that runs a validated Plan. A Plan
// describes the work; a Migration is the instruction to do it.
func (r *VMImportTaskReconciler) ensureMigration(
	ctx context.Context,
	task *migrationv1alpha1.VMImportTask,
	plan string,
) (*unstructured.Unstructured, error) {
	existing := newObject(migrationGVK)
	err := r.Get(ctx, types.NamespacedName{Namespace: task.Namespace, Name: plan}, existing)
	if err == nil {
		return existing, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}

	labels, owner := taskOwnership(task)
	obj := newObject(migrationGVK)
	obj.SetName(plan)
	obj.SetNamespace(task.Namespace)
	obj.SetLabels(labels)
	obj.SetOwnerReferences([]metav1.OwnerReference{owner})
	spec := map[string]interface{}{
		"plan": map[string]interface{}{
			"name":      plan,
			"namespace": task.Namespace,
		},
	}
	if err := unstructured.SetNestedMap(obj.Object, spec, "spec"); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, err
	}
	// Return the object as created; its status appears on a later pass.
	return obj, nil
}

// populateMaps adds one VM's networks and datastores to the task's maps.
//
// The topology comes from Forklift's inventory, because it exists nowhere else:
// a Plan's validation conditions name the VMs that are unmapped, never what
// they are unmapped to. The maps accumulate across the task's VMs — each pass
// adds only what is missing — so the task ends up with one entry per distinct
// network and datastore however many machines share them.
//
// Networks all map to the pod network. That is not a placeholder: the map
// shapes the interfaces of the KubeVirt VM Forklift creates, and this
// controller discards that VM unstarted — the imported machine's networking
// comes from the VMInstance it builds instead, which attaches to the pod
// network. When a richer network placement design lands, this is where a
// tenant-specified mapping would be honoured.
//
// Returns true when it changed something, so the caller waits for Forklift to
// re-validate rather than racing ahead.
func (r *VMImportTaskReconciler) populateMaps(
	ctx context.Context,
	task *migrationv1alpha1.VMImportTask,
	src *migrationv1alpha1.VMImportSource,
	req *migrationv1alpha1.VMImportRequest,
) (bool, error) {
	networks, datastores, err := r.sourceTopology(ctx, task, src, req)
	if err != nil {
		return false, err
	}
	if len(networks) == 0 && len(datastores) == 0 {
		return false, nil
	}

	name := mapName(task.Name)
	changed := false

	if len(networks) > 0 {
		nm := newObject(networkMapGVK)
		if err := r.Get(ctx, types.NamespacedName{Namespace: task.Namespace, Name: name}, nm); err != nil {
			return false, err
		}
		entries, _, _ := unstructured.NestedSlice(nm.Object, "spec", "map")
		added := false
		for _, id := range networks {
			if hasSourceID(entries, id) {
				continue
			}
			entries = append(entries, map[string]interface{}{
				"source":      map[string]interface{}{"id": id},
				"destination": map[string]interface{}{"type": "pod"},
			})
			added = true
		}
		if added {
			if err := unstructured.SetNestedSlice(nm.Object, entries, "spec", "map"); err != nil {
				return false, err
			}
			if err := r.Update(ctx, nm); err != nil {
				return false, err
			}
			changed = true
		}
	}

	if len(datastores) > 0 {
		sm := newObject(storageMapGVK)
		if err := r.Get(ctx, types.NamespacedName{Namespace: task.Namespace, Name: name}, sm); err != nil {
			return false, err
		}
		storageClass := sm.GetAnnotations()[storageClassAnnotation]
		if storageClass == "" {
			return false, fmt.Errorf("StorageMap %s/%s carries no resolved storage class", task.Namespace, name)
		}
		entries, _, _ := unstructured.NestedSlice(sm.Object, "spec", "map")
		added := false
		for _, id := range datastores {
			if hasSourceID(entries, id) {
				continue
			}
			entries = append(entries, map[string]interface{}{
				"source":      map[string]interface{}{"id": id},
				"destination": map[string]interface{}{"storageClass": storageClass},
			})
			added = true
		}
		if added {
			if err := unstructured.SetNestedSlice(sm.Object, entries, "spec", "map"); err != nil {
				return false, err
			}
			if err := r.Update(ctx, sm); err != nil {
				return false, err
			}
			changed = true
		}
	}

	return changed, nil
}

// sourceTopology asks Forklift's inventory which networks and datastores one
// VM actually uses.
//
// This is the only place the answer exists. Forklift's Plan conditions name the
// VMs that are unmapped, not what they are unmapped to, and no Forklift custom
// resource carries a VM's network or datastore IDs — see the comment on
// inventoryClient for the source references.
//
// Scoped to a single VM on purpose. Reading the whole task here would tie every
// VM's fate to every other one: a single mistyped managed-object reference — the
// likeliest error on this API — would fail the VM being reconciled with a
// message naming a different machine, where a missing VM is meant to fail alone
// while its siblings proceed. It is also what keeps the cost linear: one GET per
// VM per pass rather than one per VM *for* every VM, repeated on every requeue
// while anything is still transferring.
func (r *VMImportTaskReconciler) sourceTopology(
	ctx context.Context,
	task *migrationv1alpha1.VMImportTask,
	src *migrationv1alpha1.VMImportSource,
	req *migrationv1alpha1.VMImportRequest,
) (networks, datastores []string, err error) {
	if r.Inventory == nil {
		return nil, nil, fmt.Errorf("no inventory client configured; the network and storage maps cannot be built")
	}

	// The inventory addresses providers by UID, so the Provider object this
	// task's Source produced has to be read for it.
	provider := newObject(providerGVK)
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: task.Namespace, Name: sourceProviderName(src.Name),
	}, provider); err != nil {
		return nil, nil, err
	}
	uid := string(provider.GetUID())
	if uid == "" {
		return nil, nil, fmt.Errorf("source Provider %s/%s has no UID yet", task.Namespace, sourceProviderName(src.Name))
	}

	vm, err := r.Inventory.VM(ctx, uid, req.ID)
	if err != nil {
		return nil, nil, err
	}
	return vm.networkIDs(), vm.datastoreIDs(), nil
}

func hasSourceID(entries []interface{}, id string) bool {
	for _, raw := range entries {
		entry, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		existing, _, _ := unstructured.NestedString(entry, "source", "id")
		if existing == id {
			return true
		}
		name, _, _ := unstructured.NestedString(entry, "source", "name")
		if name == id {
			return true
		}
	}
	return false
}

// planCriticalCondition returns Forklift's own words for why a Plan cannot run,
// or empty if nothing is critically wrong. Unmapped references are excluded:
// those are not failures, they are what populateMaps resolves.
func planCriticalCondition(plan *unstructured.Unstructured) string {
	conditions, _, _ := unstructured.NestedSlice(plan.Object, "status", "conditions")
	var critical []string
	for _, raw := range conditions {
		cond, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		status, _, _ := unstructured.NestedString(cond, "status")
		category, _, _ := unstructured.NestedString(cond, "category")
		ctype, _, _ := unstructured.NestedString(cond, "type")
		message, _, _ := unstructured.NestedString(cond, "message")
		if status != "True" || category != "Critical" {
			continue
		}
		if strings.Contains(ctype, "NotMapped") {
			continue
		}
		critical = append(critical, fmt.Sprintf("%s: %s", ctype, message))
	}
	if len(critical) == 0 {
		return ""
	}
	return strings.Join(critical, "; ")
}

// planReady reports whether Forklift considers the Plan runnable.
func planReady(plan *unstructured.Unstructured) bool {
	conditions, _, _ := unstructured.NestedSlice(plan.Object, "status", "conditions")
	for _, raw := range conditions {
		cond, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		ctype, _, _ := unstructured.NestedString(cond, "type")
		status, _, _ := unstructured.NestedString(cond, "status")
		if ctype == "Ready" && status == "True" {
			return true
		}
	}
	return false
}

// migrationProgress reads a Migration's status: whether the transfer finished,
// how far along it is, and the failure text if it failed.
func migrationProgress(migration *unstructured.Unstructured) (done bool, progress int32, failure string) {
	vms, _, _ := unstructured.NestedSlice(migration.Object, "status", "vms")
	if len(vms) == 0 {
		return false, 0, ""
	}
	vm, ok := vms[0].(map[string]interface{})
	if !ok {
		return false, 0, ""
	}

	if reasons, found, _ := unstructured.NestedStringSlice(vm, "error", "reasons"); found && len(reasons) > 0 {
		return false, 0, strings.Join(reasons, "; ")
	}
	phase, _, _ := unstructured.NestedString(vm, "phase")
	if phase == "Failed" {
		return false, 0, "the migration engine reported the transfer as failed"
	}

	// Progress is a completed/total pair per pipeline step, counted in MiB.
	// Prefer the disk-transfer step, because averaging a four-step pipeline
	// reads 25% before a byte has moved — but fall back to the sum over every
	// step that reports a total, since the step names are not part of any
	// contract and a release that renames them would otherwise peg progress at
	// zero for the whole transfer.
	var completed, total int64
	var namedCompleted, namedTotal int64
	pipeline, _, _ := unstructured.NestedSlice(vm, "pipeline")
	for _, raw := range pipeline {
		step, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		c, _, _ := unstructured.NestedInt64(step, "progress", "completed")
		t, _, _ := unstructured.NestedInt64(step, "progress", "total")
		if t <= 0 {
			continue
		}
		completed += c
		total += t
		if name, _, _ := unstructured.NestedString(step, "name"); strings.Contains(strings.ToLower(name), "disktransfer") {
			namedCompleted += c
			namedTotal += t
		}
	}
	switch {
	case namedTotal > 0:
		progress = int32(namedCompleted * 100 / namedTotal)
	case total > 0:
		progress = int32(completed * 100 / total)
	}

	// Either signal means the transfer is over: the phase the engine settles
	// on, or the completion timestamp it stamps.
	if phase == "Completed" {
		return true, 100, ""
	}
	if ts, found, _ := unstructured.NestedString(vm, "completed"); found && ts != "" {
		return true, 100, ""
	}
	return false, progress, ""
}

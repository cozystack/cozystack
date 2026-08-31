// SPDX-License-Identifier: Apache-2.0

package migrationcontroller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	migrationv1alpha1 "github.com/cozystack/cozystack/api/migration/v1alpha1"
)

// populatedForAnnotation is what makes the handoff free.
//
// CDI reads it as pure data: a string compare against the DataVolume's name,
// with no provenance check and no requirement that CDI ever saw the claim
// before, which is exactly why a controller may stamp it on a claim CDI has
// never met. It is evaluated before claim adoption in both the validating
// webhook and the import controller's work check, so it needs neither the
// DataVolumeClaimAdoption feature gate nor any annotation on the DataVolume —
// Cozystack's CDI configuration stays untouched.
//
// This is not a novel bet: it is the same primitive Cozystack's Velero VM
// restore path already depends on in production.
const populatedForAnnotation = "cdi.kubevirt.io/storage.populatedFor"

// Helm ownership metadata. The DataVolume is created before the VMDisk that
// will own it, so it must already look like part of that release or Helm
// refuses to adopt it and the release collides with its own volume.
const (
	helmManagedByLabel        = "app.kubernetes.io/managed-by"
	helmManagedByValue        = "Helm"
	helmReleaseNameAnnotation = "meta.helm.sh/release-name"
	helmReleaseNsAnnotation   = "meta.helm.sh/release-namespace"
)

// vmDiskReleasePrefix is how a VMDisk's name becomes its Helm release, and
// therefore the name of its DataVolume and PVC. The vm-disk chart names its
// DataVolume after the release, and vm-instance references disks as
// `vm-disk-<name>`, so this prefix is part of the platform's contract rather
// than a convention this controller invents.
const vmDiskReleasePrefix = "vm-disk-"

// outputs names what an import produced for one source VM.
type outputs struct {
	VMInstance string
	Disks      []string
}

// fulfill converts a finished transfer into Cozystack objects.
//
// Forklift's output is a KubeVirt VirtualMachine over CDI-populated claims,
// which is not what a Cozystack tenant manages. This turns it into VMDisks and
// a VMInstance without copying a byte: each transferred volume is re-pointed
// into the claim its VMDisk expects, and CDI is told the claim is already
// populated. The VirtualMachine Forklift built is read for the source VM's
// shape and then discarded — it is never started.
//
// Returns nil when more passes are needed, and a validationError for a
// terminal problem the tenant must fix.
func (r *VMImportTaskReconciler) fulfill(
	ctx context.Context,
	task *migrationv1alpha1.VMImportTask,
	req *migrationv1alpha1.VMImportRequest,
) (*outputs, error) {
	logger := log.FromContext(ctx)

	vm, err := r.findForkliftVM(ctx, task, req)
	if err != nil {
		return nil, err
	}
	if vm == nil {
		// Already cleaned up: the outputs of a previous pass are authoritative.
		if prev := findVMStatus(task.Status.VMs, req.ID); prev != nil && prev.VMInstance != "" {
			return &outputs{VMInstance: prev.VMInstance, Disks: prev.Disks}, nil
		}
		return nil, &validationError{
			reason:  migrationv1alpha1.ReasonTransferFailed,
			message: fmt.Sprintf("the transfer completed but no imported VM was found for %s", req.ID),
		}
	}

	name := req.Name
	if name == "" {
		name = sanitizeName(vm.GetName())
	}

	claims := forkliftVMClaims(vm)
	if len(claims) == 0 {
		return nil, &validationError{
			reason:  migrationv1alpha1.ReasonTransferFailed,
			message: fmt.Sprintf("the imported VM %s has no volumes to adopt", vm.GetName()),
		}
	}

	diskNames := make([]string, 0, len(claims))
	for i, claim := range claims {
		diskName := fmt.Sprintf("%s-disk-%d", name, i)
		diskNames = append(diskNames, diskName)
		if err := r.adoptVolume(ctx, task, req.ID, claim, diskName); err != nil {
			return nil, err
		}
	}

	// The VMInstance is created only once every disk is in place, so a tenant
	// never sees an instance referencing a disk that does not exist yet.
	if err := r.createVMInstance(ctx, task, req, name, vm, diskNames); err != nil {
		return nil, err
	}

	// The scaffolding VM goes last: while it exists, KubeVirt's own garbage
	// collection could still reach the claims through its owner references.
	if err := r.Delete(ctx, vm); err != nil && !apierrors.IsNotFound(err) {
		return nil, err
	}
	logger.Info("imported VM fulfilled", "vm", name, "disks", len(diskNames))

	return &outputs{VMInstance: name, Disks: diskNames}, nil
}

// findForkliftVM locates the VirtualMachine Forklift created for this source VM.
//
// The link is Forklift's `plan` label, and its value is the Plan's **UID**, not
// its name — so the Plan has to be read to learn what to match against. Getting
// this wrong does not fail loudly: the lookup simply never matches, the
// transferred disk is never adopted, and the task sits in Creating forever.
func (r *VMImportTaskReconciler) findForkliftVM(
	ctx context.Context,
	task *migrationv1alpha1.VMImportTask,
	req *migrationv1alpha1.VMImportRequest,
) (*unstructured.Unstructured, error) {
	name := planName(task.Name, req.ID)

	plan := newObject(planGVK)
	err := r.Get(ctx, types.NamespacedName{Namespace: task.Namespace, Name: name}, plan)
	if err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, err
	}
	planUID := string(plan.GetUID())

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(virtualMachineGVK.GroupVersion().WithKind("VirtualMachineList"))
	if err := r.List(ctx, list, client.InNamespace(task.Namespace)); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, err
	}
	for i := range list.Items {
		item := &list.Items[i]
		labels := item.GetLabels()
		if planUID != "" && (labels["plan"] == planUID || labels["migration"] == planUID) {
			return item, nil
		}
		// Some Forklift versions stamp the name instead; accept either rather
		// than depending on which one this release happens to use.
		if labels["plan"] == name || labels["migration"] == name {
			return item, nil
		}
	}
	return nil, nil
}

// forkliftVMClaims returns the claim names backing the imported VM, in the
// source VM's own disk order — which is the order the guest's firmware expects
// to boot from, so it must be preserved rather than sorted.
func forkliftVMClaims(vm *unstructured.Unstructured) []string {
	volumes, _, _ := unstructured.NestedSlice(vm.Object, "spec", "template", "spec", "volumes")
	claims := make([]string, 0, len(volumes))
	for _, raw := range volumes {
		vol, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if name, found, _ := unstructured.NestedString(vol, "dataVolume", "name"); found && name != "" {
			claims = append(claims, name)
			continue
		}
		if name, found, _ := unstructured.NestedString(vol, "persistentVolumeClaim", "claimName"); found && name != "" {
			claims = append(claims, name)
		}
	}
	return claims
}

// sourceFirmware translates the boot configuration of the VM Forklift built
// into the vm-instance `firmware` block, or returns nil when the source says
// nothing and the instance profile's own default should stand.
//
// Forklift copies the source's firmware onto the VirtualMachine it creates, so
// this is the source's boot mode as the migration engine understood it — read
// from the object the handoff already holds rather than from a second inventory
// lookup. Carrying it across is not cosmetic: a UEFI guest rendered with a BIOS
// bootloader imports "successfully" and then does not boot, which is why the
// vm-instance firmware field (cozystack/cozystack#3002) is a merge-order
// dependency of this controller.
func sourceFirmware(vm *unstructured.Unstructured) map[string]interface{} {
	base := []string{"spec", "template", "spec", "domain", "firmware", "bootloader"}

	if efi, found, _ := unstructured.NestedMap(vm.Object, append(base, "efi")...); found {
		fw := map[string]interface{}{"bootloader": "uefi"}
		// secureBoot is reported by the source; persistent NVRAM is not, and is
		// deliberately left to the tenant — it pins the VM to a node.
		if sb, ok := efi["secureBoot"].(bool); ok && sb {
			fw["secureBoot"] = true
		}
		return fw
	}
	// `efi: {}` renders as an empty map, but a malformed value under the key
	// still means EFI was asked for; treat presence as the answer.
	if _, present, _ := unstructured.NestedFieldNoCopy(vm.Object, append(base, "efi")...); present {
		return map[string]interface{}{"bootloader": "uefi"}
	}
	if _, present, _ := unstructured.NestedFieldNoCopy(vm.Object, append(base, "bios")...); present {
		return map[string]interface{}{"bootloader": "bios"}
	}
	return nil
}

// adoptVolume moves one transferred volume under the name a VMDisk expects,
// then creates the VMDisk over it. No data is copied: the PersistentVolume is
// retained and re-bound, which is the whole point of the exercise.
func (r *VMImportTaskReconciler) adoptVolume(
	ctx context.Context,
	task *migrationv1alpha1.VMImportTask,
	vmID string,
	sourceClaim string,
	diskName string,
) error {
	target := vmDiskReleasePrefix + diskName

	// Idempotency, but only over this task's own work. A claim of the right
	// name that this task did not create is a tenant's disk standing where an
	// output goes: treating it as a finished pass would attach stale data to
	// the instance and leave the freshly transferred claim owned by the
	// scaffolding VM, to be collected the moment that VM is deleted.
	existing := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, types.NamespacedName{Namespace: task.Namespace, Name: target}, existing)
	if err == nil {
		if !isOwnOutput(existing, task, vmID) {
			return &validationError{
				reason: migrationv1alpha1.ReasonOutputExists,
				message: fmt.Sprintf(
					"a PersistentVolumeClaim named %q already exists in this namespace and was not created by this import; "+
						"the import will not overwrite it — choose another name for VM %s", target, vmID),
			}
		}
		return r.ensureVMDisk(ctx, task, vmID, diskName, existing)
	}
	if !apierrors.IsNotFound(err) {
		return err
	}

	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: task.Namespace, Name: sourceClaim}, pvc); err != nil {
		return err
	}
	if pvc.Spec.VolumeName == "" {
		return &validationError{
			reason:  migrationv1alpha1.ReasonTransferFailed,
			message: fmt.Sprintf("transferred claim %q is not bound to a volume", sourceClaim),
		}
	}

	pv := &corev1.PersistentVolume{}
	if err := r.Get(ctx, types.NamespacedName{Name: pvc.Spec.VolumeName}, pv); err != nil {
		return err
	}

	// Order matters here, and getting it wrong destroys data rather than
	// merely wasting time.
	//
	// First, orphan the claim. Forklift drives the transfer through CDI's
	// volume populator, so the transferred disk arrives as a *populated claim*
	// with no DataVolume of its own — and Forklift puts an ownerReference to
	// the VirtualMachine it built on that claim, deliberately, so an abandoned
	// migration cleans itself up. The consequence is that anything deleting
	// that VM takes the disk with it, and garbage collection is fast enough to
	// win any race against a copy: on the original implementation it removed
	// the claim 357 ms after the VM went, long before a clone could finish.
	// Clearing the reference unconditionally is what makes the rest of this
	// function safe to reorder.
	if err := clearOwnerReferences(ctx, r.Client, task.Namespace, sourceClaim); err != nil {
		return err
	}

	// Then remove any DataVolume that does own the claim. Deleting a claim
	// while its DataVolume still exists makes CDI recreate the claim and
	// provision a second volume, re-running the entire import — the exact
	// duplicate copy this design exists to remove. Observed on a live cluster.
	if err := r.deleteOwningDataVolume(ctx, task.Namespace, sourceClaim); err != nil {
		return err
	}

	// Retain is what protects the data during the swap, and it stays on
	// permanently: CDI takes a controller owner reference on an adopted claim,
	// so deleting the DataVolume garbage-collects the claim, and only the
	// reclaim policy keeps the disk. That policy is part of the contract.
	if pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		pv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
		if err := r.Update(ctx, pv); err != nil {
			return err
		}
	}

	// volumeMode and accessModes are copied from the volume rather than
	// defaulted. Pre-binding through volumeName bypasses the volumeMode match
	// check, so a claim that assumes Filesystem against a Block volume binds
	// anyway and fails much later, at mount time, with a message that points
	// nowhere near here. Also observed live.
	replacement := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      target,
			Namespace: task.Namespace,
			Labels: func() map[string]string {
				l := map[string]string{
					migrationv1alpha1.ManagedByLabel: migrationv1alpha1.ManagedByValue,
				}
				for k, v := range outputMarkers(task, vmID) {
					l[k] = v
				}
				return l
			}(),
			Annotations: map[string]string{
				populatedForAnnotation: target,
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      pv.Spec.AccessModes,
			VolumeMode:       pv.Spec.VolumeMode,
			VolumeName:       pv.Name,
			StorageClassName: &pv.Spec.StorageClassName,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: pv.Spec.Capacity[corev1.ResourceStorage],
				},
			},
		},
	}
	if err := r.Create(ctx, replacement); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}

	// Release the old claim, then hand the volume to the new one. Rewriting
	// claimRef including the UID is what makes this atomic: there is no window
	// in which the volume is Available for an unrelated claim to grab.
	old := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: task.Namespace, Name: sourceClaim}, old); err == nil {
		if err := r.Delete(ctx, old); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	bound := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: task.Namespace, Name: target}, bound); err != nil {
		return err
	}
	if err := r.Get(ctx, types.NamespacedName{Name: pv.Name}, pv); err != nil {
		return err
	}
	pv.Spec.ClaimRef = &corev1.ObjectReference{
		Kind:       "PersistentVolumeClaim",
		APIVersion: "v1",
		Namespace:  bound.Namespace,
		Name:       bound.Name,
		UID:        bound.UID,
	}
	if err := r.Update(ctx, pv); err != nil {
		return err
	}

	if err := r.createDataVolume(ctx, task, diskName, bound); err != nil {
		return err
	}
	return r.ensureVMDisk(ctx, task, vmID, diskName, bound)
}

// deleteOwningDataVolume removes the DataVolume that owns a claim, and waits
// for it to actually be gone before the caller touches the claim.
func (r *VMImportTaskReconciler) deleteOwningDataVolume(ctx context.Context, namespace, claim string) error {
	dv := newObject(dataVolumeGVK)
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: claim}, dv)
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return nil
	}
	if err != nil {
		return err
	}
	// Orphan the claim first: without this, deleting the DataVolume takes the
	// claim with it through the controller owner reference CDI set.
	if err := clearOwnerReferences(ctx, r.Client, namespace, claim); err != nil {
		return err
	}
	if err := r.Delete(ctx, dv); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func clearOwnerReferences(ctx context.Context, c client.Client, namespace, name string) error {
	pvc := &corev1.PersistentVolumeClaim{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, pvc); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if len(pvc.OwnerReferences) == 0 {
		return nil
	}
	pvc.OwnerReferences = nil
	return c.Update(ctx, pvc)
}

// createDataVolume creates the DataVolume the VMDisk release will adopt. Its
// spec is deliberately minimal and its source is blank: the claim beside it
// already carries the data and the populatedFor annotation, so CDI short
// circuits population instead of fetching anything.
func (r *VMImportTaskReconciler) createDataVolume(
	ctx context.Context,
	task *migrationv1alpha1.VMImportTask,
	diskName string,
	pvc *corev1.PersistentVolumeClaim,
) error {
	name := vmDiskReleasePrefix + diskName
	existing := newObject(dataVolumeGVK)
	err := r.Get(ctx, types.NamespacedName{Namespace: task.Namespace, Name: name}, existing)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}

	size := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	dv := newObject(dataVolumeGVK)
	dv.SetName(name)
	dv.SetNamespace(task.Namespace)
	dv.SetLabels(map[string]string{
		// Both of these matter: the instance label is what the vm-disk chart
		// stamps, and the Helm label is what lets the release adopt the object
		// instead of colliding with it.
		"app.kubernetes.io/instance": name,
		helmManagedByLabel:           helmManagedByValue,
	})
	dv.SetAnnotations(map[string]string{
		helmReleaseNameAnnotation:      name,
		helmReleaseNsAnnotation:        task.Namespace,
		"vm-disk.cozystack.io/optical": "false",
	})
	spec := map[string]interface{}{
		"contentType": "kubevirt",
		"source": map[string]interface{}{
			"blank": map[string]interface{}{},
		},
		"storage": map[string]interface{}{
			"resources": map[string]interface{}{
				"requests": map[string]interface{}{
					"storage": size.String(),
				},
			},
		},
	}
	if pvc.Spec.StorageClassName != nil && *pvc.Spec.StorageClassName != "" {
		storage, _, _ := unstructured.NestedMap(spec, "storage")
		storage["storageClassName"] = *pvc.Spec.StorageClassName
		spec["storage"] = storage
	}
	if err := unstructured.SetNestedMap(dv.Object, spec, "spec"); err != nil {
		return err
	}
	if err := r.Create(ctx, dv); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// ensureVMDisk creates the tenant-facing VMDisk over an adopted volume. It
// carries no owner reference to the task on purpose: this object is the point
// of the whole operation and must outlive the operation that produced it.
func (r *VMImportTaskReconciler) ensureVMDisk(
	ctx context.Context,
	task *migrationv1alpha1.VMImportTask,
	vmID string,
	diskName string,
	pvc *corev1.PersistentVolumeClaim,
) error {
	existing := newObject(vmDiskGVK)
	err := r.Get(ctx, types.NamespacedName{Namespace: task.Namespace, Name: diskName}, existing)
	if err == nil {
		if !isOwnOutput(existing, task, vmID) {
			return &validationError{
				reason: migrationv1alpha1.ReasonOutputExists,
				message: fmt.Sprintf(
					"a VMDisk named %q already exists in this namespace and was not created by this import; "+
						"the import will not overwrite it — choose another name for VM %s", diskName, vmID),
			}
		}
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}

	size := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	obj := newObject(vmDiskGVK)
	obj.SetName(diskName)
	obj.SetNamespace(task.Namespace)
	stampOutput(obj, task, vmID)
	spec := map[string]interface{}{
		"storage": size.String(),
		"optical": false,
	}
	if pvc.Spec.StorageClassName != nil && *pvc.Spec.StorageClassName != "" {
		spec["storageClass"] = *pvc.Spec.StorageClassName
	}
	if err := unstructured.SetNestedMap(obj.Object, spec, "spec"); err != nil {
		return err
	}
	if err := r.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// createVMInstance builds the tenant-facing VM over the imported disks.
//
// When the tenant named no instance type the source VM's own CPU and memory are
// carried across as explicit resources, so an import never silently resizes a
// guest to fit a catalog size.
func (r *VMImportTaskReconciler) createVMInstance(
	ctx context.Context,
	task *migrationv1alpha1.VMImportTask,
	req *migrationv1alpha1.VMImportRequest,
	name string,
	vm *unstructured.Unstructured,
	diskNames []string,
) error {
	existing := newObject(vmInstanceGVK)
	err := r.Get(ctx, types.NamespacedName{Namespace: task.Namespace, Name: name}, existing)
	if err == nil {
		// Only this task's own instance counts as a finished pass. Anything
		// else of that name belongs to the tenant, and an import never
		// overwrites or silently adopts one.
		if !isOwnOutput(existing, task, req.ID) {
			return &validationError{
				reason: migrationv1alpha1.ReasonOutputExists,
				message: fmt.Sprintf(
					"a VMInstance named %q already exists in this namespace and was not created by this import; "+
						"the import will not overwrite it — choose another name for VM %s", name, req.ID),
			}
		}
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}

	disks := make([]interface{}, 0, len(diskNames))
	for _, d := range diskNames {
		disks = append(disks, map[string]interface{}{"name": d})
	}

	spec := map[string]interface{}{
		"disks": disks,
		// Halted, not Always: a freshly imported guest may need its network or
		// drivers adjusted, and booting it the instant the import finishes can
		// put a second copy of a still-running production machine on the
		// network. Starting it is one click, and it is the tenant's call.
		"runStrategy": "Halted",
	}
	if req.InstanceType != "" {
		spec["instanceType"] = req.InstanceType
	} else {
		cpu, memory := sourceVMResources(vm)
		resources := map[string]interface{}{}
		if cpu != "" {
			resources["cpu"] = cpu
		}
		if memory != "" {
			resources["memory"] = memory
		}
		if len(resources) > 0 {
			spec["resources"] = resources
			// An empty instance type is what tells the chart to use the
			// explicit resources instead of a catalog size.
			spec["instanceType"] = ""
		}
	}
	if req.InstanceProfile != "" {
		spec["instanceProfile"] = req.InstanceProfile
	}

	// Carry the source's boot mode across. Without it a UEFI guest is rendered
	// with the profile's default bootloader and does not boot.
	if fw := sourceFirmware(vm); fw != nil {
		spec["firmware"] = fw
	}

	obj := newObject(vmInstanceGVK)
	obj.SetName(name)
	obj.SetNamespace(task.Namespace)
	stampOutput(obj, task, req.ID)
	if err := unstructured.SetNestedMap(obj.Object, spec, "spec"); err != nil {
		return err
	}
	if err := r.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// sourceVMResources reads the CPU and memory Forklift derived from the source
// VM's inventory record. Reading them off the object Forklift already built is
// why this controller needs no inventory client of its own.
func sourceVMResources(vm *unstructured.Unstructured) (cpu, memory string) {
	if cores, found, _ := unstructured.NestedInt64(vm.Object, "spec", "template", "spec", "domain", "cpu", "cores"); found && cores > 0 {
		sockets, _, _ := unstructured.NestedInt64(vm.Object, "spec", "template", "spec", "domain", "cpu", "sockets")
		threads, _, _ := unstructured.NestedInt64(vm.Object, "spec", "template", "spec", "domain", "cpu", "threads")
		total := cores
		if sockets > 1 {
			total *= sockets
		}
		if threads > 1 {
			total *= threads
		}
		cpu = fmt.Sprintf("%d", total)
	}
	for _, path := range [][]string{
		{"spec", "template", "spec", "domain", "memory", "guest"},
		{"spec", "template", "spec", "domain", "resources", "requests", "memory"},
	} {
		if value, found, _ := unstructured.NestedString(vm.Object, path...); found && value != "" {
			if _, err := resource.ParseQuantity(value); err == nil {
				memory = value
				break
			}
		}
	}
	return cpu, memory
}

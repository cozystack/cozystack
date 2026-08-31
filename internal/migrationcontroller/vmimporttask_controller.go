// SPDX-License-Identifier: Apache-2.0

package migrationcontroller

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	migrationv1alpha1 "github.com/cozystack/cozystack/api/migration/v1alpha1"
)

// TransferRequeue is the polling interval while disks are moving. Forklift
// updates its Migration status as the transfer progresses and this controller
// mirrors it; a watch on Migration drives most updates, and this is the
// backstop for the case where the watch is not established (Forklift absent at
// startup, CRDs installed later).
const TransferRequeue = 20 * time.Second

// defaultStorageClassAnnotation marks the cluster's default StorageClass.
const defaultStorageClassAnnotation = "storageclass.kubernetes.io/is-default-class"

// VMImportTaskReconciler drives one import operation: it renders the Forklift
// objects that perform the transfer, mirrors their progress onto the task, and
// converts each finished transfer into a Cozystack VMDisk and VMInstance.
//
// The scaffolding it creates (maps, Plans, Migrations) is owned by the task and
// disappears with it. The outputs are deliberately not owned by anything, which
// is what lets a tenant delete a finished task without losing the machines it
// imported.
type VMImportTaskReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	// Inventory reads the source's topology from Forklift's inventory service.
	// The network and datastore IDs a Plan's maps need appear on no Forklift
	// custom resource, so there is no way to build those maps without it.
	Inventory *inventoryClient
}

func (r *VMImportTaskReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	task := &migrationv1alpha1.VMImportTask{}
	if err := r.Get(ctx, req.NamespacedName, task); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// A finished task is a record, not a workload. Re-running validation or
	// re-creating scaffolding for it would resurrect Forklift objects a tenant
	// has already seen disappear.
	if task.Status.Phase.IsTerminal() {
		return ctrl.Result{}, nil
	}

	src := &migrationv1alpha1.VMImportSource{}
	err := r.Get(ctx, types.NamespacedName{Namespace: task.Namespace, Name: task.Spec.SourceRef.Name}, src)
	if apierrors.IsNotFound(err) {
		return r.pending(ctx, task, migrationv1alpha1.ReasonSourceNotReady,
			fmt.Sprintf("VMImportSource %q does not exist in this namespace", task.Spec.SourceRef.Name))
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if !meta.IsStatusConditionTrue(src.Status.Conditions, migrationv1alpha1.SourceReadyCondition) {
		reason, message := sourceNotReadyDetail(src)
		return r.pending(ctx, task, reason, message)
	}

	// Resolve and vet the storage class before anything is created. A
	// WaitForFirstConsumer class does not fail the import, it hangs it: nothing
	// consumes the claim while CDI populates it, so the transfer waits forever.
	// Failing here costs a tenant one edit; not failing here costs them a
	// support ticket.
	storageClass, err := r.resolveStorageClass(ctx, task)
	if err != nil {
		var verr *validationError
		if asValidationError(err, &verr) {
			return r.terminalFail(ctx, task, verr.reason, verr.message)
		}
		return ctrl.Result{}, err
	}

	if task.Status.StartedAt == nil {
		now := metav1.Now()
		task.Status.StartedAt = &now
	}

	if err := r.ensureMaps(ctx, task, src, storageClass); err != nil {
		// Forklift's CRDs can vanish under a running task — the operand is
		// removed, or this is the window before the Source's recheck notices.
		// reconcileVM already reports that as Pending; without the same here
		// the reconcile returns an error and hot-loops with nothing written for
		// a tenant to read.
		if meta.IsNoMatchError(err) {
			return r.pending(ctx, task, migrationv1alpha1.ReasonForkliftNotInstalled,
				"Forklift is not installed in this cluster")
		}
		return ctrl.Result{}, err
	}

	statuses := make([]migrationv1alpha1.VMImportStatus, 0, len(task.Spec.VMs))
	requeue := false
	for i := range task.Spec.VMs {
		vmStatus, again, err := r.reconcileVM(ctx, task, src, &task.Spec.VMs[i])
		if err != nil {
			return ctrl.Result{}, err
		}
		requeue = requeue || again
		statuses = append(statuses, vmStatus)
	}

	task.Status.VMs = statuses
	task.Status.Phase = aggregatePhase(statuses)
	task.Status.ObservedGeneration = task.Generation

	if task.Status.Phase.IsTerminal() && task.Status.CompletedAt == nil {
		now := metav1.Now()
		task.Status.CompletedAt = &now
		task.Status.Message = terminalMessage(statuses)
		if task.Status.Phase == migrationv1alpha1.VMImportTaskPhaseSucceeded {
			r.Recorder.Eventf(task, "Normal", "Imported", "imported %d VM(s)", len(statuses))
		} else {
			r.Recorder.Eventf(task, "Warning", "ImportFailed", "%s", task.Status.Message)
		}
	}

	if err := r.Status().Update(ctx, task); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	logger.V(1).Info("reconciled VMImportTask", "phase", task.Status.Phase, "vms", len(statuses))
	if requeue {
		return ctrl.Result{RequeueAfter: TransferRequeue}, nil
	}
	return ctrl.Result{}, nil
}

// reconcileVM advances one VM of the task: it creates the Plan and Migration
// that move its disks, mirrors their progress, and once the transfer is done
// converts the result into Cozystack objects. The bool reports whether this VM
// still needs watching.
func (r *VMImportTaskReconciler) reconcileVM(
	ctx context.Context,
	task *migrationv1alpha1.VMImportTask,
	src *migrationv1alpha1.VMImportSource,
	req *migrationv1alpha1.VMImportRequest,
) (migrationv1alpha1.VMImportStatus, bool, error) {
	status := migrationv1alpha1.VMImportStatus{
		ID:   req.ID,
		Name: req.Name,
	}
	// Carry forward what a previous pass established, so a completed VM keeps
	// its output names even after its Forklift objects are gone.
	if prev := findVMStatus(task.Status.VMs, req.ID); prev != nil {
		status = *prev
		if status.Phase.IsTerminal() {
			return status, false, nil
		}
	}

	// Refuse to overwrite anything a tenant already has, and refuse it before
	// spending a transfer on it. Checked every pass rather than once, because a
	// name can be taken while a transfer runs, and per VM rather than per task:
	// one VM whose name is occupied does not stop its siblings from finishing.
	if collision, err := r.outputCollision(ctx, task, req); err != nil {
		return status, false, err
	} else if collision != "" {
		status.Phase = migrationv1alpha1.VMImportTaskPhaseFailed
		status.Message = collision
		return status, false, nil
	}

	plan := newObject(planGVK)
	name := planName(task.Name, req.ID)
	err := r.Get(ctx, types.NamespacedName{Namespace: task.Namespace, Name: name}, plan)
	if apierrors.IsNotFound(err) {
		if err := r.createPlan(ctx, task, src, req); err != nil {
			return status, false, err
		}
		status.Phase = migrationv1alpha1.VMImportTaskPhaseValidating
		status.Message = "waiting for Forklift to validate the source VM"
		return status, true, nil
	}
	if err != nil {
		if meta.IsNoMatchError(err) {
			status.Phase = migrationv1alpha1.VMImportTaskPhasePending
			status.Message = "Forklift is not installed in this cluster"
			return status, true, nil
		}
		return status, false, err
	}

	// Forklift validates the VM reference, the maps and the provider for us and
	// says what is wrong in its own words. Surfacing that verbatim beats
	// re-implementing its checks against an inventory we would have to query
	// ourselves.
	if critical := planCriticalCondition(plan); critical != "" {
		status.Phase = migrationv1alpha1.VMImportTaskPhaseFailed
		status.Message = critical
		return status, false, nil
	}

	// Fill the maps from the source's actual topology, read out of Forklift's
	// inventory. This cannot be done from the Plan's own conditions: those name
	// the VMs that are unmapped, never the networks or datastores they use.
	if learned, err := r.populateMaps(ctx, task, src); err != nil {
		var nf *notFoundError
		if errors.As(err, &nf) {
			status.Phase = migrationv1alpha1.VMImportTaskPhaseFailed
			status.Message = nf.Error()
			return status, false, nil
		}
		return status, false, err
	} else if learned {
		status.Phase = migrationv1alpha1.VMImportTaskPhaseValidating
		status.Message = "completing the network and storage maps from the source inventory"
		return status, true, nil
	}

	if !planReady(plan) {
		status.Phase = migrationv1alpha1.VMImportTaskPhaseValidating
		status.Message = "waiting for Forklift to validate the plan"
		return status, true, nil
	}

	migration, err := r.ensureMigration(ctx, task, name)
	if err != nil {
		return status, false, err
	}

	done, progress, message := migrationProgress(migration)
	status.Progress = progress
	switch {
	case message != "" && !done:
		status.Phase = migrationv1alpha1.VMImportTaskPhaseFailed
		status.Message = message
		return status, false, nil
	case !done:
		status.Phase = migrationv1alpha1.VMImportTaskPhaseTransferring
		status.Message = ""
		return status, true, nil
	}

	// The transfer is finished. Everything from here is local: adopt the
	// volumes into VMDisks, build a VMInstance over them, and discard the
	// KubeVirt VM Forklift made along the way.
	status.Phase = migrationv1alpha1.VMImportTaskPhaseCreating
	outputs, err := r.fulfill(ctx, task, req)
	if err != nil {
		var verr *validationError
		if asValidationError(err, &verr) {
			status.Phase = migrationv1alpha1.VMImportTaskPhaseFailed
			status.Message = verr.message
			return status, false, nil
		}
		return status, false, err
	}
	if outputs == nil {
		status.Message = "creating the imported disks and instance"
		return status, true, nil
	}

	status.Phase = migrationv1alpha1.VMImportTaskPhaseSucceeded
	status.Progress = 100
	status.VMInstance = outputs.VMInstance
	status.Disks = outputs.Disks
	status.Message = ""
	return status, false, nil
}

// resolveStorageClass returns the class every disk of this task is imported
// onto, and rejects one that cannot complete an import.
func (r *VMImportTaskReconciler) resolveStorageClass(ctx context.Context, task *migrationv1alpha1.VMImportTask) (string, error) {
	name := task.Spec.StorageClass
	origin := "spec.storageClass"
	if name == "" {
		list := &storagev1.StorageClassList{}
		if err := r.List(ctx, list); err != nil {
			return "", err
		}
		for i := range list.Items {
			if list.Items[i].Annotations[defaultStorageClassAnnotation] == "true" {
				name = list.Items[i].Name
				origin = "the cluster default StorageClass"
				break
			}
		}
		if name == "" {
			return "", &validationError{
				reason:  migrationv1alpha1.ReasonStorageClassNotImmediate,
				message: "no storageClass was given and this cluster has no default StorageClass",
			}
		}
	}

	sc := &storagev1.StorageClass{}
	if err := r.Get(ctx, types.NamespacedName{Name: name}, sc); err != nil {
		if apierrors.IsNotFound(err) {
			return "", &validationError{
				reason:  migrationv1alpha1.ReasonStorageClassNotImmediate,
				message: fmt.Sprintf("StorageClass %q (%s) does not exist", name, origin),
			}
		}
		return "", err
	}
	if sc.VolumeBindingMode != nil && *sc.VolumeBindingMode == storagev1.VolumeBindingWaitForFirstConsumer {
		return "", &validationError{
			reason: migrationv1alpha1.ReasonStorageClassNotImmediate,
			message: fmt.Sprintf(
				"StorageClass %q (%s) binds WaitForFirstConsumer, which deadlocks an import: "+
					"nothing consumes the volume while it is being populated. Create a new task "+
					"naming a class with volumeBindingMode Immediate in spec.storageClass — this "+
					"task is terminal and spec.storageClass is immutable, so editing it will not "+
					"restart the import", name, origin),
		}
	}
	return name, nil
}

// outputCollision reports a tenant object standing where this VM's output goes,
// or "" when the name is free or already holds this task's own earlier output.
//
// The distinction is the whole point. Without it every resume is a guess, and
// both guesses are destructive: treating a tenant's object as its own attaches
// stale data while the fresh transfer is collected with the scaffolding VM,
// and treating its own output as a collision terminally fails an import that
// actually succeeded. The marker isOwnOutput reads is written by stampOutput on
// everything this controller creates.
func (r *VMImportTaskReconciler) outputCollision(
	ctx context.Context,
	task *migrationv1alpha1.VMImportTask,
	req *migrationv1alpha1.VMImportRequest,
) (string, error) {
	if req.Name == "" {
		// The instance is named after the imported VM, which is not known
		// until the transfer finishes; the handoff checks it then.
		return "", nil
	}
	existing := newObject(vmInstanceGVK)
	err := r.Get(ctx, types.NamespacedName{Namespace: task.Namespace, Name: req.Name}, existing)
	if err == nil {
		if isOwnOutput(existing, task, req.ID) {
			return "", nil
		}
		return fmt.Sprintf("a VMInstance named %q already exists in this namespace and was not created by "+
			"this import; the import will not overwrite it — choose another name for VM %s", req.Name, req.ID), nil
	}
	if !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
		return "", err
	}
	return "", nil
}

func (r *VMImportTaskReconciler) pending(ctx context.Context, task *migrationv1alpha1.VMImportTask, reason, message string) (ctrl.Result, error) {
	task.Status.Phase = migrationv1alpha1.VMImportTaskPhasePending
	task.Status.Message = message
	task.Status.ObservedGeneration = task.Generation
	meta.SetStatusCondition(&task.Status.Conditions, metav1.Condition{
		Type:               migrationv1alpha1.TaskValidatedCondition,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: task.Generation,
	})
	if err := r.Status().Update(ctx, task); err != nil && !apierrors.IsConflict(err) {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: ConnectionRequeue}, nil
}

func (r *VMImportTaskReconciler) terminalFail(ctx context.Context, task *migrationv1alpha1.VMImportTask, reason, message string) (ctrl.Result, error) {
	now := metav1.Now()
	task.Status.Phase = migrationv1alpha1.VMImportTaskPhaseFailed
	task.Status.Message = message
	task.Status.CompletedAt = &now
	task.Status.ObservedGeneration = task.Generation
	meta.SetStatusCondition(&task.Status.Conditions, metav1.Condition{
		Type:               migrationv1alpha1.TaskValidatedCondition,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: task.Generation,
	})
	r.Recorder.Eventf(task, "Warning", reason, "%s", message)
	if err := r.Status().Update(ctx, task); err != nil && !apierrors.IsConflict(err) {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// aggregatePhase folds per-VM phases into the task's own. A task is only
// Succeeded when every VM is, and Failed only once no VM can still progress —
// one failed VM does not stop its siblings from finishing.
func aggregatePhase(statuses []migrationv1alpha1.VMImportStatus) migrationv1alpha1.VMImportTaskPhase {
	if len(statuses) == 0 {
		return migrationv1alpha1.VMImportTaskPhasePending
	}
	var succeeded, failed int
	phase := migrationv1alpha1.VMImportTaskPhasePending
	rank := map[migrationv1alpha1.VMImportTaskPhase]int{
		migrationv1alpha1.VMImportTaskPhasePending:      0,
		migrationv1alpha1.VMImportTaskPhaseValidating:   1,
		migrationv1alpha1.VMImportTaskPhaseTransferring: 2,
		migrationv1alpha1.VMImportTaskPhaseCreating:     3,
	}
	for _, s := range statuses {
		switch s.Phase {
		case migrationv1alpha1.VMImportTaskPhaseSucceeded:
			succeeded++
		case migrationv1alpha1.VMImportTaskPhaseFailed:
			failed++
		default:
			if rank[s.Phase] >= rank[phase] {
				phase = s.Phase
			}
		}
	}
	switch {
	case succeeded == len(statuses):
		return migrationv1alpha1.VMImportTaskPhaseSucceeded
	case succeeded+failed == len(statuses):
		return migrationv1alpha1.VMImportTaskPhaseFailed
	default:
		return phase
	}
}

func terminalMessage(statuses []migrationv1alpha1.VMImportStatus) string {
	var failed []string
	for _, s := range statuses {
		if s.Phase == migrationv1alpha1.VMImportTaskPhaseFailed {
			failed = append(failed, fmt.Sprintf("%s: %s", s.ID, s.Message))
		}
	}
	if len(failed) == 0 {
		return fmt.Sprintf("imported %d VM(s)", len(statuses))
	}
	sort.Strings(failed)
	return fmt.Sprintf("%d of %d VM(s) failed — %s", len(failed), len(statuses), strings.Join(failed, "; "))
}

func findVMStatus(statuses []migrationv1alpha1.VMImportStatus, id string) *migrationv1alpha1.VMImportStatus {
	for i := range statuses {
		if statuses[i].ID == id {
			return &statuses[i]
		}
	}
	return nil
}

func sourceNotReadyDetail(src *migrationv1alpha1.VMImportSource) (string, string) {
	cond := meta.FindStatusCondition(src.Status.Conditions, migrationv1alpha1.SourceReadyCondition)
	if cond == nil {
		return migrationv1alpha1.ReasonSourceNotReady,
			fmt.Sprintf("VMImportSource %q has not been reconciled yet", src.Name)
	}
	return migrationv1alpha1.ReasonSourceNotReady,
		fmt.Sprintf("VMImportSource %q is not ready (%s): %s", src.Name, cond.Reason, cond.Message)
}

// validationError is a terminal, tenant-visible misconfiguration: retrying it
// unchanged cannot help, so the task fails with the reason attached instead of
// requeueing forever.
type validationError struct {
	reason  string
	message string
}

func (e *validationError) Error() string { return e.message }

func asValidationError(err error, target **validationError) bool {
	return errors.As(err, target)
}

func (r *VMImportTaskReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Deliberately no watch on Forklift's Migration, even though it is what
	// drives a task's progress.
	//
	// Forklift is an optional package. A watch on a kind whose CRD is absent
	// never syncs its cache, and controller-runtime will not start a controller
	// whose caches have not synced — so on a cluster without Forklift the whole
	// task controller stays down and the manager never reports ready. Observed
	// on a live cluster: the pod sat at 0/1 indefinitely, logging "if kind is a
	// CRD, it should be installed before calling Start" every ten seconds.
	//
	// Polling instead costs at most TransferRequeue of latency on a transfer
	// measured in minutes, and it works whether Forklift is installed before
	// this controller, after it, or never.
	return ctrl.NewControllerManagedBy(mgr).
		For(&migrationv1alpha1.VMImportTask{}).
		Named("vmimporttask").
		Complete(r)
}

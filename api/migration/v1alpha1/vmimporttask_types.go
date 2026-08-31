// SPDX-License-Identifier: Apache-2.0
// Package v1alpha1 defines forklift.cozystack.io API types.
//
// Group: forklift.cozystack.io
// Version: v1alpha1
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion,
			&VMImportTask{},
			&VMImportTaskList{},
		)
		return nil
	})
}

const (
	// OwningTaskNameLabel and OwningTaskNamespaceLabel mark the Forklift objects
	// a task owns, so the controller can find its own scaffolding without
	// trusting a label an unrelated object might carry.
	OwningTaskNameLabel      = thisGroup + "/owned-by.VMImportTaskName"
	OwningTaskNamespaceLabel = thisGroup + "/owned-by.VMImportTaskNamespace"

	// OutputTaskUIDLabel and OutputVMIDLabel mark what a task produced: the
	// VMDisks, the VMInstances and the claims behind them. They are how the
	// controller tells its own earlier output from a tenant object that happens
	// to carry the same name, which every resume has to decide and cannot
	// otherwise know. Mistaking a tenant's disk for its own silently attaches
	// stale data while the fresh transfer is collected with the scaffolding VM;
	// mistaking its own output for a collision terminally fails an import that
	// in fact succeeded.
	//
	// Deliberately labels, not owner references: the outputs of a task outlive
	// the task, so nothing here may create a garbage-collection edge back to it.
	// The UID rather than the name, because names are reused.
	OutputTaskUIDLabel = thisGroup + "/output-of.TaskUID"
	OutputVMIDLabel    = thisGroup + "/output-of.VMID"

	// TaskFinalizer holds a deleted task open long enough to remove the
	// transfer volumes the engine created for it.
	//
	// Everything else a task owns goes with it through owner references, but
	// the engine's own DataVolumes carry none: it manages them by label, and
	// when the Plan they name is deleted there is nothing left to collect them.
	// A failed import that is then deleted therefore leaves a DataVolume
	// retrying the transfer against the source forever — observed live, still
	// running 37 minutes and 8 restarts after its task was gone.
	//
	// This finalizer does not contradict the outputs-outlive-the-task property:
	// it removes scaffolding, and never touches a VMDisk, VMInstance or the
	// claims behind them.
	TaskFinalizer = thisGroup + "/cleanup-transfer-volumes"

	// TaskValidatedCondition reports whether the task's inputs resolved: the
	// source is ready, every named VM exists in the inventory, and the storage
	// class can actually complete an import.
	TaskValidatedCondition = "Validated"
)

// Condition reasons carried on a VMImportTask.
const (
	// ReasonSourceNotReady means the referenced VMImportSource is absent or unusable.
	ReasonSourceNotReady = "SourceNotReady"
	// ReasonVMNotFound means a named VM is absent from the source inventory.
	ReasonVMNotFound = "VMNotFound"
	// ReasonStorageClassNotImmediate means the resolved StorageClass binds
	// WaitForFirstConsumer, which deadlocks an import: nothing consumes the
	// claim while it is being populated.
	ReasonStorageClassNotImmediate = "StorageClassNotImmediate"
	// ReasonOutputExists means an output object of the requested name is
	// already present. The controller never overwrites tenant objects.
	ReasonOutputExists = "OutputExists"
	// ReasonTransferFailed means Forklift reported the migration as failed.
	ReasonTransferFailed = "TransferFailed"
	// ReasonCompleted means every VM of the task was imported.
	ReasonCompleted = "Completed"
)

// VMImportTaskPhase represents the lifecycle phase of a VMImportTask.
// +kubebuilder:validation:Enum="";Pending;Validating;Transferring;Creating;Succeeded;Failed
type VMImportTaskPhase string

const (
	VMImportTaskPhaseEmpty VMImportTaskPhase = ""
	// VMImportTaskPhasePending means the task has not started: usually its
	// source is not ready yet.
	VMImportTaskPhasePending VMImportTaskPhase = "Pending"
	// VMImportTaskPhaseValidating means inputs are being resolved against the
	// source inventory. Nothing has been created yet.
	VMImportTaskPhaseValidating VMImportTaskPhase = "Validating"
	// VMImportTaskPhaseTransferring means disk data is moving.
	VMImportTaskPhaseTransferring VMImportTaskPhase = "Transferring"
	// VMImportTaskPhaseCreating means transfers finished and the Cozystack
	// objects are being created over the transferred volumes.
	VMImportTaskPhaseCreating VMImportTaskPhase = "Creating"
	// VMImportTaskPhaseSucceeded means every VM was imported.
	VMImportTaskPhaseSucceeded VMImportTaskPhase = "Succeeded"
	// VMImportTaskPhaseFailed means the task stopped with at least one VM unimported.
	VMImportTaskPhaseFailed VMImportTaskPhase = "Failed"
)

// IsTerminal reports whether a phase is an end state that will not change again.
func (p VMImportTaskPhase) IsTerminal() bool {
	return p == VMImportTaskPhaseSucceeded || p == VMImportTaskPhaseFailed
}

// VMImportRequest names one source VM to import and what to call the result.
type VMImportRequest struct {
	// ID is the VM's managed-object reference in the source inventory
	// (e.g. `vm-1234` on vSphere). It appears in the vSphere client's URL, and
	// `govc ls -i` prints it.
	// +kubebuilder:validation:MinLength=1
	ID string `json:"id"`

	// Name is the name of the VMInstance to create. Its disks are named after
	// it. Must be a valid Kubernetes object name; defaults to the source VM's
	// name normalized to one when omitted.
	//
	// Capped at 46 rather than the usual 63 because the name is not used alone:
	// each disk becomes `vm-disk-<name>-disk-<i>`, and that whole string is
	// stamped as the DataVolume's `app.kubernetes.io/instance` label value,
	// which Kubernetes limits to 63 characters. The fixed parts cost 14
	// (`vm-disk-` plus `-disk-`), leaving 49 to share between the name and the
	// disk index; 46 keeps a VM with up to a thousand disks inside the limit.
	// Overflowing it does not fail cleanly — the DataVolume is simply rejected
	// after the transfer has already run.
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=46
	Name string `json:"name,omitempty"`

	// InstanceType is the Cozystack instance type for the created VMInstance.
	// When omitted the controller sets explicit resources matching the source
	// VM's CPU and memory, so an import never silently resizes a guest.
	// +optional
	InstanceType string `json:"instanceType,omitempty"`

	// InstanceProfile is the Cozystack instance profile (guest preference set)
	// for the created VMInstance.
	// +optional
	InstanceProfile string `json:"instanceProfile,omitempty"`
}

// VMImportTaskSpec describes a one-shot import of one or more VMs.
type VMImportTaskSpec struct {
	// SourceRef names the VMImportSource to import from. It must live in this
	// task's own namespace.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="sourceRef is immutable"
	SourceRef corev1.LocalObjectReference `json:"sourceRef"`

	// VMs lists the source VMs to import. Each produces one VMInstance over
	// one VMDisk per source disk.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="vms is immutable"
	VMs []VMImportRequest `json:"vms"`

	// StorageClass is the StorageClass every disk of this task is imported
	// onto. It must bind Immediate: a WaitForFirstConsumer class deadlocks the
	// import, because nothing consumes the claim while it is populated. Empty
	// uses the cluster default, which is validated the same way.
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="storageClass is immutable"
	StorageClass string `json:"storageClass,omitempty"`
}

// VMImportStatus reports the progress of one VM within a task.
type VMImportStatus struct {
	// ID is the source VM's managed-object reference, matching the request.
	ID string `json:"id"`

	// Name is the name of the VMInstance being created for this VM.
	// +optional
	Name string `json:"name,omitempty"`

	// Phase is this VM's own lifecycle phase; a task's phase aggregates these.
	// +optional
	Phase VMImportTaskPhase `json:"phase,omitempty"`

	// Progress is the percentage of disk data transferred, 0-100.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	Progress int32 `json:"progress,omitempty"`

	// VMInstance is the name of the created VMInstance, once it exists.
	// +optional
	VMInstance string `json:"vmInstance,omitempty"`

	// Disks lists the names of the created VMDisks, in the source VM's disk order.
	// +optional
	Disks []string `json:"disks,omitempty"`

	// Message explains this VM's current phase when it needs explaining.
	// +optional
	Message string `json:"message,omitempty"`
}

// VMImportTaskStatus represents the observed state of a VMImportTask.
type VMImportTaskStatus struct {
	// Phase is a high-level summary of the task's state.
	// +optional
	Phase VMImportTaskPhase `json:"phase,omitempty"`

	// VMs reports per-VM progress, one entry per spec.vms entry.
	// +optional
	VMs []VMImportStatus `json:"vms,omitempty"`

	// StartedAt is the time the task began validating.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt is the time the task reached a terminal phase.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// Message is a human-readable message about the current phase.
	// +optional
	Message string `json:"message,omitempty"`

	// ObservedGeneration is the .metadata.generation the status was computed from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represents the latest available observations of the task's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=vmit;vmimporttask
// +kubebuilder:printcolumn:name="Source",type="string",JSONPath=".spec.sourceRef.name",priority=0
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",priority=0
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",priority=0
// +kubebuilder:printcolumn:name="Message",type="string",JSONPath=".status.message",priority=1
// +kubebuilder:selectablefield:JSONPath=`.spec.sourceRef.name`
// +kubebuilder:selectablefield:JSONPath=`.status.phase`
// +kubebuilder:metadata:annotations={"options.cozystack.io/source.sourceRef.name=vmimportsource","options.cozystack.io/source.storageClass=storageclass","options.cozystack.io/source.vms.instanceType=instancetype","options.cozystack.io/source.vms.instanceProfile=instanceprofile","options.cozystack.io/source.vms.id=vmimportvm.{sourceRef.name}"}
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 63",message="metadata.name must be at most 63 characters: the task's name is carried as a label value on the Forklift objects it owns, and Kubernetes limits label values to 63"

// VMImportTask runs a one-shot import of VMs from a registered source. It
// produces VMDisks and VMInstances that outlive it: deleting the task removes
// the migration machinery and leaves everything already imported in place.
type VMImportTask struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VMImportTaskSpec   `json:"spec,omitempty"`
	Status VMImportTaskStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// VMImportTaskList contains a list of VMImportTasks.
type VMImportTaskList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VMImportTask `json:"items"`
}

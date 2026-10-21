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
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
)

func TestDeviceIsAttachedWhileItsVMIRunsAndBoundAfterItEnds(t *testing.T) {
	v := vmiObj("vm1", "Scheduled", "n1", "")
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		device("a", "l40s", 1, bound("n1"), requested("r1", "vm1", t0, "a")),
		v,
	)
	e.pass()
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceAttached, "n1")

	_ = unstructured.SetNestedField(v.Object, "Succeeded", "status", "phase")
	e.update(v)
	e.pass()
	d := e.device("a")
	expectPlacement(t, d, gpuv1alpha1.GPUDeviceBound, "n1")
	if d.Status.AttachedTo != nil || d.Status.BindRequest != nil {
		t.Fatalf("attachment or request kept after the VMI finished: %+v", d.Status)
	}
}

func TestDeviceLeavesAttachedWhenItsVMIIsRemoved(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		device("a", "l40s", 1, bound("n1"), attached("vm1")),
	)
	e.pass()
	d := e.device("a")
	expectPlacement(t, d, gpuv1alpha1.GPUDeviceBound, "n1")
	if d.Status.AttachedTo != nil {
		t.Fatal("attachment kept after the VMI was removed")
	}
}

func TestAttachmentWhoseVMINeverAppearsIsClearedAfterTheGrace(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		device("a", "l40s", 1, bound("n1"), requested("r1", "vm1", t0, "a")),
	)
	e.pass()
	if e.device("a").Status.AttachedTo == nil {
		t.Fatal("attachment cleared inside the grace period")
	}
	e.now = t0.Add(3 * time.Minute)
	e.pass()
	if d := e.device("a"); d.Status.AttachedTo != nil || d.Status.BindRequest != nil {
		t.Fatalf("stale attachment kept: %+v", d.Status)
	}
}

func TestTemplateDeviceIsOwnedByItsVMIAndDeletedWhenTheVMIEnds(t *testing.T) {
	v := vmiObj("vm1", "Running", "n1", "")
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		device("vm1-gpu-0", "l40s", 1, template("vm1"), bound("n1"), requested("r1", "vm1", t0, "vm1-gpu-0")),
		v,
	)
	e.pass()
	d := e.device("vm1-gpu-0")
	if vmiOwner(d) != v.GetUID() {
		t.Fatalf("owner references %+v do not name the VMI", d.OwnerReferences)
	}
	expectPlacement(t, d, gpuv1alpha1.GPUDeviceAttached, "n1")

	_ = unstructured.SetNestedField(v.Object, "Failed", "status", "phase")
	e.update(v)
	e.pass()
	if !e.gone(&gpuv1alpha1.GPUDevice{ObjectMeta: d.ObjectMeta}) {
		t.Fatal("template device kept after its VMI finished")
	}
}

func TestTemplateDeviceWhoseVMINeverAppearsIsDeleted(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		device("vm1-gpu-0", "l40s", 1, template("vm1"), bound("n1"), requested("r1", "vm1", t0, "vm1-gpu-0")),
		device("vm2-gpu-0", "l40s", 1, template("vm2")),
	)
	e.pass()
	if e.gone(e.device("vm1-gpu-0")) {
		t.Fatal("deleted inside the grace period")
	}
	e.now = t0.Add(3 * time.Minute)
	e.pass()
	for _, name := range []string{"vm1-gpu-0", "vm2-gpu-0"} {
		if !e.gone(&gpuv1alpha1.GPUDevice{ObjectMeta: device(name, "l40s", 1).ObjectMeta}) {
			t.Fatalf("template device %s kept although its VMI never appeared", name)
		}
	}
}

func TestTemplateDeviceOfAnEarlierVMIIsNotTakenByItsSuccessor(t *testing.T) {
	d := device("vm1-gpu-0", "l40s", 1, template("vm1"), bound("n1"), attached("vm1"))
	d.OwnerReferences = []metav1.OwnerReference{{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance", Name: "vm1", UID: "old-uid"}}
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		d,
		vmiObj("vm1", "Running", "n1", ""),
	)
	e.pass()
	if !e.gone(d) {
		t.Fatal("a template device owned by the previous VMI was kept")
	}
}

func TestDeletedDeviceStaysWhileAVMIUsesIt(t *testing.T) {
	v := vmiObj("vm1", "Running", "n1", "")
	d := device("a", "l40s", 1, bound("n1"), attached("vm1"))
	e := newEnv(t, class("l40s", rnL40S), node("n1", map[string]int64{rnL40S: 2}), d, v)
	e.pass()
	e.delete(d)
	e.pass()
	got := e.device("a")
	expectPlacement(t, got, gpuv1alpha1.GPUDeviceAttached, "n1")
	if !e.recordExists("a") {
		t.Fatal("usage record dropped while the VMI still uses the deleted device")
	}

	_ = unstructured.SetNestedField(v.Object, "Succeeded", "status", "phase")
	e.update(v)
	e.pass()
	if !e.gone(d) {
		t.Fatal("deleted device kept after its VMI finished")
	}
	if e.recordExists("a") {
		t.Fatal("usage record outlived the device")
	}
}

func TestClassFinalizerHoldsWhileADeviceHoldsOrOwesGPUs(t *testing.T) {
	c := class("l40s", rnL40S)
	d := device("a", "l40s", 1, lost("dead"))
	e := newEnv(t, c, node("n1", nil), d)
	e.delete(c)
	e.pass()
	if !controllerutil.ContainsFinalizer(e.class("l40s"), gpu.FinalizerClass) {
		t.Fatal("class finalizer removed while a Lost device owes GPUs")
	}
	e.delete(d)
	e.pass()
	if !e.gone(c) {
		t.Fatal("class kept after its last device went")
	}
}

// With the feature disabled no KubeVirt kind is known, and finalizers and
// usage records still run, so a deletion never hangs.
func TestDisabledFeatureStillReleasesDeletedDevicesAndClasses(t *testing.T) {
	c := class("l40s", rnL40S)
	d := device("a", "l40s", 1, bound("n1"), attached("vm1"))
	o := device("order", "l40s", 1)
	e := newEnvWith(t, false, c, d, o, node("n1", map[string]int64{rnL40S: 2}))
	e.pass()
	if !e.recordExists("a") {
		t.Fatal("usage record missing with the feature disabled")
	}
	if got := e.device("order"); got.Status.Phase != "" {
		t.Fatalf("an order was placed with the feature disabled: %+v", got.Status)
	}
	e.delete(d)
	e.delete(o)
	e.delete(c)
	e.pass()
	if !e.gone(d) || !e.gone(c) {
		t.Fatal("a disabled feature hung a deletion")
	}
	if e.recordExists("a") {
		t.Fatal("usage record kept")
	}
}

func TestFinalizersAreAddedToNewObjects(t *testing.T) {
	c := class("l40s", rnL40S)
	c.Finalizers = nil
	d := device("a", "l40s", 1)
	d.Finalizers = nil
	e := newEnvWith(t, false, c, d)
	e.pass()
	if !controllerutil.ContainsFinalizer(e.class("l40s"), gpu.FinalizerClass) ||
		!controllerutil.ContainsFinalizer(e.device("a"), gpu.FinalizerDevice) {
		t.Fatal("finalizers not added")
	}
}

// With the feature off nothing watches VMIs, so the pass itself must come
// back for a deleted device a VMI still uses.
func TestDisabledFeatureComesBackForADeviceItsVMIStillUses(t *testing.T) {
	v := vmiObj("vm1", "Running", "n1", "")
	d := device("a", "l40s", 1, bound("n1"), attached("vm1"))
	e := newEnv(t, class("l40s", rnL40S), d, v)
	e.r.Config.Enabled = false
	e.delete(d)
	res, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey})
	if err != nil || res.RequeueAfter == 0 {
		t.Fatalf("no requeue while a deleted device waits for its VMI: %v, %+v", err, res)
	}
	if e.gone(d) {
		t.Fatal("released while the VMI runs")
	}
	_ = unstructured.SetNestedField(v.Object, "Succeeded", "status", "phase")
	e.update(v)
	e.pass()
	if !e.gone(d) {
		t.Fatal("not released after the VMI finished")
	}
}

// The manager's client reads unstructured objects from the API server, so
// the KubeVirt view must come from the cache the watches fill.
func TestKubeVirtObjectsAreReadThroughTheCache(t *testing.T) {
	e := newEnv(t, class("l40s", rnL40S), node("n1", map[string]int64{rnL40S: 2}),
		device("a", "l40s", 1, bound("n1"), requested("r1", "vm1", t0, "a")))
	cached := fake.NewClientBuilder().WithScheme(testScheme(true)).WithObjects(vmiObj("vm1", "Running", "n1", "")).Build()
	e.r.Cache = cached
	e.pass()
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceAttached, "n1")
}

// The binder answered, the webhook admitted the VMI, and the cache has not
// shown it yet: a deletion in that window must not release the GPU it is
// starting with.
func TestDeletedDeviceWithAFreshAttachmentWaitsForTheGrace(t *testing.T) {
	d := device("a", "l40s", 1, bound("n1"), requested("r1", "vm1", t0, "a"))
	d.Status.BindRequest.Result = gpuv1alpha1.BindSucceeded
	e := newEnv(t, class("l40s", rnL40S), node("n1", map[string]int64{rnL40S: 2}), d)
	e.delete(d)
	e.pass()
	if e.gone(d) {
		t.Fatal("released before its VMI could appear")
	}
	e.now = t0.Add(3 * time.Minute)
	e.pass()
	if !e.gone(d) {
		t.Fatal("kept after the attachment grace")
	}
}

func TestAFailedDeviceReleaseKeepsItsClassFinalizer(t *testing.T) {
	c := class("l40s", rnL40S)
	d := device("a", "l40s", 1, bound("n1"))
	failed := false
	cl := fake.NewClientBuilder().WithScheme(testScheme(true)).WithObjects(c, d, node("n1", map[string]int64{rnL40S: 1})).
		WithStatusSubresource(&gpuv1alpha1.GPUClass{}, &gpuv1alpha1.GPUDevice{}, &cozyv1alpha1.WorkloadMonitor{}).
		WithInterceptorFuncs(interceptor.Funcs{Update: func(ctx context.Context, w client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, ok := obj.(*gpuv1alpha1.GPUDevice); ok && !failed && !obj.GetDeletionTimestamp().IsZero() {
				failed = true
				return apierrors.NewInternalError(errors.New("etcd unavailable"))
			}
			return w.Update(ctx, obj, opts...)
		}}).Build()
	e := &env{t: t, c: cl, now: t0}
	e.r = &Reconciler{Client: cl, Reader: cl, Now: func() time.Time { return e.now },
		Config: gpu.Config{Enabled: true, LostCapacityGrace: time.Minute, AttachmentGrace: time.Minute}}
	e.delete(d)
	e.delete(c)
	_, _ = e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey})
	if e.gone(&gpuv1alpha1.GPUClass{ObjectMeta: c.ObjectMeta}) {
		t.Fatal("the class went although its device's release failed")
	}
}

// With the feature enabled before KubeVirt serves its kinds, as on a fresh
// install, there is no VMI to read: passes still run, so usage records,
// finalizers and placement do not wait for KubeVirt.
func TestEnabledFeatureRunsBeforeKubeVirtServesItsKinds(t *testing.T) {
	c := class("l40s", rnL40S)
	d := device("a", "l40s", 1, bound("n1"))
	o := device("order", "l40s", 1)
	e := newEnvWith(t, false, c, d, o, node("n1", map[string]int64{rnL40S: 2}))
	e.r.Config.Enabled = true
	// The manager's cache answers a kind no CRD serves with NoKindMatch.
	e.r.Cache = interceptor.NewClient(e.c.(client.WithWatch), interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if u, ok := list.(*unstructured.UnstructuredList); ok {
				gvk := u.GroupVersionKind()
				return &meta.NoKindMatchError{GroupKind: gvk.GroupKind(), SearchedVersions: []string{gvk.Version}}
			}
			return c.List(ctx, list, opts...)
		},
	})
	e.pass()
	if !e.recordExists("a") {
		t.Fatal("usage record missing before KubeVirt serves its kinds")
	}
	if got := e.device("order"); got.Status.Phase != gpuv1alpha1.GPUDeviceBound {
		t.Fatalf("an order was not placed before KubeVirt serves its kinds: %+v", got.Status)
	}
	e.delete(d)
	e.delete(c)
	e.pass()
	if !e.gone(d) {
		t.Fatal("a deletion hung until KubeVirt serves its kinds")
	}
}

// A kind KubeVirt served a moment ago can read as not served, such as
// while an API server's discovery refills after its restart. A device
// attached to a running VMI must not read that as a VMI that is gone.
func TestUnservedKubeVirtKindsDoNotDetachAttachedDevices(t *testing.T) {
	d := device("a", "l40s", 1, bound("n1"), attached("vm1"))
	d.Status.Phase = gpuv1alpha1.GPUDeviceAttached
	e := newEnvWith(t, false, class("l40s", rnL40S), d, node("n1", map[string]int64{rnL40S: 2}))
	e.r.Config.Enabled = true
	e.r.Cache = interceptor.NewClient(e.c.(client.WithWatch), interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if u, ok := list.(*unstructured.UnstructuredList); ok {
				gvk := u.GroupVersionKind()
				return &meta.NoKindMatchError{GroupKind: gvk.GroupKind(), SearchedVersions: []string{gvk.Version}}
			}
			return c.List(ctx, list, opts...)
		},
	})
	_, err := e.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: passKey})
	if err == nil || !strings.Contains(err.Error(), "GPUDevice "+ns+"/a is attached") {
		t.Fatalf("the pass must fail naming the attached device, got %v", err)
	}
	if got := e.device("a"); got.Status.AttachedTo == nil || got.Status.Phase != gpuv1alpha1.GPUDeviceAttached {
		t.Fatalf("an unserved KubeVirt kind detached a device: %+v", got.Status)
	}
}

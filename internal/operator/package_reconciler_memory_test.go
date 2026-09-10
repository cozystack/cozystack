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

package operator

import (
	"context"
	"fmt"
	"testing"
	"time"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func memoryReconciler(cl client.Client, limit string) *PackageReconciler {
	return &PackageReconciler{Client: cl, APIReader: cl,
		SystemNamespaceMemoryLimit:   resource.MustParse(limit),
		SystemNamespaceMemoryRequest: resource.MustParse("32Mi")}
}

func memoryReconcile(t *testing.T, cl client.Client, limit string) memoryDefaultState {
	t.Helper()
	// A new reconciler on every call models controller restart: only API state survives.
	r := memoryReconciler(cl, limit)
	if err := r.reconcileSystemDefaultsLimitRange(t.Context(), "cozy-monitoring"); err != nil {
		t.Fatal(err)
	}
	_, state, err := r.readMemoryDefaultState(t.Context(), "cozy-monitoring")
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func acknowledgeMemory(t *testing.T, cl client.Client, value string) {
	t.Helper()
	ns := &corev1.Namespace{}
	if err := cl.Get(t.Context(), types.NamespacedName{Name: "cozy-monitoring"}, ns); err != nil {
		t.Fatal(err)
	}
	if ns.Annotations == nil {
		ns.Annotations = map[string]string{}
	}
	ns.Annotations[MemoryAcknowledgementAnnotation] = value
	if err := cl.Update(t.Context(), ns); err != nil {
		t.Fatal(err)
	}
}

func requireMemoryHeld(t *testing.T, cl client.Client, state memoryDefaultState) {
	t.Helper()
	if state.Hold == "" || limitRangeExists(t, cl, "cozy-monitoring") {
		t.Fatalf("expected a durable hold without a LimitRange, got %+v", state)
	}
}

func TestMemoryLoweringSurvivesDefaultedPodDisappearanceAndRestart(t *testing.T) {
	for _, init := range []bool{false, true} {
		t.Run(fmt.Sprintf("init=%v", init), func(t *testing.T) {
			cl := memoryTestClientBuilder(limitRangeScheme(t)).Build()
			memoryReconcile(t, cl, "32Gi")
			// This is a persisted Pod after actual admission defaulting. A fake client
			// cannot execute LimitRanger; the live acceptance test verifies that step.
			pod := systemPod("database-1", "8Gi", "32Gi")
			if init {
				pod.Spec.InitContainers = pod.Spec.Containers
				pod.Spec.Containers = []corev1.Container{{Name: "main"}}
			}
			if err := cl.Create(t.Context(), pod); err != nil {
				t.Fatal(err)
			}
			held := memoryReconcile(t, cl, "4Gi")
			requireMemoryHeld(t, cl, held)
			if held.LastLimit != "32Gi" {
				t.Fatalf("forgot previous admission default: %+v", held)
			}
			if err := cl.Delete(t.Context(), pod); err != nil {
				t.Fatal(err)
			}
			after := memoryReconcile(t, cl, "4Gi")
			requireMemoryHeld(t, cl, after)
			if after.Hold != held.Hold {
				t.Fatal("restart or empty scan changed the hold")
			}
			// The replacement can now be admitted without the unsafe namespace default.
			pod.ResourceVersion = ""
			pod.Spec = systemPodSpec("database-1", "8Gi", "")
			if err := cl.Create(t.Context(), pod); err != nil {
				t.Fatal(err)
			}
			acknowledgeMemory(t, cl, after.acknowledgement())
			requireMemoryHeld(t, cl, memoryReconcile(t, cl, "4Gi"))
			// An ack cannot waive this concrete request-above-default failure.
			ns := &corev1.Namespace{}
			if err := cl.Get(t.Context(), types.NamespacedName{Name: "cozy-monitoring"}, ns); err != nil {
				t.Fatal(err)
			}
			if ns.Annotations[MemoryAcknowledgementAnnotation] != "" {
				t.Fatal("invalid acknowledgement was retained")
			}
		})
	}
}

func TestMemoryAcknowledgementIsBoundToHoldAndValue(t *testing.T) {
	cl := memoryTestClientBuilder(limitRangeScheme(t)).Build()
	memoryReconcile(t, cl, "32Gi")
	first := memoryReconcile(t, cl, "4Gi")
	acknowledgeMemory(t, cl, first.acknowledgement())
	second := memoryReconcile(t, cl, "2Gi")
	requireMemoryHeld(t, cl, second)
	if second.Hold == first.Hold || second.Target != "2Gi" {
		t.Fatalf("target change did not rotate hold: %+v", second)
	}
	// Returning to the old value cannot resurrect the old approval either.
	third := memoryReconcile(t, cl, "4Gi")
	requireMemoryHeld(t, cl, third)
	if third.Hold == first.Hold {
		t.Fatal("reused a previous hold generation")
	}
	acknowledgeMemory(t, cl, third.acknowledgement())
	released := memoryReconcile(t, cl, "4294967296")
	if released.Hold != "" || !limitRangeExists(t, cl, "cozy-monitoring") {
		t.Fatalf("matching acknowledgement did not release: %+v", released)
	}
	if got := limitRangeDefaultMemory(t, cl, "cozy-monitoring"); got.Cmp(resource.MustParse("4Gi")) != 0 {
		t.Fatalf("applied %s", got.String())
	}
	// A newly observed blocker at the same value needs a new acknowledgement.
	pod := systemPod("new-controller", "8Gi", "")
	if err := cl.Create(t.Context(), pod); err != nil {
		t.Fatal(err)
	}
	newHold := memoryReconcile(t, cl, "4Gi")
	requireMemoryHeld(t, cl, newHold)
	if err := cl.Delete(t.Context(), pod); err != nil {
		t.Fatal(err)
	}
	acknowledgeMemory(t, cl, third.acknowledgement())
	requireMemoryHeld(t, cl, memoryReconcile(t, cl, "4Gi"))
}

func TestMemoryBlockerRemovalRequiresAcknowledgement(t *testing.T) {
	for _, fixture := range []client.Object{
		systemPod("pod", "8Gi", ""),
		systemDeployment("deployment", 0, "8Gi", ""),
		systemStatefulSet("statefulset", 0, "8Gi", ""),
		systemDaemonSet("daemonset", "8Gi", ""),
		systemCronJob("cronjob", "8Gi", ""),
		systemJob("job", "8Gi", ""),
	} {
		t.Run(fmt.Sprintf("%T", fixture), func(t *testing.T) {
			cl := memoryTestClientBuilder(limitRangeScheme(t)).WithObjects(fixture).Build()
			held := memoryReconcile(t, cl, "4Gi")
			requireMemoryHeld(t, cl, held)
			if err := cl.Delete(t.Context(), fixture); err != nil {
				t.Fatal(err)
			}
			requireMemoryHeld(t, cl, memoryReconcile(t, cl, "4Gi"))
			acknowledgeMemory(t, cl, held.acknowledgement())
			if memoryReconcile(t, cl, "4Gi").Hold != "" || !limitRangeExists(t, cl, "cozy-monitoring") {
				t.Fatal("explicit recovery failed")
			}
		})
	}
}

func TestMemoryTemplateRemediationDoesNotBypassHold(t *testing.T) {
	deployment := systemDeployment("database", 0, "8Gi", "")
	cl := memoryTestClientBuilder(limitRangeScheme(t)).WithObjects(deployment).Build()
	held := memoryReconcile(t, cl, "4Gi")
	current := deployment.DeepCopy()
	if err := cl.Get(t.Context(), client.ObjectKeyFromObject(current), current); err != nil {
		t.Fatal(err)
	}
	current.Spec.Template.Spec.Containers[0].Resources.Limits = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("10Gi")}
	if err := cl.Update(t.Context(), current); err != nil {
		t.Fatal(err)
	}
	requireMemoryHeld(t, cl, memoryReconcile(t, cl, "4Gi"))
	acknowledgeMemory(t, cl, held.acknowledgement())
	if memoryReconcile(t, cl, "4Gi").Hold != "" {
		t.Fatal("remediated template could not be acknowledged")
	}
	if err := cl.Get(t.Context(), client.ObjectKeyFromObject(current), current); err != nil {
		t.Fatal(err)
	}
	if got := current.Spec.Template.Spec.Containers[0].Resources.Limits.Memory(); got.Cmp(resource.MustParse("10Gi")) != 0 {
		t.Fatal("explicit container limit changed")
	}
}

func TestMemoryDisablePreservesHistoryAndHolds(t *testing.T) {
	for _, heldFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("held=%v", heldFirst), func(t *testing.T) {
			cl := memoryTestClientBuilder(limitRangeScheme(t)).Build()
			memoryReconcile(t, cl, "32Gi")
			if heldFirst {
				memoryReconcile(t, cl, "4Gi")
			}
			if err := memoryReconciler(cl, "0").deleteManagedSystemDefaultsLimitRanges(t.Context()); err != nil {
				t.Fatal(err)
			}
			if limitRangeExists(t, cl, "cozy-monitoring") {
				t.Fatal("disable left the default")
			}
			requireMemoryHeld(t, cl, memoryReconcile(t, cl, "4Gi"))
		})
	}
}

func TestMemoryStateSurvivesNamespaceApply(t *testing.T) {
	cl := memoryTestClientBuilder(limitRangeScheme(t)).Build()
	memoryReconcile(t, cl, "32Gi")
	held := memoryReconcile(t, cl, "4Gi")
	acknowledgeMemory(t, cl, held.acknowledgement())
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cozy-monitoring",
		Labels:      map[string]string{"cozystack.io/system": "true"},
		Annotations: map[string]string{"helm.sh/resource-policy": "keep"}}}
	if err := memoryReconciler(cl, "4Gi").createOrUpdateNamespace(t.Context(), ns); err != nil {
		t.Fatal(err)
	}
	if got := memoryReconcile(t, cl, "4Gi"); got.Hold != "" {
		t.Fatalf("namespace SSA lost the acknowledgement/state: %+v", got)
	}
}

func TestMemoryCorruptStateNeverAuthorizesDefault(t *testing.T) {
	for _, raw := range []string{"", "null", "{}", "{", `{"version":2}`, `{"version":1,"lastLimit":"invalid"}`, `{"version":1,"hold":"a"}`} {
		t.Run(raw, func(t *testing.T) {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cozy-monitoring", Annotations: map[string]string{MemoryStateAnnotation: raw}}}
			cl := memoryTestClientBuilder(limitRangeScheme(t)).Build()
			if err := cl.Get(t.Context(), client.ObjectKeyFromObject(ns), ns); err != nil {
				t.Fatal(err)
			}
			ns.Annotations = map[string]string{MemoryStateAnnotation: raw}
			if err := cl.Update(t.Context(), ns); err != nil {
				t.Fatal(err)
			}
			if err := memoryReconciler(cl, "4Gi").reconcileSystemDefaultsLimitRange(t.Context(), ns.Name); err == nil {
				t.Fatal("corrupt state accepted")
			}
			if limitRangeExists(t, cl, ns.Name) {
				t.Fatal("default written despite corrupt state")
			}
		})
	}
}

func TestMemoryStateWriteFailureDoesNotWithdrawOrApply(t *testing.T) {
	pod := systemPod("database", "8Gi", "")
	cl := memoryTestClientBuilder(limitRangeScheme(t)).WithObjects(pod).WithInterceptorFuncs(interceptor.Funcs{
		Patch: func(_ context.Context, _ client.WithWatch, obj client.Object, _ client.Patch, _ ...client.PatchOption) error {
			return apierrors.NewConflict(corev1.Resource("namespaces"), obj.GetName(), fmt.Errorf("changed by administrator"))
		},
	}).Build()
	if err := memoryReconciler(cl, "4Gi").reconcileSystemDefaultsLimitRange(t.Context(), "cozy-monitoring"); !apierrors.IsConflict(err) {
		t.Fatalf("got %v, want conflict", err)
	}
	if limitRangeExists(t, cl, "cozy-monitoring") {
		t.Fatal("default written before durable decision")
	}
}

func TestMemoryAcknowledgementCannotWaiveForeignPolicy(t *testing.T) {
	cl := memoryTestClientBuilder(limitRangeScheme(t)).Build()
	memoryReconcile(t, cl, "32Gi")
	held := memoryReconcile(t, cl, "4Gi")
	foreign := &corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: "admin", Namespace: "cozy-monitoring"},
		Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{Type: corev1.LimitTypeContainer,
			Max: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")}}}}}
	if err := cl.Create(t.Context(), foreign); err != nil {
		t.Fatal(err)
	}
	acknowledgeMemory(t, cl, held.acknowledgement())
	requireMemoryHeld(t, cl, memoryReconcile(t, cl, "4Gi"))
}

func TestMemoryPolicyCreateDoesNotAdoptConcurrentForeignObject(t *testing.T) {
	cl := memoryTestClientBuilder(limitRangeScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*corev1.LimitRange); ok {
				foreign := obj.DeepCopyObject().(*corev1.LimitRange)
				foreign.Labels = map[string]string{managedByLabel: "administrator"}
				if err := c.Create(ctx, foreign); err != nil {
					return err
				}
			}
			return c.Create(ctx, obj, opts...)
		},
	}).Build()
	err := memoryReconciler(cl, "4Gi").reconcileSystemDefaultsLimitRange(t.Context(), "cozy-monitoring")
	if !apierrors.IsAlreadyExists(err) {
		t.Fatalf("got %v, want concurrent create conflict", err)
	}
	lr := &corev1.LimitRange{}
	if err := cl.Get(t.Context(), types.NamespacedName{Name: SystemDefaultsLimitRangeName, Namespace: "cozy-monitoring"}, lr); err != nil {
		t.Fatal(err)
	}
	if lr.Labels[managedByLabel] != "administrator" {
		t.Fatal("foreign LimitRange was adopted")
	}
}

func TestMemoryPolicyUpdateChecksFreshOwnershipVersion(t *testing.T) {
	scheme := limitRangeScheme(t)
	lr := memoryReconciler(nil, "4Gi").systemDefaultsLimitRange("cozy-monitoring", resource.MustParse("4Gi"))
	cl := memoryTestClientBuilder(scheme).WithObjects(lr).WithInterceptorFuncs(interceptor.Funcs{
		Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
			apply := obj.(*corev1ac.LimitRangeApplyConfiguration)
			if apply.ResourceVersion == nil || *apply.ResourceVersion == "" {
				t.Fatal("owned apply lacks resourceVersion precondition")
			}
			current := &corev1.LimitRange{}
			if err := c.Get(ctx, types.NamespacedName{Name: *apply.Name, Namespace: *apply.Namespace}, current); err != nil {
				return err
			}
			current.Labels[managedByLabel] = "administrator"
			if err := c.Update(ctx, current); err != nil {
				return err
			}
			return c.Apply(ctx, obj, opts...)
		},
	}).Build()
	err := memoryReconciler(cl, "32Gi").reconcileSystemDefaultsLimitRange(t.Context(), "cozy-monitoring")
	if !apierrors.IsConflict(err) {
		t.Fatalf("got %v, want resourceVersion conflict", err)
	}
}

func TestMemoryPolicyDeleteDoesNotRemoveReplacement(t *testing.T) {
	lr := memoryReconciler(nil, "4Gi").systemDefaultsLimitRange("cozy-monitoring", resource.MustParse("4Gi"))
	lr.UID = "original"
	cl := memoryTestClientBuilder(limitRangeScheme(t)).WithObjects(lr).WithInterceptorFuncs(interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if err := c.Delete(ctx, obj); err != nil {
				return err
			}
			replacement := lr.DeepCopy()
			replacement.UID = "replacement"
			replacement.ResourceVersion = ""
			replacement.Labels[managedByLabel] = "administrator"
			if err := c.Create(ctx, replacement); err != nil {
				return err
			}
			return c.Delete(ctx, obj, opts...)
		},
	}).Build()
	if err := memoryReconciler(cl, "0").deleteSystemDefaultsLimitRange(t.Context(), "cozy-monitoring"); !apierrors.IsConflict(err) {
		t.Fatalf("got %v, want delete precondition conflict", err)
	}
	if !limitRangeExists(t, cl, "cozy-monitoring") {
		t.Fatal("administrator's replacement was deleted")
	}
}

func TestMemoryCleanupWaitsForLastPackageTarget(t *testing.T) {
	scheme := limitRangeScheme(t)
	if err := cozyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objects := []client.Object{}
	for _, name := range []string{"first", "second"} {
		objects = append(objects,
			&cozyv1alpha1.Package{ObjectMeta: metav1.ObjectMeta{Name: name}},
			&cozyv1alpha1.PackageSource{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: cozyv1alpha1.PackageSourceSpec{
				Variants: []cozyv1alpha1.Variant{{Name: "default", Components: []cozyv1alpha1.Component{{Name: "component", Install: &cozyv1alpha1.ComponentInstall{Namespace: "cozy-monitoring"}}}}}}})
	}
	cl := memoryTestClientBuilder(scheme).WithObjects(objects...).Build()
	memoryReconcile(t, cl, "32Gi")
	for i, name := range []string{"first", "second"} {
		if err := cl.Delete(t.Context(), &cozyv1alpha1.Package{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
			t.Fatal(err)
		}
		if err := memoryReconciler(cl, "32Gi").cleanupUnusedMemoryDefaults(t.Context()); err != nil {
			t.Fatal(err)
		}
		if limitRangeExists(t, cl, "cozy-monitoring") != (i == 0) {
			t.Fatalf("wrong cleanup after removing %s", name)
		}
	}
	// Reinstalling after removal still remembers the larger default.
	requireMemoryHeld(t, cl, memoryReconcile(t, cl, "4Gi"))
}

func TestMemoryReconcileSchedulesQuietClusterEvaluation(t *testing.T) {
	scheme := limitRangeScheme(t)
	if err := cozyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	pkg := &cozyv1alpha1.Package{ObjectMeta: metav1.ObjectMeta{Name: "test"}}
	source := &cozyv1alpha1.PackageSource{ObjectMeta: metav1.ObjectMeta{Name: "test"}, Spec: cozyv1alpha1.PackageSourceSpec{Variants: []cozyv1alpha1.Variant{{Name: "default"}}}}
	cl := memoryTestClientBuilder(scheme).WithStatusSubresource(pkg).WithObjects(pkg, source).Build()
	r := memoryReconciler(cl, "32Gi")
	r.Scheme = scheme
	result, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: pkg.Name}})
	if err != nil {
		t.Fatal(err)
	}
	if result.RequeueAfter <= 0 || result.RequeueAfter > 5*time.Minute {
		t.Fatalf("no bounded reevaluation: %+v", result)
	}
}

func TestMemoryDisableStillWorksWithCorruptSafetyState(t *testing.T) {
	cl := memoryTestClientBuilder(limitRangeScheme(t)).Build()
	memoryReconcile(t, cl, "32Gi")
	ns := &corev1.Namespace{}
	if err := cl.Get(t.Context(), types.NamespacedName{Name: "cozy-monitoring"}, ns); err != nil {
		t.Fatal(err)
	}
	ns.Annotations[MemoryStateAnnotation] = "null"
	if err := cl.Update(t.Context(), ns); err != nil {
		t.Fatal(err)
	}
	if err := memoryReconciler(cl, "0").deleteManagedSystemDefaultsLimitRanges(t.Context()); err != nil {
		t.Fatal(err)
	}
	if limitRangeExists(t, cl, ns.Name) {
		t.Fatal("corrupt state prevented disable")
	}
	if err := memoryReconciler(cl, "4Gi").reconcileSystemDefaultsLimitRange(t.Context(), ns.Name); err == nil {
		t.Fatal("reenable discarded corrupt state")
	}
	if limitRangeExists(t, cl, ns.Name) {
		t.Fatal("reenable bypassed invalid state")
	}
}

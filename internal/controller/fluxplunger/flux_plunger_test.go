// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package fluxplunger

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	testNS   = "tenant-root"
	testName = "vm-disk-0"
	// helm-controller's finalizer; present on every reconciled HelmRelease and
	// required by the fake client for any object carrying a deletionTimestamp.
	helmFinalizer = "finalizers.fluxcd.io"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := helmv2.AddToScheme(s); err != nil {
		t.Fatalf("add helm scheme: %v", err)
	}
	return s
}

// The controller-runtime fake client does not persist metadata.managedFields, so
// the production ownership check (ownsSuspensionManagedFields, verified by
// TestOwnsSuspensionManagedFields and the envtest suite) cannot be exercised
// through it. Behavioural tests inject FluxPlunger.owns with testOwns, a stand-in
// that reads a plain annotation the fake client DOES persist, and ssaModelApply
// maintains that annotation with the same dynamics the apiserver gives
// managedFields: set on flux-plunger's suspend apply, dropped on its relinquish.
const testOwnedKey = "test.fluxplunger.cozystack.io/owned"

func testOwns(hr *helmv2.HelmRelease) bool {
	return hr.Annotations[testOwnedKey] == "true"
}

// ownedFixture marks a HelmRelease as suspended and owned by flux-plunger.
func ownedFixture(hr *helmv2.HelmRelease) *helmv2.HelmRelease {
	hr.Spec.Suspend = true
	if hr.Annotations == nil {
		hr.Annotations = map[string]string{}
	}
	hr.Annotations[testOwnedKey] = "true"
	return hr
}

// newPlunger builds the reconciler with the injected test ownership check.
func newPlunger(c client.Client) *FluxPlunger {
	return &FluxPlunger{Client: c, owns: testOwns}
}

// suspendManagedFields builds a managedFields entry for the pure-unit test of the
// real ownership check.
func suspendManagedFields(manager string, op metav1.ManagedFieldsOperationType) []metav1.ManagedFieldsEntry {
	return []metav1.ManagedFieldsEntry{{
		Manager:    manager,
		Operation:  op,
		APIVersion: helmv2.GroupVersion.String(),
		FieldsType: "FieldsV1",
		FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:suspend":{}}}`)},
	}}
}

// noDeployedReleasesHR builds a HelmRelease stuck with the "has no deployed
// releases" error that flux-plunger is meant to recover.
func noDeployedReleasesHR() *helmv2.HelmRelease {
	return &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  testNS,
			Name:       testName,
			Finalizers: []string{helmFinalizer},
		},
		Status: helmv2.HelmReleaseStatus{
			Conditions: []metav1.Condition{{
				Type:    "Ready",
				Status:  metav1.ConditionFalse,
				Reason:  "InstallFailed",
				Message: "Helm install failed: " + errorMessageNoDeployedReleases,
			}},
		},
	}
}

func helmReleaseSecret(version string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testNS,
			Name:      "sh.helm.release.v1." + testName + "." + version,
			Labels: map[string]string{
				"name":  testName,
				"owner": "helm",
			},
		},
		Type: "helm.sh/release.v1",
	}
}

func getHR(t *testing.T, c client.Client) *helmv2.HelmRelease {
	t.Helper()
	hr := &helmv2.HelmRelease{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: testName}, hr); err != nil {
		t.Fatalf("get HelmRelease: %v", err)
	}
	return hr
}

// expectWarning drains the recorded events and fails unless one is a Warning with
// the given reason.
func expectWarning(t *testing.T, rec *record.FakeRecorder, reason string) {
	t.Helper()
	var seen []string
	for {
		select {
		case e := <-rec.Events:
			if strings.HasPrefix(e, corev1.EventTypeWarning+" "+reason+" ") {
				return
			}
			seen = append(seen, e)
		default:
			t.Fatalf("expected a Warning %s event, got %q", reason, seen)
		}
	}
}

func reconcile(t *testing.T, r *FluxPlunger) {
	t.Helper()
	_, _ = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName},
	})
}

// ssaModelApply models, on the fake client, what a real apiserver does with a
// flux-plunger server-side apply of spec.suspend, recorded via the test ownership
// annotation instead of managedFields (which the fake client drops): apply
// suspend=true and set the owned annotation; on the relinquish apply (suspend
// omitted), revert suspend to false and drop the annotation. This is exactly the
// dynamics the build-tagged envtest suite (ssa_ownership_envtest_test.go) proves
// against a real apiserver: an apply of true takes ownership, and a sole-owner
// relinquish reverts to false and drops the owner. Any other patch passes through
// unchanged.
//
// LIMITATION: this models NET STATE only. It treats every flux-plunger apply that
// carries suspend=false OR omits the field as "clear the suspension", so it cannot
// distinguish a relinquish (omit) from a forced set-false — on a real apiserver a
// relinquish leaves a value another manager owns unchanged, while a forced apply
// moves it. A test that depends on that distinction must assert the request SHAPE
// via plungerApplyDetail (present/value/force), not the resulting object; a test
// that checks only the object state is blind to it and can pass against broken code.
func ssaModelApply(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	po := &client.PatchOptions{}
	po.ApplyOptions(opts)
	suspend, isPlunger := plungerApplySuspend(obj, patch, po)
	if !isPlunger {
		return cl.Patch(ctx, obj, patch, opts...)
	}
	// Model the net effect with a plain Get+Update rather than the real SSA apply:
	// the fake client's Apply strips unmanaged fields (it would drop a finalizer off
	// a deleting object and garbage-collect it), which is a fake-client artefact, not
	// apiserver behaviour. Get+Update preserves finalizers and status while applying
	// exactly the suspend/ownership transition.
	cur := &helmv2.HelmRelease{}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(obj), cur); err != nil {
		return err
	}
	if cur.Annotations == nil {
		cur.Annotations = map[string]string{}
	}
	switch {
	case suspend:
		cur.Spec.Suspend = true
		cur.Annotations[testOwnedKey] = "true"
	case cur.Annotations[testOwnedKey] == "true":
		// Relinquishing a suspension flux-plunger owns reverts it; one another
		// manager owns keeps its value.
		cur.Spec.Suspend = false
		delete(cur.Annotations, testOwnedKey)
	}
	// The fence finalizer follows the body, and, like the apiserver, a new
	// finalizer cannot be added to an object that is being deleted.
	wantFence := false
	for _, f := range obj.GetFinalizers() {
		if f == fenceFinalizer {
			wantFence = true
		}
	}
	hasFence := controllerutil.ContainsFinalizer(cur, fenceFinalizer)
	if wantFence && !hasFence {
		if !cur.DeletionTimestamp.IsZero() {
			return apierrors.NewForbidden(schema.GroupResource{Group: "helm.toolkit.fluxcd.io", Resource: "helmreleases"},
				cur.Name, errors.New("no new finalizers can be added if the object is being deleted"))
		}
		controllerutil.AddFinalizer(cur, fenceFinalizer)
	}
	if !wantFence && hasFence {
		controllerutil.RemoveFinalizer(cur, fenceFinalizer)
	}
	return cl.Update(ctx, cur)
}

// plungerApplyDetail reports the shape of a flux-plunger spec.suspend apply so a
// test can tell whether it forces ownership: acquire attempts an unforced apply
// (present, value true, force false) first and forces (force true) only over a
// field re-read as false. plungerApplySuspend does not expose the force flag; this
// does.
func plungerApplyDetail(obj client.Object, patch client.Patch, opts []client.PatchOption) (present, value, force, ok bool) {
	po := &client.PatchOptions{}
	po.ApplyOptions(opts)
	u, isU := obj.(*unstructured.Unstructured)
	if !isU || patch.Type() != types.ApplyPatchType || po.FieldManager != suspendFieldManager {
		return false, false, false, false
	}
	val, found, _ := unstructured.NestedBool(u.Object, "spec", "suspend")
	return found, val, po.Force != nil && *po.Force, true
}

// plungerApplySuspend reports whether a patch is flux-plunger's server-side apply
// of spec.suspend (the production applySuspend sends an unstructured body carrying
// only spec.suspend) and, if so, the applied value (false when the field is
// omitted — the relinquish). The failure-injection tests use it to fail exactly
// the suspend or the unsuspend step.
func plungerApplySuspend(obj client.Object, patch client.Patch, po *client.PatchOptions) (suspend bool, ok bool) {
	u, isU := obj.(*unstructured.Unstructured)
	if !isU || patch.Type() != types.ApplyPatchType || po.FieldManager != suspendFieldManager {
		return false, false
	}
	val, found, _ := unstructured.NestedBool(u.Object, "spec", "suspend")
	return found && val, true
}

// isPlungerApply is the boolean form used where the test only cares about a
// specific applied value.
func isPlungerApply(obj client.Object, patch client.Patch, opts []client.PatchOption, suspend bool) bool {
	po := &client.PatchOptions{}
	po.ApplyOptions(opts)
	got, ok := plungerApplySuspend(obj, patch, po)
	return ok && got == suspend
}

// TestOwnsSuspensionManagedFields pins the production ownership predicate directly
// on hand-crafted managedFields: only flux-plunger's own Apply of spec.suspend
// counts, not an operator Update of the same field, nor an apply that manages
// other fields. This is the check the behavioural tests inject a stand-in for.
func TestOwnsSuspensionManagedFields(t *testing.T) {
	cases := []struct {
		name string
		mf   []metav1.ManagedFieldsEntry
		want bool
	}{
		{"plunger apply owns", suspendManagedFields(suspendFieldManager, metav1.ManagedFieldsOperationApply), true},
		// Isolates the operation conjunct: same manager as the owning case but an
		// Update, which only the Operation==Apply term rejects.
		{"plunger update does not count", suspendManagedFields(suspendFieldManager, metav1.ManagedFieldsOperationUpdate), false},
		{"operator update does not count", suspendManagedFields("kubectl", metav1.ManagedFieldsOperationUpdate), false},
		{"another manager apply does not count", suspendManagedFields("helm-controller", metav1.ManagedFieldsOperationApply), false},
		// fieldManager (flux-client-side-apply) is a SHARED client-side manager: the
		// earlier flux-plunger releases, platform migrations and the tenant delete hook all
		// set spec.suspend under it, so it does NOT identify flux-plunger — its
		// ownership must never be read as flux-plunger's.
		{"shared client-side manager does not count", suspendManagedFields(fieldManager, metav1.ManagedFieldsOperationUpdate), false},
		{"no managed fields", nil, false},
		{"plunger apply of other fields", []metav1.ManagedFieldsEntry{{
			Manager: suspendFieldManager, Operation: metav1.ManagedFieldsOperationApply,
			FieldsType: "FieldsV1", FieldsV1: &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:chart":{}}}`)},
		}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hr := &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{ManagedFields: tc.mf}}
			if got := ownsSuspensionManagedFields(hr); got != tc.want {
				t.Fatalf("ownsSuspensionManagedFields=%v want %v", got, tc.want)
			}
		})
	}
}

// The normal recovery of a stuck release must delete the latest storage Secret,
// record the processed version and leave the HelmRelease unsuspended and no
// longer owned by flux-plunger.
func TestReconcile_HappyPathRecovery(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v3")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
	r := newPlunger(c)

	reconcile(t, r)

	got := getHR(t, c)
	if got.Spec.Suspend {
		t.Fatalf("recovery must end unsuspended, got suspend=true")
	}
	if testOwns(got) {
		t.Fatalf("recovery must relinquish ownership of the suspension")
	}
	if got.Annotations[annotationLastProcessedVersion] != "3" {
		t.Fatalf("recovery must record the processed version, got %q", got.Annotations[annotationLastProcessedVersion])
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: secret.Name}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("recovery must delete the latest Helm storage Secret, got err=%v", err)
	}
}

// If the release is suspended by someone else between flux-plunger's initial
// read and its own suspend write, recovery must abort without deleting the
// storage Secret: flux-plunger does not own that suspension.
func TestReconcile_AbortsWhenSuspendedConcurrently(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	hrGets := 0
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := cl.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				// The first Get is the top-of-reconcile read (must see suspend=false
				// so recovery starts). The second Get is suspendHelmRelease's re-fetch:
				// simulate an external actor having suspended it meanwhile (no plunger
				// ownership).
				if h, ok := obj.(*helmv2.HelmRelease); ok {
					hrGets++
					if hrGets == 2 {
						h.Spec.Suspend = true
					}
				}
				return nil
			},
			Patch: ssaModelApply,
		}).Build()
	r := newPlunger(c)

	reconcile(t, r)

	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: secret.Name}, &corev1.Secret{}); apierrors.IsNotFound(err) {
		t.Fatalf("recovery must not delete the storage Secret when it did not acquire the suspension")
	}
}

// If the release starts terminating after recovery already acquired the
// suspension (the delete lands at the pre-delete re-check), recovery must leave
// the storage Secret intact and clear its own suspension so helm-controller can
// uninstall the release cleanly.
func TestReconcile_TerminatingDuringRecoveryKeepsSecretAndClearsSuspension(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	hrGets := 0
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := cl.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				// Gets 1-3 are the top read, suspendHelmRelease's re-fetch and the
				// annotation re-fetch (all must see a live release so recovery starts and
				// acquires). The 4th Get is releaseIsTerminating's pre-delete re-check:
				// simulate the delete having landed by then.
				if h, ok := obj.(*helmv2.HelmRelease); ok {
					hrGets++
					if hrGets >= 4 {
						now := metav1.Now()
						h.DeletionTimestamp = &now
						h.Finalizers = []string{helmFinalizer}
					}
				}
				return nil
			},
			Patch: ssaModelApply,
		}).Build()
	r := newPlunger(c)

	reconcile(t, r)

	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: secret.Name}, &corev1.Secret{}); apierrors.IsNotFound(err) {
		t.Fatalf("terminating recovery must leave the storage Secret for helm-controller to uninstall")
	}
	got := getHR(t, c)
	if got.Spec.Suspend {
		t.Fatalf("terminating recovery must clear the suspension so helm-controller can uninstall, got suspend=true")
	}
}

// If the release starts terminating during recovery and clearing the suspension
// then fails, recovery must requeue with an error rather than return success:
// leaving an owned suspension on a terminating release is what makes
// helm-controller skip the uninstall and orphan the resources.
func TestReconcile_TerminatingDuringRecoveryRequeuesOnUnsuspendFailure(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	hrGets := 0
	unsuspendApplies := 0
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := cl.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				if h, ok := obj.(*helmv2.HelmRelease); ok {
					hrGets++
					if hrGets >= 4 {
						now := metav1.Now()
						h.DeletionTimestamp = &now
						h.Finalizers = []string{helmFinalizer}
					}
				}
				return nil
			},
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if isPlungerApply(obj, patch, opts, false) {
					unsuspendApplies++
					return errors.New("simulated unsuspend apply failure")
				}
				return ssaModelApply(ctx, cl, obj, patch, opts...)
			},
		}).Build()
	rec := record.NewFakeRecorder(8)
	r := newPlunger(c)
	r.Recorder = rec

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName}})
	if err == nil {
		t.Fatalf("a failed unsuspend on a terminating release must requeue with an error, got nil")
	}
	// The returned error requeues the retry, so the deferred fallback must not make a
	// second, redundant attempt (symmetric with the final-unsuspend path).
	if unsuspendApplies != 1 {
		t.Fatalf("want exactly 1 unsuspend attempt on the terminating path, got %d", unsuspendApplies)
	}
	expectWarning(t, rec, "UnsuspendFailed")
}

// If a delete arrives between the top-of-Reconcile deletion check and the
// re-fetch inside suspendHelmRelease, recovery must abort without suspending the
// now-terminating release or deleting its storage Secret.
func TestReconcile_AbortsWhenDeletedConcurrently(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	hrGets := 0
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := cl.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				if h, ok := obj.(*helmv2.HelmRelease); ok {
					hrGets++
					if hrGets == 2 {
						now := metav1.Now()
						h.DeletionTimestamp = &now
						h.Finalizers = []string{helmFinalizer}
					}
				}
				return nil
			},
			Patch: ssaModelApply,
		}).Build()
	r := newPlunger(c)

	reconcile(t, r)

	got := getHR(t, c)
	if got.Spec.Suspend {
		t.Fatalf("recovery must not suspend a release that started terminating, got suspend=true")
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: secret.Name}, &corev1.Secret{}); apierrors.IsNotFound(err) {
		t.Fatalf("recovery must not delete the storage Secret of a release that started terminating")
	}
}

// A HelmRelease deleted while flux-plunger owns its suspension must have that
// suspension cleared, otherwise helm-controller v1.5.x skips the uninstall.
func TestReconcile_DeletionClearsPlungerSuspension(t *testing.T) {
	hr := noDeployedReleasesHR()
	ownedFixture(hr)
	now := metav1.Now()
	hr.DeletionTimestamp = &now

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr).
		WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
	r := newPlunger(c)

	reconcile(t, r)

	got := getHR(t, c)
	if got.Spec.Suspend {
		t.Fatalf("suspension must be cleared on a deleted HelmRelease so uninstall can proceed, got suspend=true")
	}
}

// flux-plunger must never begin recovery (suspend) on a HelmRelease that is
// already terminating.
func TestReconcile_DoesNotSuspendTerminatingHelmRelease(t *testing.T) {
	hr := noDeployedReleasesHR()
	now := metav1.Now()
	hr.DeletionTimestamp = &now

	secret := helmReleaseSecret("v1")
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
	r := newPlunger(c)

	reconcile(t, r)

	got := getHR(t, c)
	if got.Spec.Suspend {
		t.Fatalf("flux-plunger must not suspend a terminating HelmRelease, got suspend=true")
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: secret.Name}, &corev1.Secret{}); err != nil {
		t.Fatalf("flux-plunger must not touch the Helm storage Secret of a terminating release: %v", err)
	}
}

// A recovery step that fails after flux-plunger has suspended the release must
// not leave it suspended, or a later delete would skip the uninstall.
func TestReconcile_FailedStepDoesNotLeaveSuspended(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: ssaModelApply,
			Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if _, ok := obj.(*corev1.Secret); ok {
					return errors.New("simulated secret delete failure")
				}
				return cl.Delete(ctx, obj, opts...)
			},
		}).Build()
	r := newPlunger(c)

	reconcile(t, r)

	got := getHR(t, c)
	if got.Spec.Suspend {
		t.Fatalf("a failed recovery step must clear the suspension flux-plunger owns, got suspend=true")
	}
}

// When a recovery step fails and the deferred unsuspend fails as well, flux-plunger
// is left holding a suspension it cannot clear: the state in which a delete orphans
// the release. That must surface as a Warning, not only a log line.
func TestReconcile_DeferredUnsuspendFailureEmitsWarning(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if isPlungerApply(obj, patch, opts, false) {
					return errors.New("simulated unsuspend apply failure")
				}
				return ssaModelApply(ctx, cl, obj, patch, opts...)
			},
			Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if _, ok := obj.(*corev1.Secret); ok {
					return errors.New("simulated secret delete failure")
				}
				return cl.Delete(ctx, obj, opts...)
			},
		}).Build()
	rec := record.NewFakeRecorder(8)
	r := newPlunger(c)
	r.Recorder = rec

	reconcile(t, r)

	expectWarning(t, rec, "UnsuspendFailed")
}

// A suspension flux-plunger owns but never recorded progress for (recovery
// crashed right after suspending) must be released on a later reconcile.
func TestReconcile_ReleasesOwnedSuspensionWithoutProgress(t *testing.T) {
	hr := noDeployedReleasesHR()
	ownedFixture(hr)

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr).
		WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
	r := newPlunger(c)

	reconcile(t, r)

	got := getHR(t, c)
	if got.Spec.Suspend {
		t.Fatalf("a suspension flux-plunger owns with no recorded progress must be released, got suspend=true")
	}
}

// A suspension flux-plunger does not own must be left untouched even when a stale
// processed-version annotation happens to match the latest revision: the
// annotation is never an ownership signal, only the managedFields owner is.
func TestReconcile_LeavesExternalSuspensionWithStaleProcessedVersion(t *testing.T) {
	hr := noDeployedReleasesHR()
	hr.Spec.Suspend = true // suspended by an operator: not flux-plunger-owned
	hr.Annotations = map[string]string{annotationLastProcessedVersion: "5"}
	secret := helmReleaseSecret("v4")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
	r := newPlunger(c)

	reconcile(t, r)

	got := getHR(t, c)
	if !got.Spec.Suspend {
		t.Fatalf("a stale processed-version match must not be treated as ownership; operator suspension was cleared")
	}
}

// Recovery must delete the LATEST (highest-version) storage Secret and record its
// version, even when several historical revision Secrets are present.
func TestReconcile_DeletesLatestOfMultipleSecrets(t *testing.T) {
	hr := noDeployedReleasesHR()
	oldest := helmReleaseSecret("v1")
	middle := helmReleaseSecret("v2")
	latest := helmReleaseSecret("v5")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, oldest, middle, latest).
		WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
	r := newPlunger(c)

	reconcile(t, r)

	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: latest.Name}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("recovery must delete the latest storage Secret (v5), err=%v", err)
	}
	for _, s := range []*corev1.Secret{oldest, middle} {
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: s.Name}, &corev1.Secret{}); err != nil {
			t.Fatalf("recovery must not delete a non-latest Secret (%s): %v", s.Name, err)
		}
	}
	got := getHR(t, c)
	if got.Annotations[annotationLastProcessedVersion] != "5" {
		t.Fatalf("recovery must record the latest processed version 5, got %q", got.Annotations[annotationLastProcessedVersion])
	}
}

// A HelmRelease that does not carry the "has no deployed releases" error must be
// left entirely alone.
func TestReconcile_SkipsReleaseWithoutTargetError(t *testing.T) {
	hr := noDeployedReleasesHR()
	hr.Status.Conditions[0].Message = "Helm upgrade failed: some unrelated error"
	secret := helmReleaseSecret("v1")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
	r := newPlunger(c)

	reconcile(t, r)

	got := getHR(t, c)
	if got.Spec.Suspend {
		t.Fatalf("a release without the target error must not be suspended")
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: secret.Name}, &corev1.Secret{}); apierrors.IsNotFound(err) {
		t.Fatalf("a release without the target error must not have its storage Secret deleted")
	}
}

// A HelmRelease with the target error but no Helm storage Secrets must be skipped
// cleanly (pins the empty-list guard, without which getLatestSecret would panic).
func TestReconcile_SkipsWhenNoStorageSecrets(t *testing.T) {
	hr := noDeployedReleasesHR()

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr).
		WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
	r := newPlunger(c)

	reconcile(t, r) // must not panic

	got := getHR(t, c)
	if got.Spec.Suspend {
		t.Fatalf("a release with no storage Secrets must not be suspended")
	}
}

// The "already processed" skip-guard: once a pass reclaimed a revision and
// recorded the processed version, the next reconcile (latestVersion+1 ==
// processedVersion) must NOT suspend or delete another Secret.
func TestReconcile_SkipsWhenAlreadyProcessed(t *testing.T) {
	hr := noDeployedReleasesHR()
	hr.Annotations = map[string]string{annotationLastProcessedVersion: "5"}
	secret := helmReleaseSecret("v4") // 4+1 == 5 (already processed)

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
	r := newPlunger(c)

	reconcile(t, r)

	got := getHR(t, c)
	if got.Spec.Suspend {
		t.Fatalf("an already-processed release must not be suspended again, got suspend=true")
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: secret.Name}, &corev1.Secret{}); apierrors.IsNotFound(err) {
		t.Fatalf("an already-processed release must not have another storage Secret deleted")
	}
}

// The converse of the skip-guard: a recorded version NOT exactly one above the
// latest surviving revision means recovery must proceed (pins the off-by-one).
func TestReconcile_ProcessesWhenProcessedVersionDoesNotMatch(t *testing.T) {
	hr := noDeployedReleasesHR()
	hr.Annotations = map[string]string{annotationLastProcessedVersion: "5"}
	secret := helmReleaseSecret("v5") // 5+1 == 6 != 5, so NOT already processed

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
	r := newPlunger(c)

	reconcile(t, r)

	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: secret.Name}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("a non-matching processed version must not skip recovery; the latest Secret should have been deleted, err=%v", err)
	}
	got := getHR(t, c)
	if got.Annotations[annotationLastProcessedVersion] != "5" {
		t.Fatalf("recovery must record the processed version 5, got %q", got.Annotations[annotationLastProcessedVersion])
	}
}

// Deleting a release an operator suspended (flux-plunger does not own it) must
// leave the suspension in place: the single most safety-relevant boundary,
// enforced by the caller gate and the ownership check inside unsuspendHelmRelease.
func TestReconcile_LeavesExternalSuspensionOnDeletion(t *testing.T) {
	hr := noDeployedReleasesHR()
	hr.Spec.Suspend = true // operator-owned: not flux-plunger
	now := metav1.Now()
	hr.DeletionTimestamp = &now

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr).
		WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
	r := newPlunger(c)

	reconcile(t, r)

	got := getHR(t, c)
	if !got.Spec.Suspend {
		t.Fatalf("flux-plunger must not clear an operator's suspension on deletion, got suspend=false")
	}
}

// Releasing a stuck owned suspension (crash-recovery branch) must requeue on
// failure, uniform with the other unsuspend call sites.
func TestReconcile_SuspendedBranchRequeuesOnUnsuspendFailure(t *testing.T) {
	hr := noDeployedReleasesHR()
	ownedFixture(hr)

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(hr).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if isPlungerApply(obj, patch, opts, false) {
					return errors.New("simulated unsuspend apply failure")
				}
				return ssaModelApply(ctx, cl, obj, patch, opts...)
			},
		}).Build()
	rec := record.NewFakeRecorder(8)
	r := newPlunger(c)
	r.Recorder = rec

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName}})
	if err == nil {
		t.Fatalf("a failed unsuspend in the crash-recovery branch must requeue with an error, got nil")
	}
	expectWarning(t, rec, "UnsuspendFailed")
}

// A failing processed-version annotation write must requeue with an error.
func TestReconcile_RecoveryRequeuesOnAnnotationFailure(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				// Fail only the annotation patch (a merge patch carrying the
				// processed-version annotation), not the suspend/unsuspend applies.
				if h, ok := obj.(*helmv2.HelmRelease); ok && patch.Type() != types.ApplyPatchType && h.Annotations[annotationLastProcessedVersion] != "" {
					return errors.New("simulated annotation patch failure")
				}
				return ssaModelApply(ctx, cl, obj, patch, opts...)
			},
		}).Build()
	r := newPlunger(c)

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName}})
	if err == nil {
		t.Fatalf("a failed processed-version annotation write must requeue with an error, got nil")
	}
}

// A failing final unsuspend on the recovery path must requeue with an error.
func TestReconcile_RecoveryRequeuesOnUnsuspendFailure(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if isPlungerApply(obj, patch, opts, false) {
					return errors.New("simulated unsuspend apply failure")
				}
				return ssaModelApply(ctx, cl, obj, patch, opts...)
			},
		}).Build()
	rec := record.NewFakeRecorder(8)
	r := newPlunger(c)
	r.Recorder = rec

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName}})
	if err == nil {
		t.Fatalf("a failed final unsuspend on the recovery path must requeue with an error, got nil")
	}
	expectWarning(t, rec, "UnsuspendFailed")
}

// On a terminating release owned by flux-plunger, a failed unsuspend must requeue.
func TestReconcile_DeletionRequeuesOnUnsuspendFailure(t *testing.T) {
	hr := noDeployedReleasesHR()
	ownedFixture(hr)
	now := metav1.Now()
	hr.DeletionTimestamp = &now

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(hr).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if isPlungerApply(obj, patch, opts, false) {
					return errors.New("simulated unsuspend apply failure")
				}
				return ssaModelApply(ctx, cl, obj, patch, opts...)
			},
		}).Build()
	rec := record.NewFakeRecorder(8)
	r := newPlunger(c)
	r.Recorder = rec

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName}})
	if err == nil {
		t.Fatalf("a failed unsuspend on a terminating release must requeue with an error, got nil")
	}
	expectWarning(t, rec, "UnsuspendFailed")
}

// A suspension flux-plunger does not own must be left untouched — including one an
// operator owns via a client-side Update of spec.suspend (managedFields shows the
// operator, not flux-plunger). This is the misattribution boundary, closed by the
// apiserver transferring the field away from flux-plunger (proven end to end in
// the envtest suite, V2/V3).
func TestReconcile_LeavesExternalSuspensionUntouched(t *testing.T) {
	hr := noDeployedReleasesHR()
	hr.Spec.Suspend = true // operator-owned: no test ownership marker

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr).
		WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
	r := newPlunger(c)

	reconcile(t, r)

	got := getHR(t, c)
	if !got.Spec.Suspend {
		t.Fatalf("flux-plunger must not clear a suspension it does not own, got suspend=false")
	}
}

// A delete that lands after flux-plunger acquires the suspension but before it
// deletes the storage Secret must abort the delete and clear the suspension.
func TestReconcile_AbortsWhenDeletedBeforeSecretDelete(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := cl.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				// Inject a delete once the processed-version annotation this recovery
				// just wrote is visible: that is the releaseIsTerminating re-fetch, right
				// before the storage Secret delete.
				if h, ok := obj.(*helmv2.HelmRelease); ok &&
					h.Annotations[annotationLastProcessedVersion] != "" && h.DeletionTimestamp == nil {
					now := metav1.Now()
					h.DeletionTimestamp = &now
					h.Finalizers = []string{helmFinalizer}
				}
				return nil
			},
			Patch: ssaModelApply,
		}).Build()
	r := newPlunger(c)

	reconcile(t, r)

	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: secret.Name}, &corev1.Secret{}); apierrors.IsNotFound(err) {
		t.Fatalf("recovery must not delete the storage Secret of a release that started terminating before the delete")
	}
	got := getHR(t, c)
	if got.Spec.Suspend {
		t.Fatalf("the suspension flux-plunger acquired must be cleared once the release starts terminating, got suspend=true")
	}
}

// The processed-version annotation is recorded BEFORE the storage Secret is
// deleted, so a failed annotation write must leave the Secret intact.
func TestReconcile_AnnotationFailureLeavesSecretIntact(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if h, ok := obj.(*helmv2.HelmRelease); ok && patch.Type() != types.ApplyPatchType && h.Annotations[annotationLastProcessedVersion] != "" {
					return errors.New("simulated annotation patch failure")
				}
				return ssaModelApply(ctx, cl, obj, patch, opts...)
			},
		}).Build()
	r := newPlunger(c)

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName}})
	if err == nil {
		t.Fatalf("a failed processed-version write must requeue with an error, got nil")
	}
	if getErr := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: secret.Name}, &corev1.Secret{}); apierrors.IsNotFound(getErr) {
		t.Fatalf("a failed annotation write must not delete the storage Secret (annotation is recorded before the delete)")
	}
}

// unsuspendHelmRelease's own ownership re-check (defense-in-depth for a future
// caller that forgets the caller-side gate): with suspend=true and no flux-plunger
// ownership, a direct call sends no apply at all and leaves the suspension. The
// apply count is what pins the guard; a relinquish could not clear another
// manager's value anyway, so the resulting object alone would not.
func TestUnsuspendHelmRelease_LeavesUnownedSuspension(t *testing.T) {
	hr := noDeployedReleasesHR()
	hr.Spec.Suspend = true // operator-owned: no flux-plunger managedFields

	applies := 0
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if _, _, _, ok := plungerApplyDetail(obj, patch, opts); ok {
					applies++
				}
				return ssaModelApply(ctx, cl, obj, patch, opts...)
			},
		}).Build()
	r := newPlunger(c)

	if err := r.unsuspendHelmRelease(context.Background(), hr); err != nil {
		t.Fatalf("unsuspendHelmRelease must not error on an unowned suspension: %v", err)
	}

	if applies != 0 {
		t.Fatalf("unsuspendHelmRelease must not apply on a suspension flux-plunger does not own, sent %d", applies)
	}
	if got := getHR(t, c); !got.Spec.Suspend {
		t.Fatalf("unsuspendHelmRelease must not clear a suspension flux-plunger does not own (callee-side guard)")
	}
}

// The watch predicate must wake a reconcile for the error state and for a
// flux-plunger-owned suspension, and must NOT wake for an operator-owned
// suspension or an unrelated release. This pins both terms of the predicate,
// which nothing exercised before (SetupWithManager was never called in a test).
func TestWatchPredicate(t *testing.T) {
	r := &FluxPlunger{owns: testOwns}
	withError := noDeployedReleasesHR()
	ownedSuspended := ownedFixture(noDeployedReleasesHR())
	ownedSuspended.Status.Conditions = nil // no error, only an owned suspension
	externalSuspended := noDeployedReleasesHR()
	externalSuspended.Status.Conditions = nil
	externalSuspended.Spec.Suspend = true // suspended, not owned
	quiet := noDeployedReleasesHR()
	quiet.Status.Conditions = nil // no error, not suspended
	// Owned marker but NOT suspended and no error: isolates the hr.Spec.Suspend
	// conjunct. Dropping it from the predicate would wake on ownsSuspension alone and
	// this case would (wrongly) match; with it, a release that is not suspended never
	// matches on the suspension arm.
	ownedNotSuspended := noDeployedReleasesHR()
	ownedNotSuspended.Status.Conditions = nil
	ownedNotSuspended.Annotations = map[string]string{testOwnedKey: "true"}
	// A healthy release an operator or a migration suspended, now being deleted:
	// neither the error arm nor the owned arm matches, yet this is the orphan state the
	// TerminatingWhileSuspended Warning exists for, so it must wake a reconcile.
	now := metav1.Now()
	terminatingExternal := noDeployedReleasesHR()
	terminatingExternal.Status.Conditions = nil
	terminatingExternal.Spec.Suspend = true
	terminatingExternal.DeletionTimestamp = &now
	terminatingUnsuspended := noDeployedReleasesHR()
	terminatingUnsuspended.Status.Conditions = nil
	terminatingUnsuspended.DeletionTimestamp = &now
	// Only the fence: the status write helm-controller makes after the uninstall
	// carries no other signal, and it is what lets flux-plunger lift the fence.
	fencedOnly := fencedFixture(noDeployedReleasesHR())
	fencedOnly.Status.Conditions = nil

	cases := []struct {
		name string
		obj  client.Object
		want bool
	}{
		{"has target error", withError, true},
		{"owned suspension, no error", ownedSuspended, true},
		{"external suspension is ignored", externalSuspended, false},
		{"no error and not suspended", quiet, false},
		{"owned marker but not suspended", ownedNotSuspended, false},
		{"terminating under an external suspension", terminatingExternal, true},
		{"terminating and not suspended", terminatingUnsuspended, false},
		{"fenced by flux-plunger", fencedOnly, true},
		{"non-HelmRelease object", &corev1.Secret{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.watchPredicate(tc.obj); got != tc.want {
				t.Fatalf("watchPredicate=%v want %v", got, tc.want)
			}
		})
	}
}

// On the final-unsuspend-failure path recovery sets completed=true before
// returning the error, so the deferred fallback does NOT run a second, redundant
// unsuspend (the requeue covers the retry). Pin that: exactly one unsuspend apply
// is attempted. Removing the completed=true makes the deferred fallback fire and
// this count become 2.
func TestReconcile_FinalUnsuspendFailureDoesNotRetryDeferred(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	unsuspendApplies := 0
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if isPlungerApply(obj, patch, opts, false) {
					unsuspendApplies++
					return errors.New("simulated unsuspend apply failure")
				}
				return ssaModelApply(ctx, cl, obj, patch, opts...)
			},
		}).Build()
	r := newPlunger(c)

	_, _ = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName}})
	if unsuspendApplies != 1 {
		t.Fatalf("a failed final unsuspend must not trigger the deferred retry; want 1 unsuspend attempt, got %d", unsuspendApplies)
	}
}

// The pre-delete termination re-check must read the uncached APIReader, not the
// informer cache: the cache can lag a delete the apiserver already accepted, and a
// termination check that misses that delete would delete the storage Secret of a
// terminating release and orphan it. Here the cached client sees a live release
// while the APIReader sees the delete; releaseIsTerminating must report terminating.
func TestReleaseIsTerminating_ReadsAPIReaderNotCache(t *testing.T) {
	cached := noDeployedReleasesHR() // cache: not terminating
	fresh := noDeployedReleasesHR()
	now := metav1.Now()
	fresh.DeletionTimestamp = &now // apiserver: delete already accepted
	fresh.Finalizers = []string{helmFinalizer}

	cachedClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(cached).Build()
	apiReader := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(fresh).Build()
	r := &FluxPlunger{Client: cachedClient, APIReader: apiReader, owns: testOwns}

	terminating, err := r.releaseIsTerminating(context.Background(), cached)
	if err != nil {
		t.Fatalf("releaseIsTerminating: %v", err)
	}
	if !terminating {
		t.Fatalf("releaseIsTerminating must read the uncached APIReader (delete already accepted), not the stale cache")
	}
}

// The already-processed guard compares the processed-version annotation against the
// latest storage Secret, and the Secret list is read from the apiserver. If the
// annotation came from a lagging cache instead, a reconcile right after a finished
// recovery would see the fresh "latest" (the revision below the one just deleted)
// but the old annotation, fail the guard, and delete that revision too — walking the
// release history down one Secret per reconcile. Here the cache has no annotation
// while the apiserver records processed=5 over the surviving v4: recovery must not
// start, so no suspend apply may be sent.
func TestReconcile_ProcessedGuardReadsAPIReaderNotCache(t *testing.T) {
	cached := noDeployedReleasesHR() // cache: annotation write not yet observed
	fresh := noDeployedReleasesHR()
	fresh.Annotations = map[string]string{annotationLastProcessedVersion: "5"}
	secret := helmReleaseSecret("v4")

	applies := 0
	cachedClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(cached, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if isPlungerApply(obj, patch, opts, true) {
					applies++
				}
				return ssaModelApply(ctx, cl, obj, patch, opts...)
			},
		}).Build()
	apiReader := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(fresh, secret).Build()
	r := &FluxPlunger{Client: cachedClient, APIReader: apiReader, owns: testOwns}

	reconcile(t, r)

	if applies != 0 {
		t.Fatalf("the processed guard must read the apiserver: v4 is already processed (annotation 5), yet %d suspend apply was sent", applies)
	}
}

// The storage-Secret List chooses which revision is deleted and which version the
// processed annotation records, so it must read the uncached APIReader like every
// other race-sensitive read: a cache lagging a just-created revision Secret would
// pick a stale "latest" and delete the wrong revision. Here the cache is empty while
// the APIReader sees the Secret; listHelmReleaseSecrets must return it.
func TestListHelmReleaseSecrets_ReadsAPIReaderNotCache(t *testing.T) {
	secret := helmReleaseSecret("v1")
	cachedClient := fake.NewClientBuilder().WithScheme(testScheme(t)).Build() // cache: no secret
	apiReader := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(secret).Build()
	r := &FluxPlunger{Client: cachedClient, APIReader: apiReader, owns: testOwns}

	got, err := r.listHelmReleaseSecrets(context.Background(), testNS, testName)
	if err != nil {
		t.Fatalf("listHelmReleaseSecrets: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("listHelmReleaseSecrets must read the uncached APIReader (cache is empty); got %d secrets want 1", len(got))
	}
}

// When flux-plunger declines to touch an externally-owned suspension it must emit
// an Event, so an operator can see and alert on it instead of grepping logs.
func TestReconcile_EmitsEventOnExternalSuspension(t *testing.T) {
	hr := noDeployedReleasesHR()
	hr.Spec.Suspend = true // suspended by an operator: not flux-plunger-owned

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr).
		WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
	rec := record.NewFakeRecorder(8)
	r := &FluxPlunger{Client: c, owns: testOwns, Recorder: rec}

	reconcile(t, r)

	select {
	case e := <-rec.Events:
		if !strings.Contains(e, "SuspensionNotOwned") {
			t.Fatalf("expected a SuspensionNotOwned event, got %q", e)
		}
	default:
		t.Fatalf("declining an externally-owned suspension must emit an Event, got none")
	}
}

// A non-conflict error while acquiring the suspension must requeue with an error.
func TestReconcile_SuspendFailureRequeuesOnNonConflictError(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if isPlungerApply(obj, patch, opts, true) {
					return errors.New("simulated transient suspend failure")
				}
				return ssaModelApply(ctx, cl, obj, patch, opts...)
			},
		}).Build()
	r := newPlunger(c)

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName}})
	if err == nil {
		t.Fatalf("a non-conflict suspend failure must requeue with an error, got nil")
	}
}

// A conflict on the forced retry as well means another manager wrote the release
// again after the re-read: the same race as a foreign suspension caught on the
// unforced attempt, so it gets the same treatment: recovery is deferred with a
// RecoveryDeferred Event and no error, instead of a rate-limited backoff whose
// choice depends only on which of the two applies the write collided with. That
// write is itself an update event, so the next reconcile follows. Nothing is
// acquired, so the storage Secret stays intact.
func TestReconcile_ForcedAcquireConflictDefers(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				// Conflict on every acquire apply, forced included: a foreign write lands
				// before each attempt.
				if isPlungerApply(obj, patch, opts, true) {
					return apierrors.NewConflict(
						schema.GroupResource{Group: "helm.toolkit.fluxcd.io", Resource: "helmreleases"},
						testName, errors.New("the object has been modified"))
				}
				return ssaModelApply(ctx, cl, obj, patch, opts...)
			},
		}).Build()
	rec := record.NewFakeRecorder(8)
	r := &FluxPlunger{Client: c, owns: testOwns, Recorder: rec}

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName}})
	if err != nil {
		t.Fatalf("a conflict on the forced acquire must defer recovery, not requeue with an error: %v", err)
	}
	select {
	case e := <-rec.Events:
		if !strings.Contains(e, "RecoveryDeferred") {
			t.Fatalf("expected a RecoveryDeferred event, got %q", e)
		}
	default:
		t.Fatalf("a conflict on the forced acquire must emit RecoveryDeferred, got no event")
	}
	if getErr := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: secret.Name}, &corev1.Secret{}); apierrors.IsNotFound(getErr) {
		t.Fatalf("a conflicted suspend acquires nothing, so it must not delete the storage Secret")
	}
}

// A non-conflict failure of the forced acquire (an admission webhook rejecting it,
// or an API error) produces no follow-up event, so it must requeue with an error
// rather than be treated as a deferral.
func TestReconcile_ForcedAcquireNonConflictErrorRequeues(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	conflicted := false
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				present, value, force, ok := plungerApplyDetail(obj, patch, opts)
				if ok && present && value && !force && !conflicted {
					conflicted = true
					return apierrors.NewConflict(
						schema.GroupResource{Group: "helm.toolkit.fluxcd.io", Resource: "helmreleases"},
						testName, errors.New("stale ownership at false"))
				}
				if ok && present && value && force {
					return apierrors.NewForbidden(
						schema.GroupResource{Group: "helm.toolkit.fluxcd.io", Resource: "helmreleases"},
						testName, errors.New("admission webhook denied the request"))
				}
				return ssaModelApply(ctx, cl, obj, patch, opts...)
			},
		}).Build()
	r := newPlunger(c)

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName}})
	if err == nil {
		t.Fatalf("a non-conflict failure of the forced acquire must requeue with an error, got nil")
	}
}

// Acquire re-fetches on conflict and forces ONLY over a field still read as false.
// Here the conflicting write suspended the release (an operator, or a backup
// mid-restore): the re-fetch sees suspend=true, so acquire must abort WITHOUT a
// forced apply and must not delete the storage Secret.
func TestReconcile_AcquireDoesNotForceActiveForeignSuspension(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	var sawForce bool
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				present, value, force, ok := plungerApplyDetail(obj, patch, opts)
				if ok && present && value && force {
					sawForce = true
				}
				// The unforced acquire loses the race: a foreign manager suspends the
				// release, then our apply conflicts. Model both: write suspend=true to the
				// store so the post-conflict re-fetch sees it, then return the conflict.
				if ok && present && value && !force {
					cur := &helmv2.HelmRelease{}
					if err := cl.Get(ctx, client.ObjectKeyFromObject(obj), cur); err != nil {
						return err
					}
					cur.Spec.Suspend = true
					if err := cl.Update(ctx, cur); err != nil {
						return err
					}
					return apierrors.NewConflict(
						schema.GroupResource{Group: "helm.toolkit.fluxcd.io", Resource: "helmreleases"},
						testName, errors.New("foreign suspend won the race"))
				}
				return ssaModelApply(ctx, cl, obj, patch, opts...)
			},
		}).Build()
	r := newPlunger(c)

	reconcile(t, r)

	if sawForce {
		t.Fatalf("acquire must not force over a suspension it re-read as active (true)")
	}
	if getErr := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: secret.Name}, &corev1.Secret{}); apierrors.IsNotFound(getErr) {
		t.Fatalf("an aborted acquire must not delete the storage Secret")
	}
}

// The complementary case: the conflict came from a manager owning spec.suspend at
// FALSE (a backup controller that resumed, or an earlier flux-plunger release). The re-fetch
// sees suspend=false, so acquire forces over the stale ownership and recovery
// proceeds — the storage Secret is deleted.
func TestReconcile_AcquireForcesStaleForeignSuspension(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	var sawForce bool
	conflicted := false
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				present, value, force, ok := plungerApplyDetail(obj, patch, opts)
				// The unforced acquire conflicts once (foreign manager owns the field at
				// false); the value is left false, so the re-fetch reads false.
				if ok && present && value && !force && !conflicted {
					conflicted = true
					return apierrors.NewConflict(
						schema.GroupResource{Group: "helm.toolkit.fluxcd.io", Resource: "helmreleases"},
						testName, errors.New("stale ownership at false"))
				}
				if ok && present && value && force {
					sawForce = true
				}
				return ssaModelApply(ctx, cl, obj, patch, opts...)
			},
		}).Build()
	r := newPlunger(c)

	reconcile(t, r)

	if !sawForce {
		t.Fatalf("acquire must force over stale ownership re-read as false; no forced apply seen")
	}
	if getErr := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: secret.Name}, &corev1.Secret{}); !apierrors.IsNotFound(getErr) {
		t.Fatalf("after forcing acquisition recovery must delete the storage Secret")
	}
}

// Both acquire applies, the unforced first attempt and the forced retry, must carry
// the resourceVersion of the read that decided spec.suspend was false, so the
// apiserver rejects either with a conflict if another manager wrote the release in
// between. Without it the unforced apply of true over a foreign true is accepted as
// shared ownership and flux-plunger proceeds as if it had acquired a suspension a
// restore is holding. Pin that each is preconditioned on the stored resourceVersion.
func TestReconcile_AcquireAppliesCarryResourceVersion(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	conflicted := false
	var sawUnforced, sawForce bool
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				present, value, force, ok := plungerApplyDetail(obj, patch, opts)
				if ok && present && value {
					cur := &helmv2.HelmRelease{}
					if err := cl.Get(ctx, client.ObjectKeyFromObject(obj), cur); err != nil {
						return err
					}
					if got := obj.GetResourceVersion(); got == "" || got != cur.ResourceVersion {
						t.Fatalf("acquire apply (force=%v) must be preconditioned on the read resourceVersion %q, got %q", force, cur.ResourceVersion, got)
					}
					if !controllerutil.ContainsFinalizer(obj, fenceFinalizer) {
						t.Fatalf("acquire apply (force=%v) must carry the fence finalizer, got %v", force, obj.GetFinalizers())
					}
				}
				if ok && present && value && !force && !conflicted {
					sawUnforced = true
					conflicted = true
					return apierrors.NewConflict(
						schema.GroupResource{Group: "helm.toolkit.fluxcd.io", Resource: "helmreleases"},
						testName, errors.New("stale ownership at false"))
				}
				if ok && present && value && force {
					sawForce = true
				}
				return ssaModelApply(ctx, cl, obj, patch, opts...)
			},
		}).Build()
	r := newPlunger(c)

	reconcile(t, r)

	if !sawUnforced || !sawForce {
		t.Fatalf("expected an unforced then a forced acquire; unforced=%v forced=%v", sawUnforced, sawForce)
	}
}

// A delete that lands while the unforced acquire is conflicting must stop the
// forced retry: suspending a terminating release is what makes helm-controller skip
// the uninstall. The re-read after the conflict sees the deletionTimestamp, so no
// forced apply may be sent and the storage Secret stays intact.
func TestReconcile_AcquireAbortsWhenTerminatingAfterConflict(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	var sawForce bool
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				present, value, force, ok := plungerApplyDetail(obj, patch, opts)
				if ok && present && value && force {
					sawForce = true
				}
				if ok && present && value && !force {
					// The release is deleted (it keeps helm-controller's finalizer, so it
					// becomes terminating) and the unforced acquire conflicts.
					cur := &helmv2.HelmRelease{}
					if err := cl.Get(ctx, client.ObjectKeyFromObject(obj), cur); err != nil {
						return err
					}
					if err := cl.Delete(ctx, cur); err != nil {
						return err
					}
					return apierrors.NewConflict(
						schema.GroupResource{Group: "helm.toolkit.fluxcd.io", Resource: "helmreleases"},
						testName, errors.New("stale ownership at false"))
				}
				return ssaModelApply(ctx, cl, obj, patch, opts...)
			},
		}).Build()
	r := newPlunger(c)

	reconcile(t, r)

	if sawForce {
		t.Fatalf("acquire must not force a suspension onto a release that began terminating during the conflict")
	}
	if getErr := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: secret.Name}, &corev1.Secret{}); apierrors.IsNotFound(getErr) {
		t.Fatalf("an aborted acquire must not delete the storage Secret")
	}
}

// A release terminating while suspended by a manager flux-plunger does NOT own is
// the orphan state itself; flux-plunger cannot clear it but must surface it with a
// Warning Event, not only a log line, so an operator can intervene.
func TestReconcile_EmitsEventOnTerminatingExternalSuspension(t *testing.T) {
	hr := noDeployedReleasesHR()
	hr.Spec.Suspend = true // suspended by an external manager: not flux-plunger-owned
	now := metav1.Now()
	hr.DeletionTimestamp = &now

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr).
		WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
	rec := record.NewFakeRecorder(8)
	r := &FluxPlunger{Client: c, owns: testOwns, Recorder: rec}

	reconcile(t, r)

	select {
	case e := <-rec.Events:
		if !strings.Contains(e, "TerminatingWhileSuspended") {
			t.Fatalf("expected a TerminatingWhileSuspended event, got %q", e)
		}
	default:
		t.Fatalf("a terminating release under an external suspension must emit an Event, got none")
	}
}

// When recovery is deferred because the suspension was taken by another manager
// between read and write, flux-plunger must emit an Event, symmetric with the
// externally-owned-suspension branch, not only log.
func TestReconcile_EmitsEventOnDeferredRecovery(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				// The unforced acquire loses the race to a foreign suspension.
				if present, value, force, ok := plungerApplyDetail(obj, patch, opts); ok && present && value && !force {
					cur := &helmv2.HelmRelease{}
					if err := cl.Get(ctx, client.ObjectKeyFromObject(obj), cur); err != nil {
						return err
					}
					cur.Spec.Suspend = true
					if err := cl.Update(ctx, cur); err != nil {
						return err
					}
					return apierrors.NewConflict(
						schema.GroupResource{Group: "helm.toolkit.fluxcd.io", Resource: "helmreleases"},
						testName, errors.New("foreign suspend won the race"))
				}
				return ssaModelApply(ctx, cl, obj, patch, opts...)
			},
		}).Build()
	rec := record.NewFakeRecorder(8)
	r := &FluxPlunger{Client: c, owns: testOwns, Recorder: rec}

	reconcile(t, r)

	select {
	case e := <-rec.Events:
		if !strings.Contains(e, "RecoveryDeferred") {
			t.Fatalf("expected a RecoveryDeferred event, got %q", e)
		}
	default:
		t.Fatalf("a deferred recovery must emit an Event, got none")
	}
}

// fencedFixture marks a HelmRelease as held by flux-plunger's fence finalizer.
func fencedFixture(hr *helmv2.HelmRelease) *helmv2.HelmRelease {
	controllerutil.AddFinalizer(hr, fenceFinalizer)
	return hr
}

// terminatingFixture marks a HelmRelease as deleted; the fake client keeps it only
// while a finalizer remains.
func terminatingFixture(hr *helmv2.HelmRelease) *helmv2.HelmRelease {
	now := metav1.Now()
	hr.DeletionTimestamp = &now
	return hr
}

// hrExists reports whether the test HelmRelease is still stored.
func hrExists(t *testing.T, c client.Client) (*helmv2.HelmRelease, bool) {
	t.Helper()
	hr := &helmv2.HelmRelease{}
	err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: testName}, hr)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("get HelmRelease: %v", err)
	}
	return hr, true
}

// The acquire must set the fence in the same apply as the suspension, on both
// attempts, so there is no moment at which flux-plunger holds a suspension a delete
// could slip past. A completed recovery lifts both together.
func TestReconcile_AcquireSetsFenceWithSuspension(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	acquires := 0
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if present, value, _, ok := plungerApplyDetail(obj, patch, opts); ok && present && value {
					acquires++
					if !controllerutil.ContainsFinalizer(obj, fenceFinalizer) {
						t.Fatalf("the acquire apply must carry the fence finalizer, got finalizers %v", obj.GetFinalizers())
					}
				}
				return ssaModelApply(ctx, cl, obj, patch, opts...)
			},
		}).Build()
	r := newPlunger(c)

	reconcile(t, r)

	if acquires == 0 {
		t.Fatalf("expected an acquire apply")
	}
	got, ok := hrExists(t, c)
	if !ok {
		t.Fatalf("a completed recovery must leave the release in place")
	}
	if got.Spec.Suspend || controllerutil.ContainsFinalizer(got, fenceFinalizer) {
		t.Fatalf("a completed recovery must lift both the suspension and the fence; suspend=%v finalizers=%v", got.Spec.Suspend, got.Finalizers)
	}
}

// A delete on a release flux-plunger holds suspended must lift the suspension but
// keep the fence: helm-controller may already have dropped its own finalizer
// without an uninstall, and the fence is what keeps the object until it reconciles
// the unsuspended release.
func TestReconcile_TerminatingOwnedSuspensionKeepsFence(t *testing.T) {
	hr := terminatingFixture(fencedFixture(ownedFixture(noDeployedReleasesHR())))
	// The state observed on a live helm-controller v1.5.0 right after it handled the
	// delete of the suspended release: its finalizer is gone and it has observed the
	// current generation, yet nothing was uninstalled. The fence must not be lifted
	// on that; only the suspension is.
	hr.Finalizers = []string{fenceFinalizer}
	hr.Generation = 3
	hr.Status.ObservedGeneration = 3

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr).
		WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
	r := newPlunger(c)

	reconcile(t, r)

	got, ok := hrExists(t, c)
	if !ok {
		t.Fatalf("the fence must keep a terminating release until the uninstall runs")
	}
	if got.Spec.Suspend {
		t.Fatalf("the suspension must be lifted so helm-controller uninstalls")
	}
	if !controllerutil.ContainsFinalizer(got, fenceFinalizer) {
		t.Fatalf("the fence must stay until helm-controller has handled the unsuspended release")
	}
}

// uninstallReached is the condition for lifting the fence: helm-controller's
// finalizer gone AND the latest generation observed. Each conjunct on its own is
// not enough: the finalizer is also dropped, without an uninstall, while the
// release is suspended.
func TestUninstallReached(t *testing.T) {
	mk := func(helmFinalizer bool, gen, observed int64) *helmv2.HelmRelease {
		hr := noDeployedReleasesHR()
		hr.Finalizers = nil
		if helmFinalizer {
			hr.Finalizers = []string{helmv2.HelmReleaseFinalizer}
		}
		hr.Generation = gen
		hr.Status.ObservedGeneration = observed
		return hr
	}
	cases := []struct {
		name string
		hr   *helmv2.HelmRelease
		want bool
	}{
		{"finalizer gone and generation observed", mk(false, 4, 4), true},
		{"finalizer gone, unsuspended generation not yet observed", mk(false, 4, 3), false},
		{"generation observed, finalizer still held", mk(true, 4, 4), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := uninstallReached(tc.hr); got != tc.want {
				t.Fatalf("uninstallReached=%v want %v", got, tc.want)
			}
		})
	}
}

// Once helm-controller has handled the unsuspended release the fence is lifted and
// the release goes; until then it stays.
func TestReconcile_LiftsFenceOnlyOnceUninstallReached(t *testing.T) {
	cases := []struct {
		name     string
		observed int64
		wantGone bool
	}{
		{"uninstall reached", 4, true},
		{"unsuspended generation not yet observed", 3, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hr := terminatingFixture(noDeployedReleasesHR())
			hr.Finalizers = []string{fenceFinalizer}
			hr.Generation = 4
			hr.Status.ObservedGeneration = tc.observed

			c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr).
				WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
			r := newPlunger(c)

			reconcile(t, r)

			if _, exists := hrExists(t, c); exists == tc.wantGone {
				t.Fatalf("release exists=%v, want gone=%v", exists, tc.wantGone)
			}
		})
	}
}

// A release being deleted under another manager's suspension is that manager's
// decision (a migration suspends then deletes to leave the resources in place):
// flux-plunger must not keep the object with its fence, must leave the suspension
// alone, and records it.
func TestReconcile_TerminatingForeignSuspensionReleasesFence(t *testing.T) {
	hr := terminatingFixture(fencedFixture(noDeployedReleasesHR()))
	hr.Spec.Suspend = true // not flux-plunger's

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr).
		WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
	rec := record.NewFakeRecorder(8)
	r := &FluxPlunger{Client: c, owns: testOwns, Recorder: rec}

	reconcile(t, r)

	got, ok := hrExists(t, c)
	if !ok {
		t.Fatalf("helm-controller's finalizer still holds the release in this fixture")
	}
	if controllerutil.ContainsFinalizer(got, fenceFinalizer) {
		t.Fatalf("the fence must not hold a release under another manager's suspension")
	}
	if !got.Spec.Suspend {
		t.Fatalf("another manager's suspension must be left alone")
	}
	select {
	case e := <-rec.Events:
		if !strings.HasPrefix(e, corev1.EventTypeNormal+" TerminatingWhileSuspended ") {
			t.Fatalf("expected a Normal TerminatingWhileSuspended event, got %q", e)
		}
	default:
		t.Fatalf("expected a TerminatingWhileSuspended event, got none")
	}
}

// A fenced, terminating, unsuspended release that helm-controller has not handled
// yet is held by flux-plunger's fence. That hold must not be silent: the reconcile
// requeues itself so it is re-checked without waiting for an event, and once the
// delete has been held past the deadline it raises a Warning an operator can find
// before a `kubectl delete` that never returns.
func TestReconcile_HeldFenceRequeuesAndWarnsPastTheDeadline(t *testing.T) {
	deletedAt := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		since    time.Duration
		wantWarn bool
	}{
		{"within the deadline", time.Minute, false},
		{"past the deadline", fenceHeldWarnAfter + time.Minute, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hr := noDeployedReleasesHR()
			hr.Finalizers = []string{fenceFinalizer}
			hr.DeletionTimestamp = &metav1.Time{Time: deletedAt}
			hr.Generation = 4
			hr.Status.ObservedGeneration = 3 // the unsuspended generation not yet handled

			c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr).
				WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
			rec := record.NewFakeRecorder(8)
			r := &FluxPlunger{Client: c, owns: testOwns, Recorder: rec,
				now: func() time.Time { return deletedAt.Add(tc.since) }}

			res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName}})
			if err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if res.RequeueAfter <= 0 {
				t.Fatalf("a held fence must requeue itself, got %+v", res)
			}
			warned := false
			for drained := false; !drained; {
				select {
				case e := <-rec.Events:
					if strings.HasPrefix(e, corev1.EventTypeWarning+" UninstallFenceHeld ") {
						warned = true
					}
				default:
					drained = true
				}
			}
			if warned != tc.wantWarn {
				t.Fatalf("UninstallFenceHeld Warning=%v, want %v", warned, tc.wantWarn)
			}
			if _, ok := hrExists(t, c); !ok {
				t.Fatalf("the fence must keep holding the release; warning is not lifting it")
			}
		})
	}
}

// The same terminating-under-a-foreign-suspension state is two different things.
// A migration that suspends then deletes chose to keep the resources, so it is
// Normal. A release flux-plunger has recovered before (it carries the
// processed-version annotation: an operator suspended it by hand and deleted it,
// or an earlier flux-plunger release left it suspended) is losing its uninstall by
// accident, the failure this controller exists to prevent, so it is a Warning.
func TestReconcile_TerminatingForeignSuspensionWarnsOnlyForAPreviouslyRecoveredRelease(t *testing.T) {
	cases := []struct {
		name      string
		annotated bool
		wantType  string
	}{
		{"never recovered by flux-plunger (migration)", false, corev1.EventTypeNormal},
		{"recovered by flux-plunger before", true, corev1.EventTypeWarning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hr := terminatingFixture(noDeployedReleasesHR())
			hr.Spec.Suspend = true // not flux-plunger's
			if tc.annotated {
				hr.Annotations = map[string]string{annotationLastProcessedVersion: "3"}
			}

			c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr).
				WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
			rec := record.NewFakeRecorder(8)
			r := &FluxPlunger{Client: c, owns: testOwns, Recorder: rec}

			reconcile(t, r)

			select {
			case e := <-rec.Events:
				if !strings.HasPrefix(e, tc.wantType+" TerminatingWhileSuspended ") {
					t.Fatalf("expected a %s TerminatingWhileSuspended event, got %q", tc.wantType, e)
				}
			default:
				t.Fatalf("expected a TerminatingWhileSuspended event, got none")
			}
		})
	}
}

// A fence left without a suspension on a live release (another writer set
// spec.suspend=false and took the field), or a fence whose suspension another
// manager took over, guards nothing and must be lifted, without starting a recovery.
func TestReconcile_ReleasesUnguardingFence(t *testing.T) {
	cases := []struct {
		name    string
		suspend bool
	}{
		{"fence without a suspension", false},
		{"fence under another manager's suspension", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hr := fencedFixture(noDeployedReleasesHR())
			hr.Spec.Suspend = tc.suspend
			secret := helmReleaseSecret("v1")

			c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, secret).
				WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
			r := newPlunger(c)

			reconcile(t, r)

			got, _ := hrExists(t, c)
			if controllerutil.ContainsFinalizer(got, fenceFinalizer) {
				t.Fatalf("a fence that guards nothing must be lifted")
			}
			if got.Spec.Suspend != tc.suspend {
				t.Fatalf("lifting the fence must not change spec.suspend; got %v want %v", got.Spec.Suspend, tc.suspend)
			}
			if getErr := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: secret.Name}, &corev1.Secret{}); apierrors.IsNotFound(getErr) {
				t.Fatalf("lifting a stale fence must not also run a recovery")
			}
		})
	}
}

// Every apply flux-plunger sends, the relinquish included, must carry the uid and
// resourceVersion of the object it was decided on: the resourceVersion so a write in
// between is rejected rather than absorbed, the uid so an apply to a release that
// has gone cannot create an empty one.
func TestReconcile_EveryApplyCarriesPreconditions(t *testing.T) {
	hr := noDeployedReleasesHR()
	hr.UID = "release-uid"
	secret := helmReleaseSecret("v1")

	applies := 0
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if _, _, _, ok := plungerApplyDetail(obj, patch, opts); ok {
					applies++
					cur := &helmv2.HelmRelease{}
					if err := cl.Get(ctx, client.ObjectKeyFromObject(obj), cur); err != nil {
						return err
					}
					if obj.GetUID() != "release-uid" {
						t.Fatalf("apply must carry the release uid, got %q", obj.GetUID())
					}
					if obj.GetResourceVersion() == "" || obj.GetResourceVersion() != cur.ResourceVersion {
						t.Fatalf("apply must carry the read resourceVersion %q, got %q", cur.ResourceVersion, obj.GetResourceVersion())
					}
				}
				return ssaModelApply(ctx, cl, obj, patch, opts...)
			},
		}).Build()
	r := newPlunger(c)

	reconcile(t, r)

	if applies < 2 {
		t.Fatalf("expected the acquire and the relinquish applies, got %d", applies)
	}
}

// Every flux-plunger apply is preconditioned on the object it was decided on, so a
// routine write in between (helm-controller updating the status) makes the final
// unsuspend conflict. That is retried through the requeue, not a failure to clear
// the suspension: it must requeue, but must not raise the UnsuspendFailed Warning.
func TestReconcile_UnsuspendConflictRequeuesWithoutWarning(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if isPlungerApply(obj, patch, opts, false) {
					return apierrors.NewConflict(
						schema.GroupResource{Group: "helm.toolkit.fluxcd.io", Resource: "helmreleases"},
						testName, errors.New("the object has been modified"))
				}
				return ssaModelApply(ctx, cl, obj, patch, opts...)
			},
		}).Build()
	rec := record.NewFakeRecorder(8)
	r := newPlunger(c)
	r.Recorder = rec

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName}})
	if err == nil {
		t.Fatalf("a conflicted unsuspend must requeue with an error")
	}
	for drained := false; !drained; {
		select {
		case e := <-rec.Events:
			if strings.Contains(e, "UnsuspendFailed") {
				t.Fatalf("a conflicted unsuspend is retried, not a failure to clear; got %q", e)
			}
		default:
			drained = true
		}
	}
}

// A release that is gone by the pre-delete termination re-check counts as
// terminating: its storage Secret must be left alone and the reconcile must not
// requeue with an error for an object no reconcile will follow.
func TestReconcile_ReleaseGoneBeforeSecretDeleteKeepsSecret(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if err := ssaModelApply(ctx, cl, obj, patch, opts...); err != nil {
					return err
				}
				// Right after the processed-version annotation is written, the release
				// is removed outright.
				if h, ok := obj.(*helmv2.HelmRelease); ok && patch.Type() != types.ApplyPatchType && h.Annotations[annotationLastProcessedVersion] != "" {
					gone := &helmv2.HelmRelease{}
					if err := cl.Get(ctx, client.ObjectKeyFromObject(obj), gone); err != nil {
						return err
					}
					gone.Finalizers = nil
					if err := cl.Update(ctx, gone); err != nil {
						return err
					}
					return client.IgnoreNotFound(cl.Delete(ctx, gone))
				}
				return nil
			},
		}).Build()
	r := newPlunger(c)

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName}})
	if err != nil {
		t.Fatalf("a release gone before the Secret delete must not requeue with an error: %v", err)
	}
	if getErr := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: secret.Name}, &corev1.Secret{}); apierrors.IsNotFound(getErr) {
		t.Fatalf("a release gone before the Secret delete must keep its storage Secret")
	}
}

// A release that disappears in the middle of a recovery has nothing left to clear:
// the unsuspend must not report a failure it will "retry", since no reconcile will
// follow for an object that is gone.
func TestReconcile_ReleaseGoneMidRecoveryIsNotAnUnsuspendFailure(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: ssaModelApply,
			Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if err := cl.Delete(ctx, obj, opts...); err != nil {
					return err
				}
				if _, ok := obj.(*corev1.Secret); ok {
					// The release is removed outright right after its storage Secret.
					gone := &helmv2.HelmRelease{}
					if err := cl.Get(ctx, types.NamespacedName{Namespace: testNS, Name: testName}, gone); err != nil {
						return err
					}
					gone.Finalizers = nil
					if err := cl.Update(ctx, gone); err != nil {
						return err
					}
					return client.IgnoreNotFound(cl.Delete(ctx, gone))
				}
				return nil
			},
		}).Build()
	rec := record.NewFakeRecorder(8)
	r := newPlunger(c)
	r.Recorder = rec

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName}})
	if err != nil {
		t.Fatalf("a release gone mid-recovery must not requeue with an error: %v", err)
	}
	for drained := false; !drained; {
		select {
		case e := <-rec.Events:
			if strings.Contains(e, "UnsuspendFailed") {
				t.Fatalf("a release gone mid-recovery is not an unsuspend failure, got %q", e)
			}
		default:
			drained = true
		}
	}
}

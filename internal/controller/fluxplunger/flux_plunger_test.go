// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package fluxplunger

import (
	"context"
	"errors"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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

func reconcile(t *testing.T, r *FluxPlunger) {
	t.Helper()
	_, _ = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName},
	})
}

// The normal recovery of a stuck release must still delete the latest storage
// Secret and leave the HelmRelease unsuspended with no lingering ownership
// marker.
func TestReconcile_HappyPathRecovery(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v3")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, secret).Build()
	r := &FluxPlunger{Client: c}

	reconcile(t, r)

	got := getHR(t, c)
	if got.Spec.Suspend {
		t.Fatalf("recovery must end unsuspended, got suspend=true")
	}
	if _, ok := got.Annotations[annotationSuspendedByPlunger]; ok {
		t.Fatalf("recovery must clear the ownership marker, got annotation still present")
	}
	if got.Annotations[annotationLastProcessedVersion] != "3" {
		t.Fatalf("recovery must record the processed version, got %q", got.Annotations[annotationLastProcessedVersion])
	}
	err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: secret.Name}, &corev1.Secret{})
	if !apierrors.IsNotFound(err) {
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
				// so recovery starts). The second Get is suspendHelmRelease's
				// re-fetch: simulate an external actor having suspended it meanwhile.
				if h, ok := obj.(*helmv2.HelmRelease); ok {
					hrGets++
					if hrGets == 2 {
						h.Spec.Suspend = true
						delete(h.Annotations, annotationSuspendedByPlunger)
					}
				}
				return nil
			},
		}).Build()
	r := &FluxPlunger{Client: c}

	reconcile(t, r)

	err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: secret.Name}, &corev1.Secret{})
	if apierrors.IsNotFound(err) {
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
		}).Build()
	r := &FluxPlunger{Client: c}

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
				// Fail the unsuspend patch (suspend flipped back to false).
				if h, ok := obj.(*helmv2.HelmRelease); ok && !h.Spec.Suspend {
					return errors.New("simulated unsuspend patch failure")
				}
				return cl.Patch(ctx, obj, patch, opts...)
			},
		}).Build()
	r := &FluxPlunger{Client: c}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName},
	})
	if err == nil {
		t.Fatalf("a failed unsuspend on a terminating release must requeue with an error, got nil")
	}
}

// If a delete arrives between the top-of-Reconcile deletion check and the
// re-fetch inside suspendHelmRelease, recovery must abort without suspending the
// now-terminating release or deleting its storage Secret: suspending it would
// make helm-controller skip the uninstall and orphan the resources.
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
				// First Get is the top-of-reconcile read (not terminating, so recovery
				// starts). Second Get is suspendHelmRelease's re-fetch: simulate a delete
				// having landed meanwhile.
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
		}).Build()
	r := &FluxPlunger{Client: c}

	reconcile(t, r)

	got := getHR(t, c)
	if got.Spec.Suspend {
		t.Fatalf("recovery must not suspend a release that started terminating, got suspend=true")
	}
	err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: secret.Name}, &corev1.Secret{})
	if apierrors.IsNotFound(err) {
		t.Fatalf("recovery must not delete the storage Secret of a release that started terminating")
	}
}

// A HelmRelease that is deleted while flux-plunger holds it suspended must have
// that suspension cleared, otherwise helm-controller v1.5.x skips the uninstall
// and orphans every resource the release owns.
func TestReconcile_DeletionClearsPlungerSuspension(t *testing.T) {
	hr := noDeployedReleasesHR()
	hr.Spec.Suspend = true
	hr.Annotations = map[string]string{annotationSuspendedByPlunger: "true"}
	now := metav1.Now()
	hr.DeletionTimestamp = &now

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr).Build()
	r := &FluxPlunger{Client: c}

	reconcile(t, r)

	got := getHR(t, c)
	if got.Spec.Suspend {
		t.Fatalf("suspension must be cleared on a deleted HelmRelease so uninstall can proceed, got suspend=true")
	}
}

// flux-plunger must never begin recovery (suspend) on a HelmRelease that is
// already terminating: suspending a deleting release is what makes
// helm-controller skip the uninstall.
func TestReconcile_DoesNotSuspendTerminatingHelmRelease(t *testing.T) {
	hr := noDeployedReleasesHR()
	now := metav1.Now()
	hr.DeletionTimestamp = &now

	secret := helmReleaseSecret("v1")
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, secret).Build()
	r := &FluxPlunger{Client: c}

	reconcile(t, r)

	got := getHR(t, c)
	if got.Spec.Suspend {
		t.Fatalf("flux-plunger must not suspend a terminating HelmRelease, got suspend=true")
	}
	// Recovery must not run at all on a terminating release: the storage Secret
	// belongs to the uninstall helm-controller is about to perform.
	err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: secret.Name}, &corev1.Secret{})
	if err != nil {
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
			Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if _, ok := obj.(*corev1.Secret); ok {
					return errors.New("simulated secret delete failure")
				}
				return cl.Delete(ctx, obj, opts...)
			},
		}).Build()
	r := &FluxPlunger{Client: c}

	reconcile(t, r)

	got := getHR(t, c)
	if got.Spec.Suspend {
		t.Fatalf("a failed recovery step must clear the suspension flux-plunger owns, got suspend=true")
	}
}

// A suspension flux-plunger owns but never recorded progress for (recovery
// crashed right after suspending) must be released on a later reconcile, not
// left stuck forever.
func TestReconcile_ReleasesOwnedSuspensionWithoutProgress(t *testing.T) {
	hr := noDeployedReleasesHR()
	hr.Spec.Suspend = true
	hr.Annotations = map[string]string{annotationSuspendedByPlunger: "true"}

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr).Build()
	r := &FluxPlunger{Client: c}

	reconcile(t, r)

	got := getHR(t, c)
	if got.Spec.Suspend {
		t.Fatalf("a suspension flux-plunger owns with no recorded progress must be released, got suspend=true")
	}
}

// The processed-version annotation must never be read as an ownership signal: it
// is never cleared, so a stale value can arithmetically match the latest revision
// long after recovery finished. An operator suspending such a release for
// maintenance (no ownership marker) must be left untouched.
func TestReconcile_LeavesExternalSuspensionWithStaleProcessedVersion(t *testing.T) {
	hr := noDeployedReleasesHR()
	hr.Spec.Suspend = true
	// Recovery long ago deleted secret v5 and recorded version 5, then cleared its
	// marker; the release never advanced past v4, so latestVersion+1 == 5 still
	// matches. Only an operator holds this suspension now — no ownership marker.
	hr.Annotations = map[string]string{annotationLastProcessedVersion: "5"}
	secret := helmReleaseSecret("v4")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, secret).Build()
	r := &FluxPlunger{Client: c}

	reconcile(t, r)

	got := getHR(t, c)
	if !got.Spec.Suspend {
		t.Fatalf("a stale processed-version match must not be treated as ownership; operator suspension was cleared")
	}
}

// Recovery must delete the LATEST (highest-version) storage Secret and record its
// version, even when several historical revision Secrets are present: "has no
// deployed releases" routinely coexists with multiple leftover revisions, not only
// a single first-install one. This pins getLatestSecret's sort direction, which the
// annotation-first ordering and the skip-guard both depend on.
func TestReconcile_DeletesLatestOfMultipleSecrets(t *testing.T) {
	hr := noDeployedReleasesHR()
	oldest := helmReleaseSecret("v1")
	middle := helmReleaseSecret("v2")
	latest := helmReleaseSecret("v5")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, oldest, middle, latest).Build()
	r := &FluxPlunger{Client: c}

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
// left entirely alone: not suspended, its storage Secret not deleted. Removing the
// error guard would make flux-plunger act on every reconciled release.
func TestReconcile_SkipsReleaseWithoutTargetError(t *testing.T) {
	hr := noDeployedReleasesHR()
	hr.Status.Conditions[0].Message = "Helm upgrade failed: some unrelated error"
	secret := helmReleaseSecret("v1")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, secret).Build()
	r := &FluxPlunger{Client: c}

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
// cleanly. This pins the empty-list guard, without which getLatestSecret would
// index an empty slice and panic (crash-looping the controller).
func TestReconcile_SkipsWhenNoStorageSecrets(t *testing.T) {
	hr := noDeployedReleasesHR() // has the error, not suspended, no marker, and no secrets exist

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr).Build()
	r := &FluxPlunger{Client: c}

	reconcile(t, r) // must not panic

	got := getHR(t, c)
	if got.Spec.Suspend {
		t.Fatalf("a release with no storage Secrets must not be suspended")
	}
}

// The "already processed" skip-guard is the anti-cascade mechanism the
// annotation-before-delete ordering relies on: once a pass has reclaimed a
// revision and recorded the processed version, the next reconcile sees the latest
// surviving revision exactly one below it (latestVersion+1 == processedVersion) and
// must NOT suspend the release or delete another Secret, or it would walk the
// history down one revision per requeue and orphan the release.
func TestReconcile_SkipsWhenAlreadyProcessed(t *testing.T) {
	hr := noDeployedReleasesHR() // not suspended, no ownership marker, has the error
	hr.Annotations = map[string]string{annotationLastProcessedVersion: "5"}
	secret := helmReleaseSecret("v4") // latestVersion 4, and 4+1 == 5 (already processed)

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, secret).Build()
	r := &FluxPlunger{Client: c}

	reconcile(t, r)

	got := getHR(t, c)
	if got.Spec.Suspend {
		t.Fatalf("an already-processed release must not be suspended again, got suspend=true")
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: secret.Name}, &corev1.Secret{}); apierrors.IsNotFound(err) {
		t.Fatalf("an already-processed release must not have another storage Secret deleted")
	}
}

// The converse of the skip-guard: a recorded processed version that does NOT sit
// exactly one above the latest surviving revision means this revision has not been
// handled yet, so recovery must proceed (suspend + delete). This pins the guard
// against an off-by-one that would over-skip and leave a genuinely stuck release
// unrecovered.
func TestReconcile_ProcessesWhenProcessedVersionDoesNotMatch(t *testing.T) {
	hr := noDeployedReleasesHR()
	hr.Annotations = map[string]string{annotationLastProcessedVersion: "5"}
	secret := helmReleaseSecret("v5") // latestVersion 5; 5+1 == 6 != 5, so NOT already processed

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr, secret).Build()
	r := &FluxPlunger{Client: c}

	reconcile(t, r)

	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: secret.Name}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("a non-matching processed version must not skip recovery; the latest Secret should have been deleted, err=%v", err)
	}
	got := getHR(t, c)
	if got.Annotations[annotationLastProcessedVersion] != "5" {
		t.Fatalf("recovery must record the processed version 5, got %q", got.Annotations[annotationLastProcessedVersion])
	}
}

// Deleting a release an operator suspended (no ownership marker) must leave the
// suspension in place: it is the operator's choice, and clearing it would make
// helm-controller run an uninstall the operator did not ask for. This is the
// single most safety-relevant boundary in the controller, enforced by both the
// caller gate and the ownership check inside unsuspendHelmRelease.
func TestReconcile_LeavesExternalSuspensionOnDeletion(t *testing.T) {
	hr := noDeployedReleasesHR()
	hr.Spec.Suspend = true // suspended by an operator: no ownership marker
	now := metav1.Now()
	hr.DeletionTimestamp = &now

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr).Build()
	r := &FluxPlunger{Client: c}

	reconcile(t, r)

	got := getHR(t, c)
	if !got.Spec.Suspend {
		t.Fatalf("flux-plunger must not clear an operator's suspension on deletion, got suspend=false")
	}
}

// Releasing a stuck owned suspension (crash-recovery branch) must also requeue on
// failure, so error handling is uniform across all three unsuspend call sites.
func TestReconcile_SuspendedBranchRequeuesOnUnsuspendFailure(t *testing.T) {
	hr := noDeployedReleasesHR()
	hr.Spec.Suspend = true
	hr.Annotations = map[string]string{annotationSuspendedByPlunger: "true"}

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(hr).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if _, ok := obj.(*helmv2.HelmRelease); ok {
					return errors.New("simulated unsuspend patch failure")
				}
				return cl.Patch(ctx, obj, patch, opts...)
			},
		}).Build()
	r := &FluxPlunger{Client: c}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName},
	})
	if err == nil {
		t.Fatalf("a failed unsuspend in the crash-recovery branch must requeue with an error, got nil")
	}
}

// A failing processed-version annotation write must requeue with an error, so a
// transient failure that also hits the deferred unsuspend does not leave the
// release suspended-by-plunger until the next informer resync.
func TestReconcile_RecoveryRequeuesOnAnnotationFailure(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				// Fail only the annotation patch: it keeps suspend=true and carries the
				// processed-version annotation, unlike the suspend patch (marker only)
				// and the unsuspend patch (suspend=false).
				if h, ok := obj.(*helmv2.HelmRelease); ok && h.Spec.Suspend && h.Annotations[annotationLastProcessedVersion] != "" {
					return errors.New("simulated annotation patch failure")
				}
				return cl.Patch(ctx, obj, patch, opts...)
			},
		}).Build()
	r := &FluxPlunger{Client: c}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName},
	})
	if err == nil {
		t.Fatalf("a failed processed-version annotation write must requeue with an error, got nil")
	}
}

// A failing final unsuspend on the recovery (non-deletion) path must requeue with
// an error, symmetric with the deletion path, so the release does not sit
// suspended-by-plunger until the next informer resync.
func TestReconcile_RecoveryRequeuesOnUnsuspendFailure(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				// Fail only the unsuspend patch (suspend flipped back to false); let
				// the initial suspend patch and the annotation patch through.
				if h, ok := obj.(*helmv2.HelmRelease); ok && !h.Spec.Suspend {
					return errors.New("simulated unsuspend patch failure")
				}
				return cl.Patch(ctx, obj, patch, opts...)
			},
		}).Build()
	r := &FluxPlunger{Client: c}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName},
	})
	if err == nil {
		t.Fatalf("a failed final unsuspend on the recovery path must requeue with an error, got nil")
	}
}

// On a terminating release the unsuspend is what lets helm-controller run the
// uninstall, so a transient failure there must requeue (return an error), not be
// swallowed — otherwise the release stays suspended and its resources orphan.
func TestReconcile_DeletionRequeuesOnUnsuspendFailure(t *testing.T) {
	hr := noDeployedReleasesHR()
	hr.Spec.Suspend = true
	hr.Annotations = map[string]string{annotationSuspendedByPlunger: "true"}
	now := metav1.Now()
	hr.DeletionTimestamp = &now

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(hr).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if _, ok := obj.(*helmv2.HelmRelease); ok {
					return errors.New("simulated unsuspend patch failure")
				}
				return cl.Patch(ctx, obj, patch, opts...)
			},
		}).Build()
	r := &FluxPlunger{Client: c}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName},
	})
	if err == nil {
		t.Fatalf("a failed unsuspend on a terminating release must requeue with an error, got nil")
	}
}

// A suspension flux-plunger does not own (operator or another controller) must
// be left untouched.
func TestReconcile_LeavesExternalSuspensionUntouched(t *testing.T) {
	hr := noDeployedReleasesHR()
	hr.Spec.Suspend = true // suspended by someone else: no ownership marker

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr).Build()
	r := &FluxPlunger{Client: c}

	reconcile(t, r)

	got := getHR(t, c)
	if !got.Spec.Suspend {
		t.Fatalf("flux-plunger must not clear a suspension it does not own, got suspend=false")
	}
}

// A delete that lands after flux-plunger acquires the suspension but before it
// deletes the storage Secret must abort the delete: for a release stuck on its
// first install that Secret is the only revision, and removing it makes
// helm-controller treat the release as already uninstalled, drop its finalizer,
// and orphan the resources. The re-check runs immediately before the delete.
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
				// Inject a delete that lands after the suspension is acquired and the
				// processed-version annotation is written, but before the storage Secret
				// is deleted: releaseIsTerminating's re-fetch reads the annotation this
				// recovery just wrote, so key the injection on its presence.
				if h, ok := obj.(*helmv2.HelmRelease); ok &&
					h.Annotations[annotationLastProcessedVersion] != "" && h.DeletionTimestamp == nil {
					now := metav1.Now()
					h.DeletionTimestamp = &now
					h.Finalizers = []string{helmFinalizer}
				}
				return nil
			},
		}).Build()
	r := &FluxPlunger{Client: c}

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
// deleted, so a failed annotation write must leave the Secret intact. Otherwise a
// persistent write failure would delete the next revision on every requeue,
// walking the release history down to zero and orphaning it — the very hazard the
// recovery exists to avoid.
func TestReconcile_AnnotationFailureLeavesSecretIntact(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if h, ok := obj.(*helmv2.HelmRelease); ok && h.Spec.Suspend && h.Annotations[annotationLastProcessedVersion] != "" {
					return errors.New("simulated annotation patch failure")
				}
				return cl.Patch(ctx, obj, patch, opts...)
			},
		}).Build()
	r := &FluxPlunger{Client: c}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName},
	})
	if err == nil {
		t.Fatalf("a failed processed-version write must requeue with an error, got nil")
	}
	if getErr := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: secret.Name}, &corev1.Secret{}); apierrors.IsNotFound(getErr) {
		t.Fatalf("a failed annotation write must not delete the storage Secret (annotation is recorded before the delete)")
	}
}

// A marker left on a HelmRelease that an external actor resumed (spec.suspend
// cleared without removing flux-plunger's annotation) must be reaped: only this
// controller removes it, and a leftover marker would be inherited by a later,
// unrelated operator suspension.
func TestReconcile_ReapsStaleOwnershipMarker(t *testing.T) {
	hr := noDeployedReleasesHR()
	hr.Annotations = map[string]string{annotationSuspendedByPlunger: "true"} // stale marker, suspend=false

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr).Build()
	r := &FluxPlunger{Client: c}

	reconcile(t, r)

	got := getHR(t, c)
	if _, ok := got.Annotations[annotationSuspendedByPlunger]; ok {
		t.Fatalf("a stale ownership marker on a non-suspended release must be reaped, still present")
	}
}

// The reap must prevent inheritance: once the stale marker is gone, a later
// operator suspension (spec.suspend only, no marker) must be left untouched. This
// is the misattribution both the human review and the independent pass flagged.
func TestReconcile_ReapedMarkerDoesNotOverrideLaterOperatorSuspension(t *testing.T) {
	hr := noDeployedReleasesHR()
	hr.Annotations = map[string]string{annotationSuspendedByPlunger: "true"} // stale marker, suspend=false

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr).Build()
	r := &FluxPlunger{Client: c}

	reconcile(t, r) // reaps the stale marker

	// An operator now suspends the release for maintenance, touching only
	// spec.suspend (no ownership marker).
	got := getHR(t, c)
	got.Spec.Suspend = true
	if err := c.Update(context.Background(), got); err != nil {
		t.Fatalf("update: %v", err)
	}

	reconcile(t, r)

	got = getHR(t, c)
	if !got.Spec.Suspend {
		t.Fatalf("a later operator suspension must not be treated as plunger-owned via a leftover marker, it was cleared")
	}
}

// If an operator re-suspends the release in the window between the reap's
// top-of-Reconcile read (which saw it unsuspended) and clearOwnershipMarker's own
// re-fetch, the reap must still remove the stale marker WITHOUT clearing the
// operator's fresh suspension. Otherwise the surviving marker would make the next
// reconcile misread that suspension as plunger-owned and clear it — the boundary
// this controller must never cross.
func TestReconcile_ReapDoesNotMisattributeConcurrentOperatorSuspension(t *testing.T) {
	hr := noDeployedReleasesHR()
	hr.Annotations = map[string]string{annotationSuspendedByPlunger: "true"} // stale marker, suspend=false

	hrGets := 0
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(hr).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := cl.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				h, ok := obj.(*helmv2.HelmRelease)
				if !ok {
					return nil
				}
				hrGets++
				// Get #1 is the top-of-Reconcile read (suspend=false → reap path). Get #2
				// is clearOwnershipMarker's re-fetch: an operator has just re-suspended the
				// release, touching only spec.suspend. Persist that so the reap patch runs
				// against a suspended object, and reflect it in the returned copy.
				if hrGets == 2 {
					h.Spec.Suspend = true
					if err := cl.Update(ctx, h.DeepCopy()); err != nil {
						return err
					}
				}
				return nil
			},
		}).Build()
	r := &FluxPlunger{Client: c}

	reconcile(t, r)

	got := getHR(t, c)
	if _, ok := got.Annotations[annotationSuspendedByPlunger]; ok {
		t.Fatalf("the stale marker must be reaped even when an operator re-suspended concurrently, still present")
	}
	if !got.Spec.Suspend {
		t.Fatalf("the operator's concurrent suspension must be left intact by the reap, got suspend=false")
	}

	// End to end: with the marker gone, the operator's suspension is now correctly
	// classified as external and left untouched on the next reconcile.
	reconcile(t, r)
	got = getHR(t, c)
	if !got.Spec.Suspend {
		t.Fatalf("after reap, a later reconcile must not clear the operator's unowned suspension, got suspend=false")
	}
}

// unsuspendHelmRelease's own ownership re-check (defense-in-depth for a future
// caller that forgets the caller-side gate) must be pinned by its own test: with
// suspend=true and no marker, a direct call must not clear the suspension. The
// caller-side gate and this callee-side gate otherwise mask each other under
// mutation, so neither was isolated before.
func TestUnsuspendHelmRelease_LeavesSuspensionWithoutMarker(t *testing.T) {
	hr := noDeployedReleasesHR()
	hr.Spec.Suspend = true // suspended by an operator: no ownership marker

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr).Build()
	r := &FluxPlunger{Client: c}

	if err := r.unsuspendHelmRelease(context.Background(), hr); err != nil {
		t.Fatalf("unsuspendHelmRelease must not error on an unowned suspension: %v", err)
	}

	got := getHR(t, c)
	if !got.Spec.Suspend {
		t.Fatalf("unsuspendHelmRelease must not clear a suspension without the ownership marker (callee-side guard)")
	}
}

// unsuspendHelmRelease must strip a stale marker when the release it re-fetches is
// already unsuspended (an external actor resumed it, leaving the marker). This
// pins the already-unsuspended cleanup branch, which removes only the annotation
// key with a lock-free patch so it cannot fail under a concurrent spec write.
func TestUnsuspendHelmRelease_ReapsStaleMarkerWhenAlreadyUnsuspended(t *testing.T) {
	hr := noDeployedReleasesHR()
	hr.Annotations = map[string]string{annotationSuspendedByPlunger: "true"} // marker present, suspend=false

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr).Build()
	r := &FluxPlunger{Client: c}

	if err := r.unsuspendHelmRelease(context.Background(), hr); err != nil {
		t.Fatalf("unsuspendHelmRelease must not error clearing a stale marker on an unsuspended release: %v", err)
	}
	got := getHR(t, c)
	if _, ok := got.Annotations[annotationSuspendedByPlunger]; ok {
		t.Fatalf("unsuspendHelmRelease must strip a stale marker when the release is already unsuspended")
	}
}

// A non-conflict error while acquiring the suspension must requeue with an error:
// unlike an optimistic-lock conflict (which is itself an update event that wakes
// us), a transient API failure produces no follow-up event, so swallowing it
// would strand a stuck release until the next informer resync.
func TestReconcile_SuspendFailureRequeuesOnNonConflictError(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				// Fail only the suspend patch: suspend=true, marker set, no
				// processed-version annotation yet.
				if h, ok := obj.(*helmv2.HelmRelease); ok && h.Spec.Suspend &&
					h.Annotations[annotationSuspendedByPlunger] != "" &&
					h.Annotations[annotationLastProcessedVersion] == "" {
					return errors.New("simulated transient suspend failure")
				}
				return cl.Patch(ctx, obj, patch, opts...)
			},
		}).Build()
	r := &FluxPlunger{Client: c}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName},
	})
	if err == nil {
		t.Fatalf("a non-conflict suspend failure must requeue with an error, got nil")
	}
}

// An optimistic-lock conflict while acquiring the suspension must NOT requeue with
// an error: the conflicting write is itself an update event that reconciles us
// again. The failed suspend acquires nothing, so the storage Secret stays intact.
func TestReconcile_SuspendConflictDoesNotRequeue(t *testing.T) {
	hr := noDeployedReleasesHR()
	secret := helmReleaseSecret("v1")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(hr, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if h, ok := obj.(*helmv2.HelmRelease); ok && h.Spec.Suspend &&
					h.Annotations[annotationSuspendedByPlunger] != "" &&
					h.Annotations[annotationLastProcessedVersion] == "" {
					return apierrors.NewConflict(
						schema.GroupResource{Group: "helm.toolkit.fluxcd.io", Resource: "helmreleases"},
						testName, errors.New("optimistic lock"))
				}
				return cl.Patch(ctx, obj, patch, opts...)
			},
		}).Build()
	r := &FluxPlunger{Client: c}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName},
	})
	if err != nil {
		t.Fatalf("an optimistic-lock conflict on suspend must not requeue with an error, got %v", err)
	}
	if getErr := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: secret.Name}, &corev1.Secret{}); apierrors.IsNotFound(getErr) {
		t.Fatalf("a conflicted suspend acquires nothing, so it must not delete the storage Secret")
	}
}

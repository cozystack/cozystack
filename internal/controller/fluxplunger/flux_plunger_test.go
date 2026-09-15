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

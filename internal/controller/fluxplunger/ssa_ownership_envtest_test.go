// SPDX-License-Identifier: Apache-2.0
//go:build envtest

// Source of truth for what the suspension fix relies on from a REAL apiserver
// (envtest), where the controller-runtime fake client cannot stand in: it does not
// track metadata.managedFields, and it does not enforce resourceVersion or uid
// preconditions on server-side apply. It drives the PRODUCTION code against a
// realistic release, one an operator owns with a non-zero spec.interval.
//
// Ownership: acquire without conflict and without touching spec.interval, the
// operator transfer (misattribution boundary), no self-disown, relinquish, never
// claiming a suspension set under the shared client-side field manager
// (migrations, delete hook), and never acquiring an actively foreign-owned
// suspension; ownsSuspensionManagedFields is asserted directly against
// apiserver-produced managedFields.
//
// Uninstall fence (issue #4060): a delete landing during recovery reaches the Helm
// uninstall whichever of helm-controller and flux-plunger handles it first, with
// helm-controller v1.5.0's delete handling reproduced step for step; a delete
// racing the unsuspend of a live release is survived; and no apply resurrects a
// release that has gone.
//
// Built only under the `envtest` tag; `make test-controllers-envtest` fetches the
// apiserver and runs it, and CI runs that target on every pull request.
package fluxplunger

import (
	"context"
	"os"
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

const (
	fmHelm = "cozystack-operator"
	fmOper = "kubectl-patch"
)

func TestEnvtest_SuspendOwnership(t *testing.T) {
	crdDir := os.Getenv("HELMRELEASE_CRD_DIR")
	if crdDir == "" {
		t.Skip("set HELMRELEASE_CRD_DIR to the dir with the HelmRelease CRD")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{crdDir}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	defer func() { _ = env.Stop() }()

	if err := helmv2.AddToScheme(clientgoscheme.Scheme); err != nil {
		t.Fatalf("add helm scheme: %v", err)
	}
	cl, err := client.New(cfg, client.Options{Scheme: clientgoscheme.Scheme})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx := context.Background()
	r := &FluxPlunger{Client: cl}
	key := types.NamespacedName{Namespace: testNS, Name: testName}
	get := func() *helmv2.HelmRelease {
		h := &helmv2.HelmRelease{}
		if err := cl.Get(ctx, key, h); err != nil {
			t.Fatalf("get: %v", err)
		}
		return h
	}

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNS}}
	if err := cl.Create(ctx, ns); err != nil {
		t.Fatalf("create ns: %v", err)
	}

	// The operator establishes a realistic release via a full-object apply that owns
	// a NON-ZERO spec.interval and does NOT set spec.suspend. This is the shape a
	// typed-struct suspend apply conflicts with (it would emit spec.interval "0s").
	const operatorInterval = 5 * time.Minute
	base := &helmv2.HelmRelease{
		TypeMeta:   metav1.TypeMeta{APIVersion: helmv2.GroupVersion.String(), Kind: "HelmRelease"},
		ObjectMeta: metav1.ObjectMeta{Namespace: testNS, Name: testName},
		Spec: helmv2.HelmReleaseSpec{
			Interval: metav1.Duration{Duration: operatorInterval},
			Chart:    &helmv2.HelmChartTemplate{Spec: helmv2.HelmChartTemplateSpec{Chart: "x", SourceRef: helmv2.CrossNamespaceObjectReference{Kind: "HelmRepository", Name: "r"}}},
		},
	}
	if err := cl.Patch(ctx, base, client.Apply, client.FieldOwner(fmHelm)); err != nil {
		t.Fatalf("operator base apply: %v", err)
	}

	// Acquire: production suspendHelmRelease must acquire WITHOUT conflicting on
	// spec.interval, and must leave the operator's interval untouched.
	acquired, err := r.suspendHelmRelease(ctx, get())
	if err != nil {
		t.Fatalf("acquire: suspendHelmRelease errored (interval conflict regression?): %v", err)
	}
	if !acquired {
		t.Fatalf("acquire: suspendHelmRelease did not acquire the suspension")
	}
	h := get()
	if !h.Spec.Suspend || !ownsSuspensionManagedFields(h) {
		t.Fatalf("acquire: expected suspend=true owned by flux-plunger; suspend=%v owns=%v", h.Spec.Suspend, ownsSuspensionManagedFields(h))
	}
	if h.Spec.Interval.Duration != operatorInterval {
		t.Fatalf("acquire: suspend must not touch the operator's spec.interval; got %v want %v", h.Spec.Interval.Duration, operatorInterval)
	}
	t.Log("acquire: acquired suspend without conflict, operator interval preserved")

	// Unrelated re-apply: operator re-applies the whole object (unrelated fields), NOT touching
	// suspend. flux-plunger must retain ownership (no self-disown).
	base2 := &helmv2.HelmRelease{
		TypeMeta:   metav1.TypeMeta{APIVersion: helmv2.GroupVersion.String(), Kind: "HelmRelease"},
		ObjectMeta: metav1.ObjectMeta{Namespace: testNS, Name: testName},
		Spec: helmv2.HelmReleaseSpec{
			Interval: metav1.Duration{Duration: operatorInterval},
			Chart:    &helmv2.HelmChartTemplate{Spec: helmv2.HelmChartTemplateSpec{Chart: "x2", SourceRef: helmv2.CrossNamespaceObjectReference{Kind: "HelmRepository", Name: "r"}}},
		},
	}
	if err := cl.Patch(ctx, base2, client.Apply, client.FieldOwner(fmHelm)); err != nil {
		t.Fatalf("unrelated re-apply: operator re-apply: %v", err)
	}
	if h = get(); !h.Spec.Suspend || !ownsSuspensionManagedFields(h) {
		t.Fatalf("unrelated re-apply (self-disown): after unrelated operator re-apply suspend=%v owns=%v", h.Spec.Suspend, ownsSuspensionManagedFields(h))
	}
	t.Log("unrelated re-apply: unrelated operator re-apply did NOT disown flux-plunger's suspend")

	// Operator transfer: operator resumes then re-suspends via client-side patches. The field
	// transfers to the operator, so flux-plunger no longer owns it.
	for _, want := range []bool{false, true} {
		cur := get()
		patch := client.MergeFrom(cur.DeepCopy())
		cur.Spec.Suspend = want
		if err := cl.Patch(ctx, cur, patch, client.FieldOwner(fmOper)); err != nil {
			t.Fatalf("operator set suspend=%v: %v", want, err)
		}
	}
	if h = get(); ownsSuspensionManagedFields(h) {
		t.Fatalf("operator transfer (misattribution): flux-plunger still owns an operator-set suspension")
	}
	t.Log("operator transfer: after operator resume+resuspend, flux-plunger does NOT own the suspension")

	// unsuspendHelmRelease must now be a no-op (not owned): the operator's
	// suspension must survive.
	if err := r.unsuspendHelmRelease(ctx, get()); err != nil {
		t.Fatalf("unsuspend on unowned suspension errored: %v", err)
	}
	if h = get(); !h.Spec.Suspend {
		t.Fatalf("boundary FAIL: flux-plunger cleared an operator's suspension")
	}

	// Relinquish: reset to a flux-plunger-owned suspension (operator resumes so the field is
	// free), acquire, then unsuspendHelmRelease must relinquish → suspend=false,
	// interval untouched, ownership dropped.
	cur := get()
	patch := client.MergeFrom(cur.DeepCopy())
	cur.Spec.Suspend = false
	if err := cl.Patch(ctx, cur, patch, client.FieldOwner(fmOper)); err != nil {
		t.Fatalf("relinquish: operator resume: %v", err)
	}
	if acquired, err := r.suspendHelmRelease(ctx, get()); err != nil || !acquired {
		t.Fatalf("relinquish: re-acquire: acquired=%v err=%v", acquired, err)
	}
	if err := r.unsuspendHelmRelease(ctx, get()); err != nil {
		t.Fatalf("relinquish: unsuspend: %v", err)
	}
	h = get()
	if h.Spec.Suspend {
		t.Fatalf("relinquish: unsuspend did not clear the suspension")
	}
	if ownsSuspensionManagedFields(h) {
		t.Fatalf("relinquish: flux-plunger still owns suspend after unsuspend")
	}
	if h.Spec.Interval.Duration != operatorInterval {
		t.Fatalf("relinquish: unsuspend must not touch the operator's spec.interval; got %v", h.Spec.Interval.Duration)
	}
	t.Log("relinquish: unsuspend relinquished ownership, reverted suspend to false, interval preserved")

	// Shared manager: a suspension set under the SHARED client-side field manager
	// (flux-client-side-apply) must NOT be read as flux-plunger's, and
	// unsuspendHelmRelease must leave it untouched. That manager is used by the
	// platform migrations (cozy-proxy, cert-manager) and the tenant delete hook — and
	// historically by earlier flux-plunger releases, which is exactly why the field
	// manager cannot identify flux-plunger. Recognising it would let flux-plunger
	// clear a migration's suspension (unsuspending cozy-proxy deletes
	// cozystack-operator), so the boundary is pinned here on a real apiserver.
	curShared := get()
	sharedPatch := client.MergeFrom(curShared.DeepCopy())
	curShared.Spec.Suspend = true
	if err := cl.Patch(ctx, curShared, sharedPatch, client.FieldOwner(fieldManager)); err != nil {
		t.Fatalf("shared manager: shared-manager suspend: %v", err)
	}
	if h = get(); ownsSuspensionManagedFields(h) {
		t.Fatalf("shared manager (misattribution risk): a suspension under the shared client-side manager must NOT be read as flux-plunger's")
	}
	if err := r.unsuspendHelmRelease(ctx, get()); err != nil {
		t.Fatalf("shared manager: unsuspend: %v", err)
	}
	if h = get(); !h.Spec.Suspend {
		t.Fatalf("shared manager (misattribution): flux-plunger cleared a suspension set under the shared client-side manager")
	}
	t.Log("shared manager: a shared-client-side-manager suspension is left untouched")

	// Active foreign suspension: an operator actively owns suspend=TRUE. suspendHelmRelease must NOT acquire
	// or force it away — an active foreign suspension is never taken. The early
	// already-suspended guard covers the common case; the re-fetch-after-conflict
	// guard covers the race the fake-client tests pin.
	curActive := get()
	activePatch := client.MergeFrom(curActive.DeepCopy())
	curActive.Spec.Suspend = true
	if err := cl.Patch(ctx, curActive, activePatch, client.FieldOwner(fmOper)); err != nil {
		t.Fatalf("active foreign suspension: operator suspend: %v", err)
	}
	acquired, err = r.suspendHelmRelease(ctx, get())
	if err != nil {
		t.Fatalf("active foreign suspension: suspend: %v", err)
	}
	if acquired {
		t.Fatalf("active foreign suspension: acquired an actively foreign-suspended release")
	}
	if h = get(); !h.Spec.Suspend {
		t.Fatalf("active foreign suspension: cleared an active foreign suspension")
	}
	if ownsSuspensionManagedFields(h) {
		t.Fatalf("active foreign suspension: took ownership of an active foreign suspension")
	}
	t.Log("active foreign suspension: did not acquire or clear an actively foreign-owned suspension")
}

// TestEnvtest_AcquiresOverBackupOwnedSuspend pins acquiring a
// suspension a backup controller left owned. A backup/restore controller in this repo sets
// spec.suspend via a dynamic-client Update, taking ownership of the field. A
// non-forced apply of spec.suspend then 409s forever; production suspendHelmRelease
// re-fetches after the conflict, sees the field still false, and forces on acquire,
// which must succeed without touching the operator's interval. Reverting the
// conditional force in suspendHelmRelease turns this RED with a .spec.suspend
// conflict.
func TestEnvtest_AcquiresOverBackupOwnedSuspend(t *testing.T) {
	crdDir := os.Getenv("HELMRELEASE_CRD_DIR")
	if crdDir == "" {
		t.Skip("set HELMRELEASE_CRD_DIR to the dir with the HelmRelease CRD")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{crdDir}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	defer func() { _ = env.Stop() }()

	if err := helmv2.AddToScheme(clientgoscheme.Scheme); err != nil {
		t.Fatalf("add helm scheme: %v", err)
	}
	cl, err := client.New(cfg, client.Options{Scheme: clientgoscheme.Scheme})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("dynamic client: %v", err)
	}
	gvr := schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
	ctx := context.Background()
	r := &FluxPlunger{Client: cl}
	key := types.NamespacedName{Namespace: testNS, Name: testName}
	if err := cl.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNS}}); err != nil {
		t.Fatalf("create ns: %v", err)
	}

	const operatorInterval = 5 * time.Minute
	base := &helmv2.HelmRelease{
		TypeMeta:   metav1.TypeMeta{APIVersion: helmv2.GroupVersion.String(), Kind: "HelmRelease"},
		ObjectMeta: metav1.ObjectMeta{Namespace: testNS, Name: testName},
		Spec: helmv2.HelmReleaseSpec{
			Interval: metav1.Duration{Duration: operatorInterval},
			Chart:    &helmv2.HelmChartTemplate{Spec: helmv2.HelmChartTemplateSpec{Chart: "x", SourceRef: helmv2.CrossNamespaceObjectReference{Kind: "HelmRepository", Name: "r"}}},
		},
	}
	if err := cl.Patch(ctx, base, client.Apply, client.FieldOwner("cozystack-operator")); err != nil {
		t.Fatalf("operator base apply: %v", err)
	}

	// A backup/restore controller suspends then resumes via a dynamic-client Update,
	// leaving spec.suspend=false but OWNED by it (operation=Update). This is the
	// state that 409s a non-forced apply.
	for _, v := range []bool{true, false} {
		u, err := dyn.Resource(gvr).Namespace(testNS).Get(ctx, testName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("backup get: %v", err)
		}
		_ = unstructured.SetNestedField(u.Object, v, "spec", "suspend")
		if _, err := dyn.Resource(gvr).Namespace(testNS).Update(ctx, u, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("backup Update suspend=%v: %v", v, err)
		}
	}

	cur := &helmv2.HelmRelease{}
	if err := cl.Get(ctx, key, cur); err != nil {
		t.Fatalf("get: %v", err)
	}
	acquired, err := r.suspendHelmRelease(ctx, cur)
	if err != nil {
		t.Fatalf("backup-owned acquire: suspendHelmRelease could not acquire a backup-controller-owned spec.suspend (force missing?): %v", err)
	}
	if !acquired {
		t.Fatalf("backup-owned acquire: suspendHelmRelease did not acquire")
	}
	got := &helmv2.HelmRelease{}
	_ = cl.Get(ctx, key, got)
	if !got.Spec.Suspend || !ownsSuspensionManagedFields(got) {
		t.Fatalf("backup-owned acquire: expected suspend=true owned by flux-plunger; suspend=%v owns=%v", got.Spec.Suspend, ownsSuspensionManagedFields(got))
	}
	if got.Spec.Interval.Duration != operatorInterval {
		t.Fatalf("backup-owned acquire: acquire must not touch the operator's interval; got %v", got.Spec.Interval.Duration)
	}
	t.Log("backup-owned acquire: force-on-acquire took spec.suspend from a dynamic-Update owner, interval preserved")

	// Concurrent write: the backup controller holds the field at false and then
	// suspends the release for real in the gap after one of acquire's reads. Both
	// applies are preconditioned on the resourceVersion of the read that decided on
	// them, so neither the unforced first attempt (which would otherwise record
	// flux-plunger as a co-owner of the backup's true) nor the forced retry may take
	// the suspension: acquire reports not acquired, without an error, and the
	// backup's suspension keeps its owner.
	setBackupSuspend := func(v bool) {
		u, err := dyn.Resource(gvr).Namespace(testNS).Get(ctx, testName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("concurrent write: backup get: %v", err)
		}
		_ = unstructured.SetNestedField(u.Object, v, "spec", "suspend")
		if _, err := dyn.Resource(gvr).Namespace(testNS).Update(ctx, u, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("concurrent write: backup Update suspend=%v: %v", v, err)
		}
	}
	for _, tc := range []struct {
		name  string
		after int // the backup writes right after this Get of acquire
	}{
		{"before the unforced apply", 1},
		{"before the forced apply", 2},
	} {
		setBackupSuspend(false)
		racing := &FluxPlunger{Client: cl, APIReader: &writeAfterNthGet{Reader: cl, n: tc.after, write: func() { setBackupSuspend(true) }}}
		start := &helmv2.HelmRelease{}
		if err := cl.Get(ctx, key, start); err != nil {
			t.Fatalf("concurrent write %s: get: %v", tc.name, err)
		}
		acquired, err := racing.suspendHelmRelease(ctx, start)
		if err != nil || acquired {
			t.Fatalf("concurrent write %s: acquire must report not acquired without an error; acquired=%v err=%v", tc.name, acquired, err)
		}
		after := &helmv2.HelmRelease{}
		_ = cl.Get(ctx, key, after)
		if !after.Spec.Suspend || ownsSuspensionManagedFields(after) {
			t.Fatalf("concurrent write %s: the backup's suspension must survive unowned by flux-plunger; suspend=%v owns=%v", tc.name, after.Spec.Suspend, ownsSuspensionManagedFields(after))
		}
		t.Logf("concurrent write %s: acquire declined, backup's suspension kept", tc.name)
	}
}

// writeAfterNthGet runs write right after its n-th Get returns, standing in for
// another controller writing the object in the gap between a read and the apply
// decided on it.
type writeAfterNthGet struct {
	client.Reader
	n, calls int
	write    func()
}

func (w *writeAfterNthGet) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	err := w.Reader.Get(ctx, key, obj, opts...)
	w.calls++
	if w.calls == w.n {
		w.write()
	}
	return err
}

// simulateHelmControllerDelete stands in for helm-controller v1.5.0's
// reconcileDelete (internal/controller/helmrelease_controller.go, the
// deletionTimestamp branch): on a terminating HelmRelease it runs the Helm
// uninstall only when spec.suspend is false, then removes its own finalizer either
// way and records the observed generation. It reports whether it uninstalled. It is
// the behaviour issue #4060 is about: a delete handled while the release is
// suspended drops the finalizer without an uninstall.
func simulateHelmControllerDelete(t *testing.T, ctx context.Context, cl client.Client, key types.NamespacedName) (uninstalled bool) {
	t.Helper()
	hr := &helmv2.HelmRelease{}
	if err := cl.Get(ctx, key, hr); err != nil {
		t.Fatalf("helm-controller: get: %v", err)
	}
	if hr.DeletionTimestamp.IsZero() {
		t.Fatalf("helm-controller: release is not terminating")
	}
	uninstalled = !hr.Spec.Suspend
	hr.Status.ObservedGeneration = hr.Generation
	if err := cl.Status().Update(ctx, hr); err != nil {
		t.Fatalf("helm-controller: status update: %v", err)
	}
	if err := cl.Get(ctx, key, hr); err != nil {
		t.Fatalf("helm-controller: re-get: %v", err)
	}
	var kept []string
	for _, f := range hr.Finalizers {
		if f != helmv2.HelmReleaseFinalizer {
			kept = append(kept, f)
		}
	}
	if len(kept) != len(hr.Finalizers) {
		hr.Finalizers = kept
		if err := cl.Update(ctx, hr); err != nil {
			t.Fatalf("helm-controller: remove finalizer: %v", err)
		}
	}
	return uninstalled
}

// TestEnvtest_DeleteDuringRecoveryReachesUninstall is the regression for issue
// #4060 itself: a delete arriving while flux-plunger holds its recovery suspension
// must still reach the Helm uninstall. helm-controller and flux-plunger wake on the
// same delete event, so both orders are pinned: helm-controller handling it first
// (it sees the suspension, skips the uninstall and drops its finalizer; only
// flux-plunger's fence finalizer keeps the object alive until the suspension is
// lifted and the uninstall runs), and flux-plunger handling it first.
func TestEnvtest_DeleteDuringRecoveryReachesUninstall(t *testing.T) {
	crdDir := os.Getenv("HELMRELEASE_CRD_DIR")
	if crdDir == "" {
		t.Skip("set HELMRELEASE_CRD_DIR to the dir with the HelmRelease CRD")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{crdDir}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	defer func() { _ = env.Stop() }()
	if err := helmv2.AddToScheme(clientgoscheme.Scheme); err != nil {
		t.Fatalf("add helm scheme: %v", err)
	}
	cl, err := client.New(cfg, client.Options{Scheme: clientgoscheme.Scheme})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx := context.Background()
	if err := cl.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNS}}); err != nil {
		t.Fatalf("create ns: %v", err)
	}

	for _, tc := range []struct {
		name      string
		helmFirst bool
	}{
		{"helm-controller handles the delete first", true},
		{"flux-plunger handles the delete first", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := testName + map[bool]string{true: "-hc", false: "-fp"}[tc.helmFirst]
			key := types.NamespacedName{Namespace: testNS, Name: name}
			base := &helmv2.HelmRelease{
				TypeMeta:   metav1.TypeMeta{APIVersion: helmv2.GroupVersion.String(), Kind: "HelmRelease"},
				ObjectMeta: metav1.ObjectMeta{Namespace: testNS, Name: name},
				Spec: helmv2.HelmReleaseSpec{
					Interval: metav1.Duration{Duration: 5 * time.Minute},
					Chart:    &helmv2.HelmChartTemplate{Spec: helmv2.HelmChartTemplateSpec{Chart: "x", SourceRef: helmv2.CrossNamespaceObjectReference{Kind: "HelmRepository", Name: "r"}}},
				},
			}
			if err := cl.Patch(ctx, base, client.Apply, client.FieldOwner(fmHelm)); err != nil {
				t.Fatalf("operator base apply: %v", err)
			}
			hr := &helmv2.HelmRelease{}
			if err := cl.Get(ctx, key, hr); err != nil {
				t.Fatalf("get: %v", err)
			}
			hr.Finalizers = append(hr.Finalizers, helmv2.HelmReleaseFinalizer)
			if err := cl.Update(ctx, hr); err != nil {
				t.Fatalf("add helm-controller finalizer: %v", err)
			}

			r := &FluxPlunger{Client: cl, APIReader: cl}
			req := ctrl.Request{NamespacedName: key}
			if err := cl.Get(ctx, key, hr); err != nil {
				t.Fatalf("get: %v", err)
			}
			if acquired, err := r.suspendHelmRelease(ctx, hr); err != nil || !acquired {
				t.Fatalf("acquire: acquired=%v err=%v", acquired, err)
			}

			// The delete lands inside the recovery window.
			if err := cl.Get(ctx, key, hr); err != nil {
				t.Fatalf("get: %v", err)
			}
			if err := cl.Delete(ctx, hr); err != nil {
				t.Fatalf("delete: %v", err)
			}

			uninstalled := false
			if tc.helmFirst {
				uninstalled = simulateHelmControllerDelete(t, ctx, cl, key) || uninstalled
				if err := cl.Get(ctx, key, &helmv2.HelmRelease{}); err != nil {
					t.Fatalf("the release must survive helm-controller dropping its finalizer while suspended (fence), got %v", err)
				}
			}
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatalf("flux-plunger reconcile of the terminating release: %v", err)
			}
			// Lifting the suspension is itself an update event, so flux-plunger can
			// reconcile again before helm-controller has seen the new generation. When
			// helm-controller handled the delete first its finalizer is already gone;
			// only the observed generation still lagging tells flux-plunger the
			// uninstall has not run, so the fence must hold through this pass.
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatalf("flux-plunger reconcile racing ahead of helm-controller: %v", err)
			}
			if err := cl.Get(ctx, key, &helmv2.HelmRelease{}); err != nil {
				t.Fatalf("the fence must hold until helm-controller has observed the unsuspended generation, got %v", err)
			}
			if !tc.helmFirst {
				uninstalled = simulateHelmControllerDelete(t, ctx, cl, key) || uninstalled
			} else {
				// With the suspension lifted, helm-controller reconciles again and now
				// runs the uninstall.
				uninstalled = simulateHelmControllerDelete(t, ctx, cl, key) || uninstalled
			}
			if !uninstalled {
				t.Fatalf("a delete arriving during recovery must reach the Helm uninstall")
			}
			// With the uninstall done, flux-plunger lifts its fence and the release is
			// gone.
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatalf("flux-plunger reconcile after the uninstall: %v", err)
			}
			if err := cl.Get(ctx, key, &helmv2.HelmRelease{}); !apierrors.IsNotFound(err) {
				t.Fatalf("after the uninstall flux-plunger must lift its fence so the release is deleted, got %v", err)
			}
			t.Logf("%s: delete during recovery reached the uninstall, release gone", tc.name)
		})
	}
}

// startFenceEnv starts envtest and returns a client plus a helper that creates a
// release under helm-controller's finalizer, as helm-controller leaves every release
// it manages.
func startFenceEnv(t *testing.T) (context.Context, client.Client, func(name string) types.NamespacedName, func()) {
	t.Helper()
	crdDir := os.Getenv("HELMRELEASE_CRD_DIR")
	if crdDir == "" {
		t.Skip("set HELMRELEASE_CRD_DIR to the dir with the HelmRelease CRD")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{crdDir}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	if err := helmv2.AddToScheme(clientgoscheme.Scheme); err != nil {
		t.Fatalf("add helm scheme: %v", err)
	}
	cl, err := client.New(cfg, client.Options{Scheme: clientgoscheme.Scheme})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx := context.Background()
	if err := cl.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNS}}); err != nil {
		t.Fatalf("create ns: %v", err)
	}
	newRelease := func(name string) types.NamespacedName {
		key := types.NamespacedName{Namespace: testNS, Name: name}
		base := &helmv2.HelmRelease{
			TypeMeta:   metav1.TypeMeta{APIVersion: helmv2.GroupVersion.String(), Kind: "HelmRelease"},
			ObjectMeta: metav1.ObjectMeta{Namespace: testNS, Name: name},
			Spec: helmv2.HelmReleaseSpec{
				Interval: metav1.Duration{Duration: 5 * time.Minute},
				Chart:    &helmv2.HelmChartTemplate{Spec: helmv2.HelmChartTemplateSpec{Chart: "x", SourceRef: helmv2.CrossNamespaceObjectReference{Kind: "HelmRepository", Name: "r"}}},
			},
		}
		if err := cl.Patch(ctx, base, client.Apply, client.FieldOwner(fmHelm)); err != nil {
			t.Fatalf("operator base apply: %v", err)
		}
		hr := &helmv2.HelmRelease{}
		if err := cl.Get(ctx, key, hr); err != nil {
			t.Fatalf("get: %v", err)
		}
		hr.Finalizers = append(hr.Finalizers, helmv2.HelmReleaseFinalizer)
		if err := cl.Update(ctx, hr); err != nil {
			t.Fatalf("add helm-controller finalizer: %v", err)
		}
		return key
	}
	return ctx, cl, newRelease, func() { _ = env.Stop() }
}

// TestEnvtest_DeleteRacingUnsuspendOfLiveReleaseKeepsFence pins the window between
// unsuspendHelmRelease's read of a still-live release and its apply, which lifts the
// suspension and the fence together. A delete landing there that helm-controller
// handles first (suspended, so no uninstall and its finalizer dropped) would leave
// the fence as the last finalizer, and an unpreconditioned apply removing it would
// delete the release without an uninstall. The apply must fail instead, and the
// next reconcile must carry the delete through to the uninstall.
func TestEnvtest_DeleteRacingUnsuspendOfLiveReleaseKeepsFence(t *testing.T) {
	ctx, cl, newRelease, stop := startFenceEnv(t)
	defer stop()
	key := newRelease(testName + "-live")

	start := &helmv2.HelmRelease{}
	if err := cl.Get(ctx, key, start); err != nil {
		t.Fatalf("get: %v", err)
	}
	if acquired, err := (&FluxPlunger{Client: cl, APIReader: cl}).suspendHelmRelease(ctx, start); err != nil || !acquired {
		t.Fatalf("acquire: acquired=%v err=%v", acquired, err)
	}

	// The delete lands right after unsuspendHelmRelease reads the live release, and
	// helm-controller handles it while the release is still suspended.
	uninstalled := false
	racing := &FluxPlunger{Client: cl, APIReader: &writeAfterNthGet{Reader: cl, n: 1, write: func() {
		hr := &helmv2.HelmRelease{}
		if err := cl.Get(ctx, key, hr); err != nil {
			t.Fatalf("race: get: %v", err)
		}
		if err := cl.Delete(ctx, hr); err != nil {
			t.Fatalf("race: delete: %v", err)
		}
		uninstalled = simulateHelmControllerDelete(t, ctx, cl, key) || uninstalled
	}}}
	_ = racing.unsuspendHelmRelease(ctx, start)

	if err := cl.Get(ctx, key, &helmv2.HelmRelease{}); err != nil {
		t.Fatalf("the release must survive a delete racing the live unsuspend (fence), got %v", err)
	}

	r := &FluxPlunger{Client: cl, APIReader: cl}
	req := ctrl.Request{NamespacedName: key}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile of the terminating release: %v", err)
	}
	uninstalled = simulateHelmControllerDelete(t, ctx, cl, key) || uninstalled
	if !uninstalled {
		t.Fatalf("the delete must reach the Helm uninstall")
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile after the uninstall: %v", err)
	}
	if err := cl.Get(ctx, key, &helmv2.HelmRelease{}); !apierrors.IsNotFound(err) {
		t.Fatalf("after the uninstall the fence must be lifted and the release gone, got %v", err)
	}
}

// TestEnvtest_ApplyNeverRecreatesAVanishedRelease pins that flux-plunger's apply
// cannot resurrect a release. Server-side apply creates what does not exist, so an
// apply decided on a read of a release that has since gone (its fence removed by
// hand, then deleted) would otherwise create an empty HelmRelease carrying the fence.
func TestEnvtest_ApplyNeverRecreatesAVanishedRelease(t *testing.T) {
	ctx, cl, newRelease, stop := startFenceEnv(t)
	defer stop()
	key := newRelease(testName + "-ghost")

	start := &helmv2.HelmRelease{}
	if err := cl.Get(ctx, key, start); err != nil {
		t.Fatalf("get: %v", err)
	}
	r := &FluxPlunger{Client: cl, APIReader: cl}
	if acquired, err := r.suspendHelmRelease(ctx, start); err != nil || !acquired {
		t.Fatalf("acquire: acquired=%v err=%v", acquired, err)
	}
	stale := &helmv2.HelmRelease{}
	if err := cl.Get(ctx, key, stale); err != nil {
		t.Fatalf("get: %v", err)
	}

	// The release goes for good: its finalizers are cleared by hand and it is deleted.
	gone := stale.DeepCopy()
	gone.Finalizers = nil
	if err := cl.Update(ctx, gone); err != nil {
		t.Fatalf("clear finalizers: %v", err)
	}
	if err := cl.Delete(ctx, gone); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := cl.Get(ctx, key, &helmv2.HelmRelease{}); !apierrors.IsNotFound(err) {
		t.Fatalf("setup: release must be gone, got %v", err)
	}

	for _, apply := range []struct {
		name string
		run  func() error
	}{
		{"relinquish", func() error { return r.applyRecoveryState(ctx, stale, nil, false, false) }},
		{"keep the fence", func() error { return r.applyRecoveryState(ctx, stale, nil, true, false) }},
	} {
		_ = apply.run()
		if err := cl.Get(ctx, key, &helmv2.HelmRelease{}); !apierrors.IsNotFound(err) {
			t.Fatalf("%s apply on a vanished release must not recreate it, got %v", apply.name, err)
		}
	}
}

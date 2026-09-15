// SPDX-License-Identifier: Apache-2.0
//go:build envtest

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
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// fenceAndSuspend gives a release flux-plunger's recovery state: its own
// suspension and the fence, applied together as a recovery does.
func fenceAndSuspend(t *testing.T, ctx context.Context, cl client.Client, key types.NamespacedName) {
	t.Helper()
	hr := &helmv2.HelmRelease{}
	if err := cl.Get(ctx, key, hr); err != nil {
		t.Fatalf("get: %v", err)
	}
	r := &FluxPlunger{Client: cl, APIReader: cl}
	if acquired, err := r.suspendHelmRelease(ctx, hr); err != nil || !acquired {
		t.Fatalf("acquire: acquired=%v err=%v", acquired, err)
	}
}

func TestEnvtest_DrainAbortsTheRecoveryOfALiveRelease(t *testing.T) {
	ctx, cl, newRelease, stop := startFenceEnv(t)
	defer stop()
	key := newRelease(testName + "-drain-live")
	fenceAndSuspend(t, ctx, cl, key)

	r := &FluxPlunger{Client: cl, APIReader: cl}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	left, err := r.DrainFences(dctx)
	if err != nil || left != 0 {
		t.Fatalf("drain: left=%d err=%v", left, err)
	}

	hr := &helmv2.HelmRelease{}
	if err := cl.Get(ctx, key, hr); err != nil {
		t.Fatalf("get: %v", err)
	}
	if hr.Spec.Suspend || ownsSuspensionManagedFields(hr) {
		t.Fatalf("drain must lift flux-plunger's suspension: suspend=%v owned=%v", hr.Spec.Suspend, ownsSuspensionManagedFields(hr))
	}
	if controllerutil.ContainsFinalizer(hr, fenceFinalizer) {
		t.Fatalf("drain must lift the fence with the suspension, finalizers %v", hr.Finalizers)
	}
	if !controllerutil.ContainsFinalizer(hr, helmv2.HelmReleaseFinalizer) {
		t.Fatalf("drain must leave helm-controller's finalizer, finalizers %v", hr.Finalizers)
	}
}

// A live release left with the fence alone (a recovery interrupted between its
// steps) is released at once, not held until the drain's deadline.
func TestEnvtest_DrainReleasesALiveFenceWithoutASuspensionAtOnce(t *testing.T) {
	ctx, cl, newRelease, stop := startFenceEnv(t)
	defer stop()
	key := newRelease(testName + "-drain-fence-only")
	hr := &helmv2.HelmRelease{}
	if err := cl.Get(ctx, key, hr); err != nil {
		t.Fatalf("get: %v", err)
	}
	r := &FluxPlunger{Client: cl, APIReader: cl}
	if err := r.applyRecoveryState(ctx, hr, nil, true, false); err != nil {
		t.Fatalf("fence: %v", err)
	}

	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	start := time.Now()
	if left, err := r.DrainFences(dctx); err != nil || left != 0 {
		t.Fatalf("drain: left=%d err=%v", left, err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("a live fence must go at once, the drain took %v", took)
	}
	if err := cl.Get(ctx, key, hr); err != nil {
		t.Fatalf("get: %v", err)
	}
	if controllerutil.ContainsFinalizer(hr, fenceFinalizer) {
		t.Fatalf("the fence must go, finalizers %v", hr.Finalizers)
	}
}

func TestEnvtest_DrainLeavesAnotherManagersSuspensionAlone(t *testing.T) {
	ctx, cl, newRelease, stop := startFenceEnv(t)
	defer stop()
	key := newRelease(testName + "-drain-foreign")
	hr := &helmv2.HelmRelease{}
	if err := cl.Get(ctx, key, hr); err != nil {
		t.Fatalf("get: %v", err)
	}
	r := &FluxPlunger{Client: cl, APIReader: cl}
	if err := r.applyRecoveryState(ctx, hr, nil, true, false); err != nil {
		t.Fatalf("fence: %v", err)
	}
	if err := cl.Get(ctx, key, hr); err != nil {
		t.Fatalf("get: %v", err)
	}
	hr.Spec.Suspend = true
	if err := cl.Update(ctx, hr, client.FieldOwner(fmOper)); err != nil {
		t.Fatalf("operator suspend: %v", err)
	}

	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if left, err := r.DrainFences(dctx); err != nil || left != 0 {
		t.Fatalf("drain: left=%d err=%v", left, err)
	}
	if err := cl.Get(ctx, key, hr); err != nil {
		t.Fatalf("get: %v", err)
	}
	if controllerutil.ContainsFinalizer(hr, fenceFinalizer) {
		t.Fatalf("the fence must go, finalizers %v", hr.Finalizers)
	}
	if !hr.Spec.Suspend {
		t.Fatal("an operator's suspension must survive the drain")
	}
}

// A rollout or an eviction starts the next flux-plunger while the old one is
// still terminating, so two of them overlap whatever the Deployment strategy. The
// old one must still be the only writer while it drains: it keeps the leader
// Lease, which it does not give up, and the next one becomes leader only once
// the Lease has run out, after the drain.
func TestEnvtest_TheNextFluxPlungerLeadsOnlyAfterTheDrain(t *testing.T) {
	crdDir := os.Getenv("HELMRELEASE_CRD_DIR")
	if crdDir == "" {
		t.Skip("set HELMRELEASE_CRD_DIR to the dir with the HelmRelease CRD")
	}
	env := newEnvtestEnvironment(crdDir)
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	defer func() { _ = env.Stop() }()
	cl, err := client.New(cfg, client.Options{Scheme: clientgoscheme.Scheme})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if err := cl.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNS}}); err != nil {
		t.Fatalf("create ns: %v", err)
	}

	// Longer than the Lease retry period, so a Lease given up on stop would be
	// taken by the next manager while the drain still runs.
	const drainFor = 3 * leaseRetryPeriod
	newManager := func() ctrl.Manager {
		opts := ctrl.Options{
			Scheme:                  clientgoscheme.Scheme,
			Metrics:                 metricsserver.Options{BindAddress: "0"},
			HealthProbeBindAddress:  "0",
			LeaderElection:          true,
			LeaderElectionID:        "flux-plunger.cozystack.io",
			LeaderElectionNamespace: testNS,
		}
		ApplyShutdownTimings(&opts, drainFor)
		m, err := ctrl.NewManager(cfg, opts)
		if err != nil {
			t.Fatalf("manager: %v", err)
		}
		return m
	}

	old := newManager()
	oldCtx, stopOld := context.WithCancel(context.Background())
	var drainEnded time.Time
	oldDone := make(chan error, 1)
	go func() {
		oldDone <- RunThenDrain(oldCtx, old.Start, func(ctx context.Context) (int, error) {
			time.Sleep(drainFor)
			drainEnded = time.Now()
			return 0, nil
		}, drainFor)
	}()
	select {
	case <-old.Elected():
	case <-time.After(30 * time.Second):
		t.Fatal("the first flux-plunger never became leader")
	}

	next := newManager()
	nextCtx, stopNext := context.WithCancel(context.Background())
	defer stopNext()
	go func() { _ = next.Start(nextCtx) }()

	stopOld()
	if err := <-oldDone; err != nil {
		t.Fatalf("a clean stop must drain, not fail: %v", err)
	}
	if drainEnded.IsZero() {
		t.Fatal("the stopping flux-plunger did not drain")
	}
	select {
	case <-next.Elected():
		t.Fatal("the next flux-plunger became leader while the old one was still draining")
	default:
	}

	select {
	case <-next.Elected():
		if !time.Now().After(drainEnded) {
			t.Fatal("the next flux-plunger led before the drain ended")
		}
	case <-time.After(2 * time.Minute):
		t.Fatal("the next flux-plunger never became leader once the Lease ran out")
	}
}

// A delete that helm-controller handled while flux-plunger held the suspension
// (no uninstall, its finalizer dropped) leaves the fence as the only thing
// keeping the release. Draining must lift the suspension and keep the fence
// until helm-controller has handled the unsuspended generation, so the uninstall
// runs before the release goes.
func TestEnvtest_DrainCarriesADeleteThroughTheUninstall(t *testing.T) {
	ctx, cl, newRelease, stop := startFenceEnv(t)
	defer stop()
	key := newRelease(testName + "-drain-delete")
	fenceAndSuspend(t, ctx, cl, key)
	hr := &helmv2.HelmRelease{}
	if err := cl.Get(ctx, key, hr); err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := cl.Delete(ctx, hr); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if simulateHelmControllerDelete(t, ctx, cl, key) {
		t.Fatal("helm-controller must skip the uninstall of a suspended release")
	}

	r := &FluxPlunger{Client: cl, APIReader: cl}
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	type result struct {
		left int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		left, err := r.DrainFences(dctx)
		done <- result{left, err}
	}()

	// helm-controller reconciles again once the suspension is lifted. Until it
	// has, the fence must keep the release, through any number of drain passes.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := cl.Get(ctx, key, hr); err != nil {
			t.Fatalf("the fence must keep the release until helm-controller has handled it, got %v", err)
		}
		if !hr.Spec.Suspend {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("drain did not lift flux-plunger's suspension")
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(3 * drainPollInterval)
	if err := cl.Get(ctx, key, hr); err != nil {
		t.Fatalf("the fence must hold while helm-controller has not observed the unsuspended generation, got %v", err)
	}
	if !simulateHelmControllerDelete(t, ctx, cl, key) {
		t.Fatal("with the suspension lifted, helm-controller must run the uninstall")
	}

	res := <-done
	if res.err != nil || res.left != 0 {
		t.Fatalf("drain: left=%d err=%v", res.left, res.err)
	}
	if err := cl.Get(ctx, key, hr); !apierrors.IsNotFound(err) {
		t.Fatalf("after the uninstall the drain must lift the fence so the release is deleted, got %v", err)
	}
}

// A helm-controller that never handles the unsuspended release must not make the
// drain drop the fence: the next flux-plunger, or the chart's pre-delete hook when
// flux-plunger is being removed, takes it from there.
func TestEnvtest_DrainKeepsTheFenceWhenHelmControllerNeverAnswers(t *testing.T) {
	ctx, cl, newRelease, stop := startFenceEnv(t)
	defer stop()
	key := newRelease(testName + "-drain-stalled")
	fenceAndSuspend(t, ctx, cl, key)
	hr := &helmv2.HelmRelease{}
	if err := cl.Get(ctx, key, hr); err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := cl.Delete(ctx, hr); err != nil {
		t.Fatalf("delete: %v", err)
	}
	simulateHelmControllerDelete(t, ctx, cl, key)

	r := &FluxPlunger{Client: cl, APIReader: cl}
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	left, err := r.DrainFences(dctx)
	if err == nil || left != 1 {
		t.Fatalf("a drain cut short must report the fence it left: left=%d err=%v", left, err)
	}
	if err := cl.Get(ctx, key, hr); err != nil {
		t.Fatalf("the fence must keep the release, got %v", err)
	}
	if hr.Spec.Suspend {
		t.Fatal("the drain must still have lifted flux-plunger's suspension")
	}
	if !controllerutil.ContainsFinalizer(hr, fenceFinalizer) {
		t.Fatalf("the fence must stay, finalizers %v", hr.Finalizers)
	}
}

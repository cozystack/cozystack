// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"strings"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cozystack/cozystack/pkg/credentials/account"
)

const controllerKey = "cozy-system/cozystack-controller"

func controllerDeployment(image string, rolled bool) *appsv1.Deployment {
	one := int32(1)
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack-controller", Namespace: "cozy-system", Generation: 2},
		Spec: appsv1.DeploymentSpec{
			Replicas: &one,
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: image}}}},
		},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1},
	}
	if !rolled {
		// The new ReplicaSet is up but the old pod is still there.
		d.Status.Replicas = 2
	}
	return d
}

// setDeployment replaces the controller Deployment, as its rollout would.
func setDeployment(t *testing.T, m *migrator, image string, rolled bool) {
	t.Helper()
	ctx := context.Background()
	cur := &appsv1.Deployment{}
	if err := m.c.Get(ctx, types.NamespacedName{Namespace: "cozy-system", Name: "cozystack-controller"}, cur); err != nil {
		t.Fatal(err)
	}
	want := controllerDeployment(image, rolled)
	cur.Spec = want.Spec
	if err := m.c.Update(ctx, cur); err != nil {
		t.Fatal(err)
	}
	cur.Status = want.Status
	cur.Status.ObservedGeneration = cur.Generation
	if err := m.c.Status().Update(ctx, cur); err != nil {
		t.Fatal(err)
	}
}

func getRelease(t *testing.T, m *migrator, name string) *helmv2.HelmRelease {
	t.Helper()
	hr := &helmv2.HelmRelease{}
	if err := m.c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-foo", Name: name}, hr); err != nil {
		t.Fatal(err)
	}
	return hr
}

func TestSeedHoldsTheKindsReleasesAndRecordsTheRollout(t *testing.T) {
	ctx := context.Background()
	hr := release(`{"users":{"web":{}}}`, "0.1.0", true)
	foreign := release(`{"users":{"web":{}}}`, "0.1.0", true)
	foreign.Name = "postgres-paused"
	foreign.Spec.Suspend = true
	m := newMigrator(t, hr, foreign, sourceSecret(map[string]string{"web": "hunter2"}), controllerDeployment("controller:v1.7.1", true))
	m.o.holdRollouts = stringFlags{controllerKey}
	if err := m.seed(ctx); err != nil {
		t.Fatal(err)
	}
	cur := getRelease(t, m, "postgres-orders")
	if !cur.Spec.Suspend || cur.Annotations[HeldByAnnotation] != "64" {
		t.Fatalf("release not held: suspend=%v annotations=%v", cur.Spec.Suspend, cur.Annotations)
	}
	if cur.Annotations[SuspendedByAnnotation] != "" {
		t.Fatal("a hold must not pass for a rotation's suspension")
	}
	// Someone else's suspension is neither claimed nor lifted later.
	if f := getRelease(t, m, "postgres-paused"); !f.Spec.Suspend || f.Annotations[HeldByAnnotation] != "" {
		t.Fatalf("foreign suspension changed: %+v", f.ObjectMeta.Annotations)
	}
	st, err := m.loadState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Rollouts[controllerKey] != "controller:v1.7.1" {
		t.Fatalf("rollouts = %v", st.Rollouts)
	}

	// A rerun of the hook keeps the record taken before anything switched.
	setDeployment(t, m, "controller:v1.8.0", false)
	if err := m.seed(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ = m.loadState(ctx); st.Rollouts[controllerKey] != "controller:v1.7.1" {
		t.Fatalf("rollouts after rerun = %v", st.Rollouts)
	}
}

func TestSeedWithoutAHoldLeavesReleasesRunning(t *testing.T) {
	ctx := context.Background()
	hr := release(`{"users":{"web":{}}}`, "0.1.0", true)
	m := newMigrator(t, hr, sourceSecret(map[string]string{"web": "hunter2"}))
	if err := m.seed(ctx); err != nil {
		t.Fatal(err)
	}
	if cur := getRelease(t, m, "postgres-orders"); cur.Spec.Suspend {
		t.Fatal("a kind without --hold-until-rollout must not be suspended")
	}
	if st, _ := m.loadState(ctx); len(st.Rollouts) != 0 {
		t.Fatalf("rollouts = %v", st.Rollouts)
	}
}

func TestFinishKeepsReleasesHeldUntilTheNewRevisionRollsOut(t *testing.T) {
	ctx := context.Background()
	hr := release(`{"users":{"web":{}}}`, "0.1.0", true)
	m := newMigrator(t, hr, sourceSecret(map[string]string{"web": "hunter2"}), controllerDeployment("controller:v1.7.1", true))
	m.o.holdRollouts = stringFlags{controllerKey}
	if err := m.seed(ctx); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		image  string
		rolled bool
		want   string
	}{
		{"still the old revision", "controller:v1.7.1", true, "old revision"},
		{"the old pod is still running", "controller:v1.8.0", false, "rolling out"},
	} {
		setDeployment(t, m, c.image, c.rolled)
		err := m.finish(ctx)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: finish = %v, want an error naming %q", c.name, err, c.want)
		}
		if cur := getRelease(t, m, "postgres-orders"); !cur.Spec.Suspend || cur.Annotations[HeldByAnnotation] != "64" {
			t.Fatalf("%s: the release must stay held", c.name)
		}
		st, _ := m.loadState(ctx)
		if why := st.Skipped["tenant-foo/postgres-orders"]; !strings.Contains(why, "still suspended") {
			t.Fatalf("%s: skipped = %v", c.name, st.Skipped)
		}
		cm := &corev1.ConfigMap{}
		if err := m.c.Get(ctx, types.NamespacedName{Namespace: "cozy-system", Name: "credentials-migration-64"}, cm); err != nil {
			t.Fatal(err)
		}
		if cm.Labels[incompleteLabel] != "true" {
			t.Fatalf("%s: a held release must fire the incomplete alert", c.name)
		}
		if _, ok := m.secret(t, "postgres-orders-credentials"); !ok {
			t.Fatalf("%s: nothing may be retired while the release is held", c.name)
		}
	}
}

func TestFinishResumesHeldReleasesWithTheirValuesFromOnceRolledOut(t *testing.T) {
	ctx := context.Background()
	hr := release(`{"users":{"web":{}}}`, "0.1.0", true)
	m := newMigrator(t, hr, sourceSecret(map[string]string{"web": "hunter2"}), controllerDeployment("controller:v1.7.1", true))
	m.o.valuesPath = "_accounts"
	m.o.holdRollouts = stringFlags{controllerKey}
	if err := m.seed(ctx); err != nil {
		t.Fatal(err)
	}
	// The old controller rebuilds valuesFrom and drops the account entries.
	cur := getRelease(t, m, "postgres-orders")
	patch := client.MergeFrom(cur.DeepCopy())
	cur.Spec.ValuesFrom = []helmv2.ValuesReference{{Kind: "Secret", Name: "cozystack-values"}}
	if err := m.c.Patch(ctx, cur, patch); err != nil {
		t.Fatal(err)
	}

	setDeployment(t, m, "controller:v1.8.0", true)
	// Resumed, but the chart has not switched yet: the release itself is
	// left out by the existing wait, not by the hold.
	err := m.finish(ctx)
	if err == nil || strings.Contains(err.Error(), "still suspended") {
		t.Fatalf("finish = %v", err)
	}
	cur = getRelease(t, m, "postgres-orders")
	if cur.Spec.Suspend || cur.Annotations[HeldByAnnotation] != "" {
		t.Fatalf("not resumed: suspend=%v annotations=%v", cur.Spec.Suspend, cur.Annotations)
	}
	want := account.ValuesFrom("postgres-orders.web.account", "_accounts", "web")
	if len(cur.Spec.ValuesFrom) != 2 || cur.Spec.ValuesFrom[1] != want {
		t.Fatalf("valuesFrom = %+v: the entries must be back before the first render", cur.Spec.ValuesFrom)
	}

	// The chart switches; the conversion completes as before.
	cur.Status.History = helmv2.Snapshots{{Version: 2, ChartVersion: "0.2.0", Status: "deployed"}}
	if err := m.c.Status().Update(ctx, cur); err != nil {
		t.Fatal(err)
	}
	if err := m.finish(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := m.loadState(ctx); !st.Releases["tenant-foo/postgres-orders"].Done || len(st.Skipped) != 0 {
		t.Fatalf("state = %+v", st)
	}
}

func TestFinishWaitsForADeploymentAbsentAtTheHook(t *testing.T) {
	ctx := context.Background()
	hr := release(`{"users":{"web":{}}}`, "0.1.0", true)
	m := newMigrator(t, hr, sourceSecret(map[string]string{"web": "hunter2"}))
	m.o.holdRollouts = stringFlags{controllerKey}
	if err := m.seed(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := m.loadState(ctx); st.Rollouts[controllerKey] != "" {
		t.Fatalf("rollouts = %v", st.Rollouts)
	}
	if err := m.finish(ctx); err == nil || !strings.Contains(err.Error(), "absent") {
		t.Fatalf("finish = %v", err)
	}
	if err := m.c.Create(ctx, controllerDeployment("controller:v1.8.0", true)); err != nil {
		t.Fatal(err)
	}
	_ = m.finish(ctx)
	if cur := getRelease(t, m, "postgres-orders"); cur.Spec.Suspend {
		t.Fatal("the release must be resumed once the Deployment exists and rolled out")
	}
}

func TestRunRefusesAMalformedHoldRollout(t *testing.T) {
	err := run([]string{"seed", "--migration=65", "--kind=ClickHouse", "--format=sha256", "--hold-until-rollout=cozystack-controller"})
	if err == nil || !strings.Contains(err.Error(), "--hold-until-rollout") {
		t.Fatalf("run = %v", err)
	}
}

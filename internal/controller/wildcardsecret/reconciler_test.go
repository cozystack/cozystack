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

package wildcardsecret

import (
	"context"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("client-go scheme: %v", err)
	}
	return s
}

// configSecret builds the platform values channel Secret
// (cozy-system/cozystack-values) carrying the wildcard source name and the
// publishing namespace, exactly as packages/core/platform writes it.
func configSecret(wildcardName, exposeIngress string) *corev1.Secret {
	values := fmt.Sprintf("_cluster:\n  expose-ingress: %q\n  wildcard-secret-name: %q\n", exposeIngress, wildcardName)
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: platformValuesNamespace, Name: platformValuesName},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{platformValuesKey: []byte(values)},
	}
}

// tlsSecret builds an operator wildcard TLS Secret. Unlike the earlier
// label-driven model, the source carries no marker — it is identified
// purely by the name/namespace named in the platform values channel.
func tlsSecret(ns, name string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Type:       corev1.SecretTypeTLS,
		Data:       data,
	}
}

// terminationNS builds a tenant namespace that owns a termination point of
// the given kind ("ingress" or "gateway"): the owner label equals the
// namespace's own name, which is how the tenant chart marks ownership.
func terminationNS(name, kind string) *corev1.Namespace {
	labels := map[string]string{ingressOwnerLabel: "", gatewayOwnerLabel: ""}
	switch kind {
	case "ingress":
		labels[ingressOwnerLabel] = name
	case "gateway":
		labels[gatewayOwnerLabel] = name
	}
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

// inheritingNS builds a tenant namespace that does NOT own a termination
// point — its owner labels point at an ancestor, not itself.
func inheritingNS(name, owner string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   name,
		Labels: map[string]string{ingressOwnerLabel: owner, gatewayOwnerLabel: ""},
	}}
}

// newReconciler builds a reconciler whose cached Client and uncached
// Reader are both the fake client (the fake has no cache, so the two
// coincide in tests — exactly what production does for correctness, minus
// the staleness the cache could introduce).
func newReconciler(t *testing.T, c client.Client) *Reconciler {
	t.Helper()
	return &Reconciler{Client: c, Reader: c, Scheme: newScheme(t), Recorder: record.NewFakeRecorder(16)}
}

func runReconcile(t *testing.T, c client.Client) error {
	t.Helper()
	_, err := newReconciler(t, c).Reconcile(context.TODO(), ctrl.Request{NamespacedName: configKey})
	return err
}

func mustReconcile(t *testing.T, c client.Client) {
	t.Helper()
	if err := runReconcile(t, c); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

func getSecret(t *testing.T, c client.Client, ns, name string) (*corev1.Secret, bool) {
	t.Helper()
	got := &corev1.Secret{}
	err := c.Get(context.TODO(), types.NamespacedName{Namespace: ns, Name: name}, got)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("get secret %s/%s: %v", ns, name, err)
	}
	return got, true
}

// TestReconcile_ReplicatesIntoTerminationNamespacesOnly pins the core
// behavior: the operator wildcard Secret named in the values channel is
// mirrored into exactly the tenant namespaces that own a TLS termination
// point, never into inheriting namespaces, and never over the source.
func TestReconcile_ReplicatesIntoTerminationNamespacesOnly(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		configSecret("wildcard-tls", "tenant-root"),
		tlsSecret("tenant-root", "wildcard-tls", map[string][]byte{"tls.crt": []byte("CRT"), "tls.key": []byte("KEY")}),
		terminationNS("tenant-foo", "ingress"),
		terminationNS("tenant-bar", "gateway"),
		inheritingNS("tenant-baz", "tenant-root"),
	).Build()
	mustReconcile(t, c)

	for _, ns := range []string{"tenant-foo", "tenant-bar"} {
		cp, ok := getSecret(t, c, ns, "wildcard-tls")
		if !ok {
			t.Fatalf("expected a wildcard copy in %s", ns)
		}
		if string(cp.Data["tls.crt"]) != "CRT" || string(cp.Data["tls.key"]) != "KEY" {
			t.Errorf("copy in %s has wrong data: %v", ns, cp.Data)
		}
		if cp.Type != corev1.SecretTypeTLS {
			t.Errorf("copy in %s has wrong type %q", ns, cp.Type)
		}
	}
	if _, ok := getSecret(t, c, "tenant-baz", "wildcard-tls"); ok {
		t.Errorf("inheriting namespace tenant-baz must not receive a copy")
	}
	src, ok := getSecret(t, c, "tenant-root", "wildcard-tls")
	if !ok {
		t.Fatalf("source secret disappeared")
	}
	if _, isCopy := src.Labels[CopyLabel]; isCopy {
		t.Errorf("source secret must not be marked as a copy")
	}
}

// TestReconcile_CopyCarriesMarkers pins that a replica is tagged as a
// managed copy with a back-reference to its source.
func TestReconcile_CopyCarriesMarkers(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		configSecret("wildcard-tls", "tenant-root"),
		tlsSecret("tenant-root", "wildcard-tls", map[string][]byte{"tls.crt": []byte("C")}),
		terminationNS("tenant-foo", "ingress"),
	).Build()
	mustReconcile(t, c)

	cp, ok := getSecret(t, c, "tenant-foo", "wildcard-tls")
	if !ok {
		t.Fatalf("expected copy in tenant-foo")
	}
	if cp.Labels[CopyLabel] != "true" {
		t.Errorf("copy must carry %s=true, labels=%v", CopyLabel, cp.Labels)
	}
	if got := cp.Annotations[SourceRefAnnotation]; got != "tenant-root/wildcard-tls" {
		t.Errorf("copy must reference its source, got %q", got)
	}
}

// TestReconcile_RotationPropagatesToCopies pins that updating the source
// data re-syncs every existing copy.
func TestReconcile_RotationPropagatesToCopies(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		configSecret("wildcard-tls", "tenant-root"),
		tlsSecret("tenant-root", "wildcard-tls", map[string][]byte{"tls.crt": []byte("OLD")}),
		terminationNS("tenant-foo", "ingress"),
	).Build()
	mustReconcile(t, c)

	live := &corev1.Secret{}
	if err := c.Get(context.TODO(), types.NamespacedName{Namespace: "tenant-root", Name: "wildcard-tls"}, live); err != nil {
		t.Fatalf("get source: %v", err)
	}
	live.Data["tls.crt"] = []byte("NEW")
	if err := c.Update(context.TODO(), live); err != nil {
		t.Fatalf("rotate source: %v", err)
	}
	mustReconcile(t, c)

	cp, _ := getSecret(t, c, "tenant-foo", "wildcard-tls")
	if string(cp.Data["tls.crt"]) != "NEW" {
		t.Errorf("rotation did not propagate, got %q", cp.Data["tls.crt"])
	}
	// The update path must preserve the managed-by marker so the replica
	// stays prunable and is never mistaken for a foreign Secret.
	if cp.Labels[CopyLabel] != "true" {
		t.Errorf("update must preserve %s on the replica, labels=%v", CopyLabel, cp.Labels)
	}
}

// TestReconcile_PrunesCopyWhenNamespaceStopsTerminating pins cleanup when
// a namespace no longer owns a termination point.
func TestReconcile_PrunesCopyWhenNamespaceStopsTerminating(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		configSecret("wildcard-tls", "tenant-root"),
		tlsSecret("tenant-root", "wildcard-tls", map[string][]byte{"tls.crt": []byte("C")}),
		terminationNS("tenant-foo", "ingress"),
	).Build()
	mustReconcile(t, c)
	if _, ok := getSecret(t, c, "tenant-foo", "wildcard-tls"); !ok {
		t.Fatalf("precondition: expected copy in tenant-foo")
	}

	ns := &corev1.Namespace{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "tenant-foo"}, ns); err != nil {
		t.Fatalf("get ns: %v", err)
	}
	ns.Labels[ingressOwnerLabel] = "tenant-root"
	if err := c.Update(context.TODO(), ns); err != nil {
		t.Fatalf("update ns: %v", err)
	}
	mustReconcile(t, c)

	if _, ok := getSecret(t, c, "tenant-foo", "wildcard-tls"); ok {
		t.Errorf("copy in tenant-foo must be pruned once it stops terminating TLS")
	}
}

// TestReconcile_SourceDeletedKeepsReplicas pins the non-destructive
// contract: deleting the source Secret while the wildcard name is still
// configured must NOT tear replicas down — that would drop TLS across
// every tenant on a transient absence, a delete+recreate rotation, or a
// misresolved publishing namespace. Teardown happens only on an explicit
// disable (empty wildcard name), covered by DisablingViaConfig.
func TestReconcile_SourceDeletedKeepsReplicas(t *testing.T) {
	s := newScheme(t)
	src := tlsSecret("tenant-root", "wildcard-tls", map[string][]byte{"tls.crt": []byte("C")})
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		configSecret("wildcard-tls", "tenant-root"), src,
		terminationNS("tenant-foo", "ingress"), terminationNS("tenant-bar", "gateway"),
	).Build()
	mustReconcile(t, c)

	if err := c.Delete(context.TODO(), src); err != nil {
		t.Fatalf("delete source: %v", err)
	}
	mustReconcile(t, c)

	for _, ns := range []string{"tenant-foo", "tenant-bar"} {
		if _, ok := getSecret(t, c, ns, "wildcard-tls"); !ok {
			t.Errorf("copy in %s must be kept while the source is transiently absent", ns)
		}
	}
}

// TestReconcile_ReplicaSelfHealsAfterDeletion pins that a replica deleted
// out-of-band is recreated. The security posture ("each consumer reads
// only its own-namespace copy") depends on the replica actually existing.
// In production a replica deletion is delivered by the scoped Secret-cache
// watch (replicas carry CopyLabel, so they are in the cache scope) and
// mapped to the singleton via enqueueConfigKey (covered by
// TestEnqueueConfigKey); this test asserts the resulting reconcile body
// restores the replica.
func TestReconcile_ReplicaSelfHealsAfterDeletion(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		configSecret("wildcard-tls", "tenant-root"),
		tlsSecret("tenant-root", "wildcard-tls", map[string][]byte{"tls.crt": []byte("C")}),
		terminationNS("tenant-foo", "ingress"),
	).Build()
	mustReconcile(t, c)

	cp, ok := getSecret(t, c, "tenant-foo", "wildcard-tls")
	if !ok {
		t.Fatalf("precondition: expected a replica in tenant-foo")
	}
	if err := c.Delete(context.TODO(), cp); err != nil {
		t.Fatalf("delete replica: %v", err)
	}
	mustReconcile(t, c)

	if _, ok := getSecret(t, c, "tenant-foo", "wildcard-tls"); !ok {
		t.Errorf("a deleted replica must be recreated on the next reconcile")
	}
}

// TestReconcile_PublishingNamespaceNeverSelfReplicates pins the
// load-bearing exclusion: the publishing namespace owns its own ingress
// controller (its owner label equals its own name), so without the
// sourceNS exclusion the controller would try to "replicate" the source
// over itself. The source Secret must be left untouched — never relabeled
// as a managed copy and never modified.
func TestReconcile_PublishingNamespaceNeverSelfReplicates(t *testing.T) {
	s := newScheme(t)
	// tenant-root carries its own ingress-owner label, so ownsTerminationPoint
	// would be true for it.
	root := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "tenant-root",
		Labels: map[string]string{ingressOwnerLabel: "tenant-root", gatewayOwnerLabel: ""},
	}}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		configSecret("wildcard-tls", "tenant-root"),
		tlsSecret("tenant-root", "wildcard-tls", map[string][]byte{"tls.crt": []byte("SRC")}),
		root,
		terminationNS("tenant-foo", "ingress"),
	).Build()
	mustReconcile(t, c)

	src, ok := getSecret(t, c, "tenant-root", "wildcard-tls")
	if !ok {
		t.Fatalf("source secret disappeared")
	}
	if _, isCopy := src.Labels[CopyLabel]; isCopy {
		t.Errorf("the source must never be relabeled as a managed copy")
	}
	if string(src.Data["tls.crt"]) != "SRC" {
		t.Errorf("the source data must be left untouched, got %q", src.Data["tls.crt"])
	}
	if _, ok := getSecret(t, c, "tenant-foo", "wildcard-tls"); !ok {
		t.Errorf("a genuine child namespace must still receive its replica")
	}
}

// TestReconcile_DisablingViaConfigPrunesAllCopies pins the disable path:
// clearing _cluster.wildcard-secret-name (operator disables the feature)
// must tear every replica down. The values channel is in the Secret cache
// scope, so this edit is watched and re-triggers a reconcile, and the
// present-channel/empty-name path prunes every replica.
func TestReconcile_DisablingViaConfigPrunesAllCopies(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		configSecret("wildcard-tls", "tenant-root"),
		tlsSecret("tenant-root", "wildcard-tls", map[string][]byte{"tls.crt": []byte("C")}),
		terminationNS("tenant-foo", "ingress"),
	).Build()
	mustReconcile(t, c)
	if _, ok := getSecret(t, c, "tenant-foo", "wildcard-tls"); !ok {
		t.Fatalf("precondition: expected copy in tenant-foo")
	}

	// Operator clears the wildcard name in the values channel.
	cfg := &corev1.Secret{}
	if err := c.Get(context.TODO(), configKey, cfg); err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.Data[platformValuesKey] = []byte("_cluster:\n  expose-ingress: \"tenant-root\"\n  wildcard-secret-name: \"\"\n")
	if err := c.Update(context.TODO(), cfg); err != nil {
		t.Fatalf("disable: %v", err)
	}
	mustReconcile(t, c)

	if _, ok := getSecret(t, c, "tenant-foo", "wildcard-tls"); ok {
		t.Errorf("replicas must be pruned once the feature is disabled")
	}
}

// TestReconcile_DisableDeletesCopyAtSourceLocation pins the inverse of the
// active-path source-slot guard: on an explicit disable (empty name) the
// controller reads no source, so a copy-labelled Secret sitting at the old
// source location IS removed along with every other replica. A genuine
// operator source is never copy-labelled, so an un-labelled Secret is never
// touched — this guards against a future "always protect the source slot"
// tightening silently breaking disable-prune.
func TestReconcile_DisableDeletesCopyAtSourceLocation(t *testing.T) {
	s := newScheme(t)
	copyAtSource := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "wildcard-tls", Namespace: "tenant-root",
			Labels: map[string]string{CopyLabel: "true"},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{"tls.crt": []byte("STALE")},
	}
	copyInChild := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "wildcard-tls", Namespace: "tenant-foo",
			Labels: map[string]string{CopyLabel: "true"},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{"tls.crt": []byte("C")},
	}
	userSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: "tenant-bar"},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"k": []byte("v")},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		configSecret("", "tenant-root"), // disabled: explicit empty wildcard name
		copyAtSource, copyInChild, userSecret,
	).Build()
	mustReconcile(t, c)

	if _, ok := getSecret(t, c, "tenant-root", "wildcard-tls"); ok {
		t.Errorf("disable must delete a copy-labelled Secret even at the source location")
	}
	if _, ok := getSecret(t, c, "tenant-foo", "wildcard-tls"); ok {
		t.Errorf("disable must delete child replicas")
	}
	if _, ok := getSecret(t, c, "tenant-bar", "app-config"); !ok {
		t.Errorf("disable must never touch an un-labelled user Secret")
	}
}

// TestReconcile_RenamedSourcePrunesOldReplicas pins that pointing the
// values channel at a different Secret name removes the old-named replicas
// and creates new-named ones.
func TestReconcile_RenamedSourcePrunesOldReplicas(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		configSecret("wildcard-tls", "tenant-root"),
		tlsSecret("tenant-root", "wildcard-tls", map[string][]byte{"tls.crt": []byte("A")}),
		tlsSecret("tenant-root", "wildcard-tls-2", map[string][]byte{"tls.crt": []byte("B")}),
		terminationNS("tenant-foo", "ingress"),
	).Build()
	mustReconcile(t, c)
	if _, ok := getSecret(t, c, "tenant-foo", "wildcard-tls"); !ok {
		t.Fatalf("precondition: expected copy named wildcard-tls")
	}

	cfg := &corev1.Secret{}
	if err := c.Get(context.TODO(), configKey, cfg); err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.Data[platformValuesKey] = []byte("_cluster:\n  expose-ingress: \"tenant-root\"\n  wildcard-secret-name: \"wildcard-tls-2\"\n")
	if err := c.Update(context.TODO(), cfg); err != nil {
		t.Fatalf("rename: %v", err)
	}
	mustReconcile(t, c)

	if _, ok := getSecret(t, c, "tenant-foo", "wildcard-tls"); ok {
		t.Errorf("old-named replica must be pruned after a source rename")
	}
	cp, ok := getSecret(t, c, "tenant-foo", "wildcard-tls-2")
	if !ok || string(cp.Data["tls.crt"]) != "B" {
		t.Errorf("new-named replica must be created from the renamed source, got %v ok=%v", cp, ok)
	}
}

// TestReconcile_DoesNotOverwriteForeignSecret pins safety: a pre-existing
// Secret of the same name the controller does not own must never be
// clobbered, the collision must not requeue, and it must not block
// replication into the other namespaces.
func TestReconcile_DoesNotOverwriteForeignSecret(t *testing.T) {
	s := newScheme(t)
	foreign := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "wildcard-tls", Namespace: "tenant-foo"},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"tls.crt": []byte("FOREIGN")},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		configSecret("wildcard-tls", "tenant-root"),
		tlsSecret("tenant-root", "wildcard-tls", map[string][]byte{"tls.crt": []byte("SRC")}),
		foreign,
		terminationNS("tenant-foo", "ingress"),
		terminationNS("tenant-bar", "gateway"),
	).Build()

	if err := runReconcile(t, c); err != nil {
		t.Fatalf("a foreign collision must not requeue (return error): %v", err)
	}
	got, _ := getSecret(t, c, "tenant-foo", "wildcard-tls")
	if string(got.Data["tls.crt"]) != "FOREIGN" {
		t.Errorf("foreign secret was overwritten: %q", got.Data["tls.crt"])
	}
	if _, ok := getSecret(t, c, "tenant-bar", "wildcard-tls"); !ok {
		t.Errorf("foreign collision must not block replication into tenant-bar")
	}
}

// TestReconcile_ForeignCollisionEmitsWarningEvent pins that a skipped
// foreign-Secret collision is surfaced as a Warning Event on the colliding
// Secret (which lives in the affected namespace), so the otherwise-silent
// skip is visible via `kubectl get events -n <ns>`.
func TestReconcile_ForeignCollisionEmitsWarningEvent(t *testing.T) {
	s := newScheme(t)
	foreign := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "wildcard-tls", Namespace: "tenant-foo"},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"tls.crt": []byte("FOREIGN")},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		configSecret("wildcard-tls", "tenant-root"),
		tlsSecret("tenant-root", "wildcard-tls", map[string][]byte{"tls.crt": []byte("SRC")}),
		foreign,
		terminationNS("tenant-foo", "ingress"),
	).Build()

	rec := record.NewFakeRecorder(16)
	r := &Reconciler{Client: c, Reader: c, Scheme: s, Recorder: rec}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{NamespacedName: configKey}); err != nil {
		t.Fatalf("reconcile must not fail on a foreign collision: %v", err)
	}

	select {
	case e := <-rec.Events:
		if !strings.Contains(e, "Warning") || !strings.Contains(e, "WildcardReplicaSkipped") {
			t.Errorf("expected a Warning WildcardReplicaSkipped event, got %q", e)
		}
	default:
		t.Errorf("expected a Warning event on the foreign collision, got none")
	}
}

// TestReconcile_TransientWriteErrorRequeues pins that a non-collision write
// failure is surfaced (so controller-runtime requeues), while a healthy
// namespace in the same pass still gets its replica.
func TestReconcile_TransientWriteErrorRequeues(t *testing.T) {
	s := newScheme(t)
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(
		configSecret("wildcard-tls", "tenant-root"),
		tlsSecret("tenant-root", "wildcard-tls", map[string][]byte{"tls.crt": []byte("C")}),
		terminationNS("tenant-foo", "ingress"),
		terminationNS("tenant-bar", "gateway"),
	).Build()
	c := interceptor.NewClient(base, interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if obj.GetNamespace() == "tenant-foo" {
				return fmt.Errorf("synthetic transient error")
			}
			return cl.Create(ctx, obj, opts...)
		},
	})

	if err := runReconcile(t, c); err == nil {
		t.Fatalf("a transient write error must be returned so the work is requeued")
	}
	if _, ok := getSecret(t, c, "tenant-bar", "wildcard-tls"); !ok {
		t.Errorf("a transient error in one namespace must not block another")
	}
}

// TestReconcile_NonTLSSourceIsNotPropagated pins that a source named in the
// channel but not of type kubernetes.io/tls is ignored.
func TestReconcile_NonTLSSourceIsNotPropagated(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		configSecret("wildcard-tls", "tenant-root"),
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "wildcard-tls", Namespace: "tenant-root"},
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{"foo": []byte("bar")},
		},
		terminationNS("tenant-foo", "ingress"),
	).Build()
	mustReconcile(t, c)

	if _, ok := getSecret(t, c, "tenant-foo", "wildcard-tls"); ok {
		t.Errorf("a non-TLS source must not be propagated")
	}
}

// TestReconcile_NoPlatformValuesKeepsExistingReplicas pins that a
// transient loss of the platform values channel does NOT prune replicas.
// Channel absence means "desired state unknown", which is deliberately
// distinct from an explicit disable (channel present, empty wildcard
// name). Without this distinction a platform re-render or a brief delete
// of cozystack-values would drop tenant TLS cluster-wide.
func TestReconcile_NoPlatformValuesKeepsExistingReplicas(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		configSecret("wildcard-tls", "tenant-root"),
		tlsSecret("tenant-root", "wildcard-tls", map[string][]byte{"tls.crt": []byte("C")}),
		terminationNS("tenant-foo", "ingress"),
	).Build()
	mustReconcile(t, c)
	if _, ok := getSecret(t, c, "tenant-foo", "wildcard-tls"); !ok {
		t.Fatalf("precondition: expected a replica in tenant-foo")
	}

	// Simulate a transient loss of the platform values channel.
	cfg := &corev1.Secret{}
	if err := c.Get(context.TODO(), configKey, cfg); err != nil {
		t.Fatalf("get config: %v", err)
	}
	if err := c.Delete(context.TODO(), cfg); err != nil {
		t.Fatalf("delete config: %v", err)
	}
	mustReconcile(t, c)

	if _, ok := getSecret(t, c, "tenant-foo", "wildcard-tls"); !ok {
		t.Errorf("existing replicas must be preserved when the values channel is transiently absent")
	}
}

// TestReconcile_SkipsTerminatingNamespace pins that a termination-owning
// namespace being deleted (DeletionTimestamp set) is skipped: the API
// server forbids creating a replica in a Terminating namespace, and the
// owner labels linger until GC, so without the skip every tenant teardown
// would produce forbidden-error requeue churn. A healthy sibling still
// gets its replica.
func TestReconcile_SkipsTerminatingNamespace(t *testing.T) {
	s := newScheme(t)
	dying := terminationNS("tenant-dying", "ingress")
	now := metav1.Now()
	dying.DeletionTimestamp = &now
	dying.Finalizers = []string{"kubernetes"} // the fake client requires a finalizer alongside a DeletionTimestamp
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		configSecret("wildcard-tls", "tenant-root"),
		tlsSecret("tenant-root", "wildcard-tls", map[string][]byte{"tls.crt": []byte("C")}),
		dying,
		terminationNS("tenant-foo", "ingress"),
	).Build()

	if err := runReconcile(t, c); err != nil {
		t.Fatalf("reconcile must not error on a terminating namespace: %v", err)
	}
	if _, ok := getSecret(t, c, "tenant-dying", "wildcard-tls"); ok {
		t.Errorf("must not create a replica in a terminating namespace")
	}
	if _, ok := getSecret(t, c, "tenant-foo", "wildcard-tls"); !ok {
		t.Errorf("a healthy sibling must still receive its replica")
	}
}

// TestReconcile_MissingWildcardKeyKeepsReplicas pins that a values channel
// which omits the wildcard-secret-name key entirely (a partial or older
// render) is treated as "desired state unknown" — replicas kept — not as
// an explicit disable. Only an explicitly empty value prunes.
func TestReconcile_MissingWildcardKeyKeepsReplicas(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		configSecret("wildcard-tls", "tenant-root"),
		tlsSecret("tenant-root", "wildcard-tls", map[string][]byte{"tls.crt": []byte("C")}),
		terminationNS("tenant-foo", "ingress"),
	).Build()
	mustReconcile(t, c)
	if _, ok := getSecret(t, c, "tenant-foo", "wildcard-tls"); !ok {
		t.Fatalf("precondition: expected a replica in tenant-foo")
	}

	// Swap the channel to one that has _cluster but no wildcard-secret-name.
	cfg := &corev1.Secret{}
	if err := c.Get(context.TODO(), configKey, cfg); err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.Data[platformValuesKey] = []byte("_cluster:\n  expose-ingress: \"tenant-root\"\n")
	if err := c.Update(context.TODO(), cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	mustReconcile(t, c)

	if _, ok := getSecret(t, c, "tenant-foo", "wildcard-tls"); !ok {
		t.Errorf("a channel missing the wildcard-secret-name key must keep replicas, not prune")
	}
}

// TestReconcile_NeverPrunesSecretAtSourceLocation pins that a copy-labelled
// Secret sitting at the source location is never pruned — even though the
// source namespace is excluded from the keep set. The controller reads
// that Secret as its source, so deleting it would flap the source. This
// guards the unusual case of a stale replica occupying the source slot
// after a publishing-namespace switch.
func TestReconcile_NeverPrunesSecretAtSourceLocation(t *testing.T) {
	s := newScheme(t)
	sourceWithCopyLabel := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wildcard-tls",
			Namespace: "tenant-root",
			Labels:    map[string]string{CopyLabel: "true"},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{"tls.crt": []byte("SRC")},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		configSecret("wildcard-tls", "tenant-root"),
		sourceWithCopyLabel,
		terminationNS("tenant-foo", "ingress"),
	).Build()
	mustReconcile(t, c)

	if _, ok := getSecret(t, c, "tenant-root", "wildcard-tls"); !ok {
		t.Errorf("a Secret at the source location must never be pruned, even with the copy label")
	}
	// And a genuine child still gets its replica.
	if _, ok := getSecret(t, c, "tenant-foo", "wildcard-tls"); !ok {
		t.Errorf("a genuine child namespace must still receive its replica")
	}
}

// TestReconcile_PublishingNamespaceMismatchKeepsAndRequeues pins the
// split-deployment trap: when expose-ingress resolves to a namespace that
// does not hold the source, the source read NotFounds. The reconciler must
// NOT replicate (no source data) and must NOT touch the real source, but it
// must keep requeueing so the feature recovers if the namespace becomes
// correct — rather than erroring or pruning.
func TestReconcile_PublishingNamespaceMismatchKeepsAndRequeues(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		// expose-ingress points at "other-ns", but the source lives in tenant-root.
		configSecret("wildcard-tls", "other-ns"),
		tlsSecret("tenant-root", "wildcard-tls", map[string][]byte{"tls.crt": []byte("SRC")}),
		terminationNS("tenant-foo", "ingress"),
	).Build()

	res, err := newReconciler(t, c).Reconcile(context.TODO(), ctrl.Request{NamespacedName: configKey})
	if err != nil {
		t.Fatalf("mismatch reconcile must not error: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Errorf("a mismatched publishing namespace must keep requeueing, got %v", res.RequeueAfter)
	}
	if _, ok := getSecret(t, c, "tenant-foo", "wildcard-tls"); ok {
		t.Errorf("no replica must be created when the source is not found in the resolved namespace")
	}
	src, ok := getSecret(t, c, "tenant-root", "wildcard-tls")
	if !ok || string(src.Data["tls.crt"]) != "SRC" {
		t.Errorf("the real source in tenant-root must be left untouched, got %v ok=%v", src, ok)
	}
}

// TestSecretCacheByObject_ScopesToReplicasAndConfig pins the cache scope so
// a future change cannot silently widen the cozystack-controller Secret
// informer back to every cluster Secret. Only managed replicas (CopyLabel,
// any namespace) and the values channel (cozy-system/cozystack-values) may
// be cached; the operator source and arbitrary tenant Secrets must not.
func TestSecretCacheByObject_ScopesToReplicasAndConfig(t *testing.T) {
	by := SecretCacheByObject()

	all, ok := by.Namespaces[cache.AllNamespaces]
	if !ok {
		t.Fatalf("expected an all-namespaces cache config")
	}
	if all.LabelSelector == nil || all.LabelSelector.String() != CopyLabel+"=true" {
		t.Errorf("all-namespaces config must select only managed replicas, got %v", all.LabelSelector)
	}

	cfg, ok := by.Namespaces[platformValuesNamespace]
	if !ok {
		t.Fatalf("expected a %s cache config for the values channel", platformValuesNamespace)
	}
	if cfg.FieldSelector == nil || cfg.FieldSelector.String() != "metadata.name="+platformValuesName {
		t.Errorf("%s config must select only the values channel, got %v", platformValuesNamespace, cfg.FieldSelector)
	}
}

// TestEnqueueConfigKey pins the watch wiring: every delivered event (a
// replica change, a values-channel change, a namespace change) maps to the
// single config key, so the reconcile body — which self-heal, prune, and
// rotation all run through — is actually triggered.
func TestEnqueueConfigKey(t *testing.T) {
	obj := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-foo", Name: "wildcard-tls"}}
	reqs := enqueueConfigKey(context.TODO(), obj)
	if len(reqs) != 1 || reqs[0].NamespacedName != configKey {
		t.Errorf("every watched event must enqueue the config key, got %+v", reqs)
	}
}

// TestOwnsTerminationPoint is the direct table test for the gate that
// decides the whole replication set: a namespace owns a termination point
// exactly when an owner label equals its own name (it runs its own
// controller/Gateway), not when the label points at an ancestor.
func TestOwnsTerminationPoint(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"owns ingress", map[string]string{ingressOwnerLabel: "tenant-foo"}, true},
		{"owns gateway", map[string]string{gatewayOwnerLabel: "tenant-foo"}, true},
		{"inherits ingress from ancestor", map[string]string{ingressOwnerLabel: "tenant-root"}, false},
		{"empty owner labels", map[string]string{ingressOwnerLabel: "", gatewayOwnerLabel: ""}, false},
		{"no labels at all", nil, false},
	}
	for _, tc := range cases {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-foo", Labels: tc.labels}}
		if got := ownsTerminationPoint(ns); got != tc.want {
			t.Errorf("%s: ownsTerminationPoint = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestReconcile_RequeuesWhileActiveOnly pins the resync contract: because
// the source is not watched, an active reconcile must requeue so an
// in-place source rotation propagates; a disabled reconcile must NOT
// requeue (the watched values channel re-triggers on re-enable).
func TestReconcile_RequeuesWhileActiveOnly(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		configSecret("wildcard-tls", "tenant-root"),
		tlsSecret("tenant-root", "wildcard-tls", map[string][]byte{"tls.crt": []byte("C")}),
		terminationNS("tenant-foo", "ingress"),
	).Build()

	res, err := newReconciler(t, c).Reconcile(context.TODO(), ctrl.Request{NamespacedName: configKey})
	if err != nil {
		t.Fatalf("active reconcile: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Errorf("an active reconcile must requeue to catch source rotation, got %v", res.RequeueAfter)
	}

	// Disable and reconcile again: no requeue (the channel watch re-triggers).
	cfg := &corev1.Secret{}
	if err := c.Get(context.TODO(), configKey, cfg); err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.Data[platformValuesKey] = []byte("_cluster:\n  expose-ingress: \"tenant-root\"\n  wildcard-secret-name: \"\"\n")
	if err := c.Update(context.TODO(), cfg); err != nil {
		t.Fatalf("disable: %v", err)
	}
	res, err = newReconciler(t, c).Reconcile(context.TODO(), ctrl.Request{NamespacedName: configKey})
	if err != nil {
		t.Fatalf("disabled reconcile: %v", err)
	}
	// Assert the zero Result: neither an immediate requeue nor a scheduled
	// one. Comparing against ctrl.Result{} covers both the (deprecated)
	// Requeue flag and RequeueAfter without naming the deprecated field.
	if res != (ctrl.Result{}) {
		t.Errorf("a disabled reconcile must not requeue at all, got %+v", res)
	}
}

// TestReconcile_ReadsRouteToCacheExceptSourceAndCollision pins which reads go
// through the cached Client and which go through the uncached APIReader. The
// values channel, the namespace list, and the managed-replica prune list are
// cacheable (the manager scopes them into its cache via SecretCacheByObject),
// so they must use the cached Client to keep apiserver load down. The two
// reads the scoped cache cannot serve — the dynamic-name source Get and the
// foreign-collision existence check — must stay on the uncached APIReader,
// otherwise the controller would miss the operator source and any non-managed
// Secret colliding with a replica name. Wiring the two clients as distinct
// interceptors over a shared store lets the test observe the routing directly.
func TestReconcile_ReadsRouteToCacheExceptSourceAndCollision(t *testing.T) {
	s := newScheme(t)
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(
		configSecret("wildcard-tls", "tenant-root"),
		tlsSecret("tenant-root", "wildcard-tls", map[string][]byte{"tls.crt": []byte("C")}),
		terminationNS("tenant-foo", "ingress"),
	).Build()

	var cachedGets, cachedLists, uncachedGets, uncachedLists []string
	cached := interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			cachedGets = append(cachedGets, key.String())
			return cl.Get(ctx, key, obj, opts...)
		},
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			cachedLists = append(cachedLists, fmt.Sprintf("%T", list))
			return cl.List(ctx, list, opts...)
		},
	})
	uncached := interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			uncachedGets = append(uncachedGets, key.String())
			return cl.Get(ctx, key, obj, opts...)
		},
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			uncachedLists = append(uncachedLists, fmt.Sprintf("%T", list))
			return cl.List(ctx, list, opts...)
		},
	})

	r := &Reconciler{Client: cached, Reader: uncached, Scheme: s, Recorder: record.NewFakeRecorder(16)}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{NamespacedName: configKey}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	const (
		valuesKey    = platformValuesNamespace + "/" + platformValuesName
		sourceKey    = "tenant-root/wildcard-tls"
		collisionKey = "tenant-foo/wildcard-tls"
	)

	// Cacheable reads must hit the cached Client, never the APIReader.
	if !sliceHas(cachedGets, valuesKey) {
		t.Errorf("values channel must be read from the cache, cachedGets=%v", cachedGets)
	}
	if sliceHas(uncachedGets, valuesKey) {
		t.Errorf("values channel must not bypass the cache, uncachedGets=%v", uncachedGets)
	}
	if !sliceHas(cachedLists, "*v1.NamespaceList") {
		t.Errorf("namespaces must be listed from the cache, cachedLists=%v", cachedLists)
	}
	if !sliceHas(cachedLists, "*v1.SecretList") {
		t.Errorf("managed replicas must be listed from the cache, cachedLists=%v", cachedLists)
	}
	if len(uncachedLists) != 0 {
		t.Errorf("no List must bypass the cache, uncachedLists=%v", uncachedLists)
	}

	// The two reads the scoped cache cannot serve must stay on the APIReader.
	if !sliceHas(uncachedGets, sourceKey) {
		t.Errorf("dynamic-name source must be read via the uncached APIReader, uncachedGets=%v", uncachedGets)
	}
	if sliceHas(cachedGets, sourceKey) {
		t.Errorf("source must not be read from the cache, cachedGets=%v", cachedGets)
	}
	if !sliceHas(uncachedGets, collisionKey) {
		t.Errorf("foreign-collision check must use the uncached APIReader, uncachedGets=%v", uncachedGets)
	}
	if sliceHas(cachedGets, collisionKey) {
		t.Errorf("foreign-collision check must not be read from the cache, cachedGets=%v", cachedGets)
	}
}

func sliceHas(s []string, v string) bool {
	for _, e := range s {
		if e == v {
			return true
		}
	}
	return false
}

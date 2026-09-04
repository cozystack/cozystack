/*
Copyright 2025 The Cozystack Authors.

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
	"testing"

	fluxmeta "github.com/fluxcd/pkg/apis/meta"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/marketplace/tapconst"
)

func tapScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := sourcev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := cozyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func tapRepo(finalizer bool) *sourcev1.OCIRepository {
	repo := &sourcev1.OCIRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tap-foo-bar",
			Namespace: "cozy-system",
			Labels:    map[string]string{tapconst.Label: "true"},
		},
		Spec: sourcev1.OCIRepositorySpec{URL: "oci://ghcr.io/foo/bar"},
	}
	if finalizer {
		repo.Finalizers = []string{tapconst.Finalizer}
	}
	return repo
}

func req() ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Name: "tap-foo-bar", Namespace: "cozy-system"}}
}

func TestParseClusterServiceHost(t *testing.T) {
	cases := []struct {
		host    string
		svc, ns string
		ok      bool
	}{
		{"flux.cozy-fluxcd.svc", "flux", "cozy-fluxcd", true},
		{"flux.cozy-fluxcd.svc.cluster.local", "flux", "cozy-fluxcd", true},
		{"flux.cozy-fluxcd.svc.", "flux", "cozy-fluxcd", true},
		{"source-controller.flux-system.svc", "source-controller", "flux-system", true},
		{"example.com", "", "", false},
		{"flux.cozy-fluxcd", "", "", false},
		{"flux.cozy-fluxcd.svc.example.com", "", "", false},
		{"10.96.0.1", "", "", false},
		{".cozy-fluxcd.svc", "", "", false},
	}
	for _, c := range cases {
		svc, ns, ok := parseClusterServiceHost(c.host)
		if ok != c.ok || svc != c.svc || ns != c.ns {
			t.Errorf("parseClusterServiceHost(%q) = (%q,%q,%v), want (%q,%q,%v)", c.host, svc, ns, ok, c.svc, c.ns, c.ok)
		}
	}
}

func TestRewriteURLHost(t *testing.T) {
	cases := []struct{ in, ip, want string }{
		{"http://flux.cozy-fluxcd.svc/gitrepository/a/b.tar.gz", "10.96.1.2", "http://10.96.1.2/gitrepository/a/b.tar.gz"},
		{"http://flux.cozy-fluxcd.svc:9090/x?rev=1", "10.96.1.2", "http://10.96.1.2:9090/x?rev=1"},
	}
	for _, c := range cases {
		got, err := rewriteURLHost(c.in, c.ip)
		if err != nil || got != c.want {
			t.Errorf("rewriteURLHost(%q,%q) = %q,%v; want %q", c.in, c.ip, got, err, c.want)
		}
	}
}

func TestResolveArtifactURL(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "flux", Namespace: "cozy-fluxcd"},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.96.7.7"},
	}
	headless := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "flux", Namespace: "headless-ns"},
		Spec:       corev1.ServiceSpec{ClusterIP: corev1.ClusterIPNone},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc, headless).Build()
	r := &TapMaterializerReconciler{Client: cl, Scheme: scheme}

	// ClusterIP service: host rewritten to the IP.
	if got := r.resolveArtifactURL(context.Background(), "http://flux.cozy-fluxcd.svc/a/b.tar.gz"); got != "http://10.96.7.7/a/b.tar.gz" {
		t.Errorf("expected rewrite to ClusterIP, got %q", got)
	}
	// Service missing: fall back to the original URL.
	orig := "http://flux.other-ns.svc/a/b.tar.gz"
	if got := r.resolveArtifactURL(context.Background(), orig); got != orig {
		t.Errorf("expected fallback for missing service, got %q", got)
	}
	// Headless (no ClusterIP): fall back.
	head := "http://flux.headless-ns.svc/a/b.tar.gz"
	if got := r.resolveArtifactURL(context.Background(), head); got != head {
		t.Errorf("expected fallback for headless service, got %q", got)
	}
	// Non-cluster host: untouched.
	ext := "http://example.com/a/b.tar.gz"
	if got := r.resolveArtifactURL(context.Background(), ext); got != ext {
		t.Errorf("expected external host untouched, got %q", got)
	}
}

func TestReconcileAddsFinalizer(t *testing.T) {
	scheme := tapScheme(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tapRepo(false)).Build()
	r := &TapMaterializerReconciler{Client: cl, Scheme: scheme}

	res, err := r.Reconcile(context.Background(), req())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Errorf("expected a requeue after adding the finalizer")
	}
	var got sourcev1.OCIRepository
	if err := cl.Get(context.Background(), req().NamespacedName, &got); err != nil {
		t.Fatal(err)
	}
	if !controllerutil.ContainsFinalizer(&got, tapconst.Finalizer) {
		t.Errorf("finalizer not added")
	}
}

func TestReconcileWaitsForArtifact(t *testing.T) {
	scheme := tapScheme(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tapRepo(true)).Build()
	r := &TapMaterializerReconciler{Client: cl, Scheme: scheme}

	res, err := r.Reconcile(context.Background(), req())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Errorf("expected a requeue-after while waiting for the artifact, got %+v", res)
	}
}

func TestReconcileIgnoresUnlabeled(t *testing.T) {
	scheme := tapScheme(t)
	repo := tapRepo(false)
	repo.Labels = nil // not a tap
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(repo).Build()
	r := &TapMaterializerReconciler{Client: cl, Scheme: scheme}

	res, err := r.Reconcile(context.Background(), req())
	if err != nil || res.RequeueAfter != 0 {
		t.Fatalf("unlabeled source must be ignored, got res=%+v err=%v", res, err)
	}
	var got sourcev1.OCIRepository
	_ = cl.Get(context.Background(), req().NamespacedName, &got)
	if controllerutil.ContainsFinalizer(&got, tapconst.Finalizer) {
		t.Errorf("must not add a finalizer to a non-tap source")
	}
}

func TestDeleteMaterialized(t *testing.T) {
	scheme := tapScheme(t)
	mine := &cozyv1alpha1.PackageSource{ObjectMeta: metav1.ObjectMeta{
		Name:        "foo.bar",
		Labels:      map[string]string{tapconst.Label: "true"},
		Annotations: map[string]string{tapconst.SourceAnnotation: "tap-foo-bar"},
	}}
	other := &cozyv1alpha1.PackageSource{ObjectMeta: metav1.ObjectMeta{
		Name:        "baz.qux",
		Labels:      map[string]string{tapconst.Label: "true"},
		Annotations: map[string]string{tapconst.SourceAnnotation: "tap-baz-qux"},
	}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mine, other).Build()
	r := &TapMaterializerReconciler{Client: cl, Scheme: scheme}

	if err := r.deleteMaterialized(context.Background(), "tap-foo-bar"); err != nil {
		t.Fatalf("deleteMaterialized: %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "foo.bar"}, &cozyv1alpha1.PackageSource{}); !apierrors.IsNotFound(err) {
		t.Errorf("expected foo.bar deleted, got %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "baz.qux"}, &cozyv1alpha1.PackageSource{}); err != nil {
		t.Errorf("expected baz.qux kept, got %v", err)
	}
}

func TestPruneMaterializedKeepsCurrentSet(t *testing.T) {
	scheme := tapScheme(t)
	src := "tap-foo-bar"
	keepPS := &cozyv1alpha1.PackageSource{ObjectMeta: metav1.ObjectMeta{
		Name:        "foo.bar",
		Labels:      map[string]string{tapconst.Label: "true"},
		Annotations: map[string]string{tapconst.SourceAnnotation: src},
	}}
	stalePS := &cozyv1alpha1.PackageSource{ObjectMeta: metav1.ObjectMeta{
		Name:        "foo.removed",
		Labels:      map[string]string{tapconst.Label: "true"},
		Annotations: map[string]string{tapconst.SourceAnnotation: src},
	}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(keepPS, stalePS).Build()
	r := &TapMaterializerReconciler{Client: cl, Scheme: scheme}

	// The new revision still contains foo.bar but not foo.removed.
	if err := r.pruneMaterialized(context.Background(), src, map[string]bool{"foo.bar": true}); err != nil {
		t.Fatalf("pruneMaterialized: %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "foo.bar"}, &cozyv1alpha1.PackageSource{}); err != nil {
		t.Errorf("expected kept PS to survive, got %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "foo.removed"}, &cozyv1alpha1.PackageSource{}); !apierrors.IsNotFound(err) {
		t.Errorf("expected stale PS pruned, got %v", err)
	}
}

// TestReconcileBlocksNameCollision asserts that when a tapped artifact declares
// a PackageSource name already owned by a core component, the materializer
// refuses: it does not overwrite the foreign object, records the reason on the
// source, and does not stamp the revision (so a corrected artifact retries).
func TestReconcileBlocksNameCollision(t *testing.T) {
	scheme := tapScheme(t)
	data, digest := tarGz(t, map[string]string{
		// samplePS declares PackageSource "example.hello".
		"packages/core/platform/sources/hello.yaml": samplePS,
	})
	repo := tapRepo(true)
	// A core PackageSource of the same name, without the tap marker.
	foreign := &cozyv1alpha1.PackageSource{ObjectMeta: metav1.ObjectMeta{Name: "example.hello"}}
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(repo, foreign).
		WithStatusSubresource(&sourcev1.OCIRepository{}).
		Build()

	// Report a ready artifact so the reconciler proceeds to materialize.
	var live sourcev1.OCIRepository
	if err := cl.Get(context.Background(), req().NamespacedName, &live); err != nil {
		t.Fatal(err)
	}
	live.Status.Artifact = &fluxmeta.Artifact{URL: "http://example.com/a.tar.gz", Digest: digest, Revision: "rev1"}
	if err := cl.Status().Update(context.Background(), &live); err != nil {
		t.Fatal(err)
	}

	rec := record.NewFakeRecorder(10)
	r := &TapMaterializerReconciler{
		Client:   cl,
		Scheme:   scheme,
		Recorder: rec,
		Fetch:    func(context.Context, string) ([]byte, error) { return data, nil },
	}

	res, err := r.Reconcile(context.Background(), req())
	if err != nil {
		t.Fatalf("reconcile returned an error instead of surfacing the collision: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("a collision must not hot-loop; got requeue %v", res.RequeueAfter)
	}

	var got sourcev1.OCIRepository
	if err := cl.Get(context.Background(), req().NamespacedName, &got); err != nil {
		t.Fatal(err)
	}
	if got.Annotations[tapconst.MaterializeErrorAnnotation] == "" {
		t.Error("expected a materialize-error annotation recording the collision")
	}
	if got.Annotations[tapconst.MaterializedRevisionAnnotation] == "rev1" {
		t.Error("a blocked materialization must not stamp the revision (so a corrected artifact retries)")
	}

	var ps cozyv1alpha1.PackageSource
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "example.hello"}, &ps); err != nil {
		t.Fatal(err)
	}
	if ps.GetLabels()[tapconst.Label] == "true" {
		t.Error("the collision check must not overwrite the foreign PackageSource")
	}
}

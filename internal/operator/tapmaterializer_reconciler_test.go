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

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
			Name:      "community-foo-bar",
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
	return ctrl.Request{NamespacedName: types.NamespacedName{Name: "community-foo-bar", Namespace: "cozy-system"}}
}

func TestReconcileAddsFinalizer(t *testing.T) {
	scheme := tapScheme(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tapRepo(false)).Build()
	r := &TapMaterializerReconciler{Client: cl, Scheme: scheme}

	res, err := r.Reconcile(context.Background(), req())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !res.Requeue {
		t.Errorf("expected requeue after adding finalizer")
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
	if err != nil || res.Requeue || res.RequeueAfter != 0 {
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
		Name:        "community.foo.bar",
		Labels:      map[string]string{tapconst.Label: "true"},
		Annotations: map[string]string{tapconst.SourceAnnotation: "community-foo-bar"},
	}}
	other := &cozyv1alpha1.PackageSource{ObjectMeta: metav1.ObjectMeta{
		Name:        "community.baz.qux",
		Labels:      map[string]string{tapconst.Label: "true"},
		Annotations: map[string]string{tapconst.SourceAnnotation: "community-baz-qux"},
	}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mine, other).Build()
	r := &TapMaterializerReconciler{Client: cl, Scheme: scheme}

	if err := r.deleteMaterialized(context.Background(), "community-foo-bar"); err != nil {
		t.Fatalf("deleteMaterialized: %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "community.foo.bar"}, &cozyv1alpha1.PackageSource{}); !apierrors.IsNotFound(err) {
		t.Errorf("expected community.foo.bar deleted, got %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "community.baz.qux"}, &cozyv1alpha1.PackageSource{}); err != nil {
		t.Errorf("expected community.baz.qux kept, got %v", err)
	}
}

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

package nodegroupclass

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

func newReconciler(t *testing.T, objs ...client.Object) (*Reconciler, client.Client) {
	t.Helper()
	return newReconcilerWith(t, interceptor.Funcs{}, objs...)
}

func newReconcilerWith(t *testing.T, funcs interceptor.Funcs, objs ...client.Object) (*Reconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := cozyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("cozystack scheme: %v", err)
	}
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatalf("helmv2 scheme: %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithInterceptorFuncs(funcs).Build()
	return &Reconciler{Client: c}, c
}

func class(name string, finalizers ...string) *cozyv1alpha1.NodeGroupClass {
	return &cozyv1alpha1.NodeGroupClass{
		ObjectMeta: metav1.ObjectMeta{Name: name, Finalizers: finalizers},
		Spec:       cozyv1alpha1.NodeGroupClassSpec{Bootstrap: cozyv1alpha1.NodeGroupBootstrapKairos},
	}
}

func terminating(c *cozyv1alpha1.NodeGroupClass) *cozyv1alpha1.NodeGroupClass {
	now := metav1.Now()
	c.DeletionTimestamp = &now
	return c
}

func pool(namespace, name, className string) *helmv2.HelmRelease {
	values := map[string]any{"cluster": "k"}
	if className != "" {
		values["nodeGroupClassName"] = className
	}
	raw, _ := json.Marshal(values)
	return &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{appsv1alpha1.ApplicationKindLabel: poolKind},
		},
		Spec: helmv2.HelmReleaseSpec{Values: &apiextensionsv1.JSON{Raw: raw}},
	}
}

func reconcileClass(t *testing.T, r *Reconciler, name string) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: name}}); err != nil {
		t.Fatalf("reconcile %s: %v", name, err)
	}
}

func finalizers(t *testing.T, c client.Client, name string) []string {
	t.Helper()
	got := &cozyv1alpha1.NodeGroupClass{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: name}, got); err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	return got.Finalizers
}

func TestAddsTheFinalizerToAHosterClass(t *testing.T) {
	r, c := newReconciler(t, class("acme-kairos"))
	reconcileClass(t, r, "acme-kairos")
	if got := finalizers(t, c, "acme-kairos"); len(got) != 1 || got[0] != Finalizer {
		t.Fatalf("finalizers = %v, want [%s]", got, Finalizer)
	}
}

func TestLeavesTheBuiltInClassesWithoutAFinalizer(t *testing.T) {
	for _, name := range []string{"talos", "kairos"} {
		r, c := newReconciler(t, class(name), pool("tenant-a", "kubernetes-nodes-k-md0", name))
		reconcileClass(t, r, name)
		if got := finalizers(t, c, name); len(got) != 0 {
			t.Errorf("%s: finalizers = %v, want none", name, got)
		}
	}
}

func TestHoldsAReferencedClassInDeletion(t *testing.T) {
	r, c := newReconciler(t,
		terminating(class("acme-kairos", Finalizer)),
		pool("tenant-a", "kubernetes-nodes-k-md0", "acme-kairos"),
	)
	reconcileClass(t, r, "acme-kairos")
	if got := finalizers(t, c, "acme-kairos"); len(got) != 1 {
		t.Fatalf("finalizers = %v, want the class held", got)
	}
}

func TestReleasesAnUnreferencedClassInDeletion(t *testing.T) {
	r, c := newReconciler(t,
		terminating(class("acme-kairos", Finalizer)),
		pool("tenant-a", "kubernetes-nodes-k-md0", "acme-other"),
		pool("tenant-a", "kubernetes-nodes-k-md1", ""),
	)
	reconcileClass(t, r, "acme-kairos")
	err := c.Get(context.Background(), types.NamespacedName{Name: "acme-kairos"}, &cozyv1alpha1.NodeGroupClass{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("get after reconcile: %v, want the class gone", err)
	}
}

// A release of another kind that happens to carry the same value is not a pool.
func TestIgnoresReleasesOfOtherKinds(t *testing.T) {
	other := pool("tenant-a", "kubernetes-nodes-k-md0", "acme-kairos")
	other.Labels[appsv1alpha1.ApplicationKindLabel] = "Kubernetes"
	r, c := newReconciler(t, terminating(class("acme-kairos", Finalizer)), other)
	reconcileClass(t, r, "acme-kairos")
	err := c.Get(context.Background(), types.NamespacedName{Name: "acme-kairos"}, &cozyv1alpha1.NodeGroupClass{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("get after reconcile: %v, want the class gone", err)
	}
}

// The last pool leaving a terminating class must wake that class, not only
// the one the pool moved to.
func TestAPoolMovingOffATerminatingClassReleasesIt(t *testing.T) {
	before := pool("tenant-a", "kubernetes-nodes-k-md0", "acme-a")
	after := pool("tenant-a", "kubernetes-nodes-k-md0", "acme-b")

	q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	defer q.ShutDown()
	poolEvents.Update(context.Background(), event.TypedUpdateEvent[client.Object]{ObjectOld: before, ObjectNew: after}, q)
	queued := map[string]bool{}
	for q.Len() > 0 {
		req, _ := q.Get()
		queued[req.Name] = true
		q.Done(req)
	}
	if !queued["acme-a"] || !queued["acme-b"] {
		t.Fatalf("queued %v, want both acme-a and acme-b", queued)
	}

	r, c := newReconciler(t, terminating(class("acme-a", Finalizer)), class("acme-b", Finalizer), after)
	reconcileClass(t, r, "acme-a")
	err := c.Get(context.Background(), types.NamespacedName{Name: "acme-a"}, &cozyv1alpha1.NodeGroupClass{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("get acme-a after reconcile: %v, want the class gone", err)
	}
}

func TestADeletedPoolWakesItsClass(t *testing.T) {
	q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	defer q.ShutDown()
	poolEvents.Delete(context.Background(), event.TypedDeleteEvent[client.Object]{Object: pool("tenant-a", "kubernetes-nodes-k-md0", "acme-a")}, q)
	if q.Len() != 1 {
		t.Fatalf("queue length %d, want 1", q.Len())
	}
	if req, _ := q.Get(); req.Name != "acme-a" {
		t.Fatalf("queued %q, want acme-a", req.Name)
	}
}

// Without the pool list the controller cannot tell the class is unused, so it
// keeps the finalizer and retries.
func TestKeepsTheClassWhenThePoolsCannotBeListed(t *testing.T) {
	listErr := errors.New("list failed")
	r, c := newReconcilerWith(t, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*helmv2.HelmReleaseList); ok {
				return listErr
			}
			return c.List(ctx, list, opts...)
		},
	}, terminating(class("acme-kairos", Finalizer)))
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "acme-kairos"}})
	if !errors.Is(err, listErr) {
		t.Fatalf("reconcile err = %v, want the list error", err)
	}
	if got := finalizers(t, c, "acme-kairos"); len(got) != 1 {
		t.Fatalf("finalizers = %v, want the class held", got)
	}
}

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package securitygroupcontroller

import (
	"context"
	"encoding/json"
	"testing"

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

	sdnv1alpha1 "github.com/cozystack/cozystack/pkg/apis/sdn/v1alpha1"
)

const ns = "tenant-root"

func appRef(kind, name string) sdnv1alpha1.ApplicationReference {
	return sdnv1alpha1.ApplicationReference{APIGroup: defaultAppGroup, Kind: kind, Name: name}
}

func newReconciler(t *testing.T, objs ...client.Object) (*Reconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("add cilium mirror: %v", err)
	}
	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &Reconciler{Client: fc}, fc
}

// sg builds a marked CiliumNetworkPolicy with the given attachments annotation.
func sg(name string, finalizer bool, attachments ...sdnv1alpha1.ApplicationReference) *CiliumNetworkPolicy {
	ann := map[string]string{}
	if len(attachments) > 0 {
		b, _ := json.Marshal(attachments)
		ann[attachmentsAnnotation] = string(b)
	}
	cnp := &CiliumNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Labels:      map[string]string{sgLabelKey: sgLabelValue},
			Annotations: ann,
		},
	}
	if finalizer {
		cnp.Finalizers = []string{membershipFinalizer}
	}
	return cnp
}

// pod builds a managed pod carrying the lineage labels of the given application.
func pod(name, namespace string, ref sdnv1alpha1.ApplicationReference, extra map[string]string) *corev1.Pod {
	l := map[string]string{managedByLabel: "true"}
	for k, v := range appLabels(ref) {
		l[k] = v
	}
	for k, v := range extra {
		l[k] = v
	}
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: l}}
}

func doReconcile(t *testing.T, r *Reconciler, name string) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}); err != nil {
		t.Fatalf("Reconcile(%s): %v", name, err)
	}
}

func hasMembership(t *testing.T, c client.Client, podName, namespace, key string) bool {
	t.Helper()
	p := &corev1.Pod{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: podName}, p); err != nil {
		t.Fatalf("get pod %s/%s: %v", namespace, podName, err)
	}
	_, ok := p.Labels[key]
	return ok
}

func TestStampsMembershipOnMatchingPods(t *testing.T) {
	db := appRef("Postgres", "db")
	r, c := newReconciler(t,
		sg("sg-db", false, db),
		pod("db-0", ns, db, nil),
		pod("web-0", ns, appRef("Kubernetes", "web"), nil),
	)
	doReconcile(t, r, "sg-db")

	key := membershipLabelKey("sg-db")
	if !hasMembership(t, c, "db-0", ns, key) {
		t.Fatalf("matching pod db-0 was not stamped with %q", key)
	}
	if hasMembership(t, c, "web-0", ns, key) {
		t.Fatalf("non-matching pod web-0 was stamped with %q", key)
	}
}

func TestRemovesMembershipOnDetach(t *testing.T) {
	key := membershipLabelKey("sg-db")
	// The pod already carries the membership label, but the group now has no
	// attachments — the controller must remove it.
	r, c := newReconciler(t,
		sg("sg-db", true),
		pod("db-0", ns, appRef("Postgres", "db"), map[string]string{key: ""}),
	)
	doReconcile(t, r, "sg-db")

	if hasMembership(t, c, "db-0", ns, key) {
		t.Fatalf("membership label was not removed after detach")
	}
}

func TestTwoSecurityGroupsCoexistOnOnePod(t *testing.T) {
	db := appRef("Postgres", "db")
	r, c := newReconciler(t,
		sg("sg-a", true, db),
		sg("sg-b", true, db),
		pod("db-0", ns, db, nil),
	)
	doReconcile(t, r, "sg-a")
	doReconcile(t, r, "sg-b")

	keyA, keyB := membershipLabelKey("sg-a"), membershipLabelKey("sg-b")
	if !hasMembership(t, c, "db-0", ns, keyA) || !hasMembership(t, c, "db-0", ns, keyB) {
		t.Fatalf("both group labels should be present on the shared pod")
	}

	// Deleting sg-a must remove only its own label, leaving sg-b's intact.
	cnpA := &CiliumNetworkPolicy{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "sg-a"}, cnpA); err != nil {
		t.Fatalf("get sg-a: %v", err)
	}
	if err := c.Delete(context.Background(), cnpA); err != nil {
		t.Fatalf("delete sg-a: %v", err)
	}
	doReconcile(t, r, "sg-a")

	if hasMembership(t, c, "db-0", ns, keyA) {
		t.Fatalf("sg-a label should be gone after deletion")
	}
	if !hasMembership(t, c, "db-0", ns, keyB) {
		t.Fatalf("sg-b label must survive sg-a deletion")
	}
}

func TestFinalizerCleanupOnDelete(t *testing.T) {
	db := appRef("Postgres", "db")
	r, c := newReconciler(t,
		sg("sg-db", true, db),
		pod("db-0", ns, db, nil),
	)
	doReconcile(t, r, "sg-db")
	key := membershipLabelKey("sg-db")
	if !hasMembership(t, c, "db-0", ns, key) {
		t.Fatalf("precondition: pod should be stamped")
	}

	cnp := &CiliumNetworkPolicy{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "sg-db"}, cnp); err != nil {
		t.Fatalf("get sg-db: %v", err)
	}
	if err := c.Delete(context.Background(), cnp); err != nil {
		t.Fatalf("delete sg-db: %v", err)
	}
	// The finalizer keeps the policy around until the controller cleans up.
	doReconcile(t, r, "sg-db")

	if hasMembership(t, c, "db-0", ns, key) {
		t.Fatalf("membership label not cleaned up on delete")
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "sg-db"}, cnp); !apierrors.IsNotFound(err) {
		t.Fatalf("policy not garbage-collected after finalizer removal: err=%v", err)
	}
}

func TestOtherNamespaceUntouched(t *testing.T) {
	db := appRef("Postgres", "db")
	r, c := newReconciler(t,
		sg("sg-db", true, db),
		pod("db-0", ns, db, nil),
		pod("db-0", "other-tenant", db, nil),
	)
	doReconcile(t, r, "sg-db")

	key := membershipLabelKey("sg-db")
	if !hasMembership(t, c, "db-0", ns, key) {
		t.Fatalf("in-namespace pod should be stamped")
	}
	if hasMembership(t, c, "db-0", "other-tenant", key) {
		t.Fatalf("pod in another namespace must never be stamped")
	}
}

func TestEnsuresFinalizer(t *testing.T) {
	r, c := newReconciler(t, sg("sg-db", false, appRef("Postgres", "db")))
	doReconcile(t, r, "sg-db")

	cnp := &CiliumNetworkPolicy{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "sg-db"}, cnp); err != nil {
		t.Fatalf("get sg-db: %v", err)
	}
	if len(cnp.Finalizers) != 1 || cnp.Finalizers[0] != membershipFinalizer {
		t.Fatalf("finalizer not ensured: %+v", cnp.Finalizers)
	}
}

func TestReconcileRequeuesSoARacedPodStillConverges(t *testing.T) {
	// Safety net: the desired set is computed from the cached lister, so if a
	// member pod is ever not yet visible to the cache when a reconcile runs, the
	// reconcile must still request a resync rather than give up — otherwise that
	// pod could stay unlabelled until an unrelated event. Here the first pod List
	// is forced to return nothing (the pod "not yet cached"); the reconcile must
	// not label it but must requeue, and a follow-up reconcile (pod now visible)
	// must converge.
	db := appRef("Postgres", "db")
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("add cilium mirror: %v", err)
	}
	listCalls := 0
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(sg("sg-db", true, db), pod("db-0", ns, db, nil)).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if err := cl.List(ctx, list, opts...); err != nil {
					return err
				}
				// Hide the pod from the first pod List only (the desired-set list
				// of the first reconcile), simulating a not-yet-synced cache.
				if pl, ok := list.(*corev1.PodList); ok {
					listCalls++
					if listCalls == 1 {
						pl.Items = nil
					}
				}
				return nil
			},
		}).Build()
	r := &Reconciler{Client: c}
	key := membershipLabelKey("sg-db")

	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "sg-db"}})
	if err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if hasMembership(t, c, "db-0", ns, key) {
		t.Fatalf("precondition: pod should not be labelled while invisible to the cache")
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("reconcile must requeue a resync so a raced/missed pod still converges; got RequeueAfter=%v", res.RequeueAfter)
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "sg-db"}}); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if !hasMembership(t, c, "db-0", ns, key) {
		t.Fatalf("pod should be labelled once visible to the cache")
	}
}

func TestMapPodToSGs(t *testing.T) {
	db := appRef("Postgres", "db")
	r, _ := newReconciler(t,
		sg("sg-db", true, db),
		sg("sg-other", true, appRef("Kubernetes", "web")),
	)

	// A pod of the attached app enqueues exactly its SecurityGroup.
	reqs := r.mapPodToSGs(context.Background(), pod("db-0", ns, db, nil))
	if len(reqs) != 1 || reqs[0].Name != "sg-db" {
		t.Fatalf("mapPodToSGs(db-0) = %+v, want [sg-db]", reqs)
	}

	// A pod with no application identity enqueues nothing.
	bare := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "bare", Namespace: ns}}
	if reqs := r.mapPodToSGs(context.Background(), bare); len(reqs) != 0 {
		t.Fatalf("mapPodToSGs(bare) = %+v, want none", reqs)
	}
}

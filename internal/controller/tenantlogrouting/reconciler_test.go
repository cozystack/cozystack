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

package tenantlogrouting

import (
	"context"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	fluxmeta "github.com/fluxcd/pkg/apis/meta"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func namespace(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func monitoredBy(target string) map[string]string {
	return map[string]string{MonitoringLabel: target}
}

func release(annotations map[string]string) *helmv2.HelmRelease {
	return &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{
		Name:        Release.Name,
		Namespace:   Release.Namespace,
		Annotations: annotations,
	}}
}

func newReconciler(t *testing.T, objs ...client.Object) *Reconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return &Reconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()}
}

func reconcileOnce(t *testing.T, r *Reconciler) *helmv2.HelmRelease {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), sweep); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	hr := &helmv2.HelmRelease{}
	if err := r.Get(context.Background(), Release, hr); err != nil {
		t.Fatalf("get release: %v", err)
	}
	return hr
}

func assertForced(t *testing.T, hr *helmv2.HelmRelease, digest string) {
	t.Helper()
	for _, key := range []string{DigestAnnotation, fluxmeta.ReconcileRequestAnnotation, fluxmeta.ForceRequestAnnotation} {
		if got := hr.Annotations[key]; got != digest {
			t.Errorf("annotation %s = %q, want %q", key, got, digest)
		}
	}
}

func TestReconcile_ForcesUpgradeOnFirstSight(t *testing.T) {
	nsA := namespace("tenant-a", monitoredBy("tenant-a"))
	r := newReconciler(t, release(map[string]string{"cozyhr.cozystack.io/values-files": "values.yaml"}), nsA)

	hr := reconcileOnce(t, r)

	assertForced(t, hr, Digest([]corev1.Namespace{*nsA}))
	if hr.Annotations["cozyhr.cozystack.io/values-files"] != "values.yaml" {
		t.Errorf("unrelated annotation was dropped: %v", hr.Annotations)
	}
}

func TestReconcile_LeavesReleaseAloneWhileLabelsAreUnchanged(t *testing.T) {
	nsA := namespace("tenant-a", monitoredBy("tenant-a"))
	digest := Digest([]corev1.Namespace{*nsA})
	// A manual `flux reconcile` has since replaced the request tokens; the
	// recorded digest still matches, so no further upgrade may be forced.
	r := newReconciler(t, release(map[string]string{
		DigestAnnotation:                    digest,
		fluxmeta.ReconcileRequestAnnotation: "manual",
		fluxmeta.ForceRequestAnnotation:     "manual",
	}), nsA)
	before := reconcileOnce(t, r).ResourceVersion

	hr := reconcileOnce(t, r)

	if hr.ResourceVersion != before {
		t.Errorf("release was patched although no label changed")
	}
	if hr.Annotations[fluxmeta.ForceRequestAnnotation] != "manual" {
		t.Errorf("forceAt = %q, want the manual token left in place", hr.Annotations[fluxmeta.ForceRequestAnnotation])
	}
}

func TestReconcile_ForcesUpgradeWhenATenantTurnsMonitoringOn(t *testing.T) {
	nsA := namespace("tenant-a", monitoredBy("tenant-root"))
	r := newReconciler(t, release(nil), nsA)
	stale := reconcileOnce(t, r).Annotations[DigestAnnotation]

	nsA.Labels[MonitoringLabel] = "tenant-a"
	if err := r.Update(context.Background(), nsA); err != nil {
		t.Fatal(err)
	}
	hr := reconcileOnce(t, r)

	if hr.Annotations[DigestAnnotation] == stale {
		t.Fatalf("digest did not move after tenant-a turned monitoring on")
	}
	assertForced(t, hr, Digest([]corev1.Namespace{*nsA}))
}

func TestReconcile_ForcesUpgradeWhenARoutedNamespaceIsDeleted(t *testing.T) {
	nsA := namespace("tenant-a", monitoredBy("tenant-a"))
	nsB := namespace("tenant-b", monitoredBy("tenant-b"))
	r := newReconciler(t, release(nil), nsA, nsB)
	reconcileOnce(t, r)

	if err := r.Delete(context.Background(), nsB); err != nil {
		t.Fatal(err)
	}
	hr := reconcileOnce(t, r)

	assertForced(t, hr, Digest([]corev1.Namespace{*nsA}))
}

func TestReconcile_NoReleaseIsNotAnError(t *testing.T) {
	r := newReconciler(t, namespace("tenant-a", monitoredBy("tenant-a")))
	if _, err := r.Reconcile(context.Background(), sweep); err != nil {
		t.Fatalf("Reconcile without the release: %v", err)
	}
}

func TestDigest_IgnoresNamespacesWithoutTheLabel(t *testing.T) {
	labelled := []corev1.Namespace{*namespace("tenant-a", monitoredBy("tenant-a"))}
	withOthers := append([]corev1.Namespace{*namespace("cozy-system", nil)}, labelled...)
	if Digest(labelled) != Digest(withOthers) {
		t.Errorf("an unlabelled namespace moved the digest")
	}
}

func TestDigest_DependsOnValueNotOrder(t *testing.T) {
	a := *namespace("tenant-a", monitoredBy("tenant-a"))
	b := *namespace("tenant-b", monitoredBy("tenant-root"))
	if Digest([]corev1.Namespace{a, b}) != Digest([]corev1.Namespace{b, a}) {
		t.Errorf("digest depends on list order")
	}
	emptied := *namespace("tenant-b", monitoredBy(""))
	if Digest([]corev1.Namespace{a, b}) == Digest([]corev1.Namespace{a, emptied}) {
		t.Errorf("emptying a label did not move the digest")
	}
}

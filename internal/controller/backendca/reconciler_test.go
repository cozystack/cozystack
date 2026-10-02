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

package backendca

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const ns = "cozy-kubevirt"

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := gatewayv1.Install(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func policy(annotation string) *gatewayv1.BackendTLSPolicy {
	p := &gatewayv1.BackendTLSPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "exportproxy", Namespace: ns, UID: "policy-uid"},
		Spec: gatewayv1.BackendTLSPolicySpec{
			TargetRefs: []gatewayv1.LocalPolicyTargetReferenceWithSectionName{{
				LocalPolicyTargetReference: gatewayv1.LocalPolicyTargetReference{Kind: "Service", Name: "vm-exportproxy"},
			}},
			Validation: gatewayv1.BackendTLSPolicyValidation{
				CACertificateRefs: []gatewayv1.LocalObjectReference{{Kind: "ConfigMap", Name: "exportproxy-ca"}},
				Hostname:          "virt-exportproxy.cozy-kubevirt.svc",
			},
		},
	}
	if annotation != "" {
		p.Annotations = map[string]string{SourceAnnotation: annotation}
	}
	return p
}

func source(data string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "kubevirt-ca", Namespace: ns},
		Data:       map[string]string{"ca-bundle": data},
	}
}

func reconcile(t *testing.T, c client.Client) ctrl.Result {
	t.Helper()
	r := &Reconciler{Client: c, Reader: c, Scheme: c.Scheme()}
	res, err := r.Reconcile(context.TODO(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "exportproxy", Namespace: ns}})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	return res
}

func target(t *testing.T, c client.Client) *corev1.ConfigMap {
	t.Helper()
	cm := &corev1.ConfigMap{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "exportproxy-ca", Namespace: ns}, cm); err != nil {
		t.Fatalf("get target ConfigMap: %v", err)
	}
	return cm
}

// TestCopiesTheSourceKeyIntoCACrt pins the whole job: the CA a backend
// publishes under its own key lands under ca.crt, the one key Gateway
// API reads a BackendTLSPolicy CA from, in the ConfigMap the policy
// names, owned by the policy so it goes with it.
func TestCopiesTheSourceKeyIntoCACrt(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(policy("kubevirt-ca/ca-bundle"), source("CA-1")).Build()
	res := reconcile(t, c)

	cm := target(t, c)
	if got := cm.Data["ca.crt"]; got != "CA-1" {
		t.Errorf("ca.crt = %q, want CA-1", got)
	}
	if len(cm.OwnerReferences) != 1 || cm.OwnerReferences[0].UID != "policy-uid" || cm.OwnerReferences[0].Controller == nil || !*cm.OwnerReferences[0].Controller {
		t.Errorf("target is not controlled by the policy: %+v", cm.OwnerReferences)
	}
	if res.RequeueAfter <= 0 {
		t.Errorf("no periodic requeue, so a rotated CA never reaches the policy: %+v", res)
	}
}

// TestFollowsARotatedCA pins that the copy tracks the source.
func TestFollowsARotatedCA(t *testing.T) {
	src := source("CA-1")
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(policy("kubevirt-ca/ca-bundle"), src).Build()
	reconcile(t, c)

	src.Data["ca-bundle"] = "CA-1\nCA-2"
	if err := c.Update(context.TODO(), src); err != nil {
		t.Fatal(err)
	}
	reconcile(t, c)
	if got := target(t, c).Data["ca.crt"]; got != "CA-1\nCA-2" {
		t.Errorf("ca.crt = %q after rotation, want the rotated bundle", got)
	}
}

// TestLeavesAForeignConfigMapAlone pins the takeover guard: a ConfigMap
// of that name the policy does not own is neither overwritten nor
// adopted, since it may carry someone else's trust.
func TestLeavesAForeignConfigMapAlone(t *testing.T) {
	foreign := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "exportproxy-ca", Namespace: ns},
		Data:       map[string]string{"ca.crt": "SOMEONE-ELSES"},
	}
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(policy("kubevirt-ca/ca-bundle"), source("CA-1"), foreign).Build()
	r := &Reconciler{Client: c, Reader: c, Scheme: c.Scheme()}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "exportproxy", Namespace: ns}}); err == nil {
		t.Error("expected the reconcile to refuse a ConfigMap it does not own, got nil")
	}
	if got := target(t, c).Data["ca.crt"]; got != "SOMEONE-ELSES" {
		t.Errorf("foreign ConfigMap overwritten: ca.crt = %q", got)
	}
}

// TestIgnoresAPolicyWithoutTheAnnotation pins that only a policy asking
// for a copy gets one: a policy pointing at a ConfigMap its owner
// manages must not have it created underneath.
func TestIgnoresAPolicyWithoutTheAnnotation(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(policy(""), source("CA-1")).Build()
	reconcile(t, c)
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "exportproxy-ca", Namespace: ns}, &corev1.ConfigMap{}); err == nil {
		t.Error("created a ConfigMap for a policy that did not ask for one")
	}
}

// TestWaitsForTheSource pins the startup order: the backend's operator
// may publish its CA after the chart applies the policy, and an empty
// ca.crt would fail validation for every request meanwhile.
func TestWaitsForTheSource(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(policy("kubevirt-ca/ca-bundle")).Build()
	if res := reconcile(t, c); res.RequeueAfter <= 0 {
		t.Errorf("no requeue while the source is missing: %+v", res)
	}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "exportproxy-ca", Namespace: ns}, &corev1.ConfigMap{}); err == nil {
		t.Error("created a ConfigMap before the source existed")
	}
}

// TestWaitsForTheSourceKey pins the same wait when the source ConfigMap
// exists but its key is still empty or absent: an empty ca.crt would
// fail validation just the same.
func TestWaitsForTheSourceKey(t *testing.T) {
	for name, src := range map[string]*corev1.ConfigMap{
		"empty key":   source(""),
		"missing key": {ObjectMeta: metav1.ObjectMeta{Name: "kubevirt-ca", Namespace: ns}, Data: map[string]string{"other": "CA-1"}},
	} {
		t.Run(name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(policy("kubevirt-ca/ca-bundle"), src).Build()
			if res := reconcile(t, c); res.RequeueAfter <= 0 {
				t.Errorf("no requeue while the source key is empty: %+v", res)
			}
			if err := c.Get(context.TODO(), types.NamespacedName{Name: "exportproxy-ca", Namespace: ns}, &corev1.ConfigMap{}); err == nil {
				t.Error("created a ConfigMap with no CA to put in it")
			}
		})
	}
}

// TestIgnoresWhatItCannotActOn pins the two inputs the controller
// cannot act on: an annotation that does not read <configmap>/<key>,
// and a policy that names no ConfigMap to write. Neither creates
// anything, and neither fails the reconcile, since retrying cannot fix
// either.
func TestIgnoresWhatItCannotActOn(t *testing.T) {
	noConfigMap := policy("kubevirt-ca/ca-bundle")
	noConfigMap.Spec.Validation.CACertificateRefs = []gatewayv1.LocalObjectReference{{Kind: "Secret", Name: "exportproxy-ca"}}
	for name, p := range map[string]*gatewayv1.BackendTLSPolicy{
		"no slash":     policy("kubevirt-ca"),
		"empty name":   policy("/ca-bundle"),
		"empty key":    policy("kubevirt-ca/"),
		"no ConfigMap": noConfigMap,
	} {
		t.Run(name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(p, source("CA-1")).Build()
			if res := reconcile(t, c); res.RequeueAfter != 0 {
				t.Errorf("requeued an input no retry can fix: %+v", res)
			}
			if err := c.Get(context.TODO(), types.NamespacedName{Name: "exportproxy-ca", Namespace: ns}, &corev1.ConfigMap{}); err == nil {
				t.Error("created a ConfigMap from an input it should have ignored")
			}
		})
	}
}

// SPDX-License-Identifier: Apache-2.0

package securitygroup

import (
	"context"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/registry/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	sdnv1alpha1 "github.com/cozystack/cozystack/pkg/apis/sdn/v1alpha1"
)

func newSecurityGroupWriteOptionsREST(t *testing.T, funcs interceptor.Funcs, policies ...*CiliumNetworkPolicy) *REST {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("register Cilium mirror scheme: %v", err)
	}
	objects := make([]client.Object, 0, len(policies))
	for _, policy := range policies {
		objects = append(objects, policy)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithInterceptorFuncs(funcs).
		Build()
	return &REST{c: c, w: c, gvr: schema.GroupVersionResource{
		Group: sdnv1alpha1.GroupName, Version: "v1alpha1", Resource: sdnv1alpha1.SecurityGroupPluralName,
	}}
}

func captureSecurityGroupCreateOptions(opts ...client.CreateOption) *metav1.CreateOptions {
	converted := (&client.CreateOptions{}).ApplyOptions(opts)
	return converted.AsCreateOptions().DeepCopy()
}

func captureSecurityGroupUpdateOptions(opts ...client.UpdateOption) *metav1.UpdateOptions {
	converted := (&client.UpdateOptions{}).ApplyOptions(opts)
	return converted.AsUpdateOptions().DeepCopy()
}

func captureSecurityGroupDeleteOptions(opts ...client.DeleteOption) *metav1.DeleteOptions {
	converted := &client.DeleteOptions{}
	for _, option := range opts {
		option.ApplyToDelete(converted)
	}
	return converted.AsDeleteOptions().DeepCopy()
}

func writeOptionsSecurityGroup() *sdnv1alpha1.SecurityGroup {
	return &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "sg-options", Namespace: testNamespace},
		Spec:       sampleSpec(),
	}
}

func TestSecurityGroupCreatePropagatesWriteOptions(t *testing.T) {
	want := &metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}, FieldManager: "creator", FieldValidation: "Strict"}
	expected := want.DeepCopy()
	var got *metav1.CreateOptions
	r := newSecurityGroupWriteOptionsREST(t, interceptor.Funcs{
		Create: func(_ context.Context, _ client.WithWatch, _ client.Object, opts ...client.CreateOption) error {
			got = captureSecurityGroupCreateOptions(opts...)
			return nil
		},
	})
	if _, err := r.Create(ctxNS(), writeOptionsSecurityGroup(), nil, want); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("Create options lost: got %#v, want %#v", got, expected)
	}
}

func TestSecurityGroupUpdatePropagatesWriteOptions(t *testing.T) {
	want := &metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}, FieldManager: "updater", FieldValidation: "Warn"}
	expected := want.DeepCopy()
	var got *metav1.UpdateOptions
	r := newSecurityGroupWriteOptionsREST(t, interceptor.Funcs{
		Update: func(_ context.Context, _ client.WithWatch, _ client.Object, opts ...client.UpdateOption) error {
			got = captureSecurityGroupUpdateOptions(opts...)
			return nil
		},
	}, markedPolicy("sg-options"))
	if _, _, err := r.Update(ctxNS(), "sg-options", rest.DefaultUpdatedObjectInfo(writeOptionsSecurityGroup()), nil, nil, false, want); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("Update options lost: got %#v, want %#v", got, expected)
	}
}

func TestSecurityGroupDeletePropagatesWriteOptions(t *testing.T) {
	propagation := metav1.DeletePropagationForeground
	want := &metav1.DeleteOptions{PropagationPolicy: &propagation, DryRun: []string{metav1.DryRunAll}}
	expected := want.DeepCopy()
	var got *metav1.DeleteOptions
	r := newSecurityGroupWriteOptionsREST(t, interceptor.Funcs{
		Delete: func(_ context.Context, _ client.WithWatch, _ client.Object, opts ...client.DeleteOption) error {
			got = captureSecurityGroupDeleteOptions(opts...)
			return nil
		},
	}, markedPolicy("sg-options"))
	if _, _, err := r.Delete(ctxNS(), "sg-options", nil, want); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("Delete options lost: got %#v, want %#v", got, expected)
	}
}

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

package controller

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
)

const (
	tokenSecret = "nvidia-license-token"
	copyName    = "gpuclass-l40s-license"
)

func namespace(name string, vgpu string) *corev1.Namespace {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if vgpu != "" {
		ns.Labels = map[string]string{gpu.LabelNamespaceVGPU: vgpu}
	}
	return ns
}

func secret(ns, name string, labels map[string]string, data map[string]string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels}, Data: map[string][]byte{}}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

func hosterSecret(data map[string]string) *corev1.Secret {
	return secret(gpu.LicenseSecretNamespace, tokenSecret, nil, data)
}

func licenseCopy(ns, class string, data map[string]string) *corev1.Secret {
	return secret(ns, gpu.LicenseSecretName(class), map[string]string{gpu.LabelGPUClass: class}, data)
}

func licensed(secretName string, featureType int32) func(*cozyv1alpha1.GPUClass) {
	return func(c *cozyv1alpha1.GPUClass) {
		c.Spec.Mode, c.Spec.Profile = cozyv1alpha1.GPUModeVGPU, "L40S-24C"
		c.Spec.License = &cozyv1alpha1.GPUClassLicense{SecretName: secretName, FeatureType: featureType}
	}
}

func runLicense(t *testing.T, c client.Client, enabled bool, class string) {
	t.Helper()
	r := &LicenseReconciler{Client: c, Reader: c, Secrets: c, Enabled: enabled}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: class}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

// getSecret returns nil when the Secret does not exist.
func getSecret(t *testing.T, c client.Client, ns, name string) *corev1.Secret {
	t.Helper()
	s := &corev1.Secret{}
	err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, s)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func dataOf(s *corev1.Secret) map[string]string {
	out := map[string]string{}
	for k, v := range s.Data {
		out[k] = string(v)
	}
	return out
}

func wantCopy(t *testing.T, c client.Client, ns string, want map[string]string) {
	t.Helper()
	s := getSecret(t, c, ns, copyName)
	if s == nil {
		t.Fatalf("%s: no license copy", ns)
	}
	if s.Labels[gpu.LabelGPUClass] != "l40s" {
		t.Errorf("%s: labels = %v, want %s=l40s", ns, s.Labels, gpu.LabelGPUClass)
	}
	if got := dataOf(s); !reflect.DeepEqual(got, want) {
		t.Errorf("%s: data = %v, want %v", ns, got, want)
	}
}

func wantNoCopy(t *testing.T, c client.Client, ns string) {
	t.Helper()
	if s := getSecret(t, c, ns, copyName); s != nil {
		t.Errorf("%s: license copy exists: %v", ns, dataOf(s))
	}
}

func licenseCondition(t *testing.T, c client.Client) *metav1.Condition {
	t.Helper()
	return meta.FindStatusCondition(getClass(t, c, "l40s").Status.Conditions, cozyv1alpha1.GPUClassLicenseSecretMissing)
}

func TestLicenseCopiesIntoEntitledNamespacesOnly(t *testing.T) {
	c := newClient(t, nil,
		gpuClass("l40s", rnL40S, 0, licensed(tokenSecret, 4)),
		hosterSecret(map[string]string{gpu.LicenseTokenKey: "token-1", "notes.txt": "hoster only"}),
		namespace("tenant-a", "true"), namespace("tenant-b", "false"), namespace("tenant-c", ""),
	)
	runLicense(t, c, true, "l40s")

	wantCopy(t, c, "tenant-a", map[string]string{gpu.LicenseTokenKey: "token-1", gpu.LicenseGriddConfKey: "FeatureType=4\n"})
	wantNoCopy(t, c, "tenant-b")
	wantNoCopy(t, c, "tenant-c")
	if cond := licenseCondition(t, c); cond == nil || cond.Status != metav1.ConditionFalse {
		t.Errorf("LicenseSecretMissing = %+v, want False", cond)
	}
}

func TestLicenseFeatureTypeDefaultsToOne(t *testing.T) {
	c := newClient(t, nil,
		gpuClass("l40s", rnL40S, 0, licensed(tokenSecret, 0)),
		hosterSecret(map[string]string{gpu.LicenseTokenKey: "token-1"}),
		namespace("tenant-a", "true"),
	)
	runLicense(t, c, true, "l40s")
	wantCopy(t, c, "tenant-a", map[string]string{gpu.LicenseTokenKey: "token-1", gpu.LicenseGriddConfKey: "FeatureType=1\n"})
}

func TestLicenseRefreshesAStaleCopy(t *testing.T) {
	stale := licenseCopy("tenant-a", "l40s", map[string]string{gpu.LicenseTokenKey: "token-0", gpu.LicenseGriddConfKey: "FeatureType=1\n", "extra": "x"})
	stale.Annotations = map[string]string{"kept": "yes"}
	c := newClient(t, nil,
		gpuClass("l40s", rnL40S, 0, licensed(tokenSecret, 4)),
		hosterSecret(map[string]string{gpu.LicenseTokenKey: "token-1"}),
		namespace("tenant-a", "true"),
		stale,
	)
	runLicense(t, c, true, "l40s")
	wantCopy(t, c, "tenant-a", map[string]string{gpu.LicenseTokenKey: "token-1", gpu.LicenseGriddConfKey: "FeatureType=4\n"})
	if got := getSecret(t, c, "tenant-a", copyName).Annotations["kept"]; got != "yes" {
		t.Errorf("annotation kept = %q, want the copy's own metadata left alone", got)
	}
}

func TestLicenseDeletesCopies(t *testing.T) {
	copies := func() []client.Object {
		return []client.Object{
			licenseCopy("tenant-a", "l40s", map[string]string{gpu.LicenseTokenKey: "token-1"}),
			licenseCopy("tenant-b", "l40s", map[string]string{gpu.LicenseTokenKey: "token-1"}),
		}
	}
	src := func() client.Object { return hosterSecret(map[string]string{gpu.LicenseTokenKey: "token-1"}) }
	tests := []struct {
		name    string
		objs    []client.Object
		enabled bool
		gone    []string
		kept    []string
	}{
		{
			name:    "namespace loses the entitlement",
			objs:    []client.Object{gpuClass("l40s", rnL40S, 0, licensed(tokenSecret, 1)), src(), namespace("tenant-a", "true"), namespace("tenant-b", "false")},
			enabled: true, gone: []string{"tenant-b"}, kept: []string{"tenant-a"},
		},
		{
			name:    "class is gone",
			objs:    []client.Object{src(), namespace("tenant-a", "true"), namespace("tenant-b", "true")},
			enabled: true, gone: []string{"tenant-a", "tenant-b"},
		},
		{
			name:    "class drops its license",
			objs:    []client.Object{gpuClass("l40s", rnL40S, 0), src(), namespace("tenant-a", "true"), namespace("tenant-b", "true")},
			enabled: true, gone: []string{"tenant-a", "tenant-b"},
		},
		{
			name:    "GPU features disabled",
			objs:    []client.Object{gpuClass("l40s", rnL40S, 0, licensed(tokenSecret, 1)), src(), namespace("tenant-a", "true"), namespace("tenant-b", "true")},
			enabled: false, gone: []string{"tenant-a", "tenant-b"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t, nil, append(tc.objs, copies()...)...)
			runLicense(t, c, tc.enabled, "l40s")
			for _, ns := range tc.gone {
				wantNoCopy(t, c, ns)
			}
			for _, ns := range tc.kept {
				wantCopy(t, c, ns, map[string]string{gpu.LicenseTokenKey: "token-1", gpu.LicenseGriddConfKey: "FeatureType=1\n"})
			}
		})
	}
}

func TestLicenseConditionGoesWithTheLicense(t *testing.T) {
	class := gpuClass("l40s", rnL40S, 0, func(c *cozyv1alpha1.GPUClass) {
		c.Status.Conditions = []metav1.Condition{{Type: cozyv1alpha1.GPUClassLicenseSecretMissing, Status: metav1.ConditionTrue, Reason: reasonSecretNotFound, LastTransitionTime: metav1.NewTime(epoch)}}
	})
	c := newClient(t, nil, class)
	runLicense(t, c, true, "l40s")
	if cond := licenseCondition(t, c); cond != nil {
		t.Errorf("LicenseSecretMissing = %+v on a class without a license, want none", cond)
	}
}

func TestLicenseDisabledClearsComputedStatus(t *testing.T) {
	class := gpuClass("l40s", rnL40S, 0, func(c *cozyv1alpha1.GPUClass) {
		c.Status.Free, c.Status.MaxFreePerNode = ptr.To[int32](3), ptr.To[int32](2)
		for _, typ := range []string{cozyv1alpha1.GPUClassResourceNameConflict, cozyv1alpha1.GPUClassResourceNameNotPermitted} {
			c.Status.Conditions = append(c.Status.Conditions, metav1.Condition{Type: typ, Status: metav1.ConditionFalse, Reason: "Computed", LastTransitionTime: metav1.NewTime(epoch)})
		}
	})
	for _, enabled := range []bool{true, false} {
		c := newClient(t, nil, class.DeepCopy())
		runLicense(t, c, enabled, "l40s")
		got := getClass(t, c, "l40s").Status
		cleared := got.Free == nil && got.MaxFreePerNode == nil && len(got.Conditions) == 0
		if cleared == enabled {
			t.Errorf("enabled=%v: status = %+v, want it cleared only while GPU features are off", enabled, got)
		}
	}
}

func TestLicenseSourceUnusable(t *testing.T) {
	tests := []struct {
		name   string
		source *corev1.Secret
		reason string
	}{
		{"source missing", nil, reasonSecretNotFound},
		{"source without the token key", hosterSecret(map[string]string{"token": "misnamed"}), reasonTokenKeyMissing},
		{"source with an empty token", hosterSecret(map[string]string{gpu.LicenseTokenKey: ""}), reasonTokenKeyMissing},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			objs := []client.Object{
				gpuClass("l40s", rnL40S, 0, licensed(tokenSecret, 4)),
				namespace("tenant-a", "true"), namespace("tenant-new", "true"), namespace("tenant-gone", "false"),
				licenseCopy("tenant-a", "l40s", map[string]string{gpu.LicenseTokenKey: "token-0", gpu.LicenseGriddConfKey: "FeatureType=1\n"}),
				licenseCopy("tenant-gone", "l40s", map[string]string{gpu.LicenseTokenKey: "token-0"}),
			}
			if tc.source != nil {
				objs = append(objs, tc.source)
			}
			c := newClient(t, nil, objs...)
			runLicense(t, c, true, "l40s")

			wantCopy(t, c, "tenant-a", map[string]string{gpu.LicenseTokenKey: "token-0", gpu.LicenseGriddConfKey: "FeatureType=1\n"})
			wantNoCopy(t, c, "tenant-new")
			wantNoCopy(t, c, "tenant-gone")
			cond := licenseCondition(t, c)
			if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != tc.reason {
				t.Errorf("LicenseSecretMissing = %+v, want True with reason %s", cond, tc.reason)
			}
		})
	}
}

func TestLicenseTouchesOnlyItsLabelledCopies(t *testing.T) {
	foreign := secret("tenant-a", copyName, map[string]string{"app": "mine"}, map[string]string{"k": "tenant data"})
	unentitledForeign := secret("tenant-b", copyName, nil, map[string]string{"k": "tenant data"})
	otherName := secret("tenant-b", "my-secret", map[string]string{gpu.LabelGPUClass: "l40s"}, map[string]string{"k": "v"})
	otherClass := licenseCopy("tenant-b", "h100", map[string]string{gpu.LicenseTokenKey: "h"})
	hosterLabelled := licenseCopy(gpu.LicenseSecretNamespace, "l40s", map[string]string{gpu.LicenseTokenKey: "hoster"})
	terminating := namespace("tenant-d", "true")
	terminating.Finalizers = []string{"test"}
	terminating.DeletionTimestamp = ptr.To(metav1.NewTime(epoch))
	c := newClient(t, nil,
		gpuClass("l40s", rnL40S, 0, licensed(tokenSecret, 1)),
		hosterSecret(map[string]string{gpu.LicenseTokenKey: "token-1"}),
		namespace("tenant-a", "true"), namespace("tenant-b", "false"), namespace("tenant-c", "true"),
		namespace(gpu.LicenseSecretNamespace, "true"),
		foreign, unentitledForeign, otherName, otherClass, hosterLabelled, terminating,
	)
	runLicense(t, c, true, "l40s")

	if got := getSecret(t, c, "tenant-a", copyName); got == nil || !reflect.DeepEqual(dataOf(got), map[string]string{"k": "tenant data"}) || got.Labels[gpu.LabelGPUClass] != "" {
		t.Errorf("foreign Secret changed: %+v", got)
	}
	if getSecret(t, c, "tenant-b", copyName) == nil {
		t.Error("deleted an unlabelled Secret with the copy's name")
	}
	if getSecret(t, c, "tenant-b", "my-secret") == nil {
		t.Error("deleted a labelled Secret that is not named as a copy")
	}
	if got := getSecret(t, c, gpu.LicenseSecretNamespace, copyName); got == nil || string(got.Data[gpu.LicenseTokenKey]) != "hoster" {
		t.Errorf("touched a hoster Secret in cozy-system that looks like a copy: %+v", got)
	}
	wantNoCopy(t, c, "tenant-d")
	if getSecret(t, c, "tenant-b", gpu.LicenseSecretName("h100")) == nil {
		t.Error("deleted another class's copy")
	}
	wantCopy(t, c, "tenant-c", map[string]string{gpu.LicenseTokenKey: "token-1", gpu.LicenseGriddConfKey: "FeatureType=1\n"})
}

func TestLicenseSecretToClasses(t *testing.T) {
	c := newClient(t, nil,
		gpuClass("l40s", rnL40S, 0, licensed(tokenSecret, 1)),
		gpuClass("l40s-q", rnH100, 1, licensed(tokenSecret, 1)),
		gpuClass("h100", rnH100, 2),
	)
	r := &LicenseReconciler{Client: c}
	stub := func(ns, name string, labels map[string]string) *metav1.PartialObjectMetadata {
		return &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels}}
	}
	tests := []struct {
		name string
		obj  *metav1.PartialObjectMetadata
		want []string
	}{
		{"hoster Secret named by two classes", stub(gpu.LicenseSecretNamespace, tokenSecret, nil), []string{"l40s", "l40s-q"}},
		{"same name outside cozy-system", stub("tenant-a", tokenSecret, nil), nil},
		{"unrelated cozy-system Secret", stub(gpu.LicenseSecretNamespace, "other", nil), nil},
		{"a copy", stub("tenant-a", gpu.LicenseSecretName("gone"), map[string]string{gpu.LabelGPUClass: "gone"}), []string{"gone"}},
	}
	for _, tc := range tests {
		var got []string
		for _, req := range r.secretToClasses(context.Background(), tc.obj) {
			got = append(got, req.Name)
		}
		slices.Sort(got)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: classes = %v, want %v", tc.name, got, tc.want)
		}
	}

	var all []string
	for _, req := range r.allClasses(context.Background(), namespace("tenant-a", "true")) {
		all = append(all, req.Name)
	}
	slices.Sort(all)
	if want := []string{"h100", "l40s", "l40s-q"}; !reflect.DeepEqual(all, want) {
		t.Errorf("namespace event classes = %v, want %v", all, want)
	}
}

func TestLicenseSecondPassWritesNothing(t *testing.T) {
	writes := 0
	count := func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
		writes++
		return cl.Update(ctx, obj, opts...)
	}
	c := newClient(t, &interceptor.Funcs{
		Update: count,
		SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			writes++
			return cl.SubResource(sub).Update(ctx, obj, opts...)
		},
	},
		gpuClass("l40s", rnL40S, 0, licensed(tokenSecret, 1)),
		hosterSecret(map[string]string{gpu.LicenseTokenKey: "token-1"}),
		namespace("tenant-a", "true"),
	)
	runLicense(t, c, true, "l40s")
	writes = 0
	runLicense(t, c, true, "l40s")
	if writes != 0 {
		t.Errorf("writes on an unchanged second pass = %d, want 0", writes)
	}
}

// TestLicensePruneDeletesOnlyTheListedCopy covers a copy replaced by a
// tenant's own Secret of the same name after the metadata cache listed it.
func TestLicensePruneDeletesOnlyTheListedCopy(t *testing.T) {
	stale := licenseCopy("tenant-b", "l40s", nil)
	stale.UID = "listed-copy"
	cached := newClient(t, nil, stale)
	replaced := secret("tenant-b", copyName, nil, map[string]string{"k": "tenant data"})
	replaced.UID = "tenant-secret"
	// The fake client checks only resourceVersion preconditions; this
	// applies the UID one as the API server does.
	c := newClient(t, &interceptor.Funcs{
		Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			do := &client.DeleteOptions{}
			do.ApplyOptions(opts)
			cur := &corev1.Secret{}
			if err := cl.Get(ctx, client.ObjectKeyFromObject(obj), cur); err != nil {
				return err
			}
			if do.Preconditions != nil && do.Preconditions.UID != nil && *do.Preconditions.UID != cur.UID {
				return apierrors.NewConflict(corev1.Resource("secrets"), obj.GetName(), errors.New("UID precondition failed"))
			}
			return cl.Delete(ctx, obj, opts...)
		},
	}, gpuClass("l40s", rnL40S, 0), replaced)

	r := &LicenseReconciler{Client: c, Reader: c, Secrets: cached, Enabled: true}
	_, _ = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "l40s"}})
	if getSecret(t, c, "tenant-b", copyName) == nil {
		t.Error("deleted a Secret the metadata cache did not list")
	}
}

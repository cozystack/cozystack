package controller

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr/funcr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"strings"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestReferencedValuesRetainPoolClass(t *testing.T) {
	for _, artifact := range []string{"cozystack-kubernetes-nodes-application-kubevirt-kubernetes-nodes", "cozystack-computeplane-application-kubevirt-kubernetes-nodes"} {
		for _, kind := range []string{"Secret", "ConfigMap"} {
			t.Run(artifact+kind, func(t *testing.T) {
				hr := pool("tenant-a", "pool", "")
				hr.Spec.ChartRef = &helmv2.CrossNamespaceSourceReference{Kind: "ExternalArtifact", Namespace: "cozy-system", Name: artifact}
				hr.Spec.ValuesFrom = []helmv2.ValuesReference{{Kind: kind, Name: "pool-values"}}
				var obj client.Object = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "pool-values", Namespace: hr.Namespace}, Data: map[string][]byte{"values.yaml": []byte("workerClassName: acme")}}
				if kind == "ConfigMap" {
					obj = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "pool-values", Namespace: hr.Namespace}, Data: map[string]string{"values.yaml": "workerClassName: acme"}}
				}
				r, c := newReconciler(t, terminating(class("acme", Finalizer)), hr, obj)
				reconcileClass(t, r, "acme")
				if len(finalizers(t, c, "acme")) != 1 {
					t.Fatal("referenced class released")
				}
			})
		}
	}
}

func TestEffectiveClassFluxSemantics(t *testing.T) {
	cases := []struct {
		name, first, second, path, inline, want string
		optional                                bool
		wantError                               bool
	}{
		{name: "ordered roots", first: "workerClassName: first", second: "workerClassName: second", want: "second"},
		{name: "default key", first: "workerClassName: first", want: "first"},
		{name: "target path", first: "custom", path: "workerClassName", want: "custom"},
		{name: "sibling assignment", first: "x,workerClassName=custom", path: "other", want: "custom"},
		{name: "quoted sibling", first: `"x,workerClassName=123"`, path: "other", want: "123"},
		{name: "escaped dot", first: "x,workerClassName=custom", path: `other\.key`, want: "custom"},
		{name: "array", first: "x,workerClassName=custom", path: "other[0]", want: "custom"},
		{name: "inline wins last", first: "custom", path: "workerClassName", inline: `{"workerClassName":"inline"}`, want: "inline"},
		{name: "optional missing key", optional: true},
		{name: "missing key", wantError: true},
		{name: "wrong type", first: "workerClassName: 123", wantError: true},
		{name: "null type", first: "workerClassName: null", wantError: true},
		{name: "array type", first: "workerClassName: [custom]", wantError: true},
		{name: "map type", first: "workerClassName: {name: custom}", wantError: true},
		{name: "root inline wins", first: "workerClassName: custom", inline: `{"workerClassName":"inline"}`, want: "inline"},
		{name: "inline before target", first: "x,workerClassName=custom", path: "other", inline: `{"other":"before"}`, want: "custom"},
		{name: "payload error", first: "x,PRIVATE_PAYLOAD_MARKER", path: "other", wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hr := pool("tenant-a", "pool", "")
			if tc.inline != "" {
				hr.Spec.Values.Raw = []byte(tc.inline)
			}
			hr.Spec.ValuesFrom = []helmv2.ValuesReference{{Kind: "Secret", Name: "values", TargetPath: tc.path, Optional: tc.optional}}
			data := map[string][]byte{}
			if tc.first != "" {
				data["values.yaml"] = []byte(tc.first)
			}
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "values", Namespace: hr.Namespace}, Data: data}
			objects := []client.Object{secret}
			if tc.second != "" {
				hr.Spec.ValuesFrom = append(hr.Spec.ValuesFrom, helmv2.ValuesReference{Kind: "ConfigMap", Name: "second"})
				objects = append(objects, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "second", Namespace: hr.Namespace}, Data: map[string]string{"values.yaml": tc.second}})
			}
			r, _ := newReconciler(t, objects...)
			got, err := r.effectiveClass(context.Background(), hr)
			if (err != nil) != tc.wantError || got != tc.want {
				t.Fatalf("class=%q err=%v, want %q error=%v", got, err, tc.want, tc.wantError)
			}
			if err != nil && strings.Contains(err.Error(), "PRIVATE_PAYLOAD_MARKER") {
				t.Fatal("payload leaked in error")
			}
		})
	}
}

func TestUnreadableValuesKeepFinalizerWithoutLeakingPayload(t *testing.T) {
	for _, failure := range []string{"forbidden", "read", "key", "parse", "kind", "type"} {
		t.Run(failure, func(t *testing.T) {
			const marker = "PRIVATE_PAYLOAD_MARKER"
			hr := pool("tenant-a", "pool", "")
			hr.Spec.ValuesFrom = []helmv2.ValuesReference{{Kind: "Secret", Name: "values", Optional: true}}
			data := map[string][]byte{"values.yaml": []byte("workerClassName: custom")}
			switch failure {
			case "key":
				hr.Spec.ValuesFrom[0].Optional = false
				data = nil
			case "parse":
				hr.Spec.ValuesFrom[0].TargetPath = "other"
				data["values.yaml"] = []byte("x," + marker)
			case "kind":
				hr.Spec.ValuesFrom[0].Kind = "Other"
			case "type":
				data["values.yaml"] = []byte("workerClassName: [" + marker + "]")
			}
			r, c := newReconcilerWith(t, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*corev1.Secret); ok {
						if failure == "read" {
							return errors.New(marker)
						}
						if failure == "forbidden" {
							return apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, key.Name, errors.New(marker))
						}
					}
					return c.Get(ctx, key, obj, opts...)
				},
				List: func(ctx context.Context, c client.WithWatch, obj client.ObjectList, opts ...client.ListOption) error {
					if _, ok := obj.(*helmv2.HelmReleaseList); !ok {
						t.Fatalf("unexpected list: %T", obj)
					}
					return c.List(ctx, obj, opts...)
				},
			}, terminating(class("custom", Finalizer)), hr, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "values", Namespace: hr.Namespace}, Data: data})
			var logs strings.Builder
			ctx := log.IntoContext(context.Background(), funcr.New(func(prefix, args string) { fmt.Fprintln(&logs, prefix, args) }, funcr.Options{}))
			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "custom"}})
			if err == nil || strings.Contains(err.Error()+logs.String(), marker) {
				t.Fatalf("unsafe resolution failure: %v logs=%s", err, logs.String())
			}
			if len(finalizers(t, c, "custom")) != 1 {
				t.Fatal("unreadable values released class")
			}
			if failure == "forbidden" && !strings.Contains(err.Error(), string(metav1.StatusReasonForbidden)) {
				t.Fatalf("a read failure lost its API reason: %v", err)
			}
		})
	}
}

func TestOptionalMissingObjectSnapshot(t *testing.T) {
	hr := pool("tenant-a", "pool", "")
	hr.Spec.ValuesFrom = []helmv2.ValuesReference{{Kind: "Secret", Name: "absent", Optional: true}, {Kind: "Secret", Name: "absent", Optional: true}}
	gets := 0
	r, _ := newReconcilerWith(t, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		gets++
		return c.Get(ctx, key, obj, opts...)
	}})
	if got, err := r.effectiveClass(context.Background(), hr); err != nil || got != "" || gets != 1 {
		t.Fatalf("class=%q err=%v gets=%d", got, err, gets)
	}
	hr.Spec.ValuesFrom[1].Optional = false
	if _, err := r.effectiveClass(context.Background(), hr); err == nil {
		t.Fatal("required missing object accepted after optional absence")
	}
}

func TestChangedReferencedValuesReleaseLastClass(t *testing.T) {
	hr := pool("tenant-a", "pool", "")
	hr.Spec.ValuesFrom = []helmv2.ValuesReference{{Kind: "Secret", Name: "values"}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "values", Namespace: hr.Namespace}, Data: map[string][]byte{"values.yaml": []byte("workerClassName: custom")}}
	r, c := newReconciler(t, terminating(class("custom", Finalizer)), hr, secret)
	reconcileClass(t, r, "custom")
	if len(finalizers(t, c, "custom")) != 1 {
		t.Fatal("class not retained")
	}
	secret.Data["values.yaml"] = []byte("workerClassName: talos")
	if err := c.Update(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	reconcileClass(t, r, "custom")
	if err := c.Get(context.Background(), client.ObjectKey{Name: "custom"}, class("custom")); !apierrors.IsNotFound(err) {
		t.Fatalf("last reference did not release class: %v", err)
	}
}

func TestReferenceSnapshotAndNamespace(t *testing.T) {
	hr := pool("tenant-a", "pool", "")
	hr.Spec.TargetNamespace = "tenant-b"
	hr.Spec.ValuesFrom = []helmv2.ValuesReference{{Kind: "Secret", Name: "values", ValuesKey: "first"}, {Kind: "Secret", Name: "values", ValuesKey: "second"}}
	gets := 0
	r, _ := newReconcilerWith(t, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*corev1.Secret); ok {
			gets++
			if key.Namespace != hr.Namespace {
				t.Fatalf("cross namespace read: %v", key)
			}
		}
		return c.Get(ctx, key, obj, opts...)
	}}, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "values", Namespace: hr.Namespace}, Data: map[string][]byte{"first": []byte("workerClassName: first"), "second": []byte("workerClassName: second")}})
	got, err := r.effectiveClass(context.Background(), hr)
	if err != nil || got != "second" || gets != 1 {
		t.Fatalf("class=%q err=%v gets=%d", got, err, gets)
	}
}

func TestNullInlineValuesNameNoClass(t *testing.T) {
	hr := pool("tenant-a", "pool", "")
	hr.Spec.Values.Raw = []byte("null")
	r, _ := newReconciler(t)
	got, err := r.effectiveClass(context.Background(), hr)
	if err != nil || got != "" {
		t.Fatalf("class=%q err=%v, want a classless pool", got, err)
	}
}

func TestMalformedInlineValuesAreNamedAsTheValuesDocument(t *testing.T) {
	hr := pool("tenant-a", "pool", "")
	hr.Spec.Values.Raw = []byte("[]")
	r, _ := newReconciler(t)
	_, err := r.effectiveClass(context.Background(), hr)
	if err == nil || err.Error() != "invalid HelmRelease values: want an object" {
		t.Fatalf("err = %v", err)
	}
}

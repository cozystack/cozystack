package restore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"testing"
)

func TestOptionalReferenceAbsenceMatchesFlux(t *testing.T) {
	for _, kind := range []string{"Secret", "ConfigMap"} {
		for _, missing := range []string{"object", "key"} {
			for _, optional := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/optional=%v", kind, missing, optional), func(t *testing.T) {
					scheme := runtime.NewScheme()
					if err := corev1.AddToScheme(scheme); err != nil {
						t.Fatal(err)
					}
					var objects []client.Object
					if missing == "key" {
						var obj client.Object = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "values", Namespace: "tenant-test"}}
						if kind == "ConfigMap" {
							obj = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "values", Namespace: "tenant-test"}}
						}
						objects = append(objects, obj)
					}
					c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
					hr := &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-test"}, Spec: helmv2.HelmReleaseSpec{ValuesFrom: []helmv2.ValuesReference{{Kind: kind, Name: "values", Optional: optional}}}}
					values, err := ReferenceValues(context.Background(), c, hr)
					if (err == nil) != optional {
						t.Fatalf("optional=%v values=%v err=%v", optional, values, err)
					}
					if optional && len(values) != 0 {
						t.Fatalf("missing reference manufactured values: %v", values)
					}
				})
			}
		}
	}
}

func TestReferenceFailuresDoNotExposePayloads(t *testing.T) {
	for _, failure := range []string{"forbidden", "read", "parse"} {
		t.Run(failure, func(t *testing.T) {
			const marker = "PRIVATE_PAYLOAD_MARKER"
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "values", Namespace: "tenant-test"}, Data: map[string][]byte{"values.yaml": []byte("key: [" + marker)}}).WithInterceptorFuncs(interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if failure == "forbidden" {
					return apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, key.Name, errors.New(marker))
				}
				if failure == "read" {
					return errors.New(marker)
				}
				return c.Get(ctx, key, obj, opts...)
			}}).Build()
			hr := &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-test"}, Spec: helmv2.HelmReleaseSpec{ValuesFrom: []helmv2.ValuesReference{{Kind: "Secret", Name: "values", Optional: true}}}}
			_, err := ReferenceValues(context.Background(), c, hr)
			if err == nil || strings.Contains(err.Error(), marker) {
				t.Fatalf("unsafe reference failure: %v", err)
			}
			if failure == "forbidden" && !strings.Contains(err.Error(), string(metav1.StatusReasonForbidden)) {
				t.Fatalf("read failure lost its API reason: %v", err)
			}
		})
	}
}

func TestRenderReferencesAreReadWithoutFreezingThemInInlineValues(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "cozystack-values", Namespace: "tenant-test"}, Data: map[string][]byte{"values.yaml": []byte("_cluster:\n  cluster-domain: cozy.local\n_namespace:\n  host: example.com\n")}}).Build()
	hr := &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-test"}, Spec: helmv2.HelmReleaseSpec{ValuesFrom: []helmv2.ValuesReference{{Kind: "Secret", Name: "cozystack-values"}}}}
	values, err := ReferenceValues(context.Background(), c, hr)
	if err != nil {
		t.Fatal(err)
	}
	merged := MergeValues(values, map[string]any{"cluster": "k"})
	if merged["_cluster"] == nil || merged["cluster"] != "k" || values["cluster"] != nil {
		t.Fatalf("reference merge mutated source: %v %v", merged, values)
	}
	hr.Spec.ValuesFrom[0].ValuesKey = "missing"
	if _, err = ReferenceValues(context.Background(), c, hr); err == nil {
		t.Fatal("missing reference key silently ignored")
	}
	hr.Spec.ValuesFrom[0].ValuesKey = ""
	hr.Spec.ValuesFrom[0].TargetPath = "talos.version"
	if _, err = ReferenceValues(context.Background(), c, hr); err == nil || !strings.Contains(err.Error(), "targetPath") {
		t.Fatalf("unsupported targetPath silently approximated: %v", err)
	}
}

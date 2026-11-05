package restore

import (
	"context"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"testing"
)

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
	hr.Spec.ValuesFrom[0].TargetPath = "talos.version"
	if _, err = ReferenceValues(context.Background(), c, hr); err == nil {
		t.Fatal("unsupported targetPath silently approximated")
	}
}

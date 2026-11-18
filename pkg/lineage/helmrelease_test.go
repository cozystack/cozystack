package lineage

import (
	"context"
	"fmt"
	"strings"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// namedAppMapper maps a HelmRelease to the Kubernetes application it names;
// a HelmRelease missing from the map fails to map.
type namedAppMapper map[string]string

func (m namedAppMapper) Map(hr *helmv2.HelmRelease) (string, string, string, error) {
	app, ok := m[hr.Name]
	if !ok {
		return "", "", "", fmt.Errorf("no application for HelmRelease %s", hr.Name)
	}
	return "apps.cozystack.io/v1alpha1", "Kubernetes", strings.TrimSuffix(hr.Name, app), nil
}

// A HelmRelease that cannot be resolved to its application hands the walk to
// the HelmRelease its label names; one that resolves ends the walk there.
func TestWalkHelmReleaseFallsBackToItsLabel(t *testing.T) {
	parent := map[string]string{HRLabel: "kubernetes-b"}
	unconvertible := capkObject(HRAPIVersion, HRKind, "kubernetes-a", parent, nil)
	unconvertible.Object["spec"] = "not an object"
	for _, tc := range []struct {
		name   string
		hr     *unstructured.Unstructured
		mapped namedAppMapper
		want   []string
	}{
		{name: "mapped", hr: capkObject(HRAPIVersion, HRKind, "kubernetes-a", parent, nil), mapped: namedAppMapper{"kubernetes-a": "a", "kubernetes-b": "b"}, want: []string{"a"}},
		{name: "unmappable", hr: capkObject(HRAPIVersion, HRKind, "kubernetes-a", parent, nil), mapped: namedAppMapper{"kubernetes-b": "b"}, want: []string{"b"}},
		{name: "application missing", hr: capkObject(HRAPIVersion, HRKind, "kubernetes-a", parent, nil), mapped: namedAppMapper{"kubernetes-a": "absent", "kubernetes-b": "b"}, want: []string{"b"}},
		{name: "unconvertible", hr: unconvertible, mapped: namedAppMapper{"kubernetes-a": "a", "kubernetes-b": "b"}, want: []string{"b"}},
		{name: "unmappable without a label", hr: capkObject(HRAPIVersion, HRKind, "kubernetes-a", nil, nil), mapped: namedAppMapper{"kubernetes-b": "b"}, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objs := []runtime.Object{
				capkObject("apps.cozystack.io/v1alpha1", "Kubernetes", "a", nil, nil),
				capkObject("apps.cozystack.io/v1alpha1", "Kubernetes", "b", nil, nil),
				capkObject(HRAPIVersion, HRKind, "kubernetes-b", nil, nil),
			}
			mapper := meta.NewDefaultRESTMapper(nil)
			for _, o := range objs {
				mapper.Add(o.GetObjectKind().GroupVersionKind(), meta.RESTScopeNamespace)
			}
			dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objs...)
			var got []string
			for _, id := range WalkOwnershipGraph(context.Background(), dyn, mapper, tc.mapped, tc.hr) {
				got = append(got, id.Kind+"/"+id.Name)
			}
			var want []string
			for _, name := range tc.want {
				want = append(want, "Kubernetes/"+name)
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("WalkOwnershipGraph(%s) = %v, want %v", tc.name, got, want)
			}
		})
	}
}

package lineage

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

var testHRGVK = schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"}

// flakyMapper fails the first `failures` RESTMapping calls, as discovery does
// while the aggregated cozystack-api is rolling.
type flakyMapper struct {
	meta.RESTMapper
	failures int
}

func (m *flakyMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	if m.failures > 0 {
		m.failures--
		return nil, &meta.NoKindMatchError{GroupKind: gk, SearchedVersions: versions}
	}
	return m.RESTMapper.RESTMapping(gk, versions...)
}

// newGetFixture returns a dynamic client holding one HelmRelease, a mapper that
// knows its kind, and a counter of the GETs that reached the client. A non-nil
// firstErr is returned by the first GET instead of the object.
func newGetFixture(t *testing.T, firstErr error) (*dynamicfake.FakeDynamicClient, *meta.DefaultRESTMapper, *int) {
	t.Helper()
	hr := &unstructured.Unstructured{}
	hr.SetGroupVersionKind(testHRGVK)
	hr.SetNamespace("tenant-demo")
	hr.SetName("harbor-demo")

	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), hr)
	gets := 0
	dyn.PrependReactor("get", "helmreleases", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets == 1 && firstErr != nil {
			return true, nil, firstErr
		}
		return false, nil, nil
	})

	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(testHRGVK, meta.RESTScopeNamespace)
	return dyn, mapper, &gets
}

func getTestHR(dyn *dynamicfake.FakeDynamicClient, mapper meta.RESTMapper, cache *ObjectCache, name string) (*unstructured.Unstructured, error) {
	return getUnstructuredObject(context.Background(), dyn, mapper, cache, HRAPIVersion, HRKind, "tenant-demo", name)
}

func TestGetUnstructuredObject_CachesSuccess(t *testing.T) {
	dyn, mapper, gets := newGetFixture(t, nil)
	cache := NewObjectCache(time.Minute)

	for i := 0; i < 2; i++ {
		if _, err := getTestHR(dyn, mapper, cache, "harbor-demo"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if *gets != 1 {
		t.Fatalf("expected the second call to be served from the cache, got %d GETs", *gets)
	}
}

func TestGetUnstructuredObject_RetriesAfterMappingFailure(t *testing.T) {
	dyn, base, gets := newGetFixture(t, nil)
	mapper := &flakyMapper{RESTMapper: base, failures: 1}
	cache := NewObjectCache(time.Minute)

	if _, err := getTestHR(dyn, mapper, cache, "harbor-demo"); err == nil {
		t.Fatal("expected the first call to fail on RESTMapping")
	}
	obj, err := getTestHR(dyn, mapper, cache, "harbor-demo")
	if err != nil {
		t.Fatalf("a mapping failure must not be cached: %v", err)
	}
	if obj.GetName() != "harbor-demo" || *gets != 1 {
		t.Fatalf("expected the second call to reach the client, got obj=%v GETs=%d", obj, *gets)
	}
}

func TestGetUnstructuredObject_RetriesAfterGetError(t *testing.T) {
	gr := schema.GroupResource{Group: testHRGVK.Group, Resource: "helmreleases"}
	cases := map[string]error{
		"timeout":   apierrors.NewTimeoutError("apiserver overloaded", 1),
		"not found": apierrors.NewNotFound(gr, "harbor-demo"),
	}
	for name, first := range cases {
		t.Run(name, func(t *testing.T) {
			dyn, mapper, gets := newGetFixture(t, first)
			cache := NewObjectCache(time.Minute)

			if _, err := getTestHR(dyn, mapper, cache, "harbor-demo"); err == nil {
				t.Fatal("expected the first GET to fail")
			}
			obj, err := getTestHR(dyn, mapper, cache, "harbor-demo")
			if err != nil {
				t.Fatalf("a failed GET must not be cached: %v", err)
			}
			if obj.GetName() != "harbor-demo" || *gets != 2 {
				t.Fatalf("expected the second call to reach the client, got obj=%v GETs=%d", obj, *gets)
			}
		})
	}
}

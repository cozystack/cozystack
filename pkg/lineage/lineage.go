package lineage

import (
	"context"
	"fmt"
	"os"
	"strings"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	HRAPIVersion = "helm.toolkit.fluxcd.io/v2"
	HRKind       = "HelmRelease"
	HRLabel      = "helm.toolkit.fluxcd.io/name"
)

// AppMapper maps HelmRelease to application metadata.
type AppMapper interface {
	Map(*helmv2.HelmRelease) (apiVersion, kind, prefix string, err error)
}

type ObjectID struct {
	APIVersion string
	Kind       string
	Namespace  string
	Name       string
}

func (o ObjectID) GetUnstructured(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper) (*unstructured.Unstructured, error) {
	return o.GetUnstructuredCached(ctx, client, mapper, nil)
}

// GetUnstructuredCached fetches the object using the supplied cache. A nil
// cache reverts to a direct, uncached dynamic.Get.
func (o ObjectID) GetUnstructuredCached(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, cache *ObjectCache) (*unstructured.Unstructured, error) {
	return getUnstructuredObject(ctx, client, mapper, cache, o.APIVersion, o.Kind, o.Namespace, o.Name)
}

// walkState bundles the per-walk arguments that used to be threaded through the
// variadic `memory` parameter. Encapsulating them keeps the variadic backwards
// compatible (a single `*walkState` or the legacy `map[ObjectID]bool` are both
// accepted) while letting callers thread a shared ObjectCache through the
// recursion without changing every call site.
type walkState struct {
	visited map[ObjectID]bool
	cache   *ObjectCache
}

func WalkOwnershipGraph(
	ctx context.Context,
	client dynamic.Interface,
	mapper meta.RESTMapper,
	appMapper AppMapper,
	obj *unstructured.Unstructured,
	memory ...interface{},
) (out []ObjectID) {

	id := ObjectID{APIVersion: obj.GetAPIVersion(), Kind: obj.GetKind(), Namespace: obj.GetNamespace(), Name: obj.GetName()}
	out = []ObjectID{}
	l := log.FromContext(ctx)

	l.V(1).Info("processing object", "apiVersion", obj.GetAPIVersion(), "kind", obj.GetKind(), "name", obj.GetName())
	state, err := parseWalkMemory(memory)
	if err != nil {
		l.Error(err, "invalid WalkOwnershipGraph variadic arguments", "variadic_args_passed", len(memory), "expected", "0 or 1 (map[ObjectID]bool | *walkState)")
		return out
	}

	if state.visited[id] {
		return out
	}

	state.visited[id] = true

	ownerRefs := obj.GetOwnerReferences()
	for _, owner := range ownerRefs {
		ownerObj, err := getUnstructuredObject(ctx, client, mapper, state.cache, owner.APIVersion, owner.Kind, obj.GetNamespace(), owner.Name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Could not fetch owner %s/%s (%s): %v\n", obj.GetNamespace(), owner.Name, owner.Kind, err)
			continue
		}

		out = append(out, WalkOwnershipGraph(ctx, client, mapper, appMapper, ownerObj, state)...)
	}

	// if object has owners, it couldn't be owned directly by the custom app
	if len(ownerRefs) > 0 {
		return
	}

	// I want "if err1 != nil go to next block, if err2 != nil, go to next block, etc semantics",
	// like an early return from a function, but if all checks succeed, I don't want to do the rest
	// of the function, so it's a `for { if err { break } if othererr { break } if allgood { return }
	for {
		if obj.GetAPIVersion() != HRAPIVersion || obj.GetKind() != HRKind {
			break
		}
		hr := helmReleaseFromUnstructured(obj)
		if hr == nil {
			break
		}
		a, k, p, err := appMapper.Map(hr)
		if err != nil {
			l.Error(err, "failed to map HelmRelease to app")
			break
		}
		ownerObj, err := getUnstructuredObject(ctx, client, mapper, state.cache, a, k, obj.GetNamespace(), strings.TrimPrefix(obj.GetName(), p))
		if err != nil {
			l.Error(err, "couldn't get unstructured object", "APIVersion", a, "Kind", k, "Name", strings.TrimPrefix(obj.GetName(), p))
			break
		}
		// successfully mapped a HelmRelease to a custom app, no need to continue
		out = append(out,
			ObjectID{
				APIVersion: ownerObj.GetAPIVersion(),
				Kind:       ownerObj.GetKind(),
				Namespace:  ownerObj.GetNamespace(),
				Name:       ownerObj.GetName(),
			},
		)
		return
	}

	labels := obj.GetLabels()
	name, ok := labels[HRLabel]
	if !ok {
		return
	}
	ownerObj, err := getUnstructuredObject(ctx, client, mapper, state.cache, HRAPIVersion, HRKind, obj.GetNamespace(), name)
	if err != nil {
		return
	}
	out = append(out, WalkOwnershipGraph(ctx, client, mapper, appMapper, ownerObj, state)...)

	return
}

// parseWalkMemory normalises the variadic `memory` argument of
// WalkOwnershipGraph into a *walkState. Three forms are accepted for backwards
// compatibility:
//
//   - no argument  → fresh state, no cache
//   - *walkState   → use as-is (preferred, threads cache through recursion)
//   - map[ObjectID]bool → legacy visited map (no cache)
func parseWalkMemory(memory []interface{}) (*walkState, error) {
	switch len(memory) {
	case 0:
		return &walkState{visited: make(map[ObjectID]bool)}, nil
	case 1:
		switch m := memory[0].(type) {
		case *walkState:
			if m == nil {
				return &walkState{visited: make(map[ObjectID]bool)}, nil
			}
			if m.visited == nil {
				m.visited = make(map[ObjectID]bool)
			}
			return m, nil
		case map[ObjectID]bool:
			if m == nil {
				// Defend against callers who pass `map[ObjectID]bool(nil)` —
				// writing into a nil map would panic on the first visited[id] = true.
				m = make(map[ObjectID]bool)
			}
			return &walkState{visited: m}, nil
		default:
			return &walkState{visited: make(map[ObjectID]bool)}, fmt.Errorf("invalid argument: received %T, expected map[ObjectID]bool or *walkState", memory[0])
		}
	default:
		return &walkState{visited: make(map[ObjectID]bool)}, fmt.Errorf("invalid argument count: %d", len(memory))
	}
}

// WalkOwnershipGraphWithCache is the cache-aware entrypoint. It plumbs the
// supplied ObjectCache through recursion so all dynamic GETs for the lifetime
// of the walk (and across walks sharing the same cache) hit memory rather than
// the apiserver. A nil cache is valid; it falls back to the original uncached
// behaviour.
func WalkOwnershipGraphWithCache(
	ctx context.Context,
	client dynamic.Interface,
	mapper meta.RESTMapper,
	appMapper AppMapper,
	cache *ObjectCache,
	obj *unstructured.Unstructured,
) []ObjectID {
	state := &walkState{
		visited: make(map[ObjectID]bool),
		cache:   cache,
	}
	return WalkOwnershipGraph(ctx, client, mapper, appMapper, obj, state)
}

func getUnstructuredObject(
	ctx context.Context,
	client dynamic.Interface,
	mapper meta.RESTMapper,
	cache *ObjectCache,
	apiVersion, kind, namespace, name string,
) (*unstructured.Unstructured, error) {
	if cached, cachedErr, ok := cache.Get(apiVersion, kind, namespace, name); ok {
		return cached, cachedErr
	}

	l := log.FromContext(ctx)
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		l.Error(
			err, "failed to parse groupversion",
			"apiVersion", apiVersion,
		)
		// A parse error means the input was malformed — don't pollute the cache
		// with what is effectively a permanent client-side error.
		return nil, err
	}
	gvk := schema.GroupVersionKind{
		Group:   gv.Group,
		Version: gv.Version,
		Kind:    kind,
	}

	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		l.Error(err, "Could not map GVK "+gvk.String())
		// Cache the negative result: missing CRDs aren't a transient condition.
		cache.Set(apiVersion, kind, namespace, name, nil, err)
		return nil, err
	}

	ns := namespace
	if mapping.Scope.Name() != meta.RESTScopeNameNamespace {
		ns = ""
	}

	ownerObj, err := client.Resource(mapping.Resource).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	// Only cache results we are confident won't change soon:
	//   - successful lookups (err == nil)
	//   - permanent NotFound (object truly doesn't exist; recreate will get a
	//     new resourceVersion and clear the cache through the TTL)
	// Other errors (timeouts, transient 5xx, throttling) are intentionally
	// not cached so the next admission retries against the apiserver instead
	// of repeatedly returning a stale failure.
	if err == nil || apierrors.IsNotFound(err) {
		cache.Set(apiVersion, kind, namespace, name, ownerObj, err)
	}
	if err != nil {
		return nil, err
	}
	return ownerObj, nil
}

func helmReleaseFromUnstructured(obj *unstructured.Unstructured) *helmv2.HelmRelease {
	if obj.GetAPIVersion() == HRAPIVersion && obj.GetKind() == HRKind {
		hr := &helmv2.HelmRelease{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, hr); err == nil {
			return hr
		}
	}
	return nil
}

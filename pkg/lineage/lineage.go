package lineage

import (
	"context"
	"fmt"
	"os"
	"strings"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
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

	// KubeVirt VirtualMachines created by the Cluster API KubeVirt provider
	// (CAPK) for cluster nodes carry no ownerReference: CAPK links them to
	// their KubevirtMachine through these labels only.
	VMAPIVersion                  = "kubevirt.io/v1"
	VMKind                        = "VirtualMachine"
	KubevirtMachineAPIVersion     = "infrastructure.cluster.x-k8s.io/v1alpha1"
	KubevirtMachineKind           = "KubevirtMachine"
	KubevirtMachineNameLabel      = "capk.cluster.x-k8s.io/kubevirt-machine-name"
	KubevirtMachineNamespaceLabel = "capk.cluster.x-k8s.io/kubevirt-machine-namespace"
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

func (o ObjectID) GetUnstructuredCached(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, cache *ObjectCache) (*unstructured.Unstructured, error) {
	return getUnstructuredObject(ctx, client, mapper, cache, o.APIVersion, o.Kind, o.Namespace, o.Name)
}

// walkState is what WalkOwnershipGraph passes to itself through its variadic
// memory argument, which also still accepts a bare visited map.
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
	memory ...any,
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

	// A node VM of a Kubernetes cluster has no ownerReference, so without this
	// step the walk would stop at it and never reach the cluster's application.
	if name, ok := kubevirtMachineName(obj); ok {
		ownerObj, err := getUnstructuredObject(ctx, client, mapper, state.cache, KubevirtMachineAPIVersion, KubevirtMachineKind, obj.GetNamespace(), name)
		if err != nil {
			l.Error(err, "couldn't get KubevirtMachine of VirtualMachine", "namespace", obj.GetNamespace(), "virtualMachine", obj.GetName(), "kubevirtMachine", name)
			return
		}
		out = append(out, WalkOwnershipGraph(ctx, client, mapper, appMapper, ownerObj, state)...)
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

// kubevirtMachineName returns the KubevirtMachine a CAPK-created
// VirtualMachine belongs to. It only follows a KubevirtMachine in the VM's own
// namespace: CAPK can also run against an external infrastructure cluster,
// where the KubevirtMachine lives elsewhere and is not ours to resolve.
func kubevirtMachineName(obj *unstructured.Unstructured) (string, bool) {
	if obj.GetAPIVersion() != VMAPIVersion || obj.GetKind() != VMKind {
		return "", false
	}
	labels := obj.GetLabels()
	name := labels[KubevirtMachineNameLabel]
	if name == "" || labels[KubevirtMachineNamespaceLabel] != obj.GetNamespace() {
		return "", false
	}
	return name, true
}

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

// WalkOwnershipGraphWithCache is WalkOwnershipGraph with every owner GET going
// through cache, which may be nil.
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
	if cached, ok := cache.Get(apiVersion, kind, namespace, name); ok {
		return cached, nil
	}

	l := log.FromContext(ctx)
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		l.Error(
			err, "failed to parse groupversion",
			"apiVersion", apiVersion,
		)
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
		return nil, err
	}

	ns := namespace
	if mapping.Scope.Name() != meta.RESTScopeNameNamespace {
		ns = ""
	}

	ownerObj, err := client.Resource(mapping.Resource).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	cache.Set(apiVersion, kind, namespace, name, ownerObj)
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

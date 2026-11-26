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
	u, err := getUnstructuredObject(ctx, client, mapper, o.APIVersion, o.Kind, o.Namespace, o.Name)
	if err != nil {
		return nil, err
	}
	return u, nil
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

	l.Info("processing object", "apiVersion", obj.GetAPIVersion(), "kind", obj.GetKind(), "name", obj.GetName())
	var visited map[ObjectID]bool
	var ok bool
	if len(memory) == 1 {
		visited, ok = memory[0].(map[ObjectID]bool)
		if !ok {
			l.Error(
				fmt.Errorf("invalid argument"), "could not parse visited map in WalkOwnershipGraph call",
				"received", memory[0], "expected", "map[ObjectID]bool",
			)
			return out
		}
	}

	if len(memory) == 0 {
		visited = make(map[ObjectID]bool)
	}

	if len(memory) != 0 && len(memory) != 1 {
		l.Error(
			fmt.Errorf("invalid argument count"), "could not parse variadic arguments to WalkOwnershipGraph",
			"args passed", len(memory)+5, "expected args", "4|5",
		)
		return out
	}

	if visited[id] {
		return out
	}

	visited[id] = true

	ownerRefs := obj.GetOwnerReferences()
	for _, owner := range ownerRefs {
		ownerObj, err := getUnstructuredObject(ctx, client, mapper, owner.APIVersion, owner.Kind, obj.GetNamespace(), owner.Name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Could not fetch owner %s/%s (%s): %v\n", obj.GetNamespace(), owner.Name, owner.Kind, err)
			continue
		}

		out = append(out, WalkOwnershipGraph(ctx, client, mapper, appMapper, ownerObj, visited)...)
	}

	// if object has owners, it couldn't be owned directly by the custom app
	if len(ownerRefs) > 0 {
		return
	}

	// A node VM of a Kubernetes cluster has no ownerReference, so without this
	// step the walk would stop at it and never reach the cluster's application.
	if name, ok := kubevirtMachineName(obj); ok {
		ownerObj, err := getUnstructuredObject(ctx, client, mapper, KubevirtMachineAPIVersion, KubevirtMachineKind, obj.GetNamespace(), name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Could not fetch KubevirtMachine %s/%s of VirtualMachine %s: %v\n", obj.GetNamespace(), name, obj.GetName(), err)
			return
		}
		out = append(out, WalkOwnershipGraph(ctx, client, mapper, appMapper, ownerObj, visited)...)
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
		ownerObj, err := getUnstructuredObject(ctx, client, mapper, a, k, obj.GetNamespace(), strings.TrimPrefix(obj.GetName(), p))
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
	ownerObj, err := getUnstructuredObject(ctx, client, mapper, HRAPIVersion, HRKind, obj.GetNamespace(), name)
	if err != nil {
		return
	}
	out = append(out, WalkOwnershipGraph(ctx, client, mapper, appMapper, ownerObj, visited)...)

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

func getUnstructuredObject(
	ctx context.Context,
	client dynamic.Interface,
	mapper meta.RESTMapper,
	apiVersion, kind, namespace, name string,
) (*unstructured.Unstructured, error) {
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

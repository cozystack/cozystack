// SPDX-License-Identifier: Apache-2.0
// TenantNamespace registry: read-only view over core Namespaces whose names
// start with “tenant-”.

package tenantnamespace

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metainternal "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
)

const (
	coreNSGroup   = ""
	coreNSVersion = "v1"
	coreNSRes     = "namespaces"
	prefix        = "tenant-"
	singularName  = "tenantnamespace"
)

// Verify interface conformance.
var (
	_ rest.Lister               = &REST{}
	_ rest.Getter               = &REST{}
	_ rest.Scoper               = &REST{}
	_ rest.Watcher              = &REST{}
	_ rest.TableConvertor       = &REST{}
	_ rest.Storage              = &REST{}
	_ rest.SingularNameProvider = &REST{}
)

// REST provides read-only storage over Namespaces.
type REST struct {
	dynamic dynamic.Interface
	gvr     schema.GroupVersionResource
}

func NewREST(dynamicClient dynamic.Interface) *REST {
	return &REST{
		dynamic: dynamicClient,
		gvr: schema.GroupVersionResource{
			Group:    corev1alpha1.GroupName,
			Version:  "v1alpha1",
			Resource: "tenantnamespaces",
		},
	}
}

// -----------------------------------------------------------------------------
// rest.Scoper
// -----------------------------------------------------------------------------

func (r *REST) NamespaceScoped() bool { return false }

// -----------------------------------------------------------------------------
// Object & name helpers
// -----------------------------------------------------------------------------

func (r *REST) New() runtime.Object     { return &corev1alpha1.TenantNamespace{} }
func (r *REST) NewList() runtime.Object { return &corev1alpha1.TenantNamespaceList{} }
func (r *REST) Kind() string            { return "TenantNamespace" }
func (r *REST) GroupVersionKind(_ schema.GroupVersion) schema.GroupVersionKind {
	return r.gvr.GroupVersion().WithKind("TenantNamespace")
}
func (r *REST) GetSingularName() string { return singularName }

// -----------------------------------------------------------------------------
// Lister / Getter
// -----------------------------------------------------------------------------

func (r *REST) List(ctx context.Context, _ *metainternal.ListOptions) (runtime.Object, error) {
	klog.V(6).Info("Listing tenant namespaces")

	nsList, err := r.dynamic.Resource(schema.GroupVersionResource{
		Group:    coreNSGroup,
		Version:  coreNSVersion,
		Resource: coreNSRes,
	}).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	out := &corev1alpha1.TenantNamespaceList{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "core.cozystack.io/v1alpha1",
			Kind:       "TenantNamespaceList",
		},
		ListMeta: metav1.ListMeta{
			ResourceVersion: nsList.GetResourceVersion(),
		},
	}

	for _, u := range nsList.Items {
		if !strings.HasPrefix(u.GetName(), prefix) {
			continue
		}
		out.Items = append(out.Items, corev1alpha1.TenantNamespace{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "core.cozystack.io/v1alpha1",
				Kind:       "TenantNamespace",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:              u.GetName(),
				UID:               u.GetUID(),
				ResourceVersion:   u.GetResourceVersion(),
				CreationTimestamp: u.GetCreationTimestamp(),
				Labels:            u.GetLabels(),
				Annotations:       u.GetAnnotations(),
			},
		})
	}

	return out, nil
}

func (r *REST) Get(ctx context.Context, name string, opts *metav1.GetOptions) (runtime.Object, error) {
	if !strings.HasPrefix(name, prefix) {
		return nil, apierrors.NewNotFound(r.gvr.GroupResource(), name)
	}

	u, err := r.dynamic.Resource(schema.GroupVersionResource{
		Group:    coreNSGroup,
		Version:  coreNSVersion,
		Resource: coreNSRes,
	}).Get(ctx, name, *opts)
	if err != nil {
		return nil, err
	}

	return &corev1alpha1.TenantNamespace{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "core.cozystack.io/v1alpha1",
			Kind:       "TenantNamespace",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:              u.GetName(),
			UID:               u.GetUID(),
			ResourceVersion:   u.GetResourceVersion(),
			CreationTimestamp: u.GetCreationTimestamp(),
			Labels:            u.GetLabels(),
			Annotations:       u.GetAnnotations(),
		},
	}, nil
}

// -----------------------------------------------------------------------------
// Watcher
// -----------------------------------------------------------------------------

func (r *REST) Watch(ctx context.Context, opts *metainternal.ListOptions) (watch.Interface, error) {
	nsWatch, err := r.dynamic.Resource(schema.GroupVersionResource{
		Group:    coreNSGroup,
		Version:  coreNSVersion,
		Resource: coreNSRes,
	}).Watch(ctx, metav1.ListOptions{
		ResourceVersion: opts.ResourceVersion,
		Watch:           true,
	})
	if err != nil {
		return nil, err
	}

	tenantWatch := watch.Filter(nsWatch, func(e watch.Event) (watch.Event, bool) {
		acc, err := meta.Accessor(e.Object)
		if err != nil {
			return e, false
		}
		return e, strings.HasPrefix(acc.GetName(), prefix)
	})

	return tenantWatch, nil
}

// -----------------------------------------------------------------------------
// TableConvertor
// -----------------------------------------------------------------------------

func (r *REST) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	now := time.Now()
	build := func(o runtime.Object, name string, created time.Time) metav1.TableRow {
		return metav1.TableRow{
			Cells:  []interface{}{name, duration.HumanDuration(now.Sub(created))},
			Object: runtime.RawExtension{Object: o}, // ← важно
		}
	}

	table := &metav1.Table{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "meta.k8s.io/v1",
			Kind:       "Table",
		},
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "NAME", Type: "string"},
			{Name: "AGE", Type: "string"},
		},
	}

	switch v := obj.(type) {

	case *corev1alpha1.TenantNamespaceList:
		for i := range v.Items {
			ns := &v.Items[i]
			table.Rows = append(table.Rows, build(ns, ns.Name, ns.CreationTimestamp.Time))
		}

	case *corev1alpha1.TenantNamespace:
		table.Rows = append(table.Rows, build(v, v.Name, v.CreationTimestamp.Time))

	case *unstructured.UnstructuredList:
		for i := range v.Items {
			it := &v.Items[i]
			table.Rows = append(table.Rows, build(it, it.GetName(), it.GetCreationTimestamp().Time))
		}

	case *unstructured.Unstructured:
		table.Rows = append(table.Rows, build(v, v.GetName(), v.GetCreationTimestamp().Time))

	default:
		return nil, errNotAcceptable{
			resource: r.gvr.GroupResource(),
			message:  fmt.Sprintf("unexpected object type %T", obj),
		}
	}

	return table, nil
}

// -----------------------------------------------------------------------------
// Destroy — satisfy rest.Storage; nothing to clean up.
// -----------------------------------------------------------------------------

func (r *REST) Destroy() {}

// -----------------------------------------------------------------------------
// Local “NotAcceptable” error helper.
// -----------------------------------------------------------------------------

type errNotAcceptable struct {
	resource schema.GroupResource
	message  string
}

func (e errNotAcceptable) Error() string { return e.message }

func (e errNotAcceptable) Status() metav1.Status {
	return metav1.Status{
		Status:  metav1.StatusFailure,
		Code:    http.StatusNotAcceptable,
		Reason:  metav1.StatusReason("NotAcceptable"),
		Message: e.Error(),
	}
}

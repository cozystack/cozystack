// SPDX-License-Identifier: Apache-2.0
// Copyright 2025 The Cozystack Authors.

// Package tap serves the read-only, cluster-scoped Tap resource that powers the
// dashboard marketplace view. Each Tap is computed on read from a PackageSource
// and the ApplicationDefinitions attributable to it, using the apiserver's
// privileged dynamic client, so a tenant browsing the catalog needs no direct
// access to those resources and never sees a pull credential.
package tap

import (
	"context"
	"fmt"
	"net/http"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternal "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

const singularName = "tap"

var (
	_ rest.Lister               = &REST{}
	_ rest.Getter               = &REST{}
	_ rest.Watcher              = &REST{}
	_ rest.TableConvertor       = &REST{}
	_ rest.Scoper               = &REST{}
	_ rest.SingularNameProvider = &REST{}
)

// REST implements the read-only Tap resource.
type REST struct {
	dyn dynamic.Interface
	gvr schema.GroupVersionResource
}

// NewREST builds the Tap REST storage from the apiserver's privileged dynamic
// client.
func NewREST(dyn dynamic.Interface) *REST {
	return &REST{
		dyn: dyn,
		gvr: schema.GroupVersionResource{
			Group:    corev1alpha1.GroupName,
			Version:  "v1alpha1",
			Resource: "taps",
		},
	}
}

func (*REST) NamespaceScoped() bool { return false }
func (*REST) New() runtime.Object   { return &corev1alpha1.Tap{} }
func (*REST) NewList() runtime.Object {
	return &corev1alpha1.TapList{}
}
func (*REST) Kind() string { return "Tap" }
func (r *REST) GroupVersionKind(_ schema.GroupVersion) schema.GroupVersionKind {
	return r.gvr.GroupVersion().WithKind("Tap")
}
func (*REST) GetSingularName() string { return singularName }
func (*REST) Destroy()                {}

// -----------------------------------------------------------------------------
// Lister / Getter
// -----------------------------------------------------------------------------

func (r *REST) List(ctx context.Context, _ *metainternal.ListOptions) (runtime.Object, error) {
	pss, err := r.fetchPackageSources(ctx)
	if err != nil {
		return nil, apierrors.NewInternalError(fmt.Errorf("list PackageSources: %w", err))
	}
	idx := r.appDefIndex(ctx)

	out := &corev1alpha1.TapList{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1alpha1.SchemeGroupVersion.String(),
			Kind:       "TapList",
		},
		ListMeta: metav1.ListMeta{ResourceVersion: "0"},
	}
	for _, ps := range pss {
		out.Items = append(out.Items, buildTap(ps, idx))
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Name < out.Items[j].Name })
	return out, nil
}

func (r *REST) Get(ctx context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	u, err := r.dyn.Resource(gvrPackageSources).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, apierrors.NewNotFound(r.gvr.GroupResource(), name)
		}
		return nil, apierrors.NewInternalError(fmt.Errorf("get PackageSource %q: %w", name, err))
	}
	var ps cozyv1alpha1.PackageSource
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &ps); err != nil {
		return nil, apierrors.NewInternalError(fmt.Errorf("decode PackageSource %q: %w", name, err))
	}
	idx := r.appDefIndex(ctx)
	tap := buildTap(ps, idx)
	return &tap, nil
}

// fetchPackageSources lists the cluster-scoped PackageSources as typed objects.
func (r *REST) fetchPackageSources(ctx context.Context) ([]cozyv1alpha1.PackageSource, error) {
	ul, err := r.dyn.Resource(gvrPackageSources).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]cozyv1alpha1.PackageSource, 0, len(ul.Items))
	for i := range ul.Items {
		var ps cozyv1alpha1.PackageSource
		if err := fromUnstructured(&ul.Items[i], &ps); err != nil {
			klog.V(2).InfoS("skipping undecodable PackageSource", "name", ul.Items[i].GetName(), "err", err)
			continue
		}
		out = append(out, ps)
	}
	return out, nil
}

// appDefIndex lists ApplicationDefinitions and indexes them by chartRef name. A
// list failure is tolerated (the catalog degrades to taps with no packages)
// rather than failing the whole marketplace view.
func (r *REST) appDefIndex(ctx context.Context) map[string]cozyv1alpha1.ApplicationDefinition {
	ul, err := r.dyn.Resource(gvrAppDefs).List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.V(2).InfoS("could not list ApplicationDefinitions for tap catalog", "err", err)
		return map[string]cozyv1alpha1.ApplicationDefinition{}
	}
	ads := make([]cozyv1alpha1.ApplicationDefinition, 0, len(ul.Items))
	for i := range ul.Items {
		var ad cozyv1alpha1.ApplicationDefinition
		if err := fromUnstructured(&ul.Items[i], &ad); err != nil {
			klog.V(2).InfoS("skipping undecodable ApplicationDefinition", "name", ul.Items[i].GetName(), "err", err)
			continue
		}
		ads = append(ads, ad)
	}
	return indexAppDefsByChartRef(ads)
}

func fromUnstructured(u *unstructured.Unstructured, target interface{}) error {
	return runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, target)
}

// -----------------------------------------------------------------------------
// Watcher (one-shot): emit the current state once, then block until the client
// or server cancels, mirroring the Option resource. Closing the channel early
// makes the apiserver treat the watch as ended and reconnect in a tight loop.
// -----------------------------------------------------------------------------

func (r *REST) Watch(ctx context.Context, opts *metainternal.ListOptions) (watch.Interface, error) {
	events := make(chan watch.Event)
	pw := watch.NewProxyWatcher(events)

	go func() {
		defer pw.Stop()
		listObj, err := r.List(ctx, opts)
		if err == nil {
			list := listObj.(*corev1alpha1.TapList)
			for i := range list.Items {
				select {
				case events <- watch.Event{Type: watch.Added, Object: &list.Items[i]}:
				case <-ctx.Done():
					return
				}
			}
		} else {
			klog.V(2).InfoS("tap watch: initial list failed", "err", err)
		}
		<-ctx.Done()
	}()

	return pw, nil
}

// -----------------------------------------------------------------------------
// TableConvertor
// -----------------------------------------------------------------------------

func (r *REST) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	row := func(t *corev1alpha1.Tap) metav1.TableRow {
		return metav1.TableRow{
			Cells:  []interface{}{t.Name, t.Spec.Source.Kind, t.Spec.Ready, len(t.Spec.Packages)},
			Object: runtime.RawExtension{Object: t},
		}
	}
	tbl := &metav1.Table{
		TypeMeta: metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "Table"},
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "NAME", Type: "string"},
			{Name: "SOURCE", Type: "string"},
			{Name: "READY", Type: "boolean"},
			{Name: "PACKAGES", Type: "integer"},
		},
	}
	switch v := obj.(type) {
	case *corev1alpha1.TapList:
		for i := range v.Items {
			tbl.Rows = append(tbl.Rows, row(&v.Items[i]))
		}
		tbl.ResourceVersion = v.ResourceVersion
	case *corev1alpha1.Tap:
		tbl.Rows = append(tbl.Rows, row(v))
		tbl.ResourceVersion = v.ResourceVersion
	default:
		return nil, notAcceptable{r.gvr.GroupResource(), fmt.Sprintf("unexpected %T", obj)}
	}
	return tbl, nil
}

type notAcceptable struct {
	resource schema.GroupResource
	message  string
}

func (e notAcceptable) Error() string { return e.message }
func (e notAcceptable) Status() metav1.Status {
	return metav1.Status{
		Status:  metav1.StatusFailure,
		Code:    http.StatusNotAcceptable,
		Reason:  metav1.StatusReason("NotAcceptable"),
		Message: e.message,
	}
}

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
	"strings"

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
	"github.com/cozystack/cozystack/internal/marketplace/tapconst"
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
	_ rest.GracefulDeleter      = &REST{}
	_ rest.Creater              = &REST{}
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

// -----------------------------------------------------------------------------
// Creater — connect a community tap (mirrors `cozypkg tap`)
//
// Create records the intent only: it creates the labeled Flux OCIRepository
// (with an optional pull-credential secretRef). The operator's tap materializer
// then pulls the artifact and creates the PackageSource(s), so the API never
// blocks a request on a registry pull. The returned Tap reflects that
// materialization is pending.
// -----------------------------------------------------------------------------

func (r *REST) Create(ctx context.Context, obj runtime.Object, createValidation rest.ValidateObjectFunc, _ *metav1.CreateOptions) (runtime.Object, error) {
	in, ok := obj.(*corev1alpha1.Tap)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a Tap object, got %T", obj))
	}
	if in.Spec.URL == "" {
		return nil, apierrors.NewBadRequest("spec.url is required to connect a tap")
	}
	target, err := parseConnectURL(in.Spec.URL, in.Spec.Tag)
	if err != nil {
		return nil, apierrors.NewBadRequest(err.Error())
	}
	if createValidation != nil {
		if err := createValidation(ctx, obj); err != nil {
			return nil, err
		}
	}

	repo := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"kind":       "OCIRepository",
		"metadata": map[string]interface{}{
			"name":        target.FluxSourceName,
			"namespace":   "cozy-system",
			"labels":      map[string]interface{}{tapconst.Label: "true"},
			"annotations": map[string]interface{}{tapconst.NameAnnotation: target.PackageSourceName},
		},
		"spec": map[string]interface{}{
			"url":      target.URL,
			"interval": "5m0s",
			"ref":      map[string]interface{}{"tag": target.Tag},
		},
	}}
	if in.Spec.SecretRef != "" {
		_ = unstructured.SetNestedMap(repo.Object, map[string]interface{}{"name": in.Spec.SecretRef}, "spec", "secretRef")
	}

	// Create the Flux source, idempotently: a repeat connect updates the
	// existing source (new tag/secret) rather than erroring.
	src := r.dyn.Resource(gvrOCIRepos).Namespace("cozy-system")
	if _, err := src.Create(ctx, repo, metav1.CreateOptions{FieldManager: "cozystack-api"}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, apierrors.NewInternalError(fmt.Errorf("create Flux source for tap %s: %w", target.PackageSourceName, err))
		}
		cur, gerr := src.Get(ctx, target.FluxSourceName, metav1.GetOptions{})
		if gerr != nil {
			return nil, apierrors.NewInternalError(fmt.Errorf("update Flux source for tap %s: %w", target.PackageSourceName, gerr))
		}
		// Refuse to silently retarget: two repositories that share an org/repo
		// path on different registry hosts derive the same name, so a blind
		// update would repoint the first at the second. Require a disconnect
		// first when the existing source points at a different URL.
		if curURL, _, _ := unstructured.NestedString(cur.Object, "spec", "url"); curURL != "" && curURL != target.URL {
			return nil, apierrors.NewConflict(r.gvr.GroupResource(), target.PackageSourceName,
				fmt.Errorf("a different repository (%s) is already connected as %q; disconnect it before connecting %s", curURL, target.PackageSourceName, target.URL))
		}
		// Update only the fields this API owns (spec, the tap label and name
		// annotation) on the FETCHED object, so the operator's finalizer and
		// materialized-revision annotation on the existing source survive.
		cur.Object["spec"] = repo.Object["spec"]
		labels := cur.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels[tapconst.Label] = "true"
		cur.SetLabels(labels)
		ann := cur.GetAnnotations()
		if ann == nil {
			ann = map[string]string{}
		}
		ann[tapconst.NameAnnotation] = target.PackageSourceName
		cur.SetAnnotations(ann)
		if _, err := src.Update(ctx, cur, metav1.UpdateOptions{FieldManager: "cozystack-api"}); err != nil {
			return nil, apierrors.NewInternalError(fmt.Errorf("update Flux source for tap %s: %w", target.PackageSourceName, err))
		}
	}

	// Return catalog metadata only: never the url or the secret reference.
	return &corev1alpha1.Tap{
		TypeMeta:   metav1.TypeMeta{APIVersion: corev1alpha1.SchemeGroupVersion.String(), Kind: "Tap"},
		ObjectMeta: metav1.ObjectMeta{Name: target.PackageSourceName, ResourceVersion: "0"},
		Spec: corev1alpha1.TapSpec{
			Source:    corev1alpha1.TapSource{Kind: "OCIRepository", Name: target.FluxSourceName},
			Community: true,
			Ready:     false,
			Message:   "connecting: waiting for the artifact to be pulled and materialized",
		},
	}, nil
}

// -----------------------------------------------------------------------------
// GracefulDeleter — disconnect a community tap (mirrors `cozypkg untap`)
// -----------------------------------------------------------------------------

func (r *REST) Delete(ctx context.Context, name string, deleteValidation rest.ValidateObjectFunc, _ *metav1.DeleteOptions) (runtime.Object, bool, error) {
	if !strings.HasPrefix(name, tapconst.Prefix) {
		return nil, false, apierrors.NewForbidden(r.gvr.GroupResource(), name,
			fmt.Errorf("only %s* taps can be disconnected; official sources are protected", tapconst.Prefix))
	}

	u, err := r.dyn.Resource(gvrPackageSources).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// A tap connected but not yet (or never) materialized has an
			// OCIRepository but no PackageSource. Fall back to removing that
			// source so a failed or pending connect is still recoverable from
			// the dashboard rather than orphaning a finalized OCIRepository.
			return r.deleteOrphanTapSource(ctx, name)
		}
		return nil, false, apierrors.NewInternalError(fmt.Errorf("get PackageSource %q: %w", name, err))
	}
	var ps cozyv1alpha1.PackageSource
	if err := fromUnstructured(u, &ps); err != nil {
		return nil, false, apierrors.NewInternalError(fmt.Errorf("decode PackageSource %q: %w", name, err))
	}

	tap := buildTap(ps, r.appDefIndex(ctx))
	if deleteValidation != nil {
		if err := deleteValidation(ctx, &tap); err != nil {
			return nil, false, err
		}
	}

	if err := r.dyn.Resource(gvrPackageSources).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		return nil, false, apierrors.NewInternalError(fmt.Errorf("delete PackageSource %q: %w", name, err))
	}

	// Remove the Flux source too, but only when no other PackageSource still
	// references it. Installed Packages are intentionally left untouched.
	if ref := ps.Spec.SourceRef; ref != nil && ref.Kind == "OCIRepository" && ref.Name != "" {
		others, err := r.fetchPackageSources(ctx)
		if err == nil && !anyOtherReferences(others, ref.Name, name) {
			ns := ref.Namespace
			if ns == "" {
				ns = "cozy-system"
			}
			if err := r.dyn.Resource(gvrOCIRepos).Namespace(ns).Delete(ctx, ref.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				klog.V(2).InfoS("tap disconnected but its OCIRepository could not be deleted", "source", ref.Name, "err", err)
			}
		}
	}

	return &tap, true, nil
}

// deleteOrphanTapSource removes a tap whose OCIRepository exists but whose
// PackageSource has not been materialized. It matches the labeled OCIRepository
// carrying the tap-name annotation and deletes it.
func (r *REST) deleteOrphanTapSource(ctx context.Context, name string) (runtime.Object, bool, error) {
	list, err := r.dyn.Resource(gvrOCIRepos).Namespace("cozy-system").List(ctx, metav1.ListOptions{
		LabelSelector: tapconst.Label + "=true",
	})
	if err != nil {
		return nil, false, apierrors.NewNotFound(r.gvr.GroupResource(), name)
	}
	for i := range list.Items {
		item := &list.Items[i]
		if item.GetAnnotations()[tapconst.NameAnnotation] != name {
			continue
		}
		if err := r.dyn.Resource(gvrOCIRepos).Namespace("cozy-system").Delete(ctx, item.GetName(), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return nil, false, apierrors.NewInternalError(fmt.Errorf("delete tap source %q: %w", item.GetName(), err))
		}
		tap := &corev1alpha1.Tap{
			TypeMeta:   metav1.TypeMeta{APIVersion: corev1alpha1.SchemeGroupVersion.String(), Kind: "Tap"},
			ObjectMeta: metav1.ObjectMeta{Name: name, ResourceVersion: "0"},
			Spec:       corev1alpha1.TapSpec{Community: true},
		}
		return tap, true, nil
	}
	return nil, false, apierrors.NewNotFound(r.gvr.GroupResource(), name)
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

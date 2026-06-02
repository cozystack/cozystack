// SPDX-License-Identifier: Apache-2.0
// Option registry: a read-only, virtual resource that serves named lists of
// dropdown options for the dashboard. Each object (one per source name) is
// computed on read by a provider using the apiserver's privileged client, so
// tenants need no direct access to the underlying cluster resources.

package option

import (
	"context"
	"fmt"
	"net/http"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternal "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"

	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

const singularName = "option"

var (
	_ rest.Lister               = &REST{}
	_ rest.Getter               = &REST{}
	_ rest.Watcher              = &REST{}
	_ rest.TableConvertor       = &REST{}
	_ rest.Scoper               = &REST{}
	_ rest.SingularNameProvider = &REST{}
)

// REST implements the read-only Option resource.
type REST struct {
	providers map[string]providerFunc
	gvr       schema.GroupVersionResource
}

// NewREST builds the Option REST storage from a provider registry.
func NewREST(providers map[string]providerFunc) *REST {
	return &REST{
		providers: providers,
		gvr: schema.GroupVersionResource{
			Group:    corev1alpha1.GroupName,
			Version:  "v1alpha1",
			Resource: "options",
		},
	}
}

// -----------------------------------------------------------------------------
// Basic meta
// -----------------------------------------------------------------------------

func (*REST) NamespaceScoped() bool   { return true }
func (*REST) New() runtime.Object     { return &corev1alpha1.Option{} }
func (*REST) NewList() runtime.Object { return &corev1alpha1.OptionList{} }
func (*REST) Kind() string            { return "Option" }
func (r *REST) GroupVersionKind(_ schema.GroupVersion) schema.GroupVersionKind {
	return r.gvr.GroupVersion().WithKind("Option")
}
func (*REST) GetSingularName() string { return singularName }
func (*REST) Destroy()                {}

// -----------------------------------------------------------------------------
// Lister / Getter
// -----------------------------------------------------------------------------

func (r *REST) List(ctx context.Context, _ *metainternal.ListOptions) (runtime.Object, error) {
	ns, _ := request.NamespaceFrom(ctx)

	out := &corev1alpha1.OptionList{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1alpha1.SchemeGroupVersion.String(),
			Kind:       "OptionList",
		},
		ListMeta: metav1.ListMeta{ResourceVersion: "0"},
	}

	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		items, err := r.providers[name](ctx, ns)
		if err != nil {
			// Tolerate a single failing source (e.g. an optional CRD that is
			// not installed) so the rest of the dropdowns keep working.
			logProviderError(name, err)
			continue
		}
		out.Items = append(out.Items, r.makeOption(name, ns, items))
	}
	return out, nil
}

func (r *REST) Get(ctx context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	provider, ok := r.providers[name]
	if !ok {
		return nil, apierrors.NewNotFound(r.gvr.GroupResource(), name)
	}
	ns, _ := request.NamespaceFrom(ctx)
	items, err := provider(ctx, ns)
	if err != nil {
		return nil, apierrors.NewInternalError(fmt.Errorf("compute options for %q: %w", name, err))
	}
	opt := r.makeOption(name, ns, items)
	return &opt, nil
}

func (r *REST) makeOption(name, namespace string, items []corev1alpha1.OptionItem) corev1alpha1.Option {
	return corev1alpha1.Option{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1alpha1.SchemeGroupVersion.String(),
			Kind:       "Option",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       namespace,
			ResourceVersion: "0",
		},
		Spec: corev1alpha1.OptionSpec{Items: items},
	}
}

// -----------------------------------------------------------------------------
// Watcher (one-shot): emit the current state once, then block until the client
// or server cancels. There is no upstream object to relay; closing the channel
// early would make the apiserver treat the watch as ended and trigger a
// reconnect storm.
// -----------------------------------------------------------------------------

func (r *REST) Watch(ctx context.Context, opts *metainternal.ListOptions) (watch.Interface, error) {
	events := make(chan watch.Event)
	pw := watch.NewProxyWatcher(events)

	go func() {
		defer pw.Stop()
		listObj, err := r.List(ctx, opts)
		if err == nil {
			list := listObj.(*corev1alpha1.OptionList)
			for i := range list.Items {
				select {
				case events <- watch.Event{Type: watch.Added, Object: &list.Items[i]}:
				case <-ctx.Done():
					return
				}
			}
		} else {
			// One-shot watch: log so an operator can tell "no options" apart
			// from "every provider failed" (e.g. broken RBAC) instead of seeing
			// a silently empty stream.
			logProviderError("watch", err)
		}
		<-ctx.Done()
	}()

	return pw, nil
}

// -----------------------------------------------------------------------------
// TableConvertor
// -----------------------------------------------------------------------------

func (r *REST) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	row := func(o *corev1alpha1.Option) metav1.TableRow {
		return metav1.TableRow{
			Cells:  []interface{}{o.Name, len(o.Spec.Items)},
			Object: runtime.RawExtension{Object: o},
		}
	}
	tbl := &metav1.Table{
		TypeMeta: metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "Table"},
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "NAME", Type: "string"},
			{Name: "ITEMS", Type: "integer"},
		},
	}
	switch v := obj.(type) {
	case *corev1alpha1.OptionList:
		for i := range v.Items {
			tbl.Rows = append(tbl.Rows, row(&v.Items[i]))
		}
		tbl.ResourceVersion = v.ResourceVersion
	case *corev1alpha1.Option:
		tbl.Rows = append(tbl.Rows, row(v))
		tbl.ResourceVersion = v.ResourceVersion
	default:
		return nil, notAcceptable{r.gvr.GroupResource(), fmt.Sprintf("unexpected %T", obj)}
	}
	return tbl, nil
}

// -----------------------------------------------------------------------------
// Helpers / boiler-plate
// -----------------------------------------------------------------------------

// listOpts returns the standard list options used by providers.
func listOpts() metav1.ListOptions {
	return metav1.ListOptions{}
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

// SPDX-License-Identifier: Apache-2.0
// TenantNamespace registry: read-only view over Namespaces whose names start
// with “tenant-”.

package tenantnamespace

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternal "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	rbacv1client "k8s.io/client-go/kubernetes/typed/rbac/v1"

	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

const (
	prefix       = "tenant-"
	singularName = "tenantnamespace"
)

// -----------------------------------------------------------------------------
// REST storage
// -----------------------------------------------------------------------------

var (
	_ rest.Lister               = &REST{}
	_ rest.Getter               = &REST{}
	_ rest.Watcher              = &REST{}
	_ rest.TableConvertor       = &REST{}
	_ rest.Scoper               = &REST{}
	_ rest.SingularNameProvider = &REST{}
)

type REST struct {
	core corev1client.CoreV1Interface
	rbac rbacv1client.RbacV1Interface
	gvr  schema.GroupVersionResource
}

func NewREST(
	coreCli corev1client.CoreV1Interface,
	rbacCli rbacv1client.RbacV1Interface,
) *REST {
	return &REST{
		core: coreCli,
		rbac: rbacCli,
		gvr: schema.GroupVersionResource{
			Group:    corev1alpha1.GroupName,
			Version:  "v1alpha1",
			Resource: "tenantnamespaces",
		},
	}
}

// -----------------------------------------------------------------------------
// Basic meta
// -----------------------------------------------------------------------------

func (*REST) NamespaceScoped() bool { return false }
func (*REST) New() runtime.Object   { return &corev1alpha1.TenantNamespace{} }
func (*REST) NewList() runtime.Object {
	return &corev1alpha1.TenantNamespaceList{}
}
func (*REST) Kind() string { return "TenantNamespace" }
func (r *REST) GroupVersionKind(_ schema.GroupVersion) schema.GroupVersionKind {
	return r.gvr.GroupVersion().WithKind("TenantNamespace")
}
func (*REST) GetSingularName() string { return singularName }

// -----------------------------------------------------------------------------
// Lister / Getter
// -----------------------------------------------------------------------------

func (r *REST) List(
	ctx context.Context,
	_ *metainternal.ListOptions,
) (runtime.Object, error) {
	nsList, err := r.core.Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var tenantNames []string
	for i := range nsList.Items {
		if strings.HasPrefix(nsList.Items[i].Name, prefix) {
			tenantNames = append(tenantNames, nsList.Items[i].Name)
		}
	}

	allowed, err := r.filterAccessible(ctx, tenantNames)
	if err != nil {
		return nil, err
	}

	return r.makeList(nsList, allowed), nil
}

func (r *REST) Get(
	ctx context.Context,
	name string,
	opts *metav1.GetOptions,
) (runtime.Object, error) {
	if !strings.HasPrefix(name, prefix) {
		return nil, apierrors.NewNotFound(r.gvr.GroupResource(), name)
	}

	ns, err := r.core.Namespaces().Get(ctx, name, *opts)
	if err != nil {
		return nil, err
	}

	return &corev1alpha1.TenantNamespace{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1alpha1.SchemeGroupVersion.String(),
			Kind:       "TenantNamespace",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:              ns.Name,
			UID:               ns.UID,
			ResourceVersion:   ns.ResourceVersion,
			CreationTimestamp: ns.CreationTimestamp,
			Labels:            ns.Labels,
			Annotations:       ns.Annotations,
		},
	}, nil
}

// -----------------------------------------------------------------------------
// Watcher
// -----------------------------------------------------------------------------

func (r *REST) Watch(ctx context.Context, opts *metainternal.ListOptions) (watch.Interface, error) {
	nsWatch, err := r.core.Namespaces().Watch(ctx, metav1.ListOptions{
		Watch:           true,
		ResourceVersion: opts.ResourceVersion,
	})
	if err != nil {
		return nil, err
	}

	events := make(chan watch.Event)
	pw := watch.NewProxyWatcher(events)

	go func() {
		defer pw.Stop()
		for ev := range nsWatch.ResultChan() {
			ns, ok := ev.Object.(*corev1.Namespace)
			if !ok || !strings.HasPrefix(ns.Name, prefix) {
				continue
			}
			out := &corev1alpha1.TenantNamespace{
				TypeMeta: metav1.TypeMeta{
					APIVersion: corev1alpha1.SchemeGroupVersion.String(),
					Kind:       "TenantNamespace",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:              ns.Name,
					UID:               ns.UID,
					ResourceVersion:   ns.ResourceVersion,
					CreationTimestamp: ns.CreationTimestamp,
					Labels:            ns.Labels,
					Annotations:       ns.Annotations,
				},
			}
			events <- watch.Event{Type: ev.Type, Object: out}
		}
	}()

	return pw, nil
}

// -----------------------------------------------------------------------------
// TableConvertor
// -----------------------------------------------------------------------------

func (r *REST) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	now := time.Now()
	row := func(o *corev1alpha1.TenantNamespace) metav1.TableRow {
		return metav1.TableRow{
			Cells:  []interface{}{o.Name, duration.HumanDuration(now.Sub(o.CreationTimestamp.Time))},
			Object: runtime.RawExtension{Object: o},
		}
	}

	tbl := &metav1.Table{
		TypeMeta: metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "Table"},
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "NAME", Type: "string"},
			{Name: "AGE", Type: "string"},
		},
	}

	switch v := obj.(type) {
	case *corev1alpha1.TenantNamespaceList:
		for i := range v.Items {
			tbl.Rows = append(tbl.Rows, row(&v.Items[i]))
		}
		tbl.ListMeta.ResourceVersion = v.ListMeta.ResourceVersion
	case *corev1alpha1.TenantNamespace:
		tbl.Rows = append(tbl.Rows, row(v))
		tbl.ListMeta.ResourceVersion = v.ResourceVersion
	default:
		return nil, notAcceptable{r.gvr.GroupResource(), fmt.Sprintf("unexpected %T", obj)}
	}
	return tbl, nil
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func (r *REST) makeList(src *corev1.NamespaceList, allowed []string) *corev1alpha1.TenantNamespaceList {
	set := map[string]struct{}{}
	for _, n := range allowed {
		set[n] = struct{}{}
	}

	out := &corev1alpha1.TenantNamespaceList{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1alpha1.SchemeGroupVersion.String(),
			Kind:       "TenantNamespaceList",
		},
		ListMeta: metav1.ListMeta{ResourceVersion: src.ResourceVersion},
	}

	for i := range src.Items {
		ns := &src.Items[i]
		if _, ok := set[ns.Name]; !ok {
			continue
		}
		out.Items = append(out.Items, corev1alpha1.TenantNamespace{
			TypeMeta: metav1.TypeMeta{
				APIVersion: corev1alpha1.SchemeGroupVersion.String(),
				Kind:       "TenantNamespace",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:              ns.Name,
				UID:               ns.UID,
				ResourceVersion:   ns.ResourceVersion,
				CreationTimestamp: ns.CreationTimestamp,
				Labels:            ns.Labels,
				Annotations:       ns.Annotations,
			},
		})
	}
	return out
}

func (r *REST) filterAccessible(
	ctx context.Context,
	names []string,
) ([]string, error) {
	u, ok := request.UserFrom(ctx)
	if !ok {
		return []string{}, fmt.Errorf("user missing in context")
	}
	groups := make(map[string]struct{})
	for _, group := range u.GetGroups() {
		groups[group] = struct{}{}
	}
	if _, ok = groups["cozystack-cluster-admin"]; ok {
		return names, nil
	}
	nameSet := make(map[string]struct{})
	for _, name := range names {
		nameSet[name] = struct{}{}
	}
	rbs, err := r.rbac.RoleBindings("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return []string{}, fmt.Errorf("failed to list rolebindings")
	}
	allowedNameSet := make(map[string]struct{})
	for i := range rbs.Items {
		if _, ok := allowedNameSet[rbs.Items[i].Namespace]; ok {
			continue
		}
		if _, ok := nameSet[rbs.Items[i].Namespace]; !ok {
			continue
		}
		for j := range rbs.Items[i].Subjects {
			if rbs.Items[i].Subjects[j].Kind != "Group" {
				continue
			}
			if _, ok = groups[rbs.Items[i].Subjects[j].Name]; ok {
				allowedNameSet[rbs.Items[i].Namespace] = struct{}{}
				break
			}
		}
	}
	allowed := make([]string, 0, len(allowedNameSet))
	for name := range allowedNameSet {
		allowed = append(allowed, name)
	}
	return allowed, nil
}

// -----------------------------------------------------------------------------
// Boiler-plate
// -----------------------------------------------------------------------------

func (*REST) Destroy() {}

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

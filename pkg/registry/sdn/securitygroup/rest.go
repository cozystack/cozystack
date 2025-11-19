// SPDX-License-Identifier: Apache-2.0
// TenantSecret registry – namespaced view over Secrets labelled
// "internal.cozystack.io/tenantresource=true".  Internal tenant secret labels are hidden.

package securitygroup

import (
	"context"
	"fmt"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternal "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ciliumv2 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2"
	sdnv1alpha1 "github.com/cozystack/cozystack/pkg/apis/sdn/v1alpha1"
)

// -----------------------------------------------------------------------------
// Constants & helpers
// -----------------------------------------------------------------------------

func fromCiliumNetworkPolicy(np ciliumv2.CiliumNetworkPolicy) *sdnv1alpha1.SecurityGroup {
	return &sdnv1alpha1.SecurityGroup{
		TypeMeta: metav1.TypeMeta{
			APIVersion: sdnv1alpha1.SchemeGroupVersion.String(),
			Kind:       sdnv1alpha1.SecurityGroupKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:              np.Name,
			Namespace:         np.Namespace,
			UID:               np.UID,
			ResourceVersion:   np.ResourceVersion,
			CreationTimestamp: np.CreationTimestamp,
			Labels:            np.Labels,
			Annotations:       np.Annotations,
		},
	}
}

func toCiliumNetworkPolicy(sg *sdnv1alpha1.SecurityGroup, np *ciliumv2.CiliumNetworkPolicy) *ciliumv2.CiliumNetworkPolicy {
	var out ciliumv2.CiliumNetworkPolicy
	if np != nil {
		out = *np.DeepCopy()
	}
	out.TypeMeta = metav1.TypeMeta{
		APIVersion: ciliumv2.SchemeGroupVersion.String(),
		Kind:       ciliumv2.CNPKindDefinition,
	}
	out.Name, out.Namespace = np.Name, np.Namespace

	if out.Labels == nil {
		out.Labels = map[string]string{}
	}
	for k, v := range sg.Labels {
		out.Labels[k] = v
	}

	if out.Annotations == nil {
		out.Annotations = map[string]string{}
	}
	for k, v := range sg.Annotations {
		out.Annotations[k] = v
	}

	return &out
}

// -----------------------------------------------------------------------------
// REST storage
// -----------------------------------------------------------------------------

var (
	_ rest.Creater              = &REST{}
	_ rest.Getter               = &REST{}
	_ rest.Lister               = &REST{}
	_ rest.Updater              = &REST{}
	_ rest.Patcher              = &REST{}
	_ rest.GracefulDeleter      = &REST{}
	_ rest.Watcher              = &REST{}
	_ rest.TableConvertor       = &REST{}
	_ rest.Scoper               = &REST{}
	_ rest.SingularNameProvider = &REST{}
)

type REST struct {
	c client.Client
	w client.WithWatch
}

func NewREST(c client.Client, w client.WithWatch) *REST {
	return &REST{
		c: c,
		w: w,
	}
}

// -----------------------------------------------------------------------------
// Basic meta
// -----------------------------------------------------------------------------

func (*REST) NamespaceScoped() bool   { return true }
func (*REST) New() runtime.Object     { return &sdnv1alpha1.SecurityGroup{} }
func (*REST) NewList() runtime.Object { return &sdnv1alpha1.SecurityGroupList{} }
func (*REST) Kind() string            { return singularKind }
func (r *REST) GroupVersionKind(_ schema.GroupVersion) schema.GroupVersionKind {
	return sdnv1alpha1.SchemeGroupVersion.WithKind(sdnv1alpha1.SecurityGroupKind)
}
func (*REST) GetSingularName() string { return sdnv1alpha1.SecurityGroupSingularName }

// -----------------------------------------------------------------------------
// CRUD
// -----------------------------------------------------------------------------

func (r *REST) Create(
	ctx context.Context,
	obj runtime.Object,
	_ rest.ValidateObjectFunc,
	opts *metav1.CreateOptions,
) (runtime.Object, error) {
	in, ok := obj.(*sdnv1alpha1.SecurityGroup)
	if !ok {
		return nil, fmt.Errorf("expected SecurityGroup, got %T", obj)
	}

	np := toCiliumNetworkPolicy(in, nil)
	err := r.c.Create(ctx, np, &client.CreateOptions{Raw: opts})
	if err != nil {
		return nil, err
	}
	return fromCiliumNetworkPolicy(np), nil
}

func (r *REST) Get(
	ctx context.Context,
	name string,
	opts *metav1.GetOptions,
) (runtime.Object, error) {
	ns, ok := request.NamespaceFrom(ctx)
	if !ok {
		return nil, apierrors.NewBadRequest("namespace required")
	}
	np := &ciliumv2.CiliumNetworkPolicy{}
	err := r.c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, np, &client.GetOptions{Raw: opts})
	if err != nil {
		return nil, err
	}
	return fromCiliumNetworkPolicy(np), nil
}

func (r *REST) List(ctx context.Context, opts *metainternal.ListOptions) (runtime.Object, error) {
	ns, ok := request.NamespaceFrom(ctx)
	if !ok {
		return nil, apierrors.NewBadRequest("namespace required")
	}

	list := &ciliumv2.CiliumNetworkPolicyList{}
	err := r.c.List(ctx, list,
		&client.ListOptions{
			Namespace: ns,
			Raw:       &metav1.ListOptions{},
		},
	)
	if err != nil {
		return nil, err
	}

	out := &sdnv1alpha1.SecurityGroupList{
		TypeMeta: metav1.TypeMeta{
			APIVersion: sdnv1alpha1.SchemeGroupVersion.String(),
			Kind:       sdnv1alpha1.SecurityGroupListKind,
		},
		ListMeta: list.ListMeta,
	}

	for i := range list.Items {
		out.Items = append(out.Items, *fromCiliumNetworkPolicy(&list.Items[i]))
	}
	return out, nil
}

func (r *REST) Update(
	ctx context.Context,
	name string,
	objInfo rest.UpdatedObjectInfo,
	_ rest.ValidateObjectFunc,
	_ rest.ValidateObjectUpdateFunc,
	forceCreate bool,
	opts *metav1.UpdateOptions,
) (runtime.Object, bool, error) {
	ns, ok := request.NamespaceFrom(ctx)
	if !ok {
		return nil, false, apierrors.NewBadRequest("namespace required")
	}

	cur := &ciliumv2.CiliumNetworkPolicy{}
	err := r.c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, cur, &client.GetOptions{Raw: &metav1.GetOptions{}})
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, false, err
	}

	newObj, err := objInfo.UpdatedObject(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	in := newObj.(*sdnv1alpha1.SecurityGroup)

	newNp := toCiliumNetworkPolicy(in, cur)
	newNp.Namespace = ns
	if cur == nil {
		if !forceCreate && err == nil {
			return nil, false, apierrors.NewNotFound(sdnv1alpha1.Resource(sdnv1alpha1.SecurityGroupPluralName), name)
		}
		err := r.c.Create(ctx, newNp, &client.CreateOptions{Raw: &metav1.CreateOptions{}})
		return fromCiliumNetworkPolicy(newNp), true, err
	}

	newNp.ResourceVersion = cur.ResourceVersion
	err = r.c.Update(ctx, newNp, &client.UpdateOptions{Raw: opts})
	return fromCiliumNetworkPolicy(newNp), false, err
}

func (r *REST) Delete(
	ctx context.Context,
	name string,
	_ rest.ValidateObjectFunc,
	opts *metav1.DeleteOptions,
) (runtime.Object, bool, error) {
	ns, ok := request.NamespaceFrom(ctx)
	if !ok {
		return nil, false, apierrors.NewBadRequest("namespace required")
	}
	err := r.c.Delete(ctx, &ciliumv2.CiliumNetworkPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}, &client.DeleteOptions{Raw: opts})
	return nil, err == nil, err
}

func (r *REST) Patch(
	ctx context.Context,
	name string,
	pt types.PatchType,
	data []byte,
	opts *metav1.PatchOptions,
	subresources ...string,
) (runtime.Object, error) {
	if len(subresources) > 0 {
		return nil, fmt.Errorf("SecurityGroup does not have subresources")
	}
	ns, ok := request.NamespaceFrom(ctx)
	if !ok {
		return nil, apierrors.NewBadRequest("namespace required")
	}
	out := &ciliumv2.CiliumNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
		},
	}
	patch := client.RawPatch(pt, data)
	err := r.c.Patch(ctx, out, patch, &client.PatchOptions{Raw: opts})
	if err != nil {
		return nil, err
	}

	return fromCiliumNetworkPolicy(out), nil
}

// -----------------------------------------------------------------------------
// Watcher
// -----------------------------------------------------------------------------

func (r *REST) Watch(ctx context.Context, opts *metainternal.ListOptions) (watch.Interface, error) {
	ns, ok := request.NamespaceFrom(ctx)
	if !ok {
		return nil, apierrors.NewBadRequest("namespace required")
	}

	npList := &ciliumv2.CiliumNetworkPolicyList{}
	base, err := r.w.Watch(ctx, npList, &client.ListOptions{Namespace: ns, Raw: &metav1.ListOptions{
		Watch:           true,
		ResourceVersion: opts.ResourceVersion,
	}})
	if err != nil {
		return nil, err
	}

	ch := make(chan watch.Event)
	proxy := watch.NewProxyWatcher(ch)

	go func() {
		defer proxy.Stop()
		for ev := range base.ResultChan() {
			np, ok := ev.Object.(*ciliumv2.CiliumNetworkPolicy)
			if !ok || sec == nil {
				continue
			}
			sg := fromCiliumNetworkPolicy(np)
			ch <- watch.Event{
				Type:   ev.Type,
				Object: sg,
			}
		}
	}()

	return proxy, nil
}

// -----------------------------------------------------------------------------
// TableConvertor
// -----------------------------------------------------------------------------

func (r *REST) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	now := time.Now()
	row := func(o *sdnv1alpha1.SecurityGroup) metav1.TableRow {
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
	case *sdnv1alpha1.SecurityGroupList:
		for i := range v.Items {
			tbl.Rows = append(tbl.Rows, row(&v.Items[i]))
		}
		tbl.ListMeta.ResourceVersion = v.ListMeta.ResourceVersion
	case *sdnv1alpha1.SecurityGroup:
		tbl.Rows = append(tbl.Rows, row(v))
		tbl.ListMeta.ResourceVersion = v.ResourceVersion
	default:
		return nil, notAcceptable{sdnv1alpha1.Resource(sdnv1alpha1.SecurityGroupPluralName), fmt.Sprintf("unexpected %T", obj)}
	}
	return tbl, nil
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

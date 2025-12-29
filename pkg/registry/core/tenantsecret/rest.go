// SPDX-License-Identifier: Apache-2.0
// TenantSecret registry – namespaced view over Secrets labelled
// "internal.cozystack.io/tenantresource=true".  Internal tenant secret labels are hidden.

package tenantsecret

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternal "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	"github.com/cozystack/cozystack/pkg/registry/sorting"
)

// -----------------------------------------------------------------------------
// Constants & helpers
// -----------------------------------------------------------------------------

const (
	tsLabelKey           = corev1alpha1.TenantResourceLabelKey
	tsLabelValue         = corev1alpha1.TenantResourceLabelValue
	singularName         = "tenantsecret"
	kindTenantSecret     = "TenantSecret"
	kindTenantSecretList = "TenantSecretList"
)

func stripInternal(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if k == tsLabelKey {
			continue
		}
		out[k] = v
	}
	return out
}

func encodeStringData(sd map[string]string) map[string][]byte {
	if len(sd) == 0 {
		return nil
	}
	out := make(map[string][]byte, len(sd))
	for k, v := range sd {
		out[k] = []byte(v)
	}
	return out
}

func decodeStringData(d map[string][]byte) map[string]string {
	if len(d) == 0 {
		return nil
	}
	out := make(map[string]string, len(d))
	for k, v := range d {
		out[k] = base64.StdEncoding.EncodeToString(v)
	}
	return out
}

func secretToTenant(sec *corev1.Secret) *corev1alpha1.TenantSecret {
	return &corev1alpha1.TenantSecret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1alpha1.SchemeGroupVersion.String(),
			Kind:       kindTenantSecret,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:              sec.Name,
			Namespace:         sec.Namespace,
			UID:               sec.UID,
			ResourceVersion:   sec.ResourceVersion,
			CreationTimestamp: sec.CreationTimestamp,
			Labels:            stripInternal(sec.Labels),
			Annotations:       sec.Annotations,
		},
		Type:       string(sec.Type),
		Data:       sec.Data,
		StringData: decodeStringData(sec.Data),
	}
}

func tenantToSecret(ts *corev1alpha1.TenantSecret, cur *corev1.Secret) *corev1.Secret {
	var out corev1.Secret
	if cur != nil {
		out = *cur.DeepCopy()
	}
	out.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}
	out.Name, out.Namespace = ts.Name, ts.Namespace

	if out.Labels == nil {
		out.Labels = map[string]string{}
	}
	out.Labels[tsLabelKey] = tsLabelValue
	for k, v := range ts.Labels {
		out.Labels[k] = v
	}

	if out.Annotations == nil {
		out.Annotations = map[string]string{}
	}
	for k, v := range ts.Annotations {
		out.Annotations[k] = v
	}

	if len(ts.Data) != 0 {
		out.Data = ts.Data
	} else if len(ts.StringData) != 0 {
		out.Data = encodeStringData(ts.StringData)
	}
	out.Type = corev1.SecretType(ts.Type)
	return &out
}

func nsFrom(ctx context.Context) (string, error) {
	ns, ok := request.NamespaceFrom(ctx)
	if !ok {
		return "", apierrors.NewBadRequest("namespace required")
	}
	return ns, nil
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
	c   client.Client
	w   client.WithWatch
	gvr schema.GroupVersionResource
}

func NewREST(c client.Client, w client.WithWatch) *REST {
	return &REST{
		c: c,
		w: w,
		gvr: schema.GroupVersionResource{
			Group:    corev1alpha1.GroupName,
			Version:  "v1alpha1",
			Resource: "tenantsecrets",
		},
	}
}

// -----------------------------------------------------------------------------
// Basic meta
// -----------------------------------------------------------------------------

func (*REST) NamespaceScoped() bool { return true }
func (*REST) New() runtime.Object   { return &corev1alpha1.TenantSecret{} }
func (*REST) NewList() runtime.Object {
	return &corev1alpha1.TenantSecretList{}
}
func (*REST) Kind() string { return kindTenantSecret }
func (r *REST) GroupVersionKind(_ schema.GroupVersion) schema.GroupVersionKind {
	return r.gvr.GroupVersion().WithKind(kindTenantSecret)
}
func (*REST) GetSingularName() string { return singularName }

// -----------------------------------------------------------------------------
// CRUD
// -----------------------------------------------------------------------------

func (r *REST) Create(
	ctx context.Context,
	obj runtime.Object,
	_ rest.ValidateObjectFunc,
	opts *metav1.CreateOptions,
) (runtime.Object, error) {
	in, ok := obj.(*corev1alpha1.TenantSecret)
	if !ok {
		return nil, fmt.Errorf("expected TenantSecret, got %T", obj)
	}

	sec := tenantToSecret(in, nil)
	err := r.c.Create(ctx, sec, &client.CreateOptions{Raw: opts})
	if err != nil {
		return nil, err
	}
	return secretToTenant(sec), nil
}

func (r *REST) Get(
	ctx context.Context,
	name string,
	opts *metav1.GetOptions,
) (runtime.Object, error) {
	ns, err := nsFrom(ctx)
	if err != nil {
		return nil, err
	}
	sec := &corev1.Secret{}
	err = r.c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, sec, &client.GetOptions{Raw: opts})
	if err != nil {
		return nil, err
	}
	if sec.Labels == nil || sec.Labels[tsLabelKey] != tsLabelValue {
		return nil, apierrors.NewNotFound(r.gvr.GroupResource(), name)
	}
	return secretToTenant(sec), nil
}

func (r *REST) List(ctx context.Context, opts *metainternal.ListOptions) (runtime.Object, error) {
	ns, err := nsFrom(ctx)
	if err != nil {
		return nil, err
	}

	ls := labels.NewSelector()
	req, _ := labels.NewRequirement(tsLabelKey, selection.Equals, []string{tsLabelValue})
	ls = ls.Add(*req)

	if opts.LabelSelector != nil {
		if reqs, _ := opts.LabelSelector.Requirements(); len(reqs) > 0 {
			ls = ls.Add(reqs...)
		}
	}

	fieldSel := ""
	if opts.FieldSelector != nil {
		fieldSel = opts.FieldSelector.String()
	}

	list := &corev1.SecretList{}
	err = r.c.List(ctx, list,
		&client.ListOptions{
			Namespace:     ns,
			LabelSelector: ls,
			Raw: &metav1.ListOptions{
				LabelSelector: ls.String(),
				FieldSelector: fieldSel,
			},
		})
	if err != nil {
		return nil, err
	}

	out := &corev1alpha1.TenantSecretList{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1alpha1.SchemeGroupVersion.String(),
			Kind:       kindTenantSecretList,
		},
		ListMeta: list.ListMeta,
	}

	for i := range list.Items {
		out.Items = append(out.Items, *secretToTenant(&list.Items[i]))
	}
	sorting.ByNamespacedName[corev1alpha1.TenantSecret, *corev1alpha1.TenantSecret](out.Items)
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
	ns, err := nsFrom(ctx)
	if err != nil {
		return nil, false, err
	}

	var cur *corev1.Secret
	previous := &corev1.Secret{}
	if err := r.c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, previous, &client.GetOptions{Raw: &metav1.GetOptions{}}); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, false, err
		}
	} else {
		if previous.Labels == nil || previous.Labels[tsLabelKey] != tsLabelValue {
			return nil, false, apierrors.NewNotFound(r.gvr.GroupResource(), name)
		}
		cur = previous
	}

	newObj, err := objInfo.UpdatedObject(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	in := newObj.(*corev1alpha1.TenantSecret)

	newSec := tenantToSecret(in, cur)
	newSec.Namespace = ns
	if cur == nil {
		if !forceCreate {
			return nil, false, apierrors.NewNotFound(r.gvr.GroupResource(), name)
		}
		err := r.c.Create(ctx, newSec, &client.CreateOptions{Raw: &metav1.CreateOptions{}})
		return secretToTenant(newSec), true, err
	}

	newSec.ResourceVersion = cur.ResourceVersion
	err = r.c.Update(ctx, newSec, &client.UpdateOptions{Raw: opts})
	return secretToTenant(newSec), false, err
}

func (r *REST) Delete(
	ctx context.Context,
	name string,
	_ rest.ValidateObjectFunc,
	opts *metav1.DeleteOptions,
) (runtime.Object, bool, error) {
	ns, err := nsFrom(ctx)
	if err != nil {
		return nil, false, err
	}
	current := &corev1.Secret{}
	if err := r.c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, current, &client.GetOptions{Raw: &metav1.GetOptions{}}); err != nil {
		return nil, false, err
	}
	if current.Labels == nil || current.Labels[tsLabelKey] != tsLabelValue {
		return nil, false, apierrors.NewNotFound(r.gvr.GroupResource(), name)
	}
	err = r.c.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}, &client.DeleteOptions{Raw: opts})
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
		return nil, fmt.Errorf("TenantSecret does not have subresources")
	}
	ns, err := nsFrom(ctx)
	if err != nil {
		return nil, err
	}
	current := &corev1.Secret{}
	if err := r.c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, current, &client.GetOptions{Raw: &metav1.GetOptions{}}); err != nil {
		return nil, err
	}
	if current.Labels == nil || current.Labels[tsLabelKey] != tsLabelValue {
		return nil, apierrors.NewNotFound(r.gvr.GroupResource(), name)
	}
	out := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
		},
	}
	patch := client.RawPatch(pt, data)
	err = r.c.Patch(ctx, out, patch, &client.PatchOptions{Raw: opts})
	if err != nil {
		return nil, err
	}

	// Ensure tenant secret label is preserved
	if out.Labels == nil {
		out.Labels = make(map[string]string)
	}

	if out.Labels[tsLabelKey] != tsLabelValue {
		out.Labels[tsLabelKey] = tsLabelValue
		_ = r.c.Update(ctx, out, &client.UpdateOptions{Raw: &metav1.UpdateOptions{}})
	}

	return secretToTenant(out), nil
}

// -----------------------------------------------------------------------------
// Watcher
// -----------------------------------------------------------------------------

func (r *REST) Watch(ctx context.Context, opts *metainternal.ListOptions) (watch.Interface, error) {
	ns, err := nsFrom(ctx)
	if err != nil {
		return nil, err
	}

	secList := &corev1.SecretList{}
	ls := labels.Set{tsLabelKey: tsLabelValue}.AsSelector()
	base, err := r.w.Watch(ctx, secList, &client.ListOptions{
		Namespace:     ns,
		LabelSelector: ls,
		Raw: &metav1.ListOptions{
			Watch:           true,
			LabelSelector:   ls.String(),
			ResourceVersion: opts.ResourceVersion,
		},
	})
	if err != nil {
		return nil, err
	}

	ch := make(chan watch.Event)
	proxy := watch.NewProxyWatcher(ch)

	go func() {
		defer proxy.Stop()
		for ev := range base.ResultChan() {
			sec, ok := ev.Object.(*corev1.Secret)
			if !ok || sec == nil {
				continue
			}
			tenant := secretToTenant(sec)
			ch <- watch.Event{
				Type:   ev.Type,
				Object: tenant,
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
	row := func(o *corev1alpha1.TenantSecret) metav1.TableRow {
		return metav1.TableRow{
			Cells:  []interface{}{o.Name, o.Type, duration.HumanDuration(now.Sub(o.CreationTimestamp.Time))},
			Object: runtime.RawExtension{Object: o},
		}
	}

	tbl := &metav1.Table{
		TypeMeta: metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "Table"},
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "NAME", Type: "string"},
			{Name: "TYPE", Type: "string"},
			{Name: "AGE", Type: "string"},
		},
	}

	switch v := obj.(type) {
	case *corev1alpha1.TenantSecretList:
		for i := range v.Items {
			tbl.Rows = append(tbl.Rows, row(&v.Items[i]))
		}
		tbl.ResourceVersion = v.ResourceVersion
	case *corev1alpha1.TenantSecret:
		tbl.Rows = append(tbl.Rows, row(v))
		tbl.ResourceVersion = v.ResourceVersion
	default:
		return nil, notAcceptable{r.gvr.GroupResource(), fmt.Sprintf("unexpected %T", obj)}
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

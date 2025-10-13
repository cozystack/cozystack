// SPDX-License-Identifier: Apache-2.0
// TenantSecretsTable registry – namespaced, read-only flattened view over
// Secrets labelled "internal.cozystack.io/tenantresource=true". Each data key is a separate object.

package tenantsecretstable

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternal "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"

	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

const (
	tsLabelKey     = corev1alpha1.TenantResourceLabelKey
	tsLabelValue   = corev1alpha1.TenantResourceLabelValue
	kindObj        = "TenantSecretsTable"
	kindObjList    = "TenantSecretsTableList"
	singularName   = "tenantsecretstable"
	resourcePlural = "tenantsecretstables"
)

type REST struct {
	core corev1client.CoreV1Interface
	gvr  schema.GroupVersionResource
}

func NewREST(coreCli corev1client.CoreV1Interface) *REST {
	return &REST{
		core: coreCli,
		gvr: schema.GroupVersionResource{
			Group:    corev1alpha1.GroupName,
			Version:  "v1alpha1",
			Resource: resourcePlural,
		},
	}
}

var (
	_ rest.Getter               = &REST{}
	_ rest.Lister               = &REST{}
	_ rest.Watcher              = &REST{}
	_ rest.TableConvertor       = &REST{}
	_ rest.Scoper               = &REST{}
	_ rest.SingularNameProvider = &REST{}
	_ rest.Storage              = &REST{}
)

func (*REST) NamespaceScoped() bool { return true }
func (*REST) New() runtime.Object   { return &corev1alpha1.TenantSecretsTable{} }
func (*REST) NewList() runtime.Object {
	return &corev1alpha1.TenantSecretsTableList{}
}
func (*REST) Kind() string { return kindObj }
func (r *REST) GroupVersionKind(_ schema.GroupVersion) schema.GroupVersionKind {
	return r.gvr.GroupVersion().WithKind(kindObj)
}
func (*REST) GetSingularName() string { return singularName }
func (*REST) Destroy()                {}

func nsFrom(ctx context.Context) (string, error) {
	ns, ok := request.NamespaceFrom(ctx)
	if !ok {
		return "", fmt.Errorf("namespace required")
	}
	return ns, nil
}

// -----------------------
// Get/List
// -----------------------

func (r *REST) Get(ctx context.Context, name string, opts *metav1.GetOptions) (runtime.Object, error) {
	ns, err := nsFrom(ctx)
	if err != nil {
		return nil, err
	}

	// We need to identify secret name and key. Iterate secrets in namespace with tenant secret label
	// and return the matching composed object.
	list, err := r.core.Secrets(ns).List(ctx, metav1.ListOptions{LabelSelector: labels.Set{tsLabelKey: tsLabelValue}.AsSelector().String()})
	if err != nil {
		return nil, err
	}
	for i := range list.Items {
		sec := &list.Items[i]
		for k, v := range sec.Data {
			composed := composedName(sec.Name, k)
			if composed == name {
				return secretKeyToObj(sec, k, v), nil
			}
		}
	}
	return nil, apierrors.NewNotFound(r.gvr.GroupResource(), name)
}

func (r *REST) List(ctx context.Context, opts *metainternal.ListOptions) (runtime.Object, error) {
	ns, err := nsFrom(ctx)
	if err != nil {
		return nil, err
	}

	sel := labels.NewSelector()
	req, _ := labels.NewRequirement(tsLabelKey, selection.Equals, []string{tsLabelValue})
	sel = sel.Add(*req)
	if opts.LabelSelector != nil {
		if reqs, _ := opts.LabelSelector.Requirements(); len(reqs) > 0 {
			sel = sel.Add(reqs...)
		}
	}
	fieldSel := ""
	if opts.FieldSelector != nil {
		fieldSel = opts.FieldSelector.String()
	}

	list, err := r.core.Secrets(ns).List(ctx, metav1.ListOptions{LabelSelector: sel.String(), FieldSelector: fieldSel})
	if err != nil {
		return nil, err
	}

	out := &corev1alpha1.TenantSecretsTableList{
		TypeMeta: metav1.TypeMeta{APIVersion: corev1alpha1.SchemeGroupVersion.String(), Kind: kindObjList},
		ListMeta: list.ListMeta,
	}

	for i := range list.Items {
		sec := &list.Items[i]
		// Ensure stable ordering of keys
		keys := make([]string, 0, len(sec.Data))
		for k := range sec.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := sec.Data[k]
			o := secretKeyToObj(sec, k, v)
			out.Items = append(out.Items, *o)
		}
	}

	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Name < out.Items[j].Name })
	return out, nil
}

// -----------------------
// Watch
// -----------------------

func (r *REST) Watch(ctx context.Context, opts *metainternal.ListOptions) (watch.Interface, error) {
	ns, err := nsFrom(ctx)
	if err != nil {
		return nil, err
	}

	ls := labels.Set{tsLabelKey: tsLabelValue}.AsSelector().String()
	base, err := r.core.Secrets(ns).Watch(ctx, metav1.ListOptions{
		Watch:           true,
		LabelSelector:   ls,
		ResourceVersion: opts.ResourceVersion,
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
			// Emit an event per key
			for k, v := range sec.Data {
				obj := secretKeyToObj(sec, k, v)
				ch <- watch.Event{Type: ev.Type, Object: obj}
			}
		}
	}()

	return proxy, nil
}

// -----------------------
// TableConvertor
// -----------------------

func (r *REST) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	now := time.Now()
	row := func(o *corev1alpha1.TenantSecretsTable) metav1.TableRow {
		return metav1.TableRow{
			Cells:  []interface{}{o.Name, o.Data.Name, o.Data.Key, humanAge(o.CreationTimestamp.Time, now)},
			Object: runtime.RawExtension{Object: o},
		}
	}
	tbl := &metav1.Table{
		TypeMeta: metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "Table"},
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "NAME", Type: "string"},
			{Name: "SECRET", Type: "string"},
			{Name: "KEY", Type: "string"},
			{Name: "AGE", Type: "string"},
		},
	}
	switch v := obj.(type) {
	case *corev1alpha1.TenantSecretsTableList:
		for i := range v.Items {
			tbl.Rows = append(tbl.Rows, row(&v.Items[i]))
		}
		tbl.ListMeta.ResourceVersion = v.ListMeta.ResourceVersion
	case *corev1alpha1.TenantSecretsTable:
		tbl.Rows = append(tbl.Rows, row(v))
		tbl.ListMeta.ResourceVersion = v.ResourceVersion
	default:
		return nil, notAcceptable{r.gvr.GroupResource(), fmt.Sprintf("unexpected %T", obj)}
	}
	return tbl, nil
}

// -----------------------
// Helpers
// -----------------------

func composedName(secretName, key string) string {
	return secretName + "-" + key
}

func humanAge(t time.Time, now time.Time) string {
	d := now.Sub(t)
	// simple human duration
	if d.Hours() >= 24 {
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	if d.Hours() >= 1 {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	if d.Minutes() >= 1 {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func secretKeyToObj(sec *corev1.Secret, key string, val []byte) *corev1alpha1.TenantSecretsTable {
	return &corev1alpha1.TenantSecretsTable{
		TypeMeta: metav1.TypeMeta{APIVersion: corev1alpha1.SchemeGroupVersion.String(), Kind: kindObj},
		ObjectMeta: metav1.ObjectMeta{
			Name:              sec.Name,
			Namespace:         sec.Namespace,
			UID:               sec.UID,
			ResourceVersion:   sec.ResourceVersion,
			CreationTimestamp: sec.CreationTimestamp,
			Labels:            filterUserLabels(sec.Labels),
			Annotations:       sec.Annotations,
		},
		Data: corev1alpha1.TenantSecretEntry{
			Name:  sec.Name,
			Key:   key,
			Value: toBase64String(val),
		},
	}
}

func filterUserLabels(m map[string]string) map[string]string {
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

func toBase64String(b []byte) string {
	const enc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	// Minimal base64 encoder to avoid extra deps; for readability we could use stdlib encoding/base64
	// but keeping inline is fine; however using stdlib is clearer.
	// Using stdlib:
	return base64.StdEncoding.EncodeToString(b)
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

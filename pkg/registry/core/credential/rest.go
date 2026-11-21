// SPDX-License-Identifier: Apache-2.0

// Package credential serves core.cozystack.io/credentials, a view over the
// CredentialTemplates a kind's chart renders, with the mint and revoke
// subresources. The view shows the account's record as the template's status
// mirrors it, and never reads a Secret. See design-proposals/user-secrets-api
// in cozystack/community.
package credential

import (
	"context"
	"fmt"
	"net/http"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternal "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	"github.com/cozystack/cozystack/pkg/credentials/account"
	"github.com/cozystack/cozystack/pkg/registry"
	fieldfilter "github.com/cozystack/cozystack/pkg/registry/fields"
	"github.com/cozystack/cozystack/pkg/registry/sorting"
)

var (
	_ rest.Getter               = &REST{}
	_ rest.Lister               = &REST{}
	_ rest.TableConvertor       = &REST{}
	_ rest.Scoper               = &REST{}
	_ rest.SingularNameProvider = &REST{}
)

const (
	singularName   = "credential"
	kindCredential = "Credential"
	kindList       = "CredentialList"
)

// REST serves the credentials view.
type REST struct {
	c client.Client
	// direct reads and writes for mint and revoke, which the cached client
	// would answer with a template status a moment old.
	direct client.Client
	gvr    schema.GroupVersionResource
	now    func() time.Time
}

// NewREST creates the credentials storage.
func NewREST(c, direct client.Client) *REST {
	return &REST{
		c:      c,
		direct: direct,
		gvr: schema.GroupVersionResource{
			Group:    corev1alpha1.GroupName,
			Version:  "v1alpha1",
			Resource: "credentials",
		},
		now: time.Now,
	}
}

func (*REST) NamespaceScoped() bool   { return true }
func (*REST) GetSingularName() string { return singularName }
func (*REST) Destroy()                {}
func (*REST) New() runtime.Object     { return &corev1alpha1.Credential{} }
func (*REST) NewList() runtime.Object { return &corev1alpha1.CredentialList{} }
func (*REST) Kind() string            { return kindCredential }
func (r *REST) GroupVersionKind(schema.GroupVersion) schema.GroupVersionKind {
	return r.gvr.GroupVersion().WithKind(kindCredential)
}

func nsFrom(ctx context.Context) (string, error) {
	ns, ok := request.NamespaceFrom(ctx)
	if !ok || ns == "" {
		return "", apierrors.NewBadRequest("namespace required")
	}
	return ns, nil
}

// resolved is one account the registry has located: its template and the
// HelmRelease that rendered it.
type resolved struct {
	tmpl *cozyv1alpha1.CredentialTemplate
	hr   *helmv2.HelmRelease
}

// resolve locates the account a Credential name points at. The template must
// exist and the HelmRelease that rendered it must be live, or the credential
// does not exist.
func (r *REST) resolve(ctx context.Context, c client.Reader, namespace, name string) (*resolved, error) {
	tmpl := &cozyv1alpha1.CredentialTemplate{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, tmpl); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, apierrors.NewNotFound(r.gvr.GroupResource(), name)
		}
		return nil, err
	}
	hr, err := r.release(ctx, c, tmpl, nil)
	if err != nil {
		return nil, err
	}
	if hr == nil {
		return nil, apierrors.NewNotFound(r.gvr.GroupResource(), name)
	}
	return &resolved{tmpl: tmpl, hr: hr}, nil
}

// release returns the live HelmRelease that rendered a template, nil when the
// template names none or its release is gone. cache saves a list from reading
// one release per template.
func (r *REST) release(ctx context.Context, c client.Reader, tmpl *cozyv1alpha1.CredentialTemplate, cache map[string]*helmv2.HelmRelease) (*helmv2.HelmRelease, error) {
	name := tmpl.Annotations[account.ReleaseNameAnnotation]
	if name == "" || !tmpl.DeletionTimestamp.IsZero() {
		return nil, nil
	}
	if hr, seen := cache[name]; seen {
		return hr, nil
	}
	hr := &helmv2.HelmRelease{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: tmpl.Namespace, Name: name}, hr); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
		hr = nil
	}
	if cache != nil {
		cache[name] = hr
	}
	return hr, nil
}

// toCredential builds the view of one account from its template.
func toCredential(tmpl *cozyv1alpha1.CredentialTemplate, hr *helmv2.HelmRelease) corev1alpha1.Credential {
	st := tmpl.Status
	origin := corev1alpha1.CredentialOrigin(st.Password.Origin)
	if origin == "" {
		origin = corev1alpha1.CredentialOriginNotIssued
	}
	pw := corev1alpha1.CredentialPasswordState{Origin: origin, By: st.Password.By}
	if st.Password.At != nil {
		pw.At = *st.Password.At
	}
	var history []corev1alpha1.CredentialHistoryEntry
	for _, h := range st.History {
		history = append(history, corev1alpha1.CredentialHistoryEntry{Operation: corev1alpha1.CredentialOperation(h.Operation), At: h.At, By: h.By})
	}
	return corev1alpha1.Credential{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1alpha1.SchemeGroupVersion.String(),
			Kind:       kindCredential,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:              tmpl.Name,
			Namespace:         tmpl.Namespace,
			UID:               tmpl.UID,
			ResourceVersion:   st.SecretResourceVersion,
			CreationTimestamp: tmpl.CreationTimestamp,
		},
		Spec: corev1alpha1.CredentialSpec{
			Application: corev1alpha1.CredentialApplicationRef{
				APIGroup: hr.Labels[appsv1alpha1.ApplicationGroupLabel],
				Kind:     hr.Labels[appsv1alpha1.ApplicationKindLabel],
				Name:     hr.Labels[appsv1alpha1.ApplicationNameLabel],
			},
			User: tmpl.Spec.Username,
		},
		Status: corev1alpha1.CredentialStatus{Password: pw, History: history},
	}
}

func (r *REST) Get(ctx context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	ns, err := nsFrom(ctx)
	if err != nil {
		return nil, err
	}
	res, err := r.resolve(ctx, r.c, ns, name)
	if err != nil {
		return nil, err
	}
	cred := toCredential(res.tmpl, res.hr)
	return &cred, nil
}

func (r *REST) List(ctx context.Context, opts *metainternal.ListOptions) (runtime.Object, error) {
	ns, err := nsFrom(ctx)
	if err != nil {
		return nil, err
	}
	out := &corev1alpha1.CredentialList{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1alpha1.SchemeGroupVersion.String(),
			Kind:       kindList,
		},
	}
	fieldFilter, err := fieldfilter.ParseFieldSelector(opts.FieldSelector)
	if err != nil {
		return nil, err
	}
	if fieldFilter.Namespace != "" && fieldFilter.Namespace != ns {
		return out, nil
	}
	listOpts := &client.ListOptions{Namespace: ns}
	if opts.LabelSelector != nil {
		if _, selectable := opts.LabelSelector.Requirements(); !selectable {
			return out, nil
		}
		listOpts.LabelSelector = opts.LabelSelector
	}

	list := &cozyv1alpha1.CredentialTemplateList{}
	if err := r.c.List(ctx, list, listOpts); err != nil {
		return nil, err
	}
	releases := map[string]*helmv2.HelmRelease{}
	for i := range list.Items {
		tmpl := &list.Items[i]
		hr, err := r.release(ctx, r.c, tmpl, releases)
		if err != nil {
			return nil, err
		}
		if hr == nil || !fieldFilter.MatchesName(tmpl.Name) {
			continue
		}
		out.Items = append(out.Items, toCredential(tmpl, hr))
	}
	listRV := list.ResourceVersion
	if listRV == "" {
		listRV, _ = registry.MaxResourceVersion(list)
	}
	out.ResourceVersion = listRV
	sorting.ByNamespacedName[corev1alpha1.Credential, *corev1alpha1.Credential](out.Items)
	return out, nil
}

func (r *REST) ConvertToTable(ctx context.Context, object runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	table := &metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "Name", Type: "string", Format: "name"},
			{Name: "Application", Type: "string"},
			{Name: "User", Type: "string"},
			{Name: "Origin", Type: "string"},
			{Name: "Age", Type: "string"},
		},
	}
	now := r.now()
	row := func(c *corev1alpha1.Credential) metav1.TableRow {
		age := "<unknown>"
		if !c.CreationTimestamp.IsZero() {
			age = duration.HumanDuration(now.Sub(c.CreationTimestamp.Time))
		}
		return metav1.TableRow{
			Cells:  []any{c.Name, c.Spec.Application.Kind + "/" + c.Spec.Application.Name, c.Spec.User, string(c.Status.Password.Origin), age},
			Object: runtime.RawExtension{Object: c},
		}
	}
	switch v := object.(type) {
	case *corev1alpha1.Credential:
		table.Rows = append(table.Rows, row(v))
	case *corev1alpha1.CredentialList:
		table.ResourceVersion = v.ResourceVersion
		for i := range v.Items {
			table.Rows = append(table.Rows, row(&v.Items[i]))
		}
	default:
		return nil, apierrors.NewGenericServerResponse(http.StatusInternalServerError, "", r.gvr.GroupResource(), "", fmt.Sprintf("unexpected object type %T", object), 0, false)
	}
	return table, nil
}

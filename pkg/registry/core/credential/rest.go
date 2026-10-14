// SPDX-License-Identifier: Apache-2.0

// Package credential serves core.cozystack.io/credentials: a view over the
// account Secrets the Cozystack API writes for the accounts of converted
// applications, with the mint and revoke subresources that are the only way
// to obtain or retire a password. The view shows the account's record and
// never the Secret's data. See design-proposals/user-secrets-api in
// cozystack/community.
package credential

import (
	"context"
	"fmt"
	"net/http"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
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

// Kind is what the registry knows about one converted application kind: its
// credentials block, and how to resolve the accounts an application declares
// from the values its HelmRelease carries, which the application registry
// does with the kind's schema defaults applied.
type Kind struct {
	Block    *cozyv1alpha1.ApplicationDefinitionCredentials
	Accounts func(ctx context.Context, hr *helmv2.HelmRelease) ([]account.Account, error)
}

// REST serves the credentials view.
type REST struct {
	c   client.Client
	gvr schema.GroupVersionResource
	// kinds is keyed by the application kind the HelmRelease labels name.
	kinds map[string]Kind
	now   func() time.Time
}

// NewREST creates the credentials storage. kinds carries every converted
// application kind; a HelmRelease of a kind absent from it has no credentials.
func NewREST(c client.Client, kinds map[string]Kind) *REST {
	return &REST{
		c: c,
		gvr: schema.GroupVersionResource{
			Group:    corev1alpha1.GroupName,
			Version:  "v1alpha1",
			Resource: "credentials",
		},
		kinds: kinds,
		now:   time.Now,
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

// resolved is one account the registry has located: its HelmRelease, its
// kind, and its Secret where one exists.
type resolved struct {
	hr     *helmv2.HelmRelease
	kind   Kind
	mapped string
	secret *corev1.Secret
	// stale is a Secret at the account's name that is not the application's
	// own, left from a deleted application of the same name; a mint replaces
	// it rather than reusing it.
	stale *corev1.Secret
}

// resolve locates the account a Credential name points at. The HelmRelease
// must exist and belong to a converted kind, or the credential does not exist.
// A Secret that is not the application's own, by marker or by owner, counts
// as absent.
func (r *REST) resolve(ctx context.Context, namespace, name string) (*resolved, error) {
	release, mapped, ok := account.SplitCredentialName(name)
	if !ok {
		return nil, apierrors.NewNotFound(r.gvr.GroupResource(), name)
	}
	hr := &helmv2.HelmRelease{}
	if err := r.c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: release}, hr); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, apierrors.NewNotFound(r.gvr.GroupResource(), name)
		}
		return nil, err
	}
	kind, ok := r.kinds[hr.Labels[appsv1alpha1.ApplicationKindLabel]]
	if !ok || kind.Block == nil {
		return nil, apierrors.NewNotFound(r.gvr.GroupResource(), name)
	}
	res := &resolved{hr: hr, kind: kind, mapped: mapped}
	sec := &corev1.Secret{}
	err := r.c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: account.SecretName(release, mapped)}, sec)
	switch {
	case err == nil && ownedBy(sec, hr):
		res.secret = sec
	case err == nil:
		res.stale = sec
	case !apierrors.IsNotFound(err):
		return nil, err
	}
	return res, nil
}

// ownedBy reports whether a Secret is an account Secret owned by the
// HelmRelease, which is what makes it the application's own.
func ownedBy(sec *corev1.Secret, hr *helmv2.HelmRelease) bool {
	if !account.IsAccountSecret(sec) {
		return false
	}
	for _, ref := range sec.OwnerReferences {
		if ref.UID == hr.UID {
			return true
		}
	}
	return false
}

// toCredential builds the view of one account Secret.
func toCredential(sec *corev1.Secret, hr *helmv2.HelmRelease) corev1alpha1.Credential {
	rec := account.ReadRecord(sec)
	_, mapped, _ := account.SplitCredentialName(sec.Name[:len(sec.Name)-len(account.SecretSuffix)])
	return corev1alpha1.Credential{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1alpha1.SchemeGroupVersion.String(),
			Kind:       kindCredential,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:              account.CredentialName(hr.Name, mapped),
			Namespace:         sec.Namespace,
			UID:               sec.UID,
			ResourceVersion:   sec.ResourceVersion,
			CreationTimestamp: sec.CreationTimestamp,
		},
		Spec: corev1alpha1.CredentialSpec{
			Application: corev1alpha1.CredentialApplicationRef{
				APIGroup: hr.Labels[appsv1alpha1.ApplicationGroupLabel],
				Kind:     hr.Labels[appsv1alpha1.ApplicationKindLabel],
				Name:     hr.Labels[appsv1alpha1.ApplicationNameLabel],
			},
			User: string(sec.Data[account.UsernameKey]),
		},
		Status: corev1alpha1.CredentialStatus{
			Password: corev1alpha1.CredentialPasswordState{Origin: rec.Origin, At: rec.At, By: rec.By},
			History:  rec.History,
		},
	}
}

func (r *REST) Get(ctx context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	ns, err := nsFrom(ctx)
	if err != nil {
		return nil, err
	}
	res, err := r.resolve(ctx, ns, name)
	if err != nil {
		return nil, err
	}
	if res.secret == nil {
		return nil, apierrors.NewNotFound(r.gvr.GroupResource(), name)
	}
	cred := toCredential(res.secret, res.hr)
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

	sel := labels.NewSelector()
	req, _ := labels.NewRequirement(account.AccountLabel, selection.Equals, []string{"true"})
	sel = sel.Add(*req)
	if opts.LabelSelector != nil {
		reqs, selectable := opts.LabelSelector.Requirements()
		if !selectable {
			return out, nil
		}
		sel = sel.Add(reqs...)
	}

	list := &corev1.SecretList{}
	if err := r.c.List(ctx, list, &client.ListOptions{Namespace: ns, LabelSelector: sel}); err != nil {
		return nil, err
	}
	releases := map[string]*helmv2.HelmRelease{}
	for i := range list.Items {
		sec := &list.Items[i]
		hr, err := r.liveOwner(ctx, sec, releases)
		if err != nil {
			return nil, err
		}
		if hr == nil {
			continue
		}
		cred := toCredential(sec, hr)
		if !fieldFilter.MatchesName(cred.Name) {
			continue
		}
		out.Items = append(out.Items, cred)
	}
	listRV := list.ResourceVersion
	if listRV == "" {
		listRV, _ = registry.MaxResourceVersion(list)
	}
	out.ResourceVersion = listRV
	sorting.ByNamespacedName[corev1alpha1.Credential, *corev1alpha1.Credential](out.Items)
	return out, nil
}

// liveOwner returns the HelmRelease that owns an account Secret, provided it
// exists and belongs to a converted kind; nil otherwise, so a Secret no live
// release owns is not listed.
func (r *REST) liveOwner(ctx context.Context, sec *corev1.Secret, cache map[string]*helmv2.HelmRelease) (*helmv2.HelmRelease, error) {
	for _, ref := range sec.OwnerReferences {
		if ref.Kind != helmv2.HelmReleaseKind {
			continue
		}
		hr, seen := cache[ref.Name]
		if !seen {
			hr = &helmv2.HelmRelease{}
			if err := r.c.Get(ctx, types.NamespacedName{Namespace: sec.Namespace, Name: ref.Name}, hr); err != nil {
				if !apierrors.IsNotFound(err) {
					return nil, err
				}
				hr = nil
			}
			cache[ref.Name] = hr
		}
		if hr == nil || hr.UID != ref.UID {
			continue
		}
		if kind, ok := r.kinds[hr.Labels[appsv1alpha1.ApplicationKindLabel]]; !ok || kind.Block == nil {
			continue
		}
		return hr, nil
	}
	return nil, nil
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

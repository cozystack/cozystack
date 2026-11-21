// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	"github.com/cozystack/cozystack/pkg/credentials/account"
	"github.com/cozystack/cozystack/pkg/credentials/format"
)

// +kubebuilder:rbac:groups=cozystack.io,resources=credentialtemplates,verbs=get;list;watch
// +kubebuilder:rbac:groups=cozystack.io,resources=credentialtemplates/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;update;patch

// CredentialControllerActor is who the record names for the operations the
// controller performs: seeding a Secret, or seeding it again after a format
// change.
const CredentialControllerActor = "system:serviceaccount:cozy-system:cozystack-controller"

// Event reasons the controller records on a CredentialTemplate.
const (
	reasonCredentialReset   = "CredentialReset"
	reasonCredentialAdopted = "CredentialAdopted"
)

// CredentialTemplateReconciler keeps each CredentialTemplate and its account
// Secret in step: it seeds a missing Secret on a release marked for seeding,
// seeds it again when the template's format changes, adopts an unowned Secret
// the conversion wrote, leaves any other Secret at the name alone, and mirrors
// the Secret's record into the template's status. See
// design-proposals/user-secrets-api in cozystack/community.
//
// Secrets are read through Reader, uncached: the manager's Secret cache is
// scoped to other reconcilers, and an account Secret may hold a plaintext
// that has no business in a cache. Their changes reach the controller through
// a metadata-only watch.
type CredentialTemplateReconciler struct {
	client.Client
	Reader   client.Reader
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Now      func() time.Time
}

func (r *CredentialTemplateReconciler) now() metav1.Time {
	if r.Now != nil {
		return metav1.NewTime(r.Now())
	}
	return metav1.Now()
}

func (r *CredentialTemplateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	tmpl := &cozyv1alpha1.CredentialTemplate{}
	if err := r.Get(ctx, req.NamespacedName, tmpl); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !tmpl.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	status := tmpl.Status.DeepCopy()
	status.ObservedGeneration = tmpl.Generation

	if !format.Known(tmpl.Spec.Format.Name) {
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:    cozyv1alpha1.CredentialTemplateUnknownFormat,
			Status:  metav1.ConditionTrue,
			Reason:  "NotBuiltIn",
			Message: fmt.Sprintf("format %q is not a built-in format", tmpl.Spec.Format.Name),
		})
		return ctrl.Result{}, r.writeStatus(ctx, tmpl, status)
	}
	meta.RemoveStatusCondition(&status.Conditions, cozyv1alpha1.CredentialTemplateUnknownFormat)

	sec, err := r.secret(ctx, tmpl)
	if err != nil {
		return ctrl.Result{}, err
	}

	switch {
	case sec == nil:
		seed, err := r.seedAllowed(ctx, tmpl)
		if err != nil {
			return ctrl.Result{}, err
		}
		if seed {
			if sec, err = r.seed(ctx, tmpl); err != nil {
				return ctrl.Result{}, err
			}
		}
	case metav1.IsControlledBy(sec, tmpl):
		if sec.Annotations[account.FormatAnnotation] != tmpl.Spec.Format.Name {
			if sec, err = r.reseed(ctx, tmpl, sec); err != nil {
				return ctrl.Result{}, err
			}
		} else if sec, err = r.alignLabels(ctx, tmpl, sec); err != nil {
			return ctrl.Result{}, err
		}
	case adoptable(sec, tmpl):
		if sec, err = r.adopt(ctx, tmpl, sec); err != nil {
			return ctrl.Result{}, err
		}
	default:
		logger.Info("leaving a Secret the controller did not create alone", "secret", sec.Name)
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:    cozyv1alpha1.CredentialTemplateSecretConflict,
			Status:  metav1.ConditionTrue,
			Reason:  "ForeignSecret",
			Message: fmt.Sprintf("Secret %s exists and was not created for this template; remove it to let the account be seeded", sec.Name),
		})
		mirror(status, nil)
		return ctrl.Result{}, r.writeStatus(ctx, tmpl, status)
	}
	meta.RemoveStatusCondition(&status.Conditions, cozyv1alpha1.CredentialTemplateSecretConflict)
	mirror(status, sec)
	return ctrl.Result{}, r.writeStatus(ctx, tmpl, status)
}

// secret reads the template's account Secret, nil when it does not exist.
func (r *CredentialTemplateReconciler) secret(ctx context.Context, tmpl *cozyv1alpha1.CredentialTemplate) (*corev1.Secret, error) {
	sec := &corev1.Secret{}
	err := r.Reader.Get(ctx, types.NamespacedName{Namespace: tmpl.Namespace, Name: tmpl.Name + account.SecretSuffix}, sec)
	switch {
	case apierrors.IsNotFound(err):
		return nil, nil
	case err != nil:
		return nil, err
	}
	return sec, nil
}

// seedAllowed reports whether the template's release is marked for seeding.
// A release without the mark keeps passwords the platform never saw, which a
// seeded random password would replace under its clients.
func (r *CredentialTemplateReconciler) seedAllowed(ctx context.Context, tmpl *cozyv1alpha1.CredentialTemplate) (bool, error) {
	release := tmpl.Annotations[account.ReleaseNameAnnotation]
	if release == "" {
		return false, nil
	}
	hr := &helmv2.HelmRelease{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: tmpl.Namespace, Name: release}, hr); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return hr.Annotations[account.SeedAnnotation] == "true", nil
}

// newSecret builds the account Secret of a template holding the stored form of
// a random password that is discarded here.
func (r *CredentialTemplateReconciler) newSecret(tmpl *cozyv1alpha1.CredentialTemplate, rec account.Record) (*corev1.Secret, error) {
	password, err := format.GeneratePassword()
	if err != nil {
		return nil, err
	}
	stored, err := format.Encode(tmpl.Spec.Format.Name, password)
	if err != nil {
		return nil, err
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        tmpl.Name + account.SecretSuffix,
			Namespace:   tmpl.Namespace,
			Labels:      account.Labels(tmpl.Spec.Secret.Labels),
			Annotations: map[string]string{account.FormatAnnotation: tmpl.Spec.Format.Name},
		},
		Type: corev1.SecretTypeBasicAuth,
		Data: map[string][]byte{
			account.UsernameKey: []byte(tmpl.Spec.Username),
			account.PasswordKey: []byte(stored),
		},
	}
	account.WriteRecord(sec, rec.Apply(corev1alpha1.CredentialOperationCreate, r.now(), CredentialControllerActor))
	if err := controllerutil.SetControllerReference(tmpl, sec, r.Scheme); err != nil {
		return nil, err
	}
	return sec, nil
}

func (r *CredentialTemplateReconciler) seed(ctx context.Context, tmpl *cozyv1alpha1.CredentialTemplate) (*corev1.Secret, error) {
	sec, err := r.newSecret(tmpl, account.Record{})
	if err != nil {
		return nil, err
	}
	if err := r.Create(ctx, sec); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// A mint created it in between; the next reconcile mirrors it.
			return r.secret(ctx, tmpl)
		}
		return nil, err
	}
	return sec, nil
}

// reseed replaces a Secret whose value is stored in another format than the
// template now names: a stored value cannot be converted without the
// password, so the account starts over as NotIssued.
func (r *CredentialTemplateReconciler) reseed(ctx context.Context, tmpl *cozyv1alpha1.CredentialTemplate, cur *corev1.Secret) (*corev1.Secret, error) {
	sec, err := r.newSecret(tmpl, account.ReadRecord(cur))
	if err != nil {
		return nil, err
	}
	sec.ResourceVersion = cur.ResourceVersion
	if err := r.Update(ctx, sec); err != nil {
		return nil, err
	}
	if r.Recorder != nil {
		r.Recorder.Eventf(tmpl, corev1.EventTypeWarning, reasonCredentialReset,
			"format changed from %q to %q; the account was given a new password nobody holds, mint one", cur.Annotations[account.FormatAnnotation], tmpl.Spec.Format.Name)
	}
	return sec, nil
}

// alignLabels brings the Secret's labels in line with the template's, without
// touching its value or record.
func (r *CredentialTemplateReconciler) alignLabels(ctx context.Context, tmpl *cozyv1alpha1.CredentialTemplate, sec *corev1.Secret) (*corev1.Secret, error) {
	want := account.Labels(tmpl.Spec.Secret.Labels)
	if equality.Semantic.DeepEqual(sec.Labels, want) {
		return sec, nil
	}
	updated := sec.DeepCopy()
	updated.Labels = want
	if err := r.Update(ctx, updated); err != nil {
		return nil, err
	}
	return updated, nil
}

// adoptable reports whether an unowned Secret at the template's name is one
// the conversion wrote for this account: the account marker, no controller,
// and the same username.
func adoptable(sec *corev1.Secret, tmpl *cozyv1alpha1.CredentialTemplate) bool {
	return account.IsAccountSecret(sec) &&
		metav1.GetControllerOf(sec) == nil &&
		string(sec.Data[account.UsernameKey]) == tmpl.Spec.Username
}

// adopt takes over a Secret the conversion wrote, keeping its value and
// record. A Secret without a format annotation was written in the format its
// kind converted to, which the template names.
func (r *CredentialTemplateReconciler) adopt(ctx context.Context, tmpl *cozyv1alpha1.CredentialTemplate, cur *corev1.Secret) (*corev1.Secret, error) {
	sec := cur.DeepCopy()
	if err := controllerutil.SetControllerReference(tmpl, sec, r.Scheme); err != nil {
		return nil, err
	}
	if sec.Annotations == nil {
		sec.Annotations = map[string]string{}
	}
	if sec.Annotations[account.FormatAnnotation] == "" {
		sec.Annotations[account.FormatAnnotation] = tmpl.Spec.Format.Name
	}
	sec.Labels = account.Labels(tmpl.Spec.Secret.Labels)
	if err := r.Update(ctx, sec); err != nil {
		return nil, err
	}
	if r.Recorder != nil {
		r.Recorder.Event(tmpl, corev1.EventTypeNormal, reasonCredentialAdopted, "adopted the account Secret the conversion wrote")
	}
	return sec, nil
}

// mirror copies the record of the account Secret into the template's status;
// nil clears it.
func mirror(status *cozyv1alpha1.CredentialTemplateStatus, sec *corev1.Secret) {
	if sec == nil {
		status.SecretResourceVersion = ""
		status.Password = cozyv1alpha1.CredentialTemplatePasswordState{Origin: string(corev1alpha1.CredentialOriginNotIssued)}
		status.History = nil
		return
	}
	rec := account.ReadRecord(sec)
	status.SecretResourceVersion = sec.ResourceVersion
	status.Password = cozyv1alpha1.CredentialTemplatePasswordState{Origin: string(rec.Origin), By: rec.By}
	if rec.Origin == "" {
		status.Password.Origin = string(corev1alpha1.CredentialOriginNotIssued)
	}
	if !rec.At.IsZero() {
		at := rec.At
		status.Password.At = &at
	}
	status.History = nil
	for _, h := range rec.History {
		status.History = append(status.History, cozyv1alpha1.CredentialTemplateHistoryEntry{Operation: string(h.Operation), At: h.At, By: h.By})
	}
}

func (r *CredentialTemplateReconciler) writeStatus(ctx context.Context, tmpl *cozyv1alpha1.CredentialTemplate, status *cozyv1alpha1.CredentialTemplateStatus) error {
	if equality.Semantic.DeepEqual(&tmpl.Status, status) {
		return nil
	}
	updated := tmpl.DeepCopy()
	updated.Status = *status
	return r.Status().Update(ctx, updated)
}

// SetupWithManager registers the reconciler. accountSecrets is a metadata
// cache of Secrets, so a change to an account Secret (a mint, a revoke)
// reaches the template that owns it without any Secret data being cached.
func (r *CredentialTemplateReconciler) SetupWithManager(mgr ctrl.Manager, accountSecrets cache.Cache) error {
	secretMeta := &metav1.PartialObjectMetadata{}
	secretMeta.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
	isAccount := predicate.NewTypedPredicateFuncs(func(o *metav1.PartialObjectMetadata) bool {
		return o.GetLabels()[account.AccountLabel] == "true"
	})
	owner := handler.TypedEnqueueRequestForOwner[*metav1.PartialObjectMetadata](
		mgr.GetScheme(), mgr.GetRESTMapper(), &cozyv1alpha1.CredentialTemplate{}, handler.OnlyControllerOwner())
	return ctrl.NewControllerManagedBy(mgr).
		Named("credentialtemplate").
		For(&cozyv1alpha1.CredentialTemplate{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		WatchesRawSource(source.Kind(accountSecrets, secretMeta, owner, isAccount)).
		Complete(r)
}

// CredentialValuesReconciler keeps the valuesFrom entries of a HelmRelease in
// step with the templates of its release that name a valuesPath, for an
// engine whose operator reads no Secret. It is keyed by the HelmRelease but
// watches only templates, so helm-controller's status writes do not wake it.
type CredentialValuesReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;update;patch

func (r *CredentialValuesReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	hr := &helmv2.HelmRelease{}
	if err := r.Get(ctx, req.NamespacedName, hr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	templates := &cozyv1alpha1.CredentialTemplateList{}
	if err := r.List(ctx, templates, client.InNamespace(hr.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	want := DesiredValuesFrom(hr, templates.Items)
	if equality.Semantic.DeepEqual(hr.Spec.ValuesFrom, want) {
		return ctrl.Result{}, nil
	}
	updated := hr.DeepCopy()
	updated.Spec.ValuesFrom = want
	return ctrl.Result{}, r.Update(ctx, updated)
}

// DesiredValuesFrom returns the valuesFrom of a HelmRelease with its account
// entries replaced by one per live template of the release that names a
// valuesPath, ordered by template name; every other entry stays where it is.
func DesiredValuesFrom(hr *helmv2.HelmRelease, templates []cozyv1alpha1.CredentialTemplate) []helmv2.ValuesReference {
	var out []helmv2.ValuesReference
	for _, ref := range hr.Spec.ValuesFrom {
		if !account.IsAccountValuesFrom(ref) {
			out = append(out, ref)
		}
	}
	var mine []cozyv1alpha1.CredentialTemplate
	for _, t := range templates {
		if t.Annotations[account.ReleaseNameAnnotation] == hr.Name && t.Spec.ValuesPath != "" && t.DeletionTimestamp.IsZero() {
			mine = append(mine, t)
		}
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Name < mine[j].Name })
	for _, t := range mine {
		out = append(out, account.ValuesFrom(t.Name+account.SecretSuffix, t.Spec.ValuesPath, t.Spec.Username))
	}
	return out
}

// SetupWithManager registers the reconciler.
func (r *CredentialValuesReconciler) SetupWithManager(mgr ctrl.Manager) error {
	toRelease := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		release := o.GetAnnotations()[account.ReleaseNameAnnotation]
		if release == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: o.GetNamespace(), Name: release}}}
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("credentialvalues").
		Watches(&cozyv1alpha1.CredentialTemplate{}, toRelease).
		Complete(r)
}

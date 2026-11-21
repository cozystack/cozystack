// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
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
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	cosiapi "sigs.k8s.io/container-object-storage-interface-api/apis"
	cosiv1alpha1 "sigs.k8s.io/container-object-storage-interface-api/apis/objectstorage/v1alpha1"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	"github.com/cozystack/cozystack/pkg/credentials/account"
	"github.com/cozystack/cozystack/pkg/credentials/format"
)

// +kubebuilder:rbac:groups=objectstorage.k8s.io,resources=bucketaccesses,verbs=get;list;watch;create;update;delete

const (
	// cosiRequeue is how soon the reconciler looks again while the COSI
	// sidecar revokes or grants. Both end in a BucketAccess change the
	// reconciler watches; the requeue covers the Secret the sidecar writes,
	// which it does not.
	cosiRequeue = 2 * time.Second
	// bucketInfoKey is the one data key of the Secret the COSI sidecar
	// writes for a BucketAccess.
	bucketInfoKey = "BucketInfo"
	// bucketInfoPrefix is how the sidecar names the BucketInfo it writes
	// for a BucketAccess, followed by that BucketAccess's UID, which tells a
	// Secret of the live grant from one a deleted BucketAccess left behind.
	bucketInfoPrefix = "bc-"
	// s3Protocol is the protocol the bucket chart has always requested.
	s3Protocol = cosiv1alpha1.Protocol("s3")
)

// COSICredentialReconciler keeps the account of a cosi CredentialTemplate,
// whose keys the COSI driver issues: it creates and owns the BucketAccess the
// template names, adopts one the conversion marked, and copies the keys of
// the live grant into the account Secret. A mint or a revoke reaches it as a
// request id the API writes with the Secret; a BucketAccess not created for
// the current request is deleted, so the sidecar revokes its identity, and a
// new one is created for it. CredentialTemplateReconciler skips these
// templates. See design-proposals/user-secrets-api in cozystack/community.
//
// Secrets are read through Reader, uncached, for the reason
// CredentialTemplateReconciler gives.
type COSICredentialReconciler struct {
	client.Client
	Reader   client.Reader
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Now      func() time.Time
}

func (r *COSICredentialReconciler) now() metav1.Time {
	if r.Now != nil {
		return metav1.NewTime(r.Now())
	}
	return metav1.Now()
}

func isCOSI(tmpl *cozyv1alpha1.CredentialTemplate) bool {
	return tmpl.Spec.Format.Name == format.COSI
}

func (r *COSICredentialReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	tmpl := &cozyv1alpha1.CredentialTemplate{}
	if err := r.Get(ctx, req.NamespacedName, tmpl); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.cleanup(ctx, req.NamespacedName)
		}
		return ctrl.Result{}, err
	}
	if !isCOSI(tmpl) || tmpl.Spec.BucketAccess == nil || !tmpl.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	status := tmpl.Status.DeepCopy()
	status.ObservedGeneration = tmpl.Generation

	sec, err := r.accountSecret(ctx, tmpl)
	if err != nil {
		return ctrl.Result{}, err
	}
	if sec != nil && !metav1.IsControlledBy(sec, tmpl) {
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:    cozyv1alpha1.CredentialTemplateSecretConflict,
			Status:  metav1.ConditionTrue,
			Reason:  "ForeignSecret",
			Message: fmt.Sprintf("Secret %s exists and was not created for this template; remove it to let the account be issued", sec.Name),
		})
		mirror(status, nil)
		return ctrl.Result{}, writeTemplateStatus(ctx, r.Client, tmpl, status)
	}
	meta.RemoveStatusCondition(&status.Conditions, cozyv1alpha1.CredentialTemplateSecretConflict)
	request := ""
	if sec != nil {
		request = sec.Annotations[account.RequestAnnotation]
	}

	ba, err := r.bucketAccess(ctx, tmpl)
	if err != nil {
		return ctrl.Result{}, err
	}
	switch {
	case ba == nil:
		return ctrl.Result{RequeueAfter: cosiRequeue}, r.createBucketAccess(ctx, tmpl, request)
	case !ba.DeletionTimestamp.IsZero():
		// The sidecar is revoking it; a new one follows once it is gone.
		return ctrl.Result{RequeueAfter: cosiRequeue}, nil
	case !metav1.IsControlledBy(ba, tmpl):
		if !bucketAccessAdoptable(ba, tmpl) {
			meta.SetStatusCondition(&status.Conditions, metav1.Condition{
				Type:    cozyv1alpha1.CredentialTemplateBucketAccessConflict,
				Status:  metav1.ConditionTrue,
				Reason:  "ForeignBucketAccess",
				Message: fmt.Sprintf("BucketAccess %s exists and was not created for this template", ba.Name),
			})
			mirror(status, sec)
			return ctrl.Result{}, writeTemplateStatus(ctx, r.Client, tmpl, status)
		}
		if ba, err = r.adopt(ctx, tmpl, ba); err != nil {
			return ctrl.Result{}, err
		}
	}
	meta.RemoveStatusCondition(&status.Conditions, cozyv1alpha1.CredentialTemplateBucketAccessConflict)

	if stale(ba, tmpl, request) {
		// Deleting it makes the sidecar revoke the identity behind the keys
		// the account held; the next pass creates one for the request.
		if err := r.Delete(ctx, ba); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: cosiRequeue}, nil
	}
	if !ba.Status.AccessGranted {
		return ctrl.Result{RequeueAfter: cosiRequeue}, nil
	}
	keys, err := r.issuedKeys(ctx, ba)
	if err != nil {
		return ctrl.Result{}, err
	}
	if keys == nil {
		return ctrl.Result{RequeueAfter: cosiRequeue}, nil
	}
	stored, err := r.store(ctx, tmpl, sec, ba, keys, request)
	if err != nil {
		return ctrl.Result{}, err
	}
	if stored == nil {
		// A mint created the Secret in between; serve its request next.
		return ctrl.Result{Requeue: true}, nil
	}
	mirror(status, stored)
	return ctrl.Result{}, writeTemplateStatus(ctx, r.Client, tmpl, status)
}

func (r *COSICredentialReconciler) accountSecret(ctx context.Context, tmpl *cozyv1alpha1.CredentialTemplate) (*corev1.Secret, error) {
	sec := &corev1.Secret{}
	err := r.Reader.Get(ctx, types.NamespacedName{Namespace: tmpl.Namespace, Name: tmpl.Name + account.SecretSuffix}, sec)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return sec, nil
}

func (r *COSICredentialReconciler) bucketAccess(ctx context.Context, tmpl *cozyv1alpha1.CredentialTemplate) (*cosiv1alpha1.BucketAccess, error) {
	ba := &cosiv1alpha1.BucketAccess{}
	err := r.Get(ctx, types.NamespacedName{Namespace: tmpl.Namespace, Name: tmpl.Spec.BucketAccess.Name}, ba)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return ba, nil
}

// createBucketAccess creates the BucketAccess of a template for a request.
// The request annotation is present even when empty, which tells a
// BucketAccess the controller created from one it adopted.
func (r *COSICredentialReconciler) createBucketAccess(ctx context.Context, tmpl *cozyv1alpha1.CredentialTemplate, request string) error {
	spec := tmpl.Spec.BucketAccess
	ba := &cosiv1alpha1.BucketAccess{
		ObjectMeta: metav1.ObjectMeta{
			Name:        spec.Name,
			Namespace:   tmpl.Namespace,
			Annotations: map[string]string{account.RequestAnnotation: request},
		},
		Spec: cosiv1alpha1.BucketAccessSpec{
			BucketClaimName:       spec.BucketClaimName,
			BucketAccessClassName: spec.BucketAccessClassName,
			CredentialsSecretName: spec.Name,
			Protocol:              s3Protocol,
		},
	}
	if err := controllerutil.SetControllerReference(tmpl, ba, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, ba); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// bucketAccessAdoptable reports whether a BucketAccess the controller did not
// create is one the conversion marked for this template: the marker, no
// controller, and the template's own claim and class.
func bucketAccessAdoptable(ba *cosiv1alpha1.BucketAccess, tmpl *cozyv1alpha1.CredentialTemplate) bool {
	return ba.Labels[account.AdoptLabel] == "true" &&
		metav1.GetControllerOf(ba) == nil &&
		ba.Spec.BucketClaimName == tmpl.Spec.BucketAccess.BucketClaimName &&
		ba.Spec.BucketAccessClassName == tmpl.Spec.BucketAccess.BucketAccessClassName
}

// adopt takes over a BucketAccess the chart rendered before the conversion,
// keeping its grant and so its keys. It carries no request annotation, which
// is how the first copy of its keys is recorded as migrated.
func (r *COSICredentialReconciler) adopt(ctx context.Context, tmpl *cozyv1alpha1.CredentialTemplate, ba *cosiv1alpha1.BucketAccess) (*cosiv1alpha1.BucketAccess, error) {
	updated := ba.DeepCopy()
	if err := controllerutil.SetControllerReference(tmpl, updated, r.Scheme); err != nil {
		return nil, err
	}
	if err := r.Update(ctx, updated); err != nil {
		return nil, err
	}
	if r.Recorder != nil {
		r.Recorder.Event(tmpl, corev1.EventTypeNormal, reasonCredentialAdopted, "adopted the BucketAccess the conversion marked, keeping its keys")
	}
	return updated, nil
}

// stale reports whether a BucketAccess no longer serves its template: it was
// created for another request than the account Secret's latest, or for
// another claim or class.
func stale(ba *cosiv1alpha1.BucketAccess, tmpl *cozyv1alpha1.CredentialTemplate, request string) bool {
	return ba.Annotations[account.RequestAnnotation] != request ||
		ba.Spec.BucketClaimName != tmpl.Spec.BucketAccess.BucketClaimName ||
		ba.Spec.BucketAccessClassName != tmpl.Spec.BucketAccess.BucketAccessClassName
}

// s3Keys is one issued key pair.
type s3Keys struct {
	accessKeyID string
	secretKey   string
}

// issuedKeys reads the keys of a granted BucketAccess from the Secret the
// sidecar wrote for it; nil while that Secret is missing, or is one an
// earlier BucketAccess of the same name left behind.
func (r *COSICredentialReconciler) issuedKeys(ctx context.Context, ba *cosiv1alpha1.BucketAccess) (*s3Keys, error) {
	src := &corev1.Secret{}
	err := r.Reader.Get(ctx, types.NamespacedName{Namespace: ba.Namespace, Name: ba.Spec.CredentialsSecretName}, src)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	info := cosiapi.BucketInfo{}
	if err := json.Unmarshal(src.Data[bucketInfoKey], &info); err != nil {
		return nil, fmt.Errorf("secret %s/%s: decode %s: %w", src.Namespace, src.Name, bucketInfoKey, err)
	}
	if info.Name != bucketInfoPrefix+string(ba.UID) || info.Spec.S3 == nil ||
		info.Spec.S3.AccessKeyID == "" || info.Spec.S3.AccessSecretKey == "" {
		return nil, nil
	}
	return &s3Keys{accessKeyID: info.Spec.S3.AccessKeyID, secretKey: info.Spec.S3.AccessSecretKey}, nil
}

// store writes the keys into the account Secret, marked as issued for the
// request, and returns the Secret as written; nil when a mint created it in
// between. A new Secret records a Create, or a Migrate when the keys belong
// to an adopted BucketAccess; an existing one keeps the record the mint or
// revoke wrote.
func (r *COSICredentialReconciler) store(ctx context.Context, tmpl *cozyv1alpha1.CredentialTemplate, sec *corev1.Secret, ba *cosiv1alpha1.BucketAccess, keys *s3Keys, request string) (*corev1.Secret, error) {
	data := map[string][]byte{
		account.UsernameKey:    []byte(tmpl.Spec.Username),
		account.PasswordKey:    []byte(keys.secretKey),
		account.AccessKeyIDKey: []byte(keys.accessKeyID),
	}
	labels := account.Labels(tmpl.Spec.Secret.Labels)
	if sec == nil {
		op := corev1alpha1.CredentialOperationCreate
		if _, created := ba.Annotations[account.RequestAnnotation]; !created {
			op = corev1alpha1.CredentialOperationMigrate
		}
		sec = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      tmpl.Name + account.SecretSuffix,
				Namespace: tmpl.Namespace,
				Labels:    labels,
				Annotations: map[string]string{
					account.FormatAnnotation:  format.COSI,
					account.GrantedAnnotation: request,
				},
			},
			Type: corev1.SecretTypeBasicAuth,
			Data: data,
		}
		account.WriteRecord(sec, account.Record{}.Apply(op, r.now(), CredentialControllerActor))
		if err := controllerutil.SetControllerReference(tmpl, sec, r.Scheme); err != nil {
			return nil, err
		}
		if err := r.Create(ctx, sec); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return nil, nil
			}
			return nil, err
		}
		return sec, nil
	}
	if sec.Annotations[account.GrantedAnnotation] == request && sec.Annotations[account.FormatAnnotation] == format.COSI &&
		equalData(sec.Data, data) && equalStrings(sec.Labels, labels) {
		return sec, nil
	}
	updated := sec.DeepCopy()
	updated.Data = data
	updated.Labels = labels
	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	updated.Annotations[account.FormatAnnotation] = format.COSI
	updated.Annotations[account.GrantedAnnotation] = request
	// Written under the version read at the start of this pass: a mint in
	// between fails it, and the next pass serves that mint's request.
	if err := r.Update(ctx, updated); err != nil {
		return nil, err
	}
	return updated, nil
}

func equalData(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || string(v) != string(w) {
			return false
		}
	}
	return true
}

func equalStrings(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || v != w {
			return false
		}
	}
	return true
}

// cleanup removes what a deleted cosi template owned: its BucketAccess, whose
// deletion makes the sidecar revoke the identity, and its account Secret. The
// garbage collector would remove both through their owner reference; doing it
// here revokes the keys without waiting for it.
func (r *COSICredentialReconciler) cleanup(ctx context.Context, nn types.NamespacedName) error {
	accesses := &cosiv1alpha1.BucketAccessList{}
	if err := r.List(ctx, accesses, client.InNamespace(nn.Namespace)); err != nil {
		return err
	}
	for i := range accesses.Items {
		ba := &accesses.Items[i]
		if ownedByTemplate(ba, nn.Name) && ba.DeletionTimestamp.IsZero() {
			if err := r.Delete(ctx, ba); client.IgnoreNotFound(err) != nil {
				return err
			}
		}
	}
	sec := &corev1.Secret{}
	err := r.Reader.Get(ctx, types.NamespacedName{Namespace: nn.Namespace, Name: nn.Name + account.SecretSuffix}, sec)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !ownedByTemplate(sec, nn.Name) || sec.Annotations[account.FormatAnnotation] != format.COSI {
		return nil
	}
	return client.IgnoreNotFound(r.Delete(ctx, sec))
}

func ownedByTemplate(o metav1.Object, name string) bool {
	ref := metav1.GetControllerOf(o)
	return ref != nil && ref.Kind == "CredentialTemplate" && ref.APIVersion == cozyv1alpha1.GroupVersion.String() && ref.Name == name
}

// SetupWithManager registers the reconciler. accountSecrets is the same
// metadata cache of Secrets CredentialTemplateReconciler watches; only cosi
// account Secrets reach this reconciler.
func (r *COSICredentialReconciler) SetupWithManager(mgr ctrl.Manager, accountSecrets cache.Cache) error {
	cosiTemplate := predicate.NewPredicateFuncs(func(o client.Object) bool {
		tmpl, ok := o.(*cozyv1alpha1.CredentialTemplate)
		return ok && isCOSI(tmpl)
	})
	secretMeta := &metav1.PartialObjectMetadata{}
	secretMeta.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
	isCOSIAccount := predicate.NewTypedPredicateFuncs(func(o *metav1.PartialObjectMetadata) bool {
		return o.GetLabels()[account.AccountLabel] == "true" && o.GetAnnotations()[account.FormatAnnotation] == format.COSI
	})
	owner := handler.TypedEnqueueRequestForOwner[*metav1.PartialObjectMetadata](
		mgr.GetScheme(), mgr.GetRESTMapper(), &cozyv1alpha1.CredentialTemplate{}, handler.OnlyControllerOwner())
	return ctrl.NewControllerManagedBy(mgr).
		Named("credentialtemplate-cosi").
		For(&cozyv1alpha1.CredentialTemplate{}, builder.WithPredicates(predicate.GenerationChangedPredicate{}, cosiTemplate)).
		Owns(&cosiv1alpha1.BucketAccess{}).
		WatchesRawSource(source.Kind(accountSecrets, secretMeta, owner, isCOSIAccount)).
		Complete(r)
}

// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
)

// +kubebuilder:rbac:groups=objectstorage.k8s.io,resources=bucketaccesses,verbs=get;list;watch;create;update;delete

const (
	// cosiRequeue is how soon the reconciler looks again while the COSI
	// sidecar revokes or grants. Both end in a BucketAccess change the
	// reconciler watches; the requeue covers the Secret the sidecar writes,
	// which it does not.
	cosiRequeue = 2 * time.Second
	// cosiGrantRequeue is how soon the reconciler looks again at a
	// BucketAccess it waits to delete until the sidecar finishes granting it.
	// Finishing adds a finalizer, a change the reconciler watches, so this
	// only backs the watch up.
	cosiGrantRequeue = 30 * time.Second
	// bucketAccessFinalizer is what the COSI sidecar puts on a BucketAccess
	// once the driver has granted it and the sidecar has written its Secret,
	// before it reports AccessGranted; from then on a delete runs the driver
	// revoke and removes that Secret (objectstorage-sidecar v0.2.2,
	// sidecar/pkg/bucketaccess/bucketaccess_controller.go, Add and
	// deleteBucketAccessOp; client/apis/objectstorage/consts BAFinalizer).
	bucketAccessFinalizer = "cosi.objectstorage.k8s.io/bucketaccess-protection"
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

// COSICredentialReconciler keeps the account of an engine-issued
// CredentialTemplate, whose keys the COSI driver issues: it creates and owns
// the BucketAccess the template names, adopts one the conversion marked, and
// copies the keys of the live grant into the account Secret. A mint or a
// revoke reaches it as a request id the API writes with the Secret. A
// BucketAccess not created for the current mint is deleted, so the sidecar
// revokes its identity, and a new one is created for the mint. A revoke
// deletes it and creates none, so the account holds no keys until the next
// mint, and the Secret is marked served once the BucketAccess is gone. While
// the template lives, a BucketAccess is deleted only once the sidecar has
// finalized its grant: a delete before that skips the driver revoke and leaves
// the identity behind. The cleanup of a deleted template and the garbage
// collection through the owner reference do not check this, so a template
// deleted inside the grant window can leave that identity behind. The
// template's status mirrors the account Secret as the latest request left it,
// granted or not, so a request that never completes does not block the next
// one. A grant the template's claim or class no longer matches, after a change
// of the user's readonly flag or of the storage pool, is recorded as a revoke
// by the controller: the BucketAccess goes, none replaces it, and the account
// holds no keys until the next mint, which is granted under the new class.
// CredentialTemplateReconciler skips these templates. See
// design-proposals/user-secrets-api in cozystack/community.
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

// engineIssued reports whether the COSI driver issues a template's keys,
// which a template says by naming a BucketAccess, whatever its format.
func engineIssued(tmpl *cozyv1alpha1.CredentialTemplate) bool {
	return tmpl.Spec.BucketAccess != nil
}

func (r *COSICredentialReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	tmpl := &cozyv1alpha1.CredentialTemplate{}
	if err := r.Get(ctx, req.NamespacedName, tmpl); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.cleanup(ctx, req.NamespacedName)
		}
		return ctrl.Result{}, err
	}
	if !engineIssued(tmpl) || !tmpl.DeletionTimestamp.IsZero() {
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

	ba, err := r.bucketAccess(ctx, tmpl)
	if err != nil {
		return ctrl.Result{}, err
	}
	request := ""
	switch {
	case sec != nil:
		request = sec.Annotations[account.RequestAnnotation]
	case ba != nil && metav1.IsControlledBy(ba, tmpl):
		// The account Secret is gone while its grant lives on: the Secret is
		// written again from that grant, rather than the BucketAccess being
		// re-created under the clients that hold its keys.
		request = ba.Annotations[account.RequestAnnotation]
	}
	revoked := sec != nil && request != "" && account.ReadRecord(sec).Origin == corev1alpha1.CredentialOriginRevoked
	// waiting mirrors the account Secret as the latest request left it, so
	// the next mint or revoke writes over that version instead of failing on
	// the one before a request that has not completed.
	waiting := func(after time.Duration) (ctrl.Result, error) {
		mirror(status, sec)
		return ctrl.Result{RequeueAfter: after}, writeTemplateStatus(ctx, r.Client, tmpl, status)
	}

	switch {
	case ba == nil && revoked:
		served, err := r.store(ctx, tmpl, sec, nil, nil, request)
		if err != nil {
			return ctrl.Result{}, err
		}
		mirror(status, served)
		return ctrl.Result{}, writeTemplateStatus(ctx, r.Client, tmpl, status)
	case ba == nil:
		if err := r.createBucketAccess(ctx, tmpl, request); err != nil {
			return ctrl.Result{}, err
		}
		return waiting(cosiRequeue)
	case !ba.DeletionTimestamp.IsZero():
		// The sidecar is revoking it.
		return waiting(cosiRequeue)
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

	if sec != nil && !revoked && ba.Annotations[account.RequestAnnotation] == request && grantChanged(ba, tmpl) {
		revokedSec, err := r.revokeForChange(ctx, tmpl, sec)
		if err != nil {
			return ctrl.Result{}, err
		}
		sec = revokedSec
		return waiting(cosiRequeue)
	}
	if revoked || stale(ba, tmpl, request) {
		if !controllerutil.ContainsFinalizer(ba, bucketAccessFinalizer) {
			// The driver may already have granted an identity the sidecar
			// has not finalized yet; deleting now would skip its revoke.
			return waiting(cosiGrantRequeue)
		}
		// Deleting it makes the sidecar revoke the identity behind the keys
		// the account held; for a mint, the next pass creates one for the
		// request.
		if err := r.Delete(ctx, ba, client.Preconditions{UID: &ba.UID}); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		return waiting(cosiRequeue)
	}
	if !ba.Status.AccessGranted {
		return waiting(cosiRequeue)
	}
	keys, err := r.issuedKeys(ctx, ba)
	if err != nil {
		return ctrl.Result{}, err
	}
	if keys == nil {
		return waiting(cosiRequeue)
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
	return ba.Annotations[account.RequestAnnotation] != request || grantChanged(ba, tmpl)
}

// grantChanged reports whether a BucketAccess was created for another claim
// or class than the template now names, as after a change of the user's
// readonly flag or of the bucket's storage pool.
func grantChanged(ba *cosiv1alpha1.BucketAccess, tmpl *cozyv1alpha1.CredentialTemplate) bool {
	return ba.Spec.BucketClaimName != tmpl.Spec.BucketAccess.BucketClaimName ||
		ba.Spec.BucketAccessClassName != tmpl.Spec.BucketAccess.BucketAccessClassName
}

// revokeForChange records a grant the template no longer matches as a revoke
// by the controller and returns the Secret as written. The account Secret
// loses its keys and gets a request id of its own, as a revoke through the API
// writes it, so the revoke path deletes the BucketAccess and creates none:
// the new class takes effect with the next mint, whose keys are the first
// anyone is shown. Re-granting at once instead would leave keys nobody was
// shown behind a record that says the last mint issued the live ones. A
// request the Secret already carries is served first: a pending mint creates
// its BucketAccess with the new class anyway.
func (r *COSICredentialReconciler) revokeForChange(ctx context.Context, tmpl *cozyv1alpha1.CredentialTemplate, sec *corev1.Secret) (*corev1.Secret, error) {
	id, err := requestID()
	if err != nil {
		return nil, err
	}
	updated := sec.DeepCopy()
	updated.Data = map[string][]byte{account.UsernameKey: []byte(tmpl.Spec.Username)}
	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	updated.Annotations[account.RequestAnnotation] = id
	account.WriteRecord(updated, account.ReadRecord(sec).Apply(corev1alpha1.CredentialOperationRevoke, r.now(), CredentialControllerActor))
	// Written under the version read at the start of this pass: a mint in
	// between fails it, and the next pass serves that mint instead.
	if err := r.Update(ctx, updated); err != nil {
		return nil, err
	}
	if r.Recorder != nil {
		r.Recorder.Event(tmpl, corev1.EventTypeNormal, reasonCredentialRevoked,
			"the bucket access class or claim changed: revoked the keys of the old grant; mint to get keys under the new one")
	}
	return updated, nil
}

func requestID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
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

// store writes the keys into the account Secret, marked as served for the
// request, and returns the Secret as written; nil when a mint created it in
// between. Without keys, for a revoke whose BucketAccess is gone, the Secret
// keeps the user name only. A new Secret records a Create, or a Migrate when
// the keys belong to an adopted BucketAccess, and carries the request of the
// BucketAccess it was written from; an existing one keeps the record the mint
// or revoke wrote.
func (r *COSICredentialReconciler) store(ctx context.Context, tmpl *cozyv1alpha1.CredentialTemplate, sec *corev1.Secret, ba *cosiv1alpha1.BucketAccess, keys *s3Keys, request string) (*corev1.Secret, error) {
	data := map[string][]byte{account.UsernameKey: []byte(tmpl.Spec.Username)}
	if keys != nil {
		data[account.PasswordKey] = []byte(keys.secretKey)
		data[account.AccessKeyIDKey] = []byte(keys.accessKeyID)
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
					account.FormatAnnotation:       tmpl.Spec.Format.Name,
					account.BucketAccessAnnotation: tmpl.Spec.BucketAccess.Name,
					account.GrantedAnnotation:      request,
				},
			},
			Type: corev1.SecretTypeBasicAuth,
			Data: data,
		}
		if request != "" {
			sec.Annotations[account.RequestAnnotation] = request
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
	if sec.Annotations[account.GrantedAnnotation] == request && sec.Annotations[account.FormatAnnotation] == tmpl.Spec.Format.Name &&
		sec.Annotations[account.BucketAccessAnnotation] == tmpl.Spec.BucketAccess.Name &&
		equalData(sec.Data, data) && equalStrings(sec.Labels, labels) {
		return sec, nil
	}
	updated := sec.DeepCopy()
	updated.Data = data
	updated.Labels = labels
	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	updated.Annotations[account.FormatAnnotation] = tmpl.Spec.Format.Name
	updated.Annotations[account.BucketAccessAnnotation] = tmpl.Spec.BucketAccess.Name
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

// cleanup removes what a deleted engine-issued template owned: its
// BucketAccess, whose deletion makes the sidecar revoke the identity, and its
// account Secret. The garbage collector would remove both through their owner
// reference; doing it here revokes the keys without waiting for it. It deletes
// a BucketAccess the sidecar has not finalized as well, since the garbage
// collector would.
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
	if !ownedByTemplate(sec, nn.Name) || !account.IsEngineIssued(sec) {
		return nil
	}
	return client.IgnoreNotFound(r.Delete(ctx, sec))
}

func ownedByTemplate(o metav1.Object, name string) bool {
	ref := metav1.GetControllerOf(o)
	return ref != nil && ref.Kind == "CredentialTemplate" && ref.APIVersion == cozyv1alpha1.GroupVersion.String() && ref.Name == name
}

// SetupWithManager registers the reconciler. accountSecrets is the same
// metadata cache of Secrets CredentialTemplateReconciler watches; only
// engine-issued account Secrets reach this reconciler.
func (r *COSICredentialReconciler) SetupWithManager(mgr ctrl.Manager, accountSecrets cache.Cache) error {
	cosiTemplate := predicate.NewPredicateFuncs(func(o client.Object) bool {
		tmpl, ok := o.(*cozyv1alpha1.CredentialTemplate)
		return ok && engineIssued(tmpl)
	})
	secretMeta := &metav1.PartialObjectMetadata{}
	secretMeta.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
	isCOSIAccount := predicate.NewTypedPredicateFuncs(func(o *metav1.PartialObjectMetadata) bool {
		return o.GetLabels()[account.AccountLabel] == "true" && o.GetAnnotations()[account.BucketAccessAnnotation] != ""
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

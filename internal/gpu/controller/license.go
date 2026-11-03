/*
Copyright 2026 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"maps"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
)

const (
	reasonSecretFound     = "SecretFound"
	reasonSecretNotFound  = "SecretNotFound"
	reasonTokenKeyMissing = "TokenKeyMissing"
)

// LicenseReconciler copies the hoster's license Secret of one GPUClass
// into every namespace entitled to vGPU, and deletes the copies a
// namespace, the class or its license no longer calls for.
type LicenseReconciler struct {
	Client client.Client
	// Reader is uncached and reads the hoster's Secret and the copies:
	// the manager's Secret cache is scoped to other reconcilers' Secrets,
	// and the token stays out of every cache.
	Reader client.Reader
	// Secrets lists Secret metadata, without data, to find the copies.
	Secrets client.Reader
	// Enabled follows the desired GPU-for-VMs configuration, not whether
	// a retained GPU operator Package still runs. When false, it writes
	// no copy, deletes every copy and clears the computed class status.
	// Existing VMs must stop using the platform license before disabling
	// GPU-for-VMs: a retained Package does not retain their license copies.
	// Cleanup needs only the controller's Secret and GPUClass status writes.
	Enabled bool
}

func (r *LicenseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	class := &cozyv1alpha1.GPUClass{}
	if err := r.Client.Get(ctx, req.NamespacedName, class); apierrors.IsNotFound(err) {
		class = nil
	} else if err != nil {
		return ctrl.Result{}, err
	}

	var license *cozyv1alpha1.GPUClassLicense
	if r.Enabled && class != nil {
		license = class.Spec.License
	}
	entitled := sets.New[string]()
	var data map[string][]byte
	var condition *metav1.Condition
	if license != nil {
		var nsList corev1.NamespaceList
		if err := r.Client.List(ctx, &nsList, client.MatchingLabels{gpu.LabelNamespaceVGPU: gpu.LabelValueTrue}); err != nil {
			return ctrl.Result{}, err
		}
		for i := range nsList.Items {
			// A terminating namespace refuses new objects until it is gone,
			// and the hoster's namespace never holds a copy.
			if ns := &nsList.Items[i]; ns.DeletionTimestamp == nil && ns.Name != gpu.LicenseSecretNamespace {
				entitled.Insert(ns.Name)
			}
		}
		var err error
		data, condition, err = r.desired(ctx, class)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	var errs []error
	if data != nil {
		for ns := range entitled {
			errs = append(errs, r.write(ctx, ns, req.Name, data))
		}
	}
	errs = append(errs, r.prune(ctx, req.Name, entitled))
	if class != nil {
		errs = append(errs, r.setStatus(ctx, class, condition))
	}
	return ctrl.Result{}, errors.Join(errs...)
}

// desired returns the data of every copy, or nil and a True
// LicenseSecretMissing while the hoster's Secret cannot provide a token.
func (r *LicenseReconciler) desired(ctx context.Context, class *cozyv1alpha1.GPUClass) (map[string][]byte, *metav1.Condition, error) {
	license := class.Spec.License
	cond := &metav1.Condition{Type: cozyv1alpha1.GPUClassLicenseSecretMissing, ObservedGeneration: class.Generation}
	src := &corev1.Secret{}
	err := r.Reader.Get(ctx, client.ObjectKey{Namespace: gpu.LicenseSecretNamespace, Name: license.SecretName}, src)
	switch {
	case apierrors.IsNotFound(err):
		cond.Status, cond.Reason = metav1.ConditionTrue, reasonSecretNotFound
		cond.Message = fmt.Sprintf("Secret %s/%s does not exist; existing license copies are kept and no new ones are written", gpu.LicenseSecretNamespace, license.SecretName)
		return nil, cond, nil
	case err != nil:
		return nil, nil, err
	case len(src.Data[gpu.LicenseTokenKey]) == 0:
		cond.Status, cond.Reason = metav1.ConditionTrue, reasonTokenKeyMissing
		cond.Message = fmt.Sprintf("Secret %s/%s has no %s; existing license copies are kept and no new ones are written", gpu.LicenseSecretNamespace, license.SecretName, gpu.LicenseTokenKey)
		return nil, cond, nil
	}
	cond.Status, cond.Reason = metav1.ConditionFalse, reasonSecretFound
	featureType := license.FeatureType
	if featureType == 0 {
		featureType = 1
	}
	return map[string][]byte{
		gpu.LicenseTokenKey:     src.Data[gpu.LicenseTokenKey],
		gpu.LicenseGriddConfKey: fmt.Appendf(nil, "FeatureType=%d\n", featureType),
	}, cond, nil
}

// write creates or refreshes the copy in ns, and leaves alone a Secret of
// that name without the class's label, which is not a copy.
func (r *LicenseReconciler) write(ctx context.Context, ns, class string, data map[string][]byte) error {
	name := gpu.LicenseSecretName(class)
	cur := &corev1.Secret{}
	err := r.Reader.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, cur)
	if apierrors.IsNotFound(err) {
		return r.Client.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{gpu.LabelGPUClass: class}},
			Type:       corev1.SecretTypeOpaque,
			Data:       data,
		})
	}
	if err != nil {
		return err
	}
	if cur.Labels[gpu.LabelGPUClass] != class {
		log.FromContext(ctx).Info("a Secret that is not a license copy has the copy's name; the namespace gets no license",
			"namespace", ns, "secret", name, "class", class)
		return nil
	}
	if maps.EqualFunc(cur.Data, data, func(a, b []byte) bool { return string(a) == string(b) }) {
		return nil
	}
	cur.Data = data
	return r.Client.Update(ctx, cur)
}

// prune deletes the class's copies outside keep. A copy is a Secret with
// both the class's label and the copy's name, outside the namespace of the
// hoster's Secrets, which a copy is never written to.
func (r *LicenseReconciler) prune(ctx context.Context, class string, keep sets.Set[string]) error {
	list := &metav1.PartialObjectMetadataList{}
	list.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("SecretList"))
	if err := r.Secrets.List(ctx, list, client.MatchingLabels{gpu.LabelGPUClass: class}); err != nil {
		return err
	}
	var errs []error
	for i := range list.Items {
		s := &list.Items[i]
		if s.Name != gpu.LicenseSecretName(class) || s.Namespace == gpu.LicenseSecretNamespace || keep.Has(s.Namespace) {
			continue
		}
		del := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: s.Name}}
		errs = append(errs, client.IgnoreNotFound(r.Client.Delete(ctx, del, client.Preconditions{UID: &s.UID})))
	}
	return errors.Join(errs...)
}

// setStatus sets cond, or removes LicenseSecretMissing when cond is nil.
// Where GPU features are off it also clears what the status controller
// wrote, which no longer follows the nodes once that controller stops.
func (r *LicenseReconciler) setStatus(ctx context.Context, class *cozyv1alpha1.GPUClass, cond *metav1.Condition) error {
	before := class.Status.DeepCopy()
	if !r.Enabled {
		class.Status.Free, class.Status.MaxFreePerNode = nil, nil
		meta.RemoveStatusCondition(&class.Status.Conditions, cozyv1alpha1.GPUClassResourceNameConflict)
		meta.RemoveStatusCondition(&class.Status.Conditions, cozyv1alpha1.GPUClassResourceNameNotPermitted)
	}
	if cond == nil {
		meta.RemoveStatusCondition(&class.Status.Conditions, cozyv1alpha1.GPUClassLicenseSecretMissing)
	} else {
		meta.SetStatusCondition(&class.Status.Conditions, *cond)
	}
	if equality.Semantic.DeepEqual(before, &class.Status) {
		return nil
	}
	return client.IgnoreNotFound(r.Client.Status().Update(ctx, class))
}

// secretToClasses maps a hoster Secret to the classes naming it, and a
// copy to its class, so a rotation, a tampered copy and a copy left by a
// class deleted while the controller was down are all reconciled.
func (r *LicenseReconciler) secretToClasses(ctx context.Context, obj *metav1.PartialObjectMetadata) []reconcile.Request {
	var out []reconcile.Request
	if class := obj.GetLabels()[gpu.LabelGPUClass]; class != "" {
		out = append(out, reconcile.Request{NamespacedName: client.ObjectKey{Name: class}})
	}
	if obj.GetNamespace() != gpu.LicenseSecretNamespace {
		return out
	}
	var classes cozyv1alpha1.GPUClassList
	if err := r.Client.List(ctx, &classes); err != nil {
		log.FromContext(ctx).Error(err, "map a license Secret to its GPUClasses", "secret", obj.GetName())
		return out
	}
	for i := range classes.Items {
		if l := classes.Items[i].Spec.License; l != nil && l.SecretName == obj.GetName() {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKey{Name: classes.Items[i].Name}})
		}
	}
	return out
}

// allClasses maps a namespace event to every class: any of them may have
// a copy to write or delete there.
func (r *LicenseReconciler) allClasses(ctx context.Context, _ client.Object) []reconcile.Request {
	var classes cozyv1alpha1.GPUClassList
	if err := r.Client.List(ctx, &classes); err != nil {
		log.FromContext(ctx).Error(err, "list GPUClasses for a namespace event")
		return nil
	}
	out := make([]reconcile.Request, 0, len(classes.Items))
	for i := range classes.Items {
		out = append(out, reconcile.Request{NamespacedName: client.ObjectKey{Name: classes.Items[i].Name}})
	}
	return out
}

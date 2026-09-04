package controller

import (
	"context"
	"fmt"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// +kubebuilder:rbac:groups=cozystack.io,resources=applicationgroupdefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=apiregistration.k8s.io,resources=apiservices,verbs=get;list;watch;create;update;patch

// apiServiceGVK identifies the aggregation-layer APIService kind. The
// reconciler works with unstructured objects so cozystack-controller does not
// grow a kube-aggregator dependency for the one object shape it manages.
var apiServiceGVK = schema.GroupVersionKind{
	Group:   "apiregistration.k8s.io",
	Version: "v1",
	Kind:    "APIService",
}

// referenceAPIServiceName is the statically-installed (helm-managed)
// APIService of the default apps.cozystack.io group. APIServices for
// registered groups clone its service reference and CA-injection annotation,
// so aggregation for custom groups always targets the same cozystack-api
// backend, whatever namespace or service name a given installation uses.
const referenceAPIServiceName = "v1alpha1.apps.cozystack.io"

// certManagerInjectAnnotation is copied from the reference APIService so
// cert-manager keeps the cloned APIService's caBundle in sync too.
const certManagerInjectAnnotation = "cert-manager.io/inject-ca-from"

// ApplicationGroupDefinitionReconciler materializes an aggregation-layer
// APIService (v1alpha1.<group>) for every registered
// ApplicationGroupDefinition, pointing at the same backend service as the
// built-in apps.cozystack.io APIService. The APIService carries an owner
// reference to its ApplicationGroupDefinition, so deleting the registration
// garbage-collects the APIService; the group's ApplicationDefinitions then
// simply stop being served (see pkg/cmd/server buildResourceConfig), their
// HelmReleases untouched.
type ApplicationGroupDefinitionReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *ApplicationGroupDefinitionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	groupDef := &cozyv1alpha1.ApplicationGroupDefinition{}
	if err := r.Get(ctx, req.NamespacedName, groupDef); err != nil {
		// Deleted: the owner reference garbage-collects the APIService.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !groupDef.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	// CRD validation (CEL) enforces this already, but cluster state the
	// controller did not write is not trusted: never materialize an
	// APIService for a reserved or malformed group.
	if err := cozyv1alpha1.ValidateApplicationGroup(groupDef.Spec.Group); err != nil {
		logger.Error(err, "refusing to create APIService for invalid ApplicationGroupDefinition", "name", groupDef.Name)
		return ctrl.Result{}, nil
	}

	if err := r.ensureAPIService(ctx, groupDef); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *ApplicationGroupDefinitionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("applicationgroupdefinition-controller").
		For(&cozyv1alpha1.ApplicationGroupDefinition{}).
		Complete(r)
}

// ensureAPIService creates or updates the APIService for the group. Only the
// fields the reconciler owns (group/version, priorities, service reference,
// CA-injection annotation, owner reference) are enforced; everything else —
// notably spec.caBundle, written by cert-manager's CA injector — is left
// as-is on update.
func (r *ApplicationGroupDefinitionReconciler) ensureAPIService(ctx context.Context, groupDef *cozyv1alpha1.ApplicationGroupDefinition) error {
	logger := log.FromContext(ctx)
	group := groupDef.Spec.Group

	reference := &unstructured.Unstructured{}
	reference.SetGroupVersionKind(apiServiceGVK)
	if err := r.Get(ctx, types.NamespacedName{Name: referenceAPIServiceName}, reference); err != nil {
		return fmt.Errorf("get reference APIService %s: %w", referenceAPIServiceName, err)
	}
	service, found, err := unstructured.NestedMap(reference.Object, "spec", "service")
	if err != nil || !found {
		return fmt.Errorf("reference APIService %s has no spec.service (err: %v)", referenceAPIServiceName, err)
	}

	name := "v1alpha1." + group
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(apiServiceGVK)
	err = r.Get(ctx, types.NamespacedName{Name: name}, existing)
	if apierrors.IsNotFound(err) {
		desired := &unstructured.Unstructured{}
		desired.SetGroupVersionKind(apiServiceGVK)
		desired.SetName(name)
		r.applyManagedFields(desired, groupDef, group, service, reference)
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("create APIService %s: %w", name, err)
		}
		logger.Info("Created APIService for registered application group", "apiService", name, "group", group)
		return nil
	}
	if err != nil {
		return fmt.Errorf("get APIService %s: %w", name, err)
	}

	updated := existing.DeepCopy()
	r.applyManagedFields(updated, groupDef, group, service, reference)
	if equality.Semantic.DeepEqual(existing, updated) {
		return nil
	}
	if err := r.Update(ctx, updated); err != nil {
		return fmt.Errorf("update APIService %s: %w", name, err)
	}
	logger.Info("Updated APIService for registered application group", "apiService", name, "group", group)
	return nil
}

// applyManagedFields sets the reconciler-owned fields on the APIService
// object in place.
func (r *ApplicationGroupDefinitionReconciler) applyManagedFields(obj *unstructured.Unstructured, groupDef *cozyv1alpha1.ApplicationGroupDefinition, group string, service map[string]any, reference *unstructured.Unstructured) {
	annotations := obj.GetAnnotations()
	if inject, ok := reference.GetAnnotations()[certManagerInjectAnnotation]; ok {
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[certManagerInjectAnnotation] = inject
		obj.SetAnnotations(annotations)
	}

	_ = unstructured.SetNestedField(obj.Object, group, "spec", "group")
	_ = unstructured.SetNestedField(obj.Object, "v1alpha1", "spec", "version")
	_ = unstructured.SetNestedField(obj.Object, int64(1000), "spec", "groupPriorityMinimum")
	_ = unstructured.SetNestedField(obj.Object, int64(15), "spec", "versionPriority")
	_ = unstructured.SetNestedMap(obj.Object, service, "spec", "service")

	ownerRef := metav1.OwnerReference{
		APIVersion: cozyv1alpha1.GroupVersion.String(),
		Kind:       "ApplicationGroupDefinition",
		Name:       groupDef.Name,
		UID:        groupDef.UID,
	}
	refs := obj.GetOwnerReferences()
	found := false
	for i := range refs {
		if refs[i].UID == ownerRef.UID {
			found = true
			break
		}
	}
	if !found {
		refs = append(refs, ownerRef)
		obj.SetOwnerReferences(refs)
	}
}

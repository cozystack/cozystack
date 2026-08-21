// SPDX-License-Identifier: Apache-2.0

package migrationcontroller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	migrationv1alpha1 "github.com/cozystack/cozystack/api/migration/v1alpha1"
)

// ConnectionRequeue is how often a Source is re-examined while its Forklift
// Provider has not settled. Short enough that a corrected endpoint or password
// is noticed quickly, long enough that a permanently wrong one does not flood
// the apiserver.
const ConnectionRequeue = 30 * time.Second

// VMImportSourceReconciler turns a VMImportSource into the Forklift objects a
// migration needs: a credentials Secret and the source/destination Provider
// pair. It never contacts the provider itself — Forklift's Provider controller
// performs the connection test and publishes the result, so this reconciler
// mirrors that verdict onto the Source rather than shipping a second vSphere
// client that could disagree with the one doing the actual work.
type VMImportSourceReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// VDDKImage is the operator-supplied VMware Virtual Disk Development Kit
	// image. It is platform configuration, never a tenant value: naming an
	// image the cluster will run is not a tenant's decision, and this one is
	// built from a proprietary SDK that Cozystack cannot ship or mirror. Empty
	// means the VMware path is unavailable, which a vSphere Source reports on
	// its own status instead of failing somewhere deep in a transfer.
	VDDKImage string
}

func (r *VMImportSourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	src := &migrationv1alpha1.VMImportSource{}
	if err := r.Get(ctx, req.NamespacedName, src); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// A vSphere source is unusable without the VDDK image, and saying so here
	// is the whole point: the tenant learns the VMware path is not configured
	// when they register the connection, not when a 200 GiB transfer dies.
	if src.Spec.Type == migrationv1alpha1.ProviderVSphere && r.VDDKImage == "" {
		return r.fail(ctx, src, migrationv1alpha1.ReasonVDDKNotConfigured,
			"VMware import requires the platform administrator to configure a VDDK image "+
				"(migration.vddkImage in the platform values, or the vddk-image key in the "+
				"cozystack ConfigMap); until then the vSphere import path is unavailable")
	}

	secretName, err := projectCredentials(ctx, r.Client, src)
	if err != nil {
		var perr *ProjectionError
		if errors.As(err, &perr) {
			return r.fail(ctx, src, perr.Reason, perr.Message)
		}
		return ctrl.Result{}, err
	}

	if err := r.ensureProviders(ctx, src, secretName); err != nil {
		return ctrl.Result{}, err
	}

	// Mirror Forklift's own verdict. Until its Provider controller has run, the
	// Source stays not-ready with a reason that says so rather than claiming a
	// connection nobody has tested.
	ready, reason, message, err := r.providerVerdict(ctx, src)
	if err != nil {
		return ctrl.Result{}, err
	}
	if ready {
		if err := r.setReady(ctx, src, metav1.ConditionTrue, migrationv1alpha1.ReasonConnected, message); err != nil {
			return ctrl.Result{}, err
		}
		logger.V(1).Info("VMImportSource ready", "name", src.Name)
		// Re-check periodically: a connection that worked at registration can
		// stop working, and a task that starts then would fail confusingly.
		return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
	}
	if err := r.setReady(ctx, src, metav1.ConditionFalse, reason, message); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: ConnectionRequeue}, nil
}

// ensureProviders creates or updates the source and destination Providers. The
// destination is Forklift's model of the local cluster: an "openshift"-type
// provider with an empty URL, which is Forklift's own naming and says nothing
// about what Cozystack runs on.
func (r *VMImportSourceReconciler) ensureProviders(ctx context.Context, src *migrationv1alpha1.VMImportSource, secretName string) error {
	owner := ownerRef(migrationv1alpha1.GroupVersion.WithKind("VMImportSource"), src.Name, src.UID)

	sourceSpec := map[string]interface{}{
		"type": string(src.Spec.Type),
		"url":  src.Spec.URL,
		"secret": map[string]interface{}{
			"name":      secretName,
			"namespace": src.Namespace,
		},
	}
	if src.Spec.Type == migrationv1alpha1.ProviderVSphere && r.VDDKImage != "" {
		sourceSpec["settings"] = map[string]interface{}{
			"vddkInitImage": r.VDDKImage,
		}
	}
	if err := r.applyProvider(ctx, src, sourceProviderName(src.Name), sourceSpec, owner); err != nil {
		return err
	}

	destSpec := map[string]interface{}{
		"type":   "openshift",
		"url":    "",
		"secret": map[string]interface{}{},
	}
	return r.applyProvider(ctx, src, destinationProviderName(src.Name), destSpec, owner)
}

func (r *VMImportSourceReconciler) applyProvider(
	ctx context.Context,
	src *migrationv1alpha1.VMImportSource,
	name string,
	spec map[string]interface{},
	owner metav1.OwnerReference,
) error {
	existing := newObject(providerGVK)
	err := r.Get(ctx, types.NamespacedName{Namespace: src.Namespace, Name: name}, existing)
	if apierrors.IsNotFound(err) {
		obj := newObject(providerGVK)
		obj.SetName(name)
		obj.SetNamespace(src.Namespace)
		obj.SetLabels(map[string]string{
			migrationv1alpha1.ManagedByLabel: migrationv1alpha1.ManagedByValue,
		})
		obj.SetOwnerReferences([]metav1.OwnerReference{owner})
		if err := unstructured.SetNestedMap(obj.Object, spec, "spec"); err != nil {
			return err
		}
		if err := r.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
		return nil
	}
	if err != nil {
		return err
	}

	current, _, _ := unstructured.NestedMap(existing.Object, "spec")
	if specEqual(current, spec) {
		return nil
	}
	if err := unstructured.SetNestedMap(existing.Object, spec, "spec"); err != nil {
		return err
	}
	return r.Update(ctx, existing)
}

// providerVerdict reads the source Provider's conditions. Forklift publishes a
// Ready condition once it has authenticated and read the inventory; anything
// else is reported verbatim so a tenant sees Forklift's own words about their
// endpoint rather than a paraphrase.
func (r *VMImportSourceReconciler) providerVerdict(ctx context.Context, src *migrationv1alpha1.VMImportSource) (bool, string, string, error) {
	provider := newObject(providerGVK)
	err := r.Get(ctx, types.NamespacedName{Namespace: src.Namespace, Name: sourceProviderName(src.Name)}, provider)
	if apierrors.IsNotFound(err) {
		return false, migrationv1alpha1.ReasonConnectionFailed, "the Forklift Provider has not been created yet", nil
	}
	if err != nil {
		if meta.IsNoMatchError(err) {
			return false, migrationv1alpha1.ReasonConnectionFailed,
				"Forklift is not installed in this cluster: enable the forklift-operator and forklift packages", nil
		}
		return false, "", "", err
	}

	conditions, _, _ := unstructured.NestedSlice(provider.Object, "status", "conditions")
	var critical []string
	for _, raw := range conditions {
		cond, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		ctype, _, _ := unstructured.NestedString(cond, "type")
		status, _, _ := unstructured.NestedString(cond, "status")
		category, _, _ := unstructured.NestedString(cond, "category")
		message, _, _ := unstructured.NestedString(cond, "message")

		if ctype == "Ready" && status == "True" {
			return true, migrationv1alpha1.ReasonConnected, "the provider endpoint answered and its inventory is readable", nil
		}
		if status == "True" && (category == "Critical" || category == "Error") {
			critical = append(critical, fmt.Sprintf("%s: %s", ctype, message))
		}
	}
	if len(critical) > 0 {
		return false, migrationv1alpha1.ReasonConnectionFailed, strings.Join(critical, "; "), nil
	}
	return false, migrationv1alpha1.ReasonConnectionFailed, "waiting for Forklift to test the connection", nil
}

func (r *VMImportSourceReconciler) fail(ctx context.Context, src *migrationv1alpha1.VMImportSource, reason, message string) (ctrl.Result, error) {
	if err := r.setReady(ctx, src, metav1.ConditionFalse, reason, message); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: ConnectionRequeue}, nil
}

func (r *VMImportSourceReconciler) setReady(ctx context.Context, src *migrationv1alpha1.VMImportSource, status metav1.ConditionStatus, reason, message string) error {
	cond := metav1.Condition{
		Type:               migrationv1alpha1.SourceReadyCondition,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: src.Generation,
	}
	if !conditionChanged(src.Status.Conditions, cond) && src.Status.ObservedGeneration == src.Generation {
		return nil
	}
	meta.SetStatusCondition(&src.Status.Conditions, cond)
	src.Status.ObservedGeneration = src.Generation
	return r.Status().Update(ctx, src)
}

// conditionChanged reports whether setting cond would change anything the
// reader can see. Without this the reconciler would write status on every
// requeue, and a not-ready Source would rewrite its condition twice a minute
// forever.
func conditionChanged(existing []metav1.Condition, cond metav1.Condition) bool {
	current := meta.FindStatusCondition(existing, cond.Type)
	if current == nil {
		return true
	}
	return current.Status != cond.Status ||
		current.Reason != cond.Reason ||
		current.Message != cond.Message ||
		current.ObservedGeneration != cond.ObservedGeneration
}

func specEqual(a, b map[string]interface{}) bool {
	return equality.Semantic.DeepEqual(a, b)
}

func (r *VMImportSourceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&migrationv1alpha1.VMImportSource{}).
		Named("vmimportsource").
		Complete(r)
}

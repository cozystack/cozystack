package lineagecontrollerwebhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cozystack/cozystack/pkg/lineage"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	schedulerapi "github.com/cozystack/cozystack-scheduler/pkg/apis/v1alpha1"
)

var (
	NoAncestors       = errors.New("no managed apps found in lineage")
	AncestryAmbiguous = errors.New("object ancestry is ambiguous")
)

const (
	ManagedObjectKey = "internal.cozystack.io/managed-by-cozystack"
	ManagerGroupKey  = "apps.cozystack.io/application.group"
	ManagerKindKey   = "apps.cozystack.io/application.kind"
	ManagerNameKey   = "apps.cozystack.io/application.name"
)

// getResourceSelectors returns the appropriate ApplicationDefinitionResources for a given GroupKind
func (h *LineageControllerWebhook) getResourceSelectors(gk schema.GroupKind, crd *cozyv1alpha1.ApplicationDefinition) *cozyv1alpha1.ApplicationDefinitionResources {
	switch {
	case gk.Group == "" && gk.Kind == "Secret":
		return &crd.Spec.Secrets
	case gk.Group == "" && gk.Kind == "Service":
		return &crd.Spec.Services
	case gk.Group == "networking.k8s.io" && gk.Kind == "Ingress":
		return &crd.Spec.Ingresses
	default:
		return nil
	}
}

// SetupWithManager registers the handler with the webhook server.
func (h *LineageControllerWebhook) SetupWithManagerAsWebhook(mgr ctrl.Manager) error {
	cfg := rest.CopyConfig(mgr.GetConfig())

	var err error
	h.dynClient, err = dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}

	httpClient, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return err
	}

	h.mapper, err = apiutil.NewDynamicRESTMapper(cfg, httpClient)
	if err != nil {
		return err
	}

	h.initConfig()
	// Register HTTP path -> handler.
	mgr.GetWebhookServer().Register("/mutate-lineage", &admission.Webhook{Handler: h})

	return nil
}

// InjectDecoder lets controller-runtime give us a decoder for AdmissionReview requests.
func (h *LineageControllerWebhook) InjectDecoder(d admission.Decoder) error {
	h.decoder = d
	return nil
}

// Handle is called for each AdmissionReview that matches the webhook config.
func (h *LineageControllerWebhook) Handle(ctx context.Context, req admission.Request) admission.Response {
	logger := log.FromContext(ctx).WithValues(
		"gvk", req.Kind.String(),
		"namespace", req.Namespace,
		"name", req.Name,
		"operation", req.Operation,
	)
	warn := make(admission.Warnings, 0)

	obj := &unstructured.Unstructured{}
	if err := h.decodeUnstructured(req, obj); err != nil {
		return admission.Errored(400, fmt.Errorf("decode object: %w", err))
	}

	owner, err := h.getOwner(ctx, obj)
	switch {
	case err != nil && errors.Is(err, AncestryAmbiguous):
		warn = append(warn, "object ancestry ambiguous, using first ancestor found")
	case err != nil && errors.Is(err, NoAncestors):
		// not a problem, mark object as unmanaged
	case err != nil:
		logger.Error(err, "error computing lineage labels")
		return admission.Errored(500, fmt.Errorf("error computing lineage labels: %w", err))
	}
	labels, err := h.computeLabels(ctx, obj, owner)
	if err != nil {
		logger.Error(err, "error computing lineage labels")
		return admission.Errored(500, fmt.Errorf("error computing lineage labels: %w", err))
	}

	h.applyLabels(obj, labels)

	if err := h.applySchedulingClass(ctx, obj, owner, req.Namespace); err != nil {
		logger.Error(err, "error applying scheduling class")
		return admission.Errored(500, fmt.Errorf("error applying scheduling class: %w", err))
	}

	mutated, err := json.Marshal(obj)
	if err != nil {
		return admission.Errored(500, fmt.Errorf("marshal mutated pod: %w", err))
	}
	logger.V(1).Info("mutated pod", "namespace", obj.GetNamespace(), "name", obj.GetName())
	return admission.PatchResponseFromRaw(req.Object.Raw, mutated).WithWarnings(warn...)
}

func (h *LineageControllerWebhook) getOwner(ctx context.Context, o *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	owners := lineage.WalkOwnershipGraph(ctx, h.dynClient, h.mapper, h, o)
	if len(owners) == 0 {
		return nil, NoAncestors
	}
	obj, err := owners[0].GetUnstructured(ctx, h.dynClient, h.mapper)
	if err != nil {
		return nil, err
	}
	if len(owners) > 1 {
		err = AncestryAmbiguous
	}
	return obj, err
}

func (h *LineageControllerWebhook) computeLabels(ctx context.Context, obj *unstructured.Unstructured, owner *unstructured.Unstructured) (map[string]string, error) {
	if owner == nil {
		return nil, nil
	}
	gv, err := schema.ParseGroupVersion(owner.GetAPIVersion())
	if err != nil {
		// should never happen, we got an APIVersion right from the API
		return nil, fmt.Errorf("could not parse APIVersion %s to a group and version: %w", owner.GetAPIVersion(), err)
	}
	labels := map[string]string{
		// truncate apigroup to first 63 chars
		ManagedObjectKey: "true",
		ManagerGroupKey: func(s string) string {
			if len(s) < 63 {
				return s
			}
			s = s[:63]
			for b := s[62]; !((b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')); s = s[:len(s)-1] {
				b = s[len(s)-1]
			}
			return s
		}(gv.Group),
		ManagerKindKey: owner.GetKind(),
		ManagerNameKey: owner.GetName(),
	}
	templateLabels := map[string]string{
		"kind":      strings.ToLower(owner.GetKind()),
		"name":      owner.GetName(),
		"namespace": obj.GetNamespace(),
	}
	cfg := h.config.Load().(*runtimeConfig)
	crd := cfg.appCRDMap[appRef{gv.Group, owner.GetKind()}]
	resourceSelectors := h.getResourceSelectors(obj.GroupVersionKind().GroupKind(), crd)

	labels[corev1alpha1.TenantResourceLabelKey] = func(b bool) string {
		if b {
			return corev1alpha1.TenantResourceLabelValue
		}
		return "false"
	}(matchResourceToExcludeInclude(ctx, obj.GetName(), templateLabels, obj.GetLabels(), resourceSelectors))
	return labels, nil
}

func (h *LineageControllerWebhook) applyLabels(o *unstructured.Unstructured, labels map[string]string) {
	existing := o.GetLabels()
	if existing == nil {
		existing = make(map[string]string)
	}
	for k, v := range labels {
		existing[k] = v
	}
	o.SetLabels(existing)
}

// applySchedulingClass injects schedulerName and scheduling-class annotation
// into Pods whose namespace carries the scheduler.cozystack.io/scheduling-class label.
// If the referenced SchedulingClass CR does not exist (e.g. the scheduler
// package is not installed), the injection is silently skipped so that pods
// are not left Pending.
func (h *LineageControllerWebhook) applySchedulingClass(ctx context.Context, obj, owner *unstructured.Unstructured, namespace string) error {
	if obj.GetKind() != "Pod" {
		return nil
	}

	// Determine scheduling class: owner Application field takes priority,
	// then fall back to namespace label.
	var schedulingClass string
	if owner != nil {
		var app appsv1alpha1.Application
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(owner.Object, &app); err == nil {
			schedulingClass = app.SchedulingClass()
		}
	}
	if schedulingClass == "" {
		ns := &corev1.Namespace{}
		if err := h.Get(ctx, client.ObjectKey{Name: namespace}, ns); err != nil {
			return fmt.Errorf("getting namespace %s: %w", namespace, err)
		}
		schedulingClass = ns.Labels[schedulerapi.SchedulingClassLabel]
	}

	if schedulingClass == "" {
		return nil
	}

	// Verify that the referenced SchedulingClass CR exists.
	// If the CRD is not installed or the CR is missing, skip injection
	// so that pods are not stuck Pending on a non-existent scheduler.
	_, err := h.dynClient.Resource(schedulingClassGVR).Get(ctx, schedulingClass, metav1.GetOptions{})
	if err != nil {
		logger := log.FromContext(ctx)
		logger.Info("SchedulingClass not found, skipping scheduler injection",
			"schedulingClass", schedulingClass, "namespace", namespace)
		return nil
	}

	if err := unstructured.SetNestedField(obj.Object, schedulerapi.SchedulerName, "spec", "schedulerName"); err != nil {
		return fmt.Errorf("setting schedulerName: %w", err)
	}

	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[schedulerapi.SchedulingClassAnnotation] = schedulingClass
	obj.SetAnnotations(annotations)

	return nil
}

var schedulingClassGVR = schema.GroupVersionResource{
	Group:    schedulerapi.Group,
	Version:  schedulerapi.Version,
	Resource: schedulerapi.Resource,
}

func (h *LineageControllerWebhook) decodeUnstructured(req admission.Request, out *unstructured.Unstructured) error {
	if h.decoder != nil {
		if err := h.decoder.Decode(req, out); err == nil {
			return nil
		}
		if req.Kind.Group != "" || req.Kind.Kind != "" || req.Kind.Version != "" {
			out.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   req.Kind.Group,
				Version: req.Kind.Version,
				Kind:    req.Kind.Kind,
			})
			if err := h.decoder.Decode(req, out); err == nil {
				return nil
			}
		}
	}
	if len(req.Object.Raw) == 0 {
		return errors.New("empty admission object")
	}
	return json.Unmarshal(req.Object.Raw, &out.Object)
}

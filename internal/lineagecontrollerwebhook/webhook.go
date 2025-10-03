package lineagecontrollerwebhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cozystack/cozystack/pkg/lineage"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

var (
	NoAncestors       = fmt.Errorf("no managed apps found in lineage")
	AncestryAmbiguous = fmt.Errorf("object ancestry is ambiguous")
)

// getResourceSelectors returns the appropriate CozystackResourceDefinitionResources for a given GroupKind
func (h *LineageControllerWebhook) getResourceSelectors(gk schema.GroupKind, crd *cozyv1alpha1.CozystackResourceDefinition) *cozyv1alpha1.CozystackResourceDefinitionResources {
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

	discoClient, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return err
	}

	cachedDisco := memory.NewMemCacheClient(discoClient)
	h.mapper = restmapper.NewDeferredDiscoveryRESTMapper(cachedDisco)

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

	labels, err := h.computeLabels(ctx, obj)
	for {
		if err != nil && errors.Is(err, NoAncestors) {
			return admission.Allowed("object not managed by app")
		}
		if err != nil && errors.Is(err, AncestryAmbiguous) {
			warn = append(warn, "object ancestry ambiguous, using first ancestor found")
			break
		}
		if err != nil {
			logger.Error(err, "error computing lineage labels")
			return admission.Errored(500, fmt.Errorf("error computing lineage labels: %w", err))
		}
		if err == nil {
			break
		}
	}

	h.applyLabels(obj, labels)

	mutated, err := json.Marshal(obj)
	if err != nil {
		return admission.Errored(500, fmt.Errorf("marshal mutated pod: %w", err))
	}
	logger.V(1).Info("mutated pod", "namespace", obj.GetNamespace(), "name", obj.GetName())
	return admission.PatchResponseFromRaw(req.Object.Raw, mutated).WithWarnings(warn...)
}

func (h *LineageControllerWebhook) computeLabels(ctx context.Context, o *unstructured.Unstructured) (map[string]string, error) {
	owners := lineage.WalkOwnershipGraph(ctx, h.dynClient, h.mapper, h, o)
	if len(owners) == 0 {
		return nil, NoAncestors
	}
	obj, err := owners[0].GetUnstructured(ctx, h.dynClient, h.mapper)
	if err != nil {
		return nil, err
	}
	gv, err := schema.ParseGroupVersion(obj.GetAPIVersion())
	if err != nil {
		// should never happen, we got an APIVersion right from the API
		return nil, fmt.Errorf("could not parse APIVersion %s to a group and version: %w", obj.GetAPIVersion(), err)
	}
	if len(owners) > 1 {
		err = AncestryAmbiguous
	}
	labels := map[string]string{
		// truncate apigroup to first 63 chars
		"apps.cozystack.io/application.group": func(s string) string {
			if len(s) < 63 {
				return s
			}
			s = s[:63]
			for b := s[62]; !((b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')); s = s[:len(s)-1] {
				b = s[len(s)-1]
			}
			return s
		}(gv.Group),
		"apps.cozystack.io/application.kind": obj.GetKind(),
		"apps.cozystack.io/application.name": obj.GetName(),
	}
	templateLabels := map[string]string{
		"kind":      strings.ToLower(obj.GetKind()),
		"name":      obj.GetName(),
		"namespace": o.GetNamespace(),
	}
	cfg := h.config.Load().(*runtimeConfig)
	crd := cfg.appCRDMap[appRef{gv.Group, obj.GetKind()}]
	resourceSelectors := h.getResourceSelectors(o.GroupVersionKind().GroupKind(), crd)

	labels[corev1alpha1.TenantResourceLabelKey] = func(b bool) string {
		if b {
			return corev1alpha1.TenantResourceLabelValue
		}
		return "false"
	}(matchResourceToExcludeInclude(ctx, o.GetName(), templateLabels, o.GetLabels(), resourceSelectors))
	return labels, err
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

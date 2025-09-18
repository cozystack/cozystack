package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"

	dashv1alpha1 "github.com/cozystack/cozystack/api/dashboard/v1alpha1"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// AddToScheme exposes dashboard types registration for controller setup.
func AddToScheme(s *runtime.Scheme) error {
	return dashv1alpha1.AddToScheme(s)
}

// Manager owns logic for creating/updating dashboard resources derived from CRDs.
// It’s easy to extend: add new ensure* methods and wire them into EnsureForCRD.
type Manager struct {
	client    client.Client
	scheme    *runtime.Scheme
	crdListFn func(context.Context) ([]cozyv1alpha1.CozystackResourceDefinition, error)
}

// Option pattern so callers can inject a custom lister.
type Option func(*Manager)

// WithCRDListFunc overrides how Manager lists all CozystackResourceDefinitions.
func WithCRDListFunc(fn func(context.Context) ([]cozyv1alpha1.CozystackResourceDefinition, error)) Option {
	return func(m *Manager) { m.crdListFn = fn }
}

// NewManager constructs a dashboard Manager.
func NewManager(c client.Client, scheme *runtime.Scheme, opts ...Option) *Manager {
	m := &Manager{client: c, scheme: scheme}
	for _, o := range opts {
		o(m)
	}
	return m
}

// EnsureForCRD is the single entry-point used by the controller.
// Add more ensure* calls here as you implement support for other resources:
//
//   - ensureBreadcrumb            (implemented)
//   - ensureCustomColumnsOverride (implemented)
//   - ensureCustomFormsOverride   (implemented)
//   - ensureCustomFormsPrefill    (implemented)
//   - ensureFactory
//   - ensureMarketplacePanel      (implemented)
//   - ensureSidebar               (implemented)
//   - ensureTableUriMapping 	     (implemented)
func (m *Manager) EnsureForCRD(ctx context.Context, crd *cozyv1alpha1.CozystackResourceDefinition) (reconcile.Result, error) {
	// MarketplacePanel
	if res, err := m.ensureMarketplacePanel(ctx, crd); err != nil || res.Requeue || res.RequeueAfter > 0 {
		return res, err
	}
	// CustomFormsPrefill
	if res, err := m.ensureCustomFormsPrefill(ctx, crd); err != nil || res.Requeue || res.RequeueAfter > 0 {
		return res, err
	}
	// CustomColumnsOverride
	if _, err := m.ensureCustomColumnsOverride(ctx, crd); err != nil {
		return reconcile.Result{}, err
	}
	if err := m.ensureTableUriMapping(ctx, crd); err != nil {
		return reconcile.Result{}, err
	}
	if err := m.ensureBreadcrumb(ctx, crd); err != nil {
		return reconcile.Result{}, err
	}
	if err := m.ensureCustomFormsOverride(ctx, crd); err != nil {
		return reconcile.Result{}, err
	}
	if err := m.ensureSidebar(ctx, crd); err != nil {
		return reconcile.Result{}, err
	}
	if err := m.ensureFactory(ctx, crd); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

// ----------------------- MarketplacePanel -----------------------

func (m *Manager) ensureMarketplacePanel(ctx context.Context, crd *cozyv1alpha1.CozystackResourceDefinition) (reconcile.Result, error) {
	logger := log.FromContext(ctx)

	mp := &dashv1alpha1.MarketplacePanel{}
	mp.Name = crd.Name // cluster-scoped resource, name mirrors CRD name

	// If dashboard is not set, delete the panel if it exists.
	if crd.Spec.Dashboard == nil {
		err := m.client.Get(ctx, client.ObjectKey{Name: mp.Name}, mp)
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		if err != nil {
			return reconcile.Result{}, err
		}
		if err := m.client.Delete(ctx, mp); err != nil && !apierrors.IsNotFound(err) {
			return reconcile.Result{}, err
		}
		logger.Info("Deleted MarketplacePanel because dashboard is not set", "name", mp.Name)
		return reconcile.Result{}, nil
	}

	// Build desired spec from CRD fields
	d := crd.Spec.Dashboard
	app := crd.Spec.Application

	displayName := d.Singular
	if displayName == "" {
		displayName = app.Kind
	}

	tags := make([]any, len(d.Tags))
	for i, t := range d.Tags {
		tags[i] = t
	}

	specMap := map[string]any{
		"description": d.Description,
		"name":        displayName,
		"type":        "nonCrd",
		"apiGroup":    "apps.cozystack.io",
		"apiVersion":  "v1alpha1",
		"typeName":    app.Plural, // e.g., "buckets"
		"disabled":    false,
		"hidden":      false,
		"tags":        tags,
		"icon":        d.Icon,
	}

	specBytes, err := json.Marshal(specMap)
	if err != nil {
		return reconcile.Result{}, err
	}

	mutate := func() error {
		if err := controllerutil.SetOwnerReference(crd, mp, m.scheme); err != nil {
			return err
		}
		// Inline JSON payload (the ArbitrarySpec type inlines apiextv1.JSON)
		mp.Spec = dashv1alpha1.ArbitrarySpec{
			JSON: apiextv1.JSON{Raw: specBytes},
		}
		return nil
	}

	op, err := controllerutil.CreateOrUpdate(ctx, m.client, mp, mutate)
	if err != nil {
		return reconcile.Result{}, err
	}
	switch op {
	case controllerutil.OperationResultCreated:
		logger.Info("Created MarketplacePanel", "name", mp.Name)
	case controllerutil.OperationResultUpdated:
		logger.Info("Updated MarketplacePanel", "name", mp.Name)
	}
	return reconcile.Result{}, nil
}

// ----------------------- Helpers (OpenAPI → values) -----------------------

// defaultOrZero returns the schema default if present; otherwise a reasonable zero value.
func defaultOrZero(sub map[string]interface{}) interface{} {
	if v, ok := sub["default"]; ok {
		return v
	}
	typ, _ := sub["type"].(string)
	switch typ {
	case "string":
		return ""
	case "boolean":
		return false
	case "array":
		return []interface{}{}
	case "integer", "number":
		return 0
	case "object":
		return map[string]interface{}{}
	default:
		return nil
	}
}

// toIfaceSlice converts []string -> []interface{}.
func toIfaceSlice(ss []string) []interface{} {
	out := make([]interface{}, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// buildPrefillValues converts an OpenAPI schema (JSON string) into a []interface{} "values" list
// suitable for CustomFormsPrefill.spec.values.
// Rules:
//   - For top-level primitive/array fields: emit an entry, using default if present, otherwise zero.
//   - For top-level objects: only emit entries for child properties that have defaults
//     or for keys present in the parent's "default" map. Skip non-defaulted children.
func buildPrefillValues(openAPISchema string) ([]interface{}, error) {
	var root map[string]interface{}
	if err := json.Unmarshal([]byte(openAPISchema), &root); err != nil {
		return nil, fmt.Errorf("cannot parse openAPISchema: %w", err)
	}
	props, _ := root["properties"].(map[string]interface{})
	if props == nil {
		return []interface{}{}, nil
	}

	var values []interface{}

	for pname, raw := range props {
		sub, _ := raw.(map[string]interface{})
		if sub == nil {
			continue
		}

		typ, _ := sub["type"].(string)
		switch typ {
		case "object":
			objDefault, _ := sub["default"].(map[string]interface{})
			childProps, _ := sub["properties"].(map[string]interface{})
			if childProps == nil {
				continue
			}
			for cname, craw := range childProps {
				csub, _ := craw.(map[string]interface{})
				if csub == nil {
					continue
				}
				val, hasChildDefault := csub["default"]
				if !hasChildDefault && objDefault != nil {
					if v, ok := objDefault[cname]; ok {
						val = v
						hasChildDefault = true
					}
				}
				if !hasChildDefault {
					continue
				}
				entry := map[string]interface{}{
					"path":  toIfaceSlice([]string{"spec", pname, cname}),
					"value": val,
				}
				values = append(values, entry)
			}
		default:
			entry := map[string]interface{}{
				"path":  toIfaceSlice([]string{"spec", pname}),
				"value": defaultOrZero(sub),
			}
			values = append(values, entry)
		}
	}

	return values, nil
}

// normalizeJSON makes maps/slices JSON-safe for k8s Unstructured:
// - converts all int/int32/... to float64
// - leaves strings, bools, nil as-is
func normalizeJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalizeJSON(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = normalizeJSON(t[i])
		}
		return out
	case int:
		return float64(t)
	case int8:
		return float64(t)
	case int16:
		return float64(t)
	case int32:
		return float64(t)
	case int64:
		return float64(t)
	case uint, uint8, uint16, uint32, uint64:
		return float64(reflect.ValueOf(t).Convert(reflect.TypeOf(uint64(0))).Uint())
	case float32:
		return float64(t)
	default:
		return v
	}
}

var camelSplitter = regexp.MustCompile(`(?m)([A-Z]+[a-z0-9]*|[a-z0-9]+)`)

func splitCamel(s string) []string {
	return camelSplitter.FindAllString(s, -1)
}

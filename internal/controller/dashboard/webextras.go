package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	dashv1alpha1 "github.com/cozystack/cozystack/api/dashboard/v1alpha1"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Ensure the three additional dashboard/frontend resources exist:
// - TableUriMapping (dashboard.cozystack.io/v1alpha1)
// - Breadcrumb       (dashboard.cozystack.io/v1alpha1)
// - CustomFormsOverride (dashboard.cozystack.io/v1alpha1)
//
// Call these from Manager.EnsureForCRD() after ensureCustomColumnsOverride.

// --------------------------- TableUriMapping -----------------------------

func (m *Manager) ensureTableUriMapping(ctx context.Context, crd *cozyv1alpha1.CozystackResourceDefinition) error {
	g, v, kind := pickGVK(crd)
	plural := pickPlural(kind, crd)
	lowerKind := strings.ToLower(kind)

	name := fmt.Sprintf("stock-namespace-default-%s.%s.%s", g, v, plural)
	id := fmt.Sprintf("stock-namespace-/%s/%s/%s", g, v, plural)
	path := fmt.Sprintf("/openapi-ui/{2}/{3}/factory/%s-details/~recordValue~", lowerKind)

	obj := &dashv1alpha1.TableUriMapping{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "dashboard.cozystack.io",
		Version: "v1alpha1",
		Kind:    "TableUriMapping",
	})
	obj.SetName(name)

	spec := map[string]any{
		"id": id,
		"keysToParse": []any{
			"metadata",
			"name",
		},
		"pathToNavigate": path,
	}

	_, err := controllerutil.CreateOrUpdate(ctx, m.client, obj, func() error {
		if err := controllerutil.SetOwnerReference(crd, obj, m.scheme); err != nil {
			return err
		}
		b, err := json.Marshal(spec)
		if err != nil {
			return err
		}
		obj.Spec = dashv1alpha1.ArbitrarySpec{JSON: apiextv1.JSON{Raw: b}}
		return nil
	})
	return err
}

// ------------------------------- Breadcrumb -----------------------------

func (m *Manager) ensureBreadcrumb(ctx context.Context, crd *cozyv1alpha1.CozystackResourceDefinition) error {
	g, v, kind := pickGVK(crd)
	plural := pickPlural(kind, crd)

	lowerKind := strings.ToLower(kind)
	detailID := fmt.Sprintf("stock-project-factory-%s-details", lowerKind)

	// Prefer dashboard.Plural for UI label if provided
	labelPlural := titleFromKindPlural(kind, plural)
	if crd != nil && crd.Spec.Dashboard != nil && crd.Spec.Dashboard.Plural != "" {
		labelPlural = crd.Spec.Dashboard.Plural
	}

	obj := &dashv1alpha1.Breadcrumb{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "dashboard.cozystack.io",
		Version: "v1alpha1",
		Kind:    "Breadcrumb",
	})
	obj.SetName(detailID)

	items := []any{
		map[string]any{
			"key":   plural, // e.g., "virtualmachines"
			"label": labelPlural,
			"link":  fmt.Sprintf("/openapi-ui/{clusterName}/{namespace}/api-table/%s/%s/%s", g, v, plural),
		},
		map[string]any{
			"key":   strings.ToLower(kind), // "virtualmachine"
			"label": "{6}",                 // literal, as in your example
		},
	}

	spec := map[string]any{
		"id":              detailID,
		"breadcrumbItems": items,
	}

	_, err := controllerutil.CreateOrUpdate(ctx, m.client, obj, func() error {
		if err := controllerutil.SetOwnerReference(crd, obj, m.scheme); err != nil {
			return err
		}
		b, err := json.Marshal(spec)
		if err != nil {
			return err
		}
		obj.Spec = dashv1alpha1.ArbitrarySpec{JSON: apiextv1.JSON{Raw: b}}
		return nil
	})
	return err
}

// --------------------------- CustomFormsOverride ------------------------

func (m *Manager) ensureCustomFormsOverride(ctx context.Context, crd *cozyv1alpha1.CozystackResourceDefinition) error {
	g, v, kind := pickGVK(crd)
	plural := pickPlural(kind, crd)

	name := fmt.Sprintf("%s.%s.%s", g, v, plural)
	customizationID := fmt.Sprintf("default-/%s/%s/%s", g, v, plural)

	obj := &dashv1alpha1.CustomFormsOverride{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "dashboard.cozystack.io",
		Version: "v1alpha1",
		Kind:    "CustomFormsOverride",
	})
	obj.SetName(name)

	// Replicates your Helm includes (system metadata + api + status).
	hidden := []any{}
	hidden = append(hidden, hiddenMetadataSystem()...)
	hidden = append(hidden, hiddenMetadataAPI()...)
	hidden = append(hidden, hiddenStatus()...)

	// If Name is set, hide metadata
	if crd.Spec.Dashboard != nil && strings.TrimSpace(crd.Spec.Dashboard.Name) != "" {
		hidden = append([]interface{}{
			[]any{"metadata"},
		}, hidden...)
	}

	spec := map[string]any{
		"customizationId": customizationID,
		"hidden":          hidden,
		"schema":          map[string]any{}, // {}
		"strategy":        "merge",
	}

	_, err := controllerutil.CreateOrUpdate(ctx, m.client, obj, func() error {
		if err := controllerutil.SetOwnerReference(crd, obj, m.scheme); err != nil {
			return err
		}
		b, err := json.Marshal(spec)
		if err != nil {
			return err
		}
		obj.Spec = dashv1alpha1.ArbitrarySpec{JSON: apiextv1.JSON{Raw: b}}
		return nil
	})
	return err
}

// ----------------------- CustomFormsPrefill -----------------------

func (m *Manager) ensureCustomFormsPrefill(ctx context.Context, crd *cozyv1alpha1.CozystackResourceDefinition) (reconcile.Result, error) {
	logger := log.FromContext(ctx)

	app := crd.Spec.Application
	group := "apps.cozystack.io"
	version := "v1alpha1"

	name := fmt.Sprintf("%s.%s.%s", group, version, app.Plural)
	customizationID := fmt.Sprintf("default-/%s/%s/%s", group, version, app.Plural)

	values, err := buildPrefillValues(app.OpenAPISchema)
	if err != nil {
		return reconcile.Result{}, err
	}

	// If Name is set, prefill metadata.name
	if crd.Spec.Dashboard != nil && strings.TrimSpace(crd.Spec.Dashboard.Name) != "" {
		values = append([]interface{}{
			map[string]interface{}{
				"path":  toIfaceSlice([]string{"metadata", "name"}),
				"value": crd.Spec.Dashboard.Name,
			},
		}, values...)
	}

	cfp := &dashv1alpha1.CustomFormsPrefill{}
	cfp.Name = name // cluster-scoped

	specMap := map[string]any{
		"customizationId": customizationID,
		"values":          values,
	}
	specBytes, err := json.Marshal(specMap)
	if err != nil {
		return reconcile.Result{}, err
	}

	mutate := func() error {
		if err := controllerutil.SetOwnerReference(crd, cfp, m.scheme); err != nil {
			return err
		}
		cfp.Spec = dashv1alpha1.ArbitrarySpec{
			JSON: apiextv1.JSON{Raw: specBytes},
		}
		return nil
	}

	op, err := controllerutil.CreateOrUpdate(ctx, m.client, cfp, mutate)
	if err != nil {
		return reconcile.Result{}, err
	}
	switch op {
	case controllerutil.OperationResultCreated:
		logger.Info("Created CustomFormsPrefill", "name", cfp.Name)
	case controllerutil.OperationResultUpdated:
		logger.Info("Updated CustomFormsPrefill", "name", cfp.Name)
	}
	return reconcile.Result{}, nil
}

// ------------------------------ Helpers ---------------------------------

// titleFromKindPlural returns a presentable plural label, e.g.:
// kind="VirtualMachine", plural="virtualmachines" => "VirtualMachines"
func titleFromKindPlural(kind, plural string) string {
	label := kind
	if !strings.HasSuffix(strings.ToLower(plural), "s") || !strings.HasSuffix(strings.ToLower(plural), "S") {
		label += "s"
	} else {
		label += "s"
	}
	return label
}

// The hidden lists below mirror the Helm templates you shared.
// Each entry is a path as nested string array, e.g. ["metadata","creationTimestamp"].

func hiddenMetadataSystem() []any {
	return []any{
		[]any{"metadata", "annotations"},
		[]any{"metadata", "labels"},
		[]any{"metadata", "namespace"},
		[]any{"metadata", "creationTimestamp"},
		[]any{"metadata", "deletionGracePeriodSeconds"},
		[]any{"metadata", "deletionTimestamp"},
		[]any{"metadata", "finalizers"},
		[]any{"metadata", "generateName"},
		[]any{"metadata", "generation"},
		[]any{"metadata", "managedFields"},
		[]any{"metadata", "ownerReferences"},
		[]any{"metadata", "resourceVersion"},
		[]any{"metadata", "selfLink"},
		[]any{"metadata", "uid"},
	}
}

func hiddenMetadataAPI() []any {
	return []any{
		[]any{"kind"},
		[]any{"apiVersion"},
		[]any{"appVersion"},
	}
}

func hiddenStatus() []any {
	return []any{
		[]any{"status"},
	}
}

package dashboard

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	dashv1alpha1 "github.com/cozystack/cozystack/api/dashboard/v1alpha1"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// ensureCustomColumnsOverride creates or updates a CustomColumnsOverride that
// renders a header row with a colored badge and resource name link, plus a few
// useful columns (Ready, Created, Version).
//
// Naming convention mirrors your example:
//
//	metadata.name: stock-namespace-<group>.<version>.<plural>
//	spec.id:       stock-namespace-/<group>/<version>/<plural>
func (m *Manager) ensureCustomColumnsOverride(ctx context.Context, crd *cozyv1alpha1.CozystackResourceDefinition) (controllerutil.OperationResult, error) {
	g, v, kind := pickGVK(crd)
	plural := pickPlural(kind, crd)
	// Details page segment uses lowercase kind, mirroring your example
	detailsSegment := strings.ToLower(kind) + "-details"

	name := fmt.Sprintf("stock-namespace-%s.%s.%s", g, v, plural)
	id := fmt.Sprintf("stock-namespace-/%s/%s/%s", g, v, plural)

	// Badge content & color derived from kind
	badgeText := initialsFromKind(kind) // e.g., "VirtualMachine" -> "VM", "Bucket" -> "B"
	badgeColor := hexColorForKind(kind) // deterministic, dark enough for white text

	obj := &dashv1alpha1.CustomColumnsOverride{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "dashboard.cozystack.io",
		Version: "v1alpha1",
		Kind:    "CustomColumnsOverride",
	})
	obj.SetName(name)

	desired := map[string]any{
		"spec": map[string]any{
			"id": id,
			"additionalPrinterColumns": []any{
				map[string]any{
					"name":     "Name",
					"type":     "factory",
					"jsonPath": ".metadata.name",
					"customProps": map[string]any{
						"disableEventBubbling": true,
						"items": []any{
							map[string]any{
								"type": "antdFlex",
								"data": map[string]any{
									"id":    "header-row",
									"align": "center",
									"gap":   6,
								},
								"children": []any{
									map[string]any{
										"type": "antdText",
										"data": map[string]any{
											"id":    "header-badge",
											"text":  badgeText,
											"title": strings.ToLower(kind), // optional tooltip
											"style": map[string]any{
												"backgroundColor": badgeColor,
												"borderRadius":    "20px",
												"color":           "#fff",
												"display":         "inline-block",
												"fontFamily":      "RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif",
												"fontSize":        "15px",
												"fontWeight":      400,
												"lineHeight":      "24px",
												"minWidth":        24,
												"padding":         "0 9px",
												"textAlign":       "center",
												"whiteSpace":      "nowrap",
											},
										},
									},
									map[string]any{
										"type": "antdLink",
										"data": map[string]any{
											"id":   "name-link",
											"text": "{reqsJsonPath[0]['.metadata.name']['-']}",
											"href": fmt.Sprintf("/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/%s/{reqsJsonPath[0]['.metadata.name']['-']}", detailsSegment),
										},
									},
								},
							},
						},
					},
				},
				map[string]any{
					"name":     "Ready",
					"type":     "Boolean",
					"jsonPath": `.status.conditions[?(@.type=="Ready")].status`,
				},
				map[string]any{
					"name":     "Created",
					"type":     "factory",
					"jsonPath": ".metadata.creationTimestamp",
					"customProps": map[string]any{
						"disableEventBubbling": true,
						"items": []any{
							map[string]any{
								"type": "antdFlex",
								"data": map[string]any{
									"id":    "time-block",
									"align": "center",
									"gap":   6,
								},
								"children": []any{
									map[string]any{
										"type": "antdText",
										"data": map[string]any{
											"id":   "time-icon",
											"text": "🌐",
										},
									},
									map[string]any{
										"type": "parsedText",
										"data": map[string]any{
											"id":        "time-value",
											"text":      "{reqsJsonPath[0]['.metadata.creationTimestamp']['-']}",
											"formatter": "timestamp",
										},
									},
								},
							},
						},
					},
				},
				map[string]any{
					"name":     "Version",
					"type":     "string",
					"jsonPath": ".status.version",
				},
			},
		},
	}

	// CreateOrUpdate using typed resource
	_, err := controllerutil.CreateOrUpdate(ctx, m.client, obj, func() error {
		if err := controllerutil.SetOwnerReference(crd, obj, m.scheme); err != nil {
			return err
		}
		b, err := json.Marshal(desired["spec"])
		if err != nil {
			return err
		}
		obj.Spec = dashv1alpha1.ArbitrarySpec{JSON: apiextv1.JSON{Raw: b}}
		return nil
	})
	// Return OperationResultCreated/Updated is not available here with unstructured; we can mimic Updated when no error.
	return controllerutil.OperationResultNone, err
}

// --- helpers ---

// pickGVK tries to read group/version/kind from the CRD. We prefer the "application" section,
// falling back to other likely fields if your schema differs.
func pickGVK(crd *cozyv1alpha1.CozystackResourceDefinition) (group, version, kind string) {
	// Best guess based on your examples:
	if crd.Spec.Application.Kind != "" {
		kind = crd.Spec.Application.Kind
	}

	// Reasonable fallbacks if any are empty:
	if group == "" {
		group = "apps.cozystack.io"
	}
	if version == "" {
		version = "v1alpha1"
	}
	if kind == "" {
		kind = "Resource"
	}
	return
}

// pickPlural prefers a field on the CRD if you have it; otherwise do a simple lowercase + "s".
func pickPlural(kind string, crd *cozyv1alpha1.CozystackResourceDefinition) string {
	// If you have crd.Spec.Application.Plural, prefer it. Example:
	if crd.Spec.Application.Plural != "" {
		return crd.Spec.Application.Plural
	}
	// naive pluralization
	k := strings.ToLower(kind)
	if strings.HasSuffix(k, "s") {
		return k
	}
	return k + "s"
}

// initialsFromKind splits CamelCase and returns the first letters in upper case.
// "VirtualMachine" -> "VM"; "Bucket" -> "B".
func initialsFromKind(kind string) string {
	parts := splitCamel(kind)
	if len(parts) == 0 {
		return strings.ToUpper(kind)
	}
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(string(p[0])))
		// Limit to 3 chars to keep the badge compact (VM, PVC, etc.)
		if b.Len() >= 3 {
			break
		}
	}
	return b.String()
}

// hexColorForKind returns a dark, saturated color (hex) derived from a stable hash of the kind.
// We map the hash to an HSL hue; fix S/L for consistent readability with white text.
func hexColorForKind(kind string) string {
	// Stable short hash (sha1 → bytes → hue)
	sum := sha1.Sum([]byte(kind))
	// Use first two bytes for hue [0..359]
	hue := int(sum[0])<<8 | int(sum[1])
	hue = hue % 360

	// Fixed S/L chosen to contrast with white text:
	// S = 80%, L = 35% (dark enough so #fff is readable)
	r, g, b := hslToRGB(float64(hue), 0.80, 0.35)

	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

// hslToRGB converts HSL (0..360, 0..1, 0..1) to sRGB (0..255).
func hslToRGB(h float64, s float64, l float64) (uint8, uint8, uint8) {
	c := (1 - absFloat(2*l-1)) * s
	hp := h / 60.0
	x := c * (1 - absFloat(modFloat(hp, 2)-1))
	var r1, g1, b1 float64
	switch {
	case 0 <= hp && hp < 1:
		r1, g1, b1 = c, x, 0
	case 1 <= hp && hp < 2:
		r1, g1, b1 = x, c, 0
	case 2 <= hp && hp < 3:
		r1, g1, b1 = 0, c, x
	case 3 <= hp && hp < 4:
		r1, g1, b1 = 0, x, c
	case 4 <= hp && hp < 5:
		r1, g1, b1 = x, 0, c
	default:
		r1, g1, b1 = c, 0, x
	}
	m := l - c/2
	r := uint8(clamp01(r1+m) * 255.0)
	g := uint8(clamp01(g1+m) * 255.0)
	b := uint8(clamp01(b1+m) * 255.0)
	return r, g, b
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func modFloat(a, b float64) float64 {
	return a - b*float64(int(a/b))
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// optional: tiny helper to expose the compact color hash (useful for debugging)
func shortHashHex(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:4])
}

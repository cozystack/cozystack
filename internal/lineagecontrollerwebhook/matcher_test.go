package lineagecontrollerwebhook

import (
	"context"
	"testing"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestMatchName_NilMatchesEverything pins the documented behaviour:
// resourceNames=nil means "match any name", so we don't accidentally
// regress to require an explicit list.
func TestMatchName_NilMatchesEverything(t *testing.T) {
	if !matchName(context.Background(), "anything", nil, nil) {
		t.Errorf("matchName(name=anything, resourceNames=nil) = false, want true")
	}
}

// TestMatchName_EmptySliceMatchesNothing pins the edge between "no
// names" (block all) and "no list" (allow all). Empty slice is "no
// names allowed", which evaluates to false on every input.
func TestMatchName_EmptySliceMatchesNothing(t *testing.T) {
	if matchName(context.Background(), "anything", nil, []string{}) {
		t.Errorf("matchName(name=anything, resourceNames=[]) = true, want false")
	}
}

// TestMatchName_ExactLiteral pins literal matching with no template
// variables.
func TestMatchName_ExactLiteral(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		templates []string
		want      bool
	}{
		{"matches one of", "harbor", []string{"foo", "harbor", "bucket"}, true},
		{"no match", "registry", []string{"foo", "harbor"}, false},
		{"single match", "only", []string{"only"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchName(context.Background(), tc.input, nil, tc.templates)
			if got != tc.want {
				t.Errorf("matchName(%q, %v) = %v, want %v", tc.input, tc.templates, got, tc.want)
			}
		})
	}
}

// TestMatchName_TemplateVariables pins go-template substitution from
// the templateContext map. This is the documented mechanism that
// resourceNames entries like "{{ .name }}-secret" rely on.
func TestMatchName_TemplateVariables(t *testing.T) {
	ctx := map[string]string{
		"name":      "harbor",
		"kind":      "harbor",
		"namespace": "tenant-foo",
	}
	templates := []string{
		"{{ .name }}-secret",
		"{{ .kind }}-{{ .name }}-tls",
		"specificname",
	}
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"first template", "harbor-secret", true},
		{"second template", "harbor-harbor-tls", true},
		{"literal", "specificname", true},
		{"none", "registry-secret", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchName(context.Background(), tc.input, ctx, templates)
			if got != tc.want {
				t.Errorf("matchName(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// TestMatchName_BadTemplateContinues pins error tolerance: a malformed
// go-template entry must be logged and skipped, not break the loop.
// Subsequent valid entries should still match.
func TestMatchName_BadTemplateContinues(t *testing.T) {
	templates := []string{
		"{{ .unclosed",      // bad — parse error
		"{{ .missingfield }}", // bad — execute error (no such field)
		"valid-target",        // good
	}
	got := matchName(context.Background(), "valid-target", map[string]string{"name": "x"}, templates)
	if !got {
		t.Errorf("expected fallthrough to literal entry to match, got false")
	}
}

// TestMatchResourceToSelector_LabelsAndName pins both halves of the
// selector match: labels must satisfy the LabelSelector AND name must
// satisfy resourceNames (when set).
func TestMatchResourceToSelector_LabelsAndName(t *testing.T) {
	sel := &cozyv1alpha1.ApplicationDefinitionResourceSelector{
		LabelSelector: metav1.LabelSelector{
			MatchLabels: map[string]string{"app": "harbor"},
		},
		ResourceNames: []string{"harbor-secret"},
	}
	cases := []struct {
		name      string
		objName   string
		objLabels map[string]string
		want      bool
	}{
		{"both match", "harbor-secret", map[string]string{"app": "harbor"}, true},
		{"name match, labels mismatch", "harbor-secret", map[string]string{"app": "registry"}, false},
		{"labels match, name mismatch", "registry-secret", map[string]string{"app": "harbor"}, false},
		{"neither", "registry-secret", map[string]string{"app": "registry"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchResourceToSelector(context.Background(), tc.objName, nil, tc.objLabels, sel)
			if got != tc.want {
				t.Errorf("matchResourceToSelector = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMatchResourceToSelector_NilResourceNamesAllowsAnyName pins that
// when ResourceNames is nil the selector reduces to label-only
// matching.
func TestMatchResourceToSelector_NilResourceNamesAllowsAnyName(t *testing.T) {
	sel := &cozyv1alpha1.ApplicationDefinitionResourceSelector{
		LabelSelector: metav1.LabelSelector{
			MatchLabels: map[string]string{"app": "harbor"},
		},
	}
	if !matchResourceToSelector(context.Background(), "any-name", nil, map[string]string{"app": "harbor"}, sel) {
		t.Errorf("expected label-only match, got false")
	}
	if matchResourceToSelector(context.Background(), "any-name", nil, map[string]string{"app": "registry"}, sel) {
		t.Errorf("expected mismatch on labels, got true")
	}
}

// TestMatchResourceToSelectorArray_AnyMatch pins OR semantics across
// the selector array — at least one selector must match.
func TestMatchResourceToSelectorArray_AnyMatch(t *testing.T) {
	selectors := []*cozyv1alpha1.ApplicationDefinitionResourceSelector{
		{LabelSelector: metav1.LabelSelector{MatchLabels: map[string]string{"role": "primary"}}},
		{LabelSelector: metav1.LabelSelector{MatchLabels: map[string]string{"role": "replica"}}},
	}
	if !matchResourceToSelectorArray(context.Background(), "x", nil, map[string]string{"role": "replica"}, selectors) {
		t.Errorf("expected match on second selector, got false")
	}
	if matchResourceToSelectorArray(context.Background(), "x", nil, map[string]string{"role": "other"}, selectors) {
		t.Errorf("expected no match, got true")
	}
}

// TestMatchResourceToSelectorArray_EmptyArrayIsFalse pins the contract:
// no selectors means no match (deliberately not "match anything").
func TestMatchResourceToSelectorArray_EmptyArrayIsFalse(t *testing.T) {
	if matchResourceToSelectorArray(context.Background(), "x", nil, nil, nil) {
		t.Errorf("expected empty selector array to never match, got true")
	}
}

// TestMatchResourceToExcludeInclude_NilResourcesIsFalse pins the
// guard: nil resources block always evaluates false.
func TestMatchResourceToExcludeInclude_NilResourcesIsFalse(t *testing.T) {
	if matchResourceToExcludeInclude(context.Background(), "x", nil, nil, nil) {
		t.Errorf("expected nil resources to never match, got true")
	}
}

// TestMatchResourceToExcludeInclude_ExcludeWins pins precedence: an
// exclude match short-circuits to false even if include also matches.
func TestMatchResourceToExcludeInclude_ExcludeWins(t *testing.T) {
	resources := &cozyv1alpha1.ApplicationDefinitionResources{
		Exclude: []*cozyv1alpha1.ApplicationDefinitionResourceSelector{
			{LabelSelector: metav1.LabelSelector{MatchLabels: map[string]string{"sensitive": "true"}}},
		},
		Include: []*cozyv1alpha1.ApplicationDefinitionResourceSelector{
			{LabelSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "harbor"}}},
		},
	}
	got := matchResourceToExcludeInclude(context.Background(), "x", nil,
		map[string]string{"app": "harbor", "sensitive": "true"}, resources)
	if got {
		t.Errorf("expected exclude to win over include, got true")
	}
}

// TestMatchResourceToExcludeInclude_OnlyInclude pins the simple case:
// no excludes, include matches → true.
func TestMatchResourceToExcludeInclude_OnlyInclude(t *testing.T) {
	resources := &cozyv1alpha1.ApplicationDefinitionResources{
		Include: []*cozyv1alpha1.ApplicationDefinitionResourceSelector{
			{LabelSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "harbor"}}},
		},
	}
	if !matchResourceToExcludeInclude(context.Background(), "x", nil, map[string]string{"app": "harbor"}, resources) {
		t.Errorf("expected include match, got false")
	}
}

package application

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestBuildSpecSchema_FromCRDsWithCELRule pins the contract that an
// ApplicationDefinition's openAPISchema carrying an
// x-kubernetes-validations entry (the @immutable annotation on MariaDB
// storageClass) builds into a valid structural schema. Without this,
// any future change to the k8s apiextensions structural-schema rules
// could silently degrade specSchema to nil and defaulting for the
// affected Application kind would stop working without an obvious
// failure mode — just a klog.Errorf and no observable contract break.
func TestBuildSpecSchema_FromCRDsWithCELRule(t *testing.T) {
	raw := readEmbeddedOpenAPISchema(t,
		"../../../../packages/system/mariadb-rd/cozyrds/mariadb.yaml")

	if !strings.Contains(raw, "x-kubernetes-validations") {
		t.Fatalf("test fixture lost its x-kubernetes-validations entry; " +
			"the @immutable annotation on storageClass must reach the embedded schema")
	}

	got, err := buildSpecSchema(raw)
	if err != nil {
		t.Fatalf("buildSpecSchema returned error on a real cluster-shipped schema: %v", err)
	}
	if got == nil {
		t.Fatal("buildSpecSchema returned nil structural schema for a non-empty input — defaulting would be a no-op")
	}
	sc, ok := got.Properties["storageClass"]
	if !ok {
		t.Fatal("structural schema lost the storageClass property after v1->internal conversion")
	}
	// The CEL rule must survive v1→internal→structural. Asserting only that
	// the property exists is not enough: a future upstream change could
	// drop XValidations during conversion and the property would still be
	// present, silently degrading the schema we ship to clients.
	if len(sc.XValidations) == 0 {
		t.Fatal("storageClass lost its x-kubernetes-validations entries after v1->internal->structural conversion")
	}
	var foundImmutable bool
	for _, rule := range sc.XValidations {
		if rule.Rule == "self == oldSelf" {
			foundImmutable = true
			break
		}
	}
	if !foundImmutable {
		t.Fatalf("expected 'self == oldSelf' rule on storageClass, got %+v", sc.XValidations)
	}
}

// TestBuildSpecSchema_EmptyRawReturnsNilNoError pins the "no embedded
// schema" path: callers (NewREST) must see (nil, nil), not an error.
func TestBuildSpecSchema_EmptyRawReturnsNilNoError(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\t"} {
		s, err := buildSpecSchema(in)
		if err != nil {
			t.Errorf("buildSpecSchema(%q) returned error: %v", in, err)
		}
		if s != nil {
			t.Errorf("buildSpecSchema(%q) returned non-nil structural schema for empty input", in)
		}
	}
}

func TestBuildSpecSchema_MalformedJSONReturnsError(t *testing.T) {
	_, err := buildSpecSchema("{not json")
	if err == nil {
		t.Fatal("buildSpecSchema accepted malformed JSON without error")
	}
}

// readEmbeddedOpenAPISchema loads ApplicationDefinition YAML on disk and
// extracts spec.application.openAPISchema. Returning the raw JSON string
// keeps the test honest — it exercises the exact byte sequence shipped
// to the cluster, not a hand-edited mock.
func readEmbeddedOpenAPISchema(t *testing.T, relPath string) string {
	t.Helper()
	abs, err := filepath.Abs(relPath)
	if err != nil {
		t.Fatalf("resolve fixture path: %v", err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read fixture %s: %v", abs, err)
	}
	var doc struct {
		Spec struct {
			Application struct {
				OpenAPISchema string `json:"openAPISchema"`
			} `json:"application"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal fixture %s: %v", abs, err)
	}
	if doc.Spec.Application.OpenAPISchema == "" {
		t.Fatalf("fixture %s has empty spec.application.openAPISchema", abs)
	}
	return doc.Spec.Application.OpenAPISchema
}

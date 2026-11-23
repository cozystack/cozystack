package server

import (
	"strings"
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

// TestOwnedDefinitionsDropsKindDuplicate pins that cozystack-api registers one
// resource per kind: a definition declaring a kind an older definition already
// owns is left out rather than registered as a second resource for one GVK, and
// is reported so the operator can see why its kind is missing.
func TestOwnedDefinitionsDropsKindDuplicate(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mk := func(name, kind string, at time.Time) v1alpha1.ApplicationDefinition {
		d := v1alpha1.ApplicationDefinition{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(at)}}
		d.Spec.Application.Kind = kind
		return d
	}
	kept, skipped := ownedDefinitions([]v1alpha1.ApplicationDefinition{
		mk("a-redis-shadow", "Redis", base.Add(time.Hour)),
		mk("redis", "Redis", base),
		mk("postgres", "Postgres", base),
		mk("no-kind", "", base),
	})
	names := map[string]bool{}
	for _, d := range kept {
		names[d.Name] = true
	}
	// A definition with no kind claims nothing, so it is not anyone's duplicate.
	if len(kept) != 3 || !names["redis"] || !names["postgres"] || !names["no-kind"] {
		t.Fatalf("kept = %v, want redis, postgres and no-kind", names)
	}
	if len(skipped) != 1 || skipped[0] != `ApplicationDefinition "a-redis-shadow" declares kind Redis already owned by "redis"` {
		t.Fatalf("skipped = %q", skipped)
	}
}

// TestResourceConfigFromRegistersOwnerOnly pins that the resource config
// cozystack-api serves is built from the owners only: one resource per kind,
// carrying the owner's chart.
func TestResourceConfigFromRegistersOwnerOnly(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mk := func(name, artifact string, at time.Time) v1alpha1.ApplicationDefinition {
		d := v1alpha1.ApplicationDefinition{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(at)}}
		d.Spec.Application.Kind = "Redis"
		d.Spec.Release.ChartRef = &helmv2.CrossNamespaceSourceReference{Kind: "ExternalArtifact", Name: artifact, Namespace: "cozy-system"}
		return d
	}
	rc, invalid := resourceConfigFrom([]v1alpha1.ApplicationDefinition{
		mk("a-redis-shadow", "shadow-artifact", base.Add(time.Hour)),
		mk("redis", "redis-artifact", base),
	}, helmReleaseFlagValues{})
	if len(invalid) != 0 {
		t.Fatal(invalid)
	}
	if len(rc.Resources) != 1 || rc.Resources[0].Release.ChartRef.Name != "redis-artifact" {
		t.Fatalf("resources = %+v, want the owner's redis-artifact only", rc.Resources)
	}
}

// TestResourceConfigFromSkipsInvalidDefinition pins that one broken
// ApplicationDefinition no longer stops cozystack-api from starting: it is left
// out and reported, and every valid definition is still served.
func TestResourceConfigFromSkipsInvalidDefinition(t *testing.T) {
	mk := func(name, kind string) v1alpha1.ApplicationDefinition {
		d := v1alpha1.ApplicationDefinition{ObjectMeta: metav1.ObjectMeta{Name: name}}
		d.Spec.Application.Kind = kind
		d.Spec.Release.ChartRef = &helmv2.CrossNamespaceSourceReference{Kind: "ExternalArtifact", Name: name, Namespace: "cozy-system"}
		return d
	}
	badTimeout := mk("bad-timeout", "BadTimeout")
	badTimeout.Annotations = map[string]string{config.HelmInstallTimeoutAnnotation: "ten minutes"}
	badSchema := mk("bad-schema", "BadSchema")
	badSchema.Spec.Application.OpenAPISchema = `{"type": "object",`
	// A valid, nonblank schema must still be accepted.
	redis := mk("redis", "Redis")
	redis.Spec.Application.OpenAPISchema = `{"type": "object", "properties": {"replicas": {"type": "integer"}}}`

	rc, invalid := resourceConfigFrom([]v1alpha1.ApplicationDefinition{
		redis,
		badTimeout,
		badSchema,
		mk("postgres", "Postgres"),
	}, helmReleaseFlagValues{})

	kinds := map[string]bool{}
	for _, r := range rc.Resources {
		kinds[r.Application.Kind] = true
	}
	if len(rc.Resources) != 2 || !kinds["Redis"] || !kinds["Postgres"] {
		t.Fatalf("served kinds = %v, want Redis and Postgres only", kinds)
	}
	if len(invalid) != 2 {
		t.Fatalf("invalid = %v, want one error each for bad-timeout and bad-schema", invalid)
	}
	for i, want := range []string{
		`ApplicationDefinition "bad-timeout" has invalid ` + config.HelmInstallTimeoutAnnotation + ` annotation`,
		`ApplicationDefinition "bad-schema" has invalid spec.application.openAPISchema`,
	} {
		if !strings.Contains(invalid[i].Error(), want) {
			t.Errorf("invalid[%d] = %q, want it to contain %q", i, invalid[i], want)
		}
	}
}

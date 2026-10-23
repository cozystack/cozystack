package server

import (
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cozystack/cozystack/api/v1alpha1"
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
	rc, err := resourceConfigFrom([]v1alpha1.ApplicationDefinition{
		mk("a-redis-shadow", "shadow-artifact", base.Add(time.Hour)),
		mk("redis", "redis-artifact", base),
	}, helmReleaseFlagValues{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.Resources) != 1 || rc.Resources[0].Release.ChartRef.Name != "redis-artifact" {
		t.Fatalf("resources = %+v, want the owner's redis-artifact only", rc.Resources)
	}
}

// TestResourceConfigFromSkipsUnservableCredentials pins that a definition
// whose credentials block the API cannot honour leaves its kind unserved,
// while a block it can honour reaches the resource config.
func TestResourceConfigFromSkipsUnservableCredentials(t *testing.T) {
	mk := func(name, kind string, block *v1alpha1.ApplicationDefinitionCredentials) v1alpha1.ApplicationDefinition {
		d := v1alpha1.ApplicationDefinition{ObjectMeta: metav1.ObjectMeta{Name: name}}
		d.Spec.Application.Kind = kind
		d.Spec.Release.ChartRef = &helmv2.CrossNamespaceSourceReference{Kind: "ExternalArtifact", Name: name, Namespace: "cozy-system"}
		d.Spec.Credentials = block
		return d
	}
	served := &v1alpha1.ApplicationDefinitionCredentials{
		Format:   v1alpha1.CredentialFormat{Name: "scram-sha-256"},
		Accounts: []v1alpha1.CredentialAccountSource{{Kind: v1alpha1.CredentialAccountsValuesMap, Path: "users"}},
	}
	unbuilt := &v1alpha1.ApplicationDefinitionCredentials{
		Format:   v1alpha1.CredentialFormat{Name: "apr1", Kind: v1alpha1.CredentialFormatWebhook},
		Accounts: []v1alpha1.CredentialAccountSource{{Kind: v1alpha1.CredentialAccountsValuesMap, Path: "users"}},
	}
	rc, err := resourceConfigFrom([]v1alpha1.ApplicationDefinition{
		mk("postgres", "Postgres", served),
		mk("foo", "Foo", unbuilt),
		mk("redis", "Redis", nil),
	}, helmReleaseFlagValues{})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]*v1alpha1.ApplicationDefinitionCredentials{}
	for _, r := range rc.Resources {
		kinds[r.Application.Kind] = r.Credentials
	}
	if len(kinds) != 2 || kinds["Postgres"] == nil || kinds["Postgres"].Format.Name != "scram-sha-256" {
		t.Fatalf("resources = %v, want Postgres with its block and Redis without", kinds)
	}
	if _, ok := kinds["Foo"]; ok {
		t.Fatal("a kind with an unbuilt format must not be served")
	}
	if _, ok := kinds["Redis"]; !ok || kinds["Redis"] != nil {
		t.Fatalf("Redis = %v, want served with no block", kinds["Redis"])
	}
}

package appdefowner

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
)

func def(name, kind string, created time.Time) cozyv1alpha1.ApplicationDefinition {
	d := cozyv1alpha1.ApplicationDefinition{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(created)}}
	d.Spec.Application.Kind = kind
	return d
}

func TestOwnersOldestDefinitionWins(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// The newcomer sorts first by name, so a name-only rule would hand it the kind.
	owners := Owners([]cozyv1alpha1.ApplicationDefinition{
		def("aaa-newcomer", "Redis", base.Add(time.Hour)),
		def("redis", "Redis", base),
	})
	if got := owners["Redis"]; got != "redis" {
		t.Fatalf("owner of Redis = %q, want the older definition %q", got, "redis")
	}
}

func TestOwnersTieBrokenByName(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, order := range [][]string{{"b", "a"}, {"a", "b"}} {
		owners := Owners([]cozyv1alpha1.ApplicationDefinition{def(order[0], "Redis", ts), def(order[1], "Redis", ts)})
		if got := owners["Redis"]; got != "a" {
			t.Fatalf("input order %v: owner = %q, want %q regardless of list order", order, got, "a")
		}
	}
}

func TestOwnersSkipsEmptyKind(t *testing.T) {
	owners := Owners([]cozyv1alpha1.ApplicationDefinition{def("x", "", time.Now())})
	if _, ok := owners[""]; ok {
		t.Fatal("a definition with no kind must not own the empty kind")
	}
}

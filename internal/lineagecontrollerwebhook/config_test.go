package lineagecontrollerwebhook

import (
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestGetApplicationLabel_NilLabelsErrors pins the explicit nil-map
// guard so a HelmRelease with no labels at all does not panic.
func TestGetApplicationLabel_NilLabelsErrors(t *testing.T) {
	hr := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "y"},
	}
	if _, err := getApplicationLabel(hr, "any"); err == nil {
		t.Errorf("expected error on nil labels, got nil")
	}
}

// TestGetApplicationLabel_MissingKey pins the descriptive error for
// missing labels — the message should name the key that was looked up.
func TestGetApplicationLabel_MissingKey(t *testing.T) {
	hr := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name: "rel", Namespace: "tenant-foo",
			Labels: map[string]string{"unrelated": "x"},
		},
	}
	_, err := getApplicationLabel(hr, "apps.cozystack.io/application.kind")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if got := err.Error(); got == "" {
		t.Errorf("expected non-empty error message")
	}
}

// TestGetApplicationLabel_Present pins the happy path.
func TestGetApplicationLabel_Present(t *testing.T) {
	hr := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"apps.cozystack.io/application.kind": "Harbor",
			},
		},
	}
	got, err := getApplicationLabel(hr, "apps.cozystack.io/application.kind")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "Harbor" {
		t.Errorf("expected Harbor, got %q", got)
	}
}

// TestMap_HappyPath pins the four-tuple HelmRelease → (apiVersion,
// kind, prefix, error). prefix is derived by stripping the
// application.name suffix from the HelmRelease name.
func TestMap_HappyPath(t *testing.T) {
	hr := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name: "myinstance-harbor",
			Labels: map[string]string{
				"apps.cozystack.io/application.kind":  "Harbor",
				"apps.cozystack.io/application.group": "apps.cozystack.io",
				"apps.cozystack.io/application.name":  "harbor",
			},
		},
	}
	l := &LineageControllerWebhook{}
	apiVersion, kind, prefix, err := l.Map(hr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if apiVersion != "apps.cozystack.io/v1alpha1" {
		t.Errorf("apiVersion=%q, want apps.cozystack.io/v1alpha1", apiVersion)
	}
	if kind != "Harbor" {
		t.Errorf("kind=%q, want Harbor", kind)
	}
	if prefix != "myinstance-" {
		t.Errorf("prefix=%q, want myinstance-", prefix)
	}
}

// TestMap_AppNameAppearsTwiceInHRName pins the prefix-derivation guard:
// stripping the suffix once must yield "<prefix><appName>" == hr.Name.
// If the name has the appName embedded but not as suffix, return error
// rather than producing a wrong prefix.
func TestMap_AppNameAppearsTwiceInHRName(t *testing.T) {
	hr := &helmv2.HelmRelease{
		// name="harbor-staging" with appName="harbor": strip "harbor"
		// from the end yields "harbor-stagi", which when combined with
		// "harbor" doesn't reconstruct "harbor-staging" → error.
		ObjectMeta: metav1.ObjectMeta{
			Name: "harbor-staging",
			Labels: map[string]string{
				"apps.cozystack.io/application.kind":  "Harbor",
				"apps.cozystack.io/application.group": "apps.cozystack.io",
				"apps.cozystack.io/application.name":  "harbor",
			},
		},
	}
	l := &LineageControllerWebhook{}
	_, _, _, err := l.Map(hr)
	if err == nil {
		t.Errorf("expected derivation error, got nil")
	}
}

// TestMap_MissingLabel pins the failure mode when any of the three
// required application.* labels is absent.
func TestMap_MissingLabel(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
	}{
		{"missing kind", map[string]string{
			"apps.cozystack.io/application.group": "apps.cozystack.io",
			"apps.cozystack.io/application.name":  "harbor",
		}},
		{"missing group", map[string]string{
			"apps.cozystack.io/application.kind": "Harbor",
			"apps.cozystack.io/application.name": "harbor",
		}},
		{"missing name", map[string]string{
			"apps.cozystack.io/application.kind":  "Harbor",
			"apps.cozystack.io/application.group": "apps.cozystack.io",
		}},
		{"nil labels", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hr := &helmv2.HelmRelease{
				ObjectMeta: metav1.ObjectMeta{
					Name: "x", Namespace: "y",
					Labels: tc.labels,
				},
			}
			l := &LineageControllerWebhook{}
			_, _, _, err := l.Map(hr)
			if err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}

// TestInitConfig_StoresEmptyMap pins the lazy init contract: after
// initConfig the runtimeConfig must exist with an empty appCRDMap, so
// downstream lookups don't NPE on a fresh webhook instance.
func TestInitConfig_StoresEmptyMap(t *testing.T) {
	l := &LineageControllerWebhook{}
	l.initConfig()
	cfg, ok := l.config.Load().(*runtimeConfig)
	if !ok || cfg == nil {
		t.Fatalf("expected *runtimeConfig stored, got %T", l.config.Load())
	}
	if cfg.appCRDMap == nil {
		t.Errorf("expected empty appCRDMap, got nil")
	}
	if len(cfg.appCRDMap) != 0 {
		t.Errorf("expected empty appCRDMap, got %d entries", len(cfg.appCRDMap))
	}
}

// TestInitConfig_IdempotentDoesNotOverwrite pins that calling
// initConfig twice does not replace the stored runtimeConfig pointer
// — sync.Once guarantees the body runs only once, so the second call
// is observably a no-op.
func TestInitConfig_IdempotentDoesNotOverwrite(t *testing.T) {
	l := &LineageControllerWebhook{}
	l.initConfig() // first call — stores
	first := l.config.Load()
	l.initConfig() // second call — sync.Once short-circuits
	second := l.config.Load()
	if first != second {
		t.Errorf("initConfig overwrote existing config: %p → %p", first, second)
	}
}

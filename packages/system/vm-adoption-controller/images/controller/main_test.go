package main

import "testing"

func TestResolveTargetNamespace(t *testing.T) {
	cases := []struct {
		name          string
		planNamespace string
		requested     string
		want          string
	}{
		{"empty request stays local", "tenant-foo", "", "tenant-foo"},
		{"same-namespace request", "tenant-foo", "tenant-foo", "tenant-foo"},
		{"tenant cannot target another tenant", "tenant-foo", "tenant-bar", "tenant-foo"},
		{"tenant cannot target arbitrary namespace", "tenant-foo", "kube-system", "tenant-foo"},
		{"admin namespace may target a tenant", "cozy-forklift", "tenant-bar", "tenant-bar"},
		{"admin namespace empty request stays local", "cozy-forklift", "", "cozy-forklift"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveTargetNamespace(tc.planNamespace, "plan", tc.requested); got != tc.want {
				t.Errorf("resolveTargetNamespace(%q, plan, %q) = %q, want %q", tc.planNamespace, tc.requested, got, tc.want)
			}
		})
	}
}

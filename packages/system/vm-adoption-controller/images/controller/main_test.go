package main

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

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

func newPlan(namespace, targetNamespace string, vmIDs ...string) *unstructured.Unstructured {
	plan := &unstructured.Unstructured{Object: map[string]interface{}{}}
	plan.SetAPIVersion("forklift.konveyor.io/v1beta1")
	plan.SetKind("Plan")
	plan.SetNamespace(namespace)
	plan.SetName("import-1")
	if targetNamespace != "" {
		_ = unstructured.SetNestedField(plan.Object, targetNamespace, "spec", "targetNamespace")
	}
	vms := make([]interface{}, 0, len(vmIDs))
	for _, id := range vmIDs {
		vms = append(vms, map[string]interface{}{"id": id})
	}
	if len(vms) > 0 {
		_ = unstructured.SetNestedSlice(plan.Object, vms, "spec", "vms")
	}
	return plan
}

func TestValidateVMBelongsToPlan(t *testing.T) {
	cases := []struct {
		name            string
		planNamespace   string
		targetNamespace string
		planVMs         []string
		vmNamespace     string
		vmID            string
		wantErr         bool
	}{
		{"VM in the Plan's own namespace", "tenant-a", "", nil, "tenant-a", "", false},
		{"VM in the namespace the Plan targets", "cozy-forklift", "tenant-a", nil, "tenant-a", "", false},
		{"VM in the Plan's namespace while the Plan targets elsewhere", "cozy-forklift", "tenant-a", nil, "cozy-forklift", "", false},
		// A tenant-a super-admin labels a raw VM with the UID of tenant-victim's
		// Plan to have the controller write into tenant-victim.
		{"VM in a foreign namespace", "tenant-victim", "", nil, "tenant-a", "", true},
		{"VM in a namespace the Plan neither owns nor targets", "cozy-forklift", "tenant-victim", nil, "tenant-a", "", true},
		{"vmID listed in the Plan", "tenant-a", "", []string{"vm-100", "vm-200"}, "tenant-a", "vm-200", false},
		{"vmID absent from the Plan", "tenant-a", "", []string{"vm-100"}, "tenant-a", "vm-999", true},
		{"vmID absent from the Plan and from the VM", "tenant-a", "", []string{"vm-100"}, "tenant-a", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := newPlan(tc.planNamespace, tc.targetNamespace, tc.planVMs...)
			err := validateVMBelongsToPlan(plan, tc.vmNamespace, tc.vmID)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateVMBelongsToPlan() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

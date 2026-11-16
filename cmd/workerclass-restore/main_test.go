package main

import (
	"context"
	"reflect"
	"strings"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cozystack/cozystack/internal/kubernetesworker/config"
	"github.com/cozystack/cozystack/internal/kubernetesworker/restore"
)

func TestRequiresExplicitTargetAndRenderProofInputs(t *testing.T) {
	if err := run(nil); err == nil {
		t.Fatal("implicit kubeconfig/target allowed")
	}
	all := map[string]string{"kubeconfig": "/nonexistent/kubeconfig", "context": "c", "namespace": "tenant-a", "release": "pool", "current-chart": "/current", "old-chart": "/old"}
	for missing := range all {
		var args []string
		for name, value := range all {
			if name != missing {
				args = append(args, "--"+name+"="+value)
			}
		}
		err := run(args)
		if err == nil || !strings.Contains(err.Error(), "are required") {
			t.Fatalf("without --%s: %v", missing, err)
		}
	}
}

func TestHelmTemplateCommandUsesEffectiveReleaseIdentity(t *testing.T) {
	const (
		poolRelease      = "kubernetes-nodes-k-md0"
		poolRecord       = "pool-record"
		tenantNamespace  = "tenant-test"
		controlNamespace = "flux-system"
	)
	tests := []struct {
		name, record, namespace, release, target, wantName, wantNamespace string
	}{
		{
			name: "default identity", record: poolRelease, namespace: tenantNamespace,
			wantName: poolRelease, wantNamespace: tenantNamespace,
		},
		{
			name: "explicit release name", record: poolRecord, namespace: tenantNamespace,
			release: poolRelease, wantName: poolRelease, wantNamespace: tenantNamespace,
		},
		{
			name: "explicit target namespace", record: poolRecord, namespace: controlNamespace,
			release: poolRelease, target: tenantNamespace,
			wantName: poolRelease, wantNamespace: tenantNamespace,
		},
		{
			name: "target namespace default name", record: poolRecord, namespace: controlNamespace,
			target: tenantNamespace, wantName: "tenant-test-pool-record", wantNamespace: tenantNamespace,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hr := &helmv2.HelmRelease{
				ObjectMeta: metav1.ObjectMeta{Name: tt.record, Namespace: tt.namespace},
				Spec: helmv2.HelmReleaseSpec{
					ReleaseName: tt.release, TargetNamespace: tt.target,
				},
			}
			cmd := helmTemplateCommand(context.Background(), hr, "chart", "kubeconfig", "context", "values.json")
			want := []string{
				"helm", "template", tt.wantName, "chart", "--namespace", tt.wantNamespace,
				"--kubeconfig", "kubeconfig", "--kube-context", "context", "--dry-run=server", "--values", "values.json",
			}
			if !reflect.DeepEqual(cmd.Args, want) {
				t.Errorf("arguments = %q, want %q", cmd.Args, want)
			}
		})
	}
}

func TestAClasslessPoolIsRefusedAsLegacyBeforeAnyClassRead(t *testing.T) {
	_, err := poolClass(context.Background(), nil, "")
	if err == nil || err.Error() != "pool is already legacy" {
		t.Fatalf("err = %v, want the legacy refusal", err)
	}
	class, err := poolClass(context.Background(), nil, "talos")
	if err != nil || class.Spec.Bootstrap != "talos" {
		t.Fatalf("built-in class = %v, %v", class, err)
	}
}

func TestAClassSetThroughValuesFromIsRefusedBeforeTheInlineName(t *testing.T) {
	references := map[string]any{"workerClassName": "other"}
	for _, inline := range []map[string]any{{"workerClassName": "custom"}, {}} {
		if _, err := inlineClass(inline, references); err == nil || !strings.Contains(err.Error(), "set through valuesFrom") {
			t.Fatalf("inline %v: err = %v, want the valuesFrom refusal", inline, err)
		}
	}
	inline := map[string]any{"workerClassName": "custom"}
	if got, _ := config.ClassName(restore.MergeValues(references, inline)); got != "custom" {
		t.Fatalf("effective class = %q, want the inline one", got)
	}
	if name, err := inlineClass(inline, map[string]any{}); err != nil || name != "custom" {
		t.Fatalf("references without a class: %q, %v", name, err)
	}
}

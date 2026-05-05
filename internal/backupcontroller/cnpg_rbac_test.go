// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"sigs.k8s.io/yaml"

	rbacv1 "k8s.io/api/rbac/v1"
)

// TestBackupStrategyControllerClusterRole_NoSecretAccess locks in the
// security contract that the backupstrategy controller does NOT receive
// cluster-scoped Secret read. The CNPG restore path now forwards the
// operator-supplied credentials Secret name through the Postgres app spec
// (chart values), so the controller never reads Secret bytes itself.
//
// Regressing this would expose every Secret in every namespace - including
// service-account tokens, tenant Keycloak Secrets, the CA bundle - to
// whoever owns the controller's service account.
func TestBackupStrategyControllerClusterRole_NoSecretAccess(t *testing.T) {
	rbacPath := repoPath(t, "packages/system/backupstrategy-controller/templates/rbac.yaml")
	data, err := os.ReadFile(rbacPath)
	if err != nil {
		t.Fatalf("read %s: %v", rbacPath, err)
	}

	role := &rbacv1.ClusterRole{}
	if err := yaml.Unmarshal(data, role); err != nil {
		t.Fatalf("unmarshal ClusterRole: %v", err)
	}

	for i, rule := range role.Rules {
		// "*" in APIGroups covers every API group, including the core (empty)
		// group, so a wildcard rule on resources: ["*"] or ["secrets"] would
		// also expose Secrets. Treat both "" and "*" as core-group matches.
		coversCoreGroup := false
		for _, group := range rule.APIGroups {
			if group == "" || group == "*" {
				coversCoreGroup = true
				break
			}
		}
		if !coversCoreGroup {
			continue
		}
		for _, res := range rule.Resources {
			if res == "secrets" || res == "*" {
				t.Errorf("ClusterRole %q rule[%d] grants core/secrets verbs %v (resource=%q) - controller must not read Secrets cluster-wide; route credentials via spec.backup.s3CredentialsSecret instead",
					role.Name, i, rule.Verbs, res)
			}
		}
	}
}

// repoPath returns an absolute path to a file relative to the repository
// root. Walks up from the current test file until it finds the go.mod.
func repoPath(t *testing.T, rel string) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(filename)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, rel)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repo root (go.mod)")
		}
		dir = parent
	}
}

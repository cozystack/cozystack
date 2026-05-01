// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestPostgresCozyrdReflectsS3CredentialsSecret guards against drift
// between packages/apps/postgres/values.yaml and the generated cozyrd at
// packages/system/postgres-rd/cozyrds/postgres.yaml. The CNPG backup driver
// writes spec.backup.s3CredentialsSecret on restore - if the cozyrd's
// openAPISchema does not expose that field, cozystack-api validates the CR
// against a stale schema and the dashboard cannot render the field.
//
// The original review-Blocker 1 was a missed `make generate` after
// values.yaml gained s3CredentialsSecret; this test fails immediately when
// the cozyrd is out of sync.
func TestPostgresCozyrdReflectsS3CredentialsSecret(t *testing.T) {
	rdPath := repoPath(t, "packages/system/postgres-rd/cozyrds/postgres.yaml")
	data, err := os.ReadFile(rdPath)
	if err != nil {
		t.Fatalf("read %s: %v", rdPath, err)
	}

	// The cozyrd embeds the schema as a stringified JSON blob under
	// spec.application.openAPISchema. Walk into the spec.backup branch and
	// confirm the new field is present.
	rd := struct {
		Spec struct {
			Application struct {
				OpenAPISchema string `json:"openAPISchema"`
			} `json:"application"`
			Dashboard struct {
				KeysOrder [][]string `json:"keysOrder"`
			} `json:"dashboard"`
		} `json:"spec"`
	}{}
	if err := yaml.Unmarshal(data, &rd); err != nil {
		t.Fatalf("unmarshal cozyrd: %v", err)
	}

	schema := map[string]interface{}{}
	if err := json.Unmarshal([]byte(rd.Spec.Application.OpenAPISchema), &schema); err != nil {
		t.Fatalf("unmarshal openAPISchema JSON: %v", err)
	}

	backupProps := navigate(t, schema, "properties", "backup", "properties")
	creds, ok := backupProps["s3CredentialsSecret"].(map[string]interface{})
	if !ok {
		t.Fatalf("openAPISchema is missing properties.backup.properties.s3CredentialsSecret; run `make generate` in packages/apps/postgres after editing values.yaml")
	}
	credsProps, ok := creds["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("s3CredentialsSecret has no properties block: %#v", creds)
	}
	for _, key := range []string{"name", "accessKeyIDKey", "secretAccessKeyKey"} {
		if _, ok := credsProps[key]; !ok {
			t.Errorf("s3CredentialsSecret.properties is missing %q", key)
		}
	}

	// keysOrder must include the s3CredentialsSecret block so the dashboard
	// renders the field in the right place.
	wantKey := []string{"spec", "backup", "s3CredentialsSecret"}
	if !containsKeysPath(rd.Spec.Dashboard.KeysOrder, wantKey) {
		t.Errorf("keysOrder is missing %v; run `make generate` in packages/apps/postgres", wantKey)
	}
}

// navigate walks a parsed JSON schema by successive keys and fails the
// test if any step lands on a non-object value.
func navigate(t *testing.T, m map[string]interface{}, path ...string) map[string]interface{} {
	t.Helper()
	cur := m
	for i, p := range path {
		next, ok := cur[p].(map[string]interface{})
		if !ok {
			t.Fatalf("schema path %q not found at step %d", strings.Join(path[:i+1], "."), i)
		}
		cur = next
	}
	return cur
}

func containsKeysPath(keys [][]string, want []string) bool {
	for _, k := range keys {
		if equalStringSlice(k, want) {
			return true
		}
	}
	return false
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}


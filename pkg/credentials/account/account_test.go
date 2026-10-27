// SPDX-License-Identifier: Apache-2.0

package account

import (
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

// The rows from "web" to "x." are pinned for the postgres chart's account-name
// helper too (packages/apps/postgres/tests/managed_roles_cluster_test.yaml):
// the chart names each managed role's Secret and must name it as the API does.
func TestMapName(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "web", want: "web"},
		{in: "Bob.Smith", want: "bob-smith"},
		{in: "app_user", want: "app-user"},
		{in: "UPPER", want: "upper"},
		{in: "x-1", want: "x-1"},
		{in: "db.user_2", want: "db-user-2"},
		{in: "app_", wantErr: true},
		{in: "_svc", wantErr: true},
		{in: "x.", wantErr: true},
		{in: "-leading", wantErr: true},
		{in: "", wantErr: true},
		{in: strings.Repeat("a", 64), wantErr: true},
	}
	for _, tt := range tests {
		got, err := MapName(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("MapName(%q) = %q, want an error", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("MapName(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("MapName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNames(t *testing.T) {
	if got := SecretName("postgres-orders", "web"); got != "postgres-orders.web.account" {
		t.Errorf("SecretName = %q", got)
	}
	if got := CredentialName("postgres-orders", "web"); got != "postgres-orders.web" {
		t.Errorf("CredentialName = %q", got)
	}
	release, mapped, ok := SplitCredentialName("postgres-orders.bob-smith")
	if !ok || release != "postgres-orders" || mapped != "bob-smith" {
		t.Errorf("SplitCredentialName = %q, %q, %v", release, mapped, ok)
	}
	for _, bad := range []string{"nodot", "a.b.c", ".x", "x."} {
		if _, _, ok := SplitCredentialName(bad); ok {
			t.Errorf("SplitCredentialName(%q) accepted", bad)
		}
	}
}

func block(sources ...cozyv1alpha1.CredentialAccountSource) *cozyv1alpha1.ApplicationDefinitionCredentials {
	return &cozyv1alpha1.ApplicationDefinitionCredentials{
		Format:   cozyv1alpha1.CredentialFormat{Name: "scram-sha-256"},
		Accounts: sources,
	}
}

func valuesMap(path string) cozyv1alpha1.CredentialAccountSource {
	return cozyv1alpha1.CredentialAccountSource{Kind: cozyv1alpha1.CredentialAccountsValuesMap, Path: path}
}

func static(name, when string, owner cozyv1alpha1.CredentialAccountOwner) cozyv1alpha1.CredentialAccountSource {
	return cozyv1alpha1.CredentialAccountSource{Kind: cozyv1alpha1.CredentialAccountsStatic, Name: name, When: when, Owner: owner}
}

func TestValidate(t *testing.T) {
	if err := Validate(nil); err != nil {
		t.Errorf("nil block: %v", err)
	}
	if err := Validate(block(valuesMap("users"))); err != nil {
		t.Errorf("valid block: %v", err)
	}

	notBuilt := []*cozyv1alpha1.ApplicationDefinitionCredentials{
		{Format: cozyv1alpha1.CredentialFormat{Name: "apr1", Kind: cozyv1alpha1.CredentialFormatWebhook}, Accounts: []cozyv1alpha1.CredentialAccountSource{valuesMap("users")}},
		block(cozyv1alpha1.CredentialAccountSource{Kind: cozyv1alpha1.CredentialAccountsValuesList, Path: "accounts", NameField: "name"}),
		block(cozyv1alpha1.CredentialAccountSource{Kind: cozyv1alpha1.CredentialAccountsExpression, Expression: "spec.users"}),
		block(cozyv1alpha1.CredentialAccountSource{Kind: cozyv1alpha1.CredentialAccountsWebhook}),
	}
	for i, b := range notBuilt {
		err := Validate(b)
		if !errors.Is(err, ErrNotBuilt) {
			t.Errorf("notBuilt[%d]: got %v, want ErrNotBuilt", i, err)
		}
	}

	invalid := []*cozyv1alpha1.ApplicationDefinitionCredentials{
		{Format: cozyv1alpha1.CredentialFormat{Name: "apr1"}, Accounts: []cozyv1alpha1.CredentialAccountSource{valuesMap("users")}},
		{Format: cozyv1alpha1.CredentialFormat{Name: "sha256"}, ValuesPath: "accounts", Accounts: []cozyv1alpha1.CredentialAccountSource{valuesMap("users")}},
		{Format: cozyv1alpha1.CredentialFormat{Name: "sha256"}},
		block(cozyv1alpha1.CredentialAccountSource{Kind: cozyv1alpha1.CredentialAccountsValuesMap}),
		block(cozyv1alpha1.CredentialAccountSource{Kind: cozyv1alpha1.CredentialAccountsStatic}),
		block(static("x", "", "nobody")),
	}
	for i, b := range invalid {
		if err := Validate(b); err == nil || errors.Is(err, ErrNotBuilt) {
			t.Errorf("invalid[%d]: got %v, want a validation error", i, err)
		}
	}
}

func TestResolve(t *testing.T) {
	values := map[string]any{
		"users": map[string]any{
			"web":       map[string]any{},
			"Bob.Smith": map[string]any{},
			"backup":    map[string]any{},
		},
		"authEnabled": true,
		"nested":      map[string]any{"accounts": map[string]any{"deep": map[string]any{}}},
	}

	b := block(valuesMap("users"), static("backup", "", cozyv1alpha1.CredentialAccountOwnerPlatform), static("default", "authEnabled", ""), static("off", "missing", ""))
	got, err := Resolve(b, values)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := []Account{{"Bob.Smith", "bob-smith"}, {"web", "web"}, {"default", "default"}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("accounts[%d] = %v, want %v", i, got[i], want[i])
		}
	}

	if got, err := Resolve(block(valuesMap("nested.accounts")), values); err != nil || len(got) != 1 || got[0].Username != "deep" {
		t.Errorf("dotted path: %v, %v", got, err)
	}

	if got, err := Resolve(block(valuesMap("absent")), values); err != nil || len(got) != 0 {
		t.Errorf("absent path: %v, %v", got, err)
	}

	collide := map[string]any{"users": map[string]any{"a_b": map[string]any{}, "a-b": map[string]any{}}}
	if _, err := Resolve(block(valuesMap("users")), collide); err == nil || !strings.Contains(err.Error(), "a-b") {
		t.Errorf("collision: %v", err)
	}

	bad := map[string]any{"users": map[string]any{"-x": map[string]any{}}}
	if _, err := Resolve(block(valuesMap("users")), bad); err == nil {
		t.Error("invalid name accepted")
	}

	if names := PlatformAccounts(b); len(names) != 1 || names[0] != "backup" {
		t.Errorf("PlatformAccounts = %v", names)
	}
	if paths := ValuesMapPaths(b); len(paths) != 1 || paths[0] != "users" {
		t.Errorf("ValuesMapPaths = %v", paths)
	}
}

func TestRecordRoundTrip(t *testing.T) {
	sec := &corev1.Secret{}
	if r := ReadRecord(sec); r.Origin != "" || len(r.History) != 0 {
		t.Errorf("empty Secret read as %+v", r)
	}

	at := metav1.NewTime(time.Date(2026, 10, 2, 9, 14, 7, 0, time.UTC))
	r := Record{}.Apply(corev1alpha1.CredentialOperationCreate, at, "jane@example.org")
	r = r.Apply(corev1alpha1.CredentialOperationMint, at, "jane@example.org")
	WriteRecord(sec, r)
	back := ReadRecord(sec)
	if back.Origin != corev1alpha1.CredentialOriginMinted || back.By != "jane@example.org" || !back.At.Equal(&at) {
		t.Errorf("record read back as %+v", back)
	}
	if len(back.History) != 2 || back.History[0].Operation != corev1alpha1.CredentialOperationMint || back.History[1].Operation != corev1alpha1.CredentialOperationCreate {
		t.Errorf("history read back as %+v", back.History)
	}

	for range HistoryLimit + 5 {
		r = r.Apply(corev1alpha1.CredentialOperationRevoke, at, "bob")
	}
	if len(r.History) != HistoryLimit {
		t.Errorf("history has %d entries, want %d", len(r.History), HistoryLimit)
	}
	if r.Origin != corev1alpha1.CredentialOriginRevoked {
		t.Errorf("origin %s after revoke", r.Origin)
	}
}

func TestLabels(t *testing.T) {
	b := &cozyv1alpha1.ApplicationDefinitionCredentials{SecretLabels: map[string]string{"cnpg.io/reload": "true"}}
	l := Labels(b, "apps.cozystack.io", "Postgres", "orders")
	for k, v := range map[string]string{
		AccountLabel:                          "true",
		"apps.cozystack.io/application.group": "apps.cozystack.io",
		"apps.cozystack.io/application.kind":  "Postgres",
		"apps.cozystack.io/application.name":  "orders",
		"cnpg.io/reload":                      "true",
	} {
		if l[k] != v {
			t.Errorf("label %s = %q, want %q", k, l[k], v)
		}
	}
	if !IsAccountSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Labels: l}}) {
		t.Error("labelled Secret not recognised")
	}
	if IsAccountSecret(&corev1.Secret{}) {
		t.Error("bare Secret recognised")
	}
}

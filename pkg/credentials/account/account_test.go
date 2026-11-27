// SPDX-License-Identifier: Apache-2.0

package account

import (
	"strings"
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
	l := Labels(map[string]string{"cnpg.io/reload": "true", AccountLabel: "false"})
	if l[AccountLabel] != "true" || l["cnpg.io/reload"] != "true" || len(l) != 2 {
		t.Errorf("labels = %v, want the template's plus the account marker, which a template cannot override", l)
	}
	if !IsAccountSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Labels: l}}) {
		t.Error("labelled Secret not recognised")
	}
	if IsAccountSecret(&corev1.Secret{}) {
		t.Error("bare Secret recognised")
	}
}

func TestValuesFrom(t *testing.T) {
	ref := ValuesFrom("clickhouse-logs.web.account", "_accounts", "web")
	if ref.Kind != "Secret" || ref.Name != "clickhouse-logs.web.account" || ref.ValuesKey != PasswordKey || !ref.Optional {
		t.Errorf("entry = %+v", ref)
	}
	if !strings.HasPrefix(ref.TargetPath, "_accounts.u") || len(ref.TargetPath) != len("_accounts.u")+64 {
		t.Errorf("targetPath = %q, want _accounts.u<sha256 hex>", ref.TargetPath)
	}
	if !IsAccountValuesFrom(ref) {
		t.Error("an entry ValuesFrom builds is not recognised")
	}
	if IsAccountValuesFrom(helmv2.ValuesReference{Kind: "Secret", Name: "cozystack-values"}) {
		t.Error("the platform values entry is taken for an account entry")
	}
}

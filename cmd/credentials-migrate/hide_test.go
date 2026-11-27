// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

// definition is the Postgres ApplicationDefinition with the admin Secret
// either included by name, as before a conversion, or excluded, as after.
func definition(adminVisible bool) *cozyv1alpha1.ApplicationDefinition {
	sel := []*cozyv1alpha1.ApplicationDefinitionResourceSelector{{ResourceNames: []string{"postgres-{{ .name }}-default-user"}}}
	def := &cozyv1alpha1.ApplicationDefinition{ObjectMeta: metav1.ObjectMeta{Name: "postgres"}}
	def.Spec.Application.Kind = "Postgres"
	if adminVisible {
		def.Spec.Secrets.Include = sel
	} else {
		def.Spec.Secrets.Exclude = sel
	}
	return def
}

func TestHidePlatformSecretsWaitsForTheDefinitionToHideIt(t *testing.T) {
	ctx := context.Background()
	hr := release(`{}`, "0.2.0", true)
	admin := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "postgres-orders-default-user", Namespace: "tenant-foo", Labels: map[string]string{
		corev1alpha1.TenantResourceLabelKey: "true",
	}}}
	def := definition(true)
	m := newMigrator(t, hr, admin, def)
	m.o.hideSuffixes = stringFlags{"-default-user"}

	// The old definition still selects it: a label now would be undone by
	// the operator's next write, so none is written.
	err := m.hidePlatformSecrets(ctx, hr)
	if err == nil || !strings.Contains(err.Error(), "still selects postgres-orders-default-user") {
		t.Fatalf("hide under the old definition = %v", err)
	}
	if sec, _ := m.secret(t, "postgres-orders-default-user"); sec.Labels[corev1alpha1.TenantResourceLabelKey] != "true" {
		t.Fatal("labelled while the definition still selects the Secret")
	}

	// The rd chart upgrades and excludes it: labelled, and the webhook stamps
	// every later rewrite the same.
	cur := &cozyv1alpha1.ApplicationDefinition{}
	if err := m.c.Get(ctx, client.ObjectKeyFromObject(def), cur); err != nil {
		t.Fatal(err)
	}
	cur.Spec.Secrets = definition(false).Spec.Secrets
	if err := m.c.Update(ctx, cur); err != nil {
		t.Fatal(err)
	}
	if err := m.hidePlatformSecrets(ctx, hr); err != nil {
		t.Fatal(err)
	}
	if sec, _ := m.secret(t, "postgres-orders-default-user"); sec.Labels[corev1alpha1.TenantResourceLabelKey] != "false" {
		t.Fatalf("labels = %v", sec.Labels)
	}
}

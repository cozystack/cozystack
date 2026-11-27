// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/lineagecontrollerwebhook"
	"github.com/cozystack/cozystack/internal/shared/appdefowner"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

// waitDefinitionHides waits until the ApplicationDefinition of the kind, as
// it stands in the API, no longer selects sec as tenant-visible. An operator
// that rewrites the Secret replaces its labels, and the lineage webhook then
// stamps it again from that definition, so a label written while the old
// definition is in place is undone by the next such write. The definition
// comes from the kind's rd chart, which upgrades only after the operator it
// depends on is Ready: a new operator rewriting the Secret on its first pass
// lands before it. Labelling only once the new definition hides the Secret
// leaves every later rewrite stamped not tenant-visible as well.
func (m *migrator) waitDefinitionHides(ctx context.Context, hr *helmv2.HelmRelease, sec *corev1.Secret) error {
	appName := hr.Labels[appsv1alpha1.ApplicationNameLabel]
	deadline := m.now().Add(m.o.timeout)
	for {
		defs := &cozyv1alpha1.ApplicationDefinitionList{}
		if err := m.c.List(ctx, defs); err != nil {
			return fmt.Errorf("list ApplicationDefinitions: %w", err)
		}
		owner := appdefowner.Owners(defs.Items)[m.o.kind]
		var def *cozyv1alpha1.ApplicationDefinition
		for i := range defs.Items {
			if defs.Items[i].Name == owner {
				def = &defs.Items[i]
			}
		}
		if !lineagecontrollerwebhook.SecretTenantVisible(ctx, def, appName, hr.Namespace, sec.Name, sec.Labels) {
			return nil
		}
		if m.now().After(deadline) {
			return fmt.Errorf("ApplicationDefinition %s still selects %s as tenant-visible after %s; it would be stamped visible again at the operator's next write, so it is not labelled yet", owner, sec.Name, m.o.timeout)
		}
		time.Sleep(10 * time.Second)
	}
}

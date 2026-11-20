// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"fmt"
	"time"

	fluxmeta "github.com/fluxcd/pkg/apis/meta"
)

// helmForceUpgradePatch is the merge patch that has helm-controller run a
// Helm upgrade of a HelmRelease whose chart and values are unchanged.
// requestedAt alone only asks for a reconcile, which is a no-op for a release
// in sync; forceAt makes it a real upgrade, and helm-controller honours it
// only when it carries the same stamp as requestedAt.
func helmForceUpgradePatch(now time.Time) []byte {
	stamp := now.UTC().Format(time.RFC3339Nano)
	return fmt.Appendf(nil, `{"metadata":{"annotations":{%q:%q,%q:%q}}}`,
		fluxmeta.ForceRequestAnnotation, stamp, fluxmeta.ReconcileRequestAnnotation, stamp)
}

// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

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

// helmForcePending reports whether hr carries a forced upgrade request that
// helm-controller has not handled yet, so stamping another one would only
// queue a second upgrade behind it.
func helmForcePending(hr *unstructured.Unstructured) bool {
	ann := hr.GetAnnotations()
	forceAt := ann[fluxmeta.ForceRequestAnnotation]
	if forceAt == "" || forceAt != ann[fluxmeta.ReconcileRequestAnnotation] {
		return false
	}
	handled, _, _ := unstructured.NestedString(hr.Object, "status", "lastHandledForceAt")
	return handled != forceAt
}

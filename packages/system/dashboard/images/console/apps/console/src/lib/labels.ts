import type {
  ApplicationDefinition,
  ApplicationInstance,
} from "@cozystack/types"
import { APPS_GROUP } from "@cozystack/types"

/**
 * Build a label selector that matches all Kubernetes objects created by the
 * application chart for a specific instance. Cozystack helm templates stamp
 * every resource with:
 *
 *   apps.cozystack.io/application.kind: <Kind>
 *   apps.cozystack.io/application.name: <instanceName>
 *
 * We filter by both so sibling applications in the same namespace don't bleed
 * into the detail view.
 */
export function appInstanceLabel(
  ad: ApplicationDefinition,
  instance: ApplicationInstance,
): string {
  const kind = ad.spec?.application.kind ?? ""
  return [
    `${APPS_GROUP}/application.kind=${kind}`,
    `${APPS_GROUP}/application.name=${instance.metadata.name}`,
  ].join(",")
}

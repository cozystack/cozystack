import {
  APPS_GROUP,
  APPS_VERSION,
  type ApplicationDefinition,
  type ApplicationInstance,
} from "@cozystack/types"

/**
 * Compose a fully-qualified Kubernetes resource for an application.
 *
 * The object has the shape accepted by `POST /apis/apps.cozystack.io/v1alpha1
 * /namespaces/<ns>/<plural>` and `PUT .../<name>` — i.e. the standard resource
 * wrapper built from the AD's kind + the user-provided name and spec.
 */
export function composeResource(
  ad: ApplicationDefinition,
  namespace: string,
  name: string,
  spec: unknown,
): ApplicationInstance {
  return {
    apiVersion: `${APPS_GROUP}/${APPS_VERSION}`,
    kind: ad.spec?.application.kind ?? "",
    metadata: {
      name,
      namespace,
    },
    spec: (spec ?? {}) as Record<string, unknown>,
  }
}

import { useMemo, useEffect, useRef, forwardRef, useImperativeHandle } from "react"
import Form from "@rjsf/core"
import validator from "@rjsf/validator-ajv8"
import { getDefaultFormState } from "@rjsf/utils"
import type { RJSFSchema, UiSchema, TemplatesType } from "@rjsf/utils"
import { keysOrderToUiSchema, sanitizeSchema } from "../lib/keys-order.ts"
import { addSensitiveStringWidgets } from "../lib/sensitive-fields.ts"
import {
  IMMUTABLE_HELP_TEXT,
  findImmutablePaths,
  type ImmutablePath,
} from "../lib/immutable-paths.ts"
import { customTemplates, customWidgets } from "./rjsf-templates.tsx"
import { addDynamicOptionWidgets } from "../lib/dynamic-options.ts"
import { AdditionalPropertiesField } from "./AdditionalPropertiesField.tsx"
import { ResourceQuotasField } from "./ResourceQuotasField.tsx"
import { SourceField } from "./SourceField.tsx"
import "./schema-form.css"

/**
 * Recursively find all fields with additionalProperties schema and add widget.
 * Walks nested objects AND array items, so a map nested inside array elements
 * (e.g. spec.strategies[].parameters) gets the key/value editor too — without
 * this, such maps fall back to native rendering whose Add control the custom
 * ObjectFieldTemplate omits, leaving empty maps with no way to add entries.
 */
function addAdditionalPropertiesWidgets(schema: RJSFSchema, uiSchema: UiSchema = {}): UiSchema {
  if (!schema || typeof schema !== "object") return uiSchema

  const properties = (schema as any).properties
  if (!properties || typeof properties !== "object") return uiSchema

  const result = { ...uiSchema }

  for (const [key, value] of Object.entries(properties)) {
    if (typeof value === "object" && value !== null) {
      const bound = bindAdditionalProperties(value as RJSFSchema, result[key] as UiSchema | undefined)
      if (bound !== undefined) result[key] = bound
    }
  }

  return result
}

/**
 * Minimal structural view of a JSON-schema node used by the walk below.
 * RJSFSchema is intersected with an `any` index signature, so reading fields
 * straight off it yields `any`; routing through this interface keeps the walk
 * typed without an `as any` cast.
 */
interface SchemaNode {
  type?: string | string[]
  properties?: Record<string, unknown>
  additionalProperties?: unknown
  items?: unknown
}

/**
 * Resolve the uiSchema fragment for one schema node: bind the custom field to
 * an additionalProperties map, recurse into nested objects, or recurse into
 * array `items`. Returns the (possibly unchanged) ui fragment.
 */
function bindAdditionalProperties(
  fieldSchema: RJSFSchema,
  uiNode: UiSchema | undefined,
): UiSchema | undefined {
  const node: SchemaNode = fieldSchema

  const isAdditionalPropertiesMap =
    node.type === "object" &&
    (!node.properties || Object.keys(node.properties).length === 0) &&
    typeof node.additionalProperties === "object" &&
    node.additionalProperties !== null

  if (isAdditionalPropertiesMap) {
    return { ...uiNode, "ui:field": "AdditionalPropertiesField" }
  }

  if (node.properties) {
    return addAdditionalPropertiesWidgets(fieldSchema, uiNode)
  }

  const items = node.items
  if (
    node.type === "array" &&
    items &&
    typeof items === "object" &&
    !Array.isArray(items)
  ) {
    const itemsUi = bindAdditionalProperties(items as RJSFSchema, uiNode?.items)
    if (itemsUi !== undefined) {
      return { ...uiNode, items: itemsUi }
    }
  }

  return uiNode
}

/**
 * Apply ui:disabled + ui:help to every path the schema declares immutable.
 * The disabled flag (not readonly) gives the grey-out treatment specified
 * by product. Wildcard "*" segments translate to "items" for arrays. For
 * object maps (additionalProperties) the disabled flag is set on the
 * field itself so AdditionalPropertiesField hides Add/Remove controls and
 * disables the nested forms — see the comment at the additionalProperties
 * branch below for the UX trade-off.
 *
 * NOTE: this walker navigates the sanitised schema (which still carries
 * `properties`, `items` and `additionalProperties` structurally). The
 * immutable-path *set* is harvested separately from the *raw* schema via
 * findImmutablePaths, since sanitizeSchema strips x-kubernetes-validations
 * on its way to AJV. If a future sanitisation step ever rewrites those
 * structural keys, this walker needs to be updated in lockstep.
 */
function addImmutableReadonly(
  schema: RJSFSchema,
  uiSchema: UiSchema,
  paths: readonly ImmutablePath[],
): UiSchema {
  if (paths.length === 0) return uiSchema
  const next: UiSchema = { ...uiSchema }
  for (const path of paths) {
    applyImmutablePath(schema, next, path, 0)
  }
  return next
}

function applyImmutablePath(
  schemaNode: unknown,
  uiNode: Record<string, unknown>,
  path: ImmutablePath,
  depth: number,
): void {
  if (depth === path.length) {
    uiNode["ui:disabled"] = true
    uiNode["ui:help"] = IMMUTABLE_HELP_TEXT
    return
  }
  const seg = path[depth]
  const schemaObj =
    schemaNode && typeof schemaNode === "object" && !Array.isArray(schemaNode)
      ? (schemaNode as Record<string, unknown>)
      : null
  if (seg === "*") {
    if (schemaObj && schemaObj.items) {
      const isLast = depth === path.length - 1
      if (isLast) {
        // Whole-array immutable: mark the wrapper itself disabled so
        // RJSF's ArrayFieldTemplate hides Add/Remove and disables every
        // element. Mirrors the additionalProperties-map handling below;
        // without this the user could click Add, fill an entry, and
        // watch it silently disappear on save when overlay clones source.
        uiNode["ui:disabled"] = true
        uiNode["ui:help"] = IMMUTABLE_HELP_TEXT
        return
      }
      const childUi = ensureChild(uiNode, "items")
      applyImmutablePath(schemaObj.items, childUi, path, depth + 1)
      return
    }
    // additionalProperties object map. Per-value immutability is rendered
    // here as whole-map immutability: the field itself is marked disabled,
    // AdditionalPropertiesField hides Add/Remove and disables every inner
    // input. Splitting "keys editable, values frozen" needs custom plumbing
    // through that field plus a UX decision on whether deleting an entry
    // counts as mutating its value — deliberately deferred until a real
    // schema asks for it.
    uiNode["ui:disabled"] = true
    uiNode["ui:help"] = IMMUTABLE_HELP_TEXT
    return
  }
  const childSchema = schemaObj
    ? (schemaObj.properties as Record<string, unknown> | undefined)?.[seg]
    : undefined
  const childUi = ensureChild(uiNode, seg)
  applyImmutablePath(childSchema, childUi, path, depth + 1)
}

function ensureChild(
  uiNode: Record<string, unknown>,
  key: string,
): Record<string, unknown> {
  const existing = uiNode[key]
  if (existing && typeof existing === "object" && !Array.isArray(existing)) {
    const cloned = { ...(existing as Record<string, unknown>) }
    uiNode[key] = cloned
    return cloned
  }
  const fresh: Record<string, unknown> = {}
  uiNode[key] = fresh
  return fresh
}

interface SchemaFormProps {
  openAPISchema: string
  keysOrder?: string[][]
  formData: unknown
  onChange: (data: unknown) => void
  children?: React.ReactNode
  /**
   * When "enforce", fields whose schema carries a CEL immutability rule
   * (`self == oldSelf`) are rendered greyed out (disabled) with helper
   * text. Default "off" keeps every field editable — used for create
   * flows.
   */
  immutableMode?: "enforce" | "off"
}

export interface SchemaFormHandle {
  /**
   * Run RJSF validation against the current form data and render any errors
   * inline. Returns whether the form is valid. The Deploy button lives
   * outside this component and bypasses RJSF's own submit, so callers gate
   * their submit on this before sending the resource to the API.
   */
  validate: () => boolean
}

export const SchemaForm = forwardRef<SchemaFormHandle, SchemaFormProps>(function SchemaForm({
  openAPISchema,
  keysOrder,
  formData,
  onChange,
  children,
  immutableMode,
}: SchemaFormProps, ref) {
  const formRef = useRef<Form>(null)
  useImperativeHandle(
    ref,
    () => ({ validate: () => formRef.current?.validateForm() ?? true }),
    [],
  )

  // Parse the raw schema once, then derive the sanitised RJSFSchema and the
  // immutable-path set from it. We keep the raw object around so we don't
  // re-parse the same string twice on every render. The contract is "same
  // openAPISchema string ⇒ same parsedSchema reference"; the dependent
  // memos rely on that identity to avoid recomputation.
  const parsedSchema = useMemo<unknown>(() => {
    try {
      return JSON.parse(openAPISchema)
    } catch {
      return {}
    }
  }, [openAPISchema])

  const schema = useMemo<RJSFSchema>(
    () => sanitizeSchema(parsedSchema) as RJSFSchema,
    [parsedSchema],
  )

  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange
  const formDataRef = useRef(formData)
  formDataRef.current = formData
  const emittedSchemaRef = useRef<RJSFSchema | null>(null)

  // Emit defaults to parent once per schema so spec is never empty on first submit.
  // Uses formDataRef (current parent state, not the initial mount snapshot) so
  // user input is preserved when the parent recomputes openAPISchema due to
  // async sibling data (e.g. instancesData loading) — without this,
  // getDefaultFormState would re-emit defaults computed from the stale initial
  // formData and wipe whatever the user already typed.
  useEffect(() => {
    if (!schema || Object.keys(schema).length === 0) return
    if (emittedSchemaRef.current === schema) return
    emittedSchemaRef.current = schema
    const defaults = getDefaultFormState(validator, schema, formDataRef.current ?? {}, schema)
    onChangeRef.current(defaults)
  }, [schema])

  const immutablePaths = useMemo<ImmutablePath[]>(
    () => (immutableMode === "enforce" ? findImmutablePaths(parsedSchema) : []),
    [parsedSchema, immutableMode],
  )

  const uiSchema = useMemo<UiSchema>(() => {
    const baseUiSchema: UiSchema = {
      "ui:submitButtonOptions": { norender: true },
      ...keysOrderToUiSchema(keysOrder),
      // Use SourceField for mutually exclusive source fields
      source: {
        "ui:field": "SourceField",
      },
    }

    // Bind every field carrying x-cozystack-options to the single generic
    // DynamicOptionsWidget (GPU, instanceType, instanceProfile, network,
    // image, storagePool, storageClass, vmdisk — populated from the cluster
    // via the cozystack-api Option resource).
    const withDynamicOptions = addDynamicOptionWidgets(schema, baseUiSchema)

    // Automatically add AdditionalPropertiesField for fields with additionalProperties schema
    const withAdditionalProps = addAdditionalPropertiesWidgets(schema, withDynamicOptions)

    // Mask credential-shaped string fields (access/secret keys, passwords, tokens).
    const withSensitive = addSensitiveStringWidgets(schema, withAdditionalProps)

    // Override resourceQuotas field with structured quota editor.
    // Scoped to schemas where resourceQuotas has additionalProperties: {type: "string"}
    // (the cozystack-tenants chart shape) to avoid activating on unrelated CRDs.
    const rqSchema = (schema as any).properties?.resourceQuotas
    if (rqSchema && rqSchema.additionalProperties?.type === "string") {
      withSensitive.resourceQuotas = {
        ...withSensitive.resourceQuotas,
        "ui:field": "ResourceQuotasField",
      }
    }

    return addImmutableReadonly(schema, withSensitive, immutablePaths)
  }, [keysOrder, schema, immutablePaths])

  const customFields = useMemo(
    () => ({
      AdditionalPropertiesField: AdditionalPropertiesField,
      ResourceQuotasField: ResourceQuotasField,
      SourceField: SourceField,
    }),
    []
  )

  // Create templates without submit button
  const templatesWithoutSubmit = useMemo<Partial<TemplatesType>>(() => {
    return {
      ...customTemplates,
      ButtonTemplates: {
        ...customTemplates.ButtonTemplates,
        SubmitButton: () => null,
      },
    }
  }, [])

  return (
    <div className="rjsf-container">
      <Form
        ref={formRef}
        tagName="div"
        schema={schema}
        uiSchema={uiSchema}
        formData={formData}
        validator={validator}
        templates={templatesWithoutSubmit}
        widgets={customWidgets}
        fields={customFields}
        onChange={(e) => onChange(e.formData)}
        liveValidate={false}
        showErrorList={false}
      >
        {children}
      </Form>
    </div>
  )
})

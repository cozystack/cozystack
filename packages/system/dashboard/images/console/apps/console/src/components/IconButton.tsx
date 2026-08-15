import type {
  IconButtonProps,
  FormContextType,
  RJSFSchema,
  StrictRJSFSchema,
} from "@rjsf/utils"

/**
 * Bare button used by every RJSF ButtonTemplate. Lives in its own module so
 * rjsf-templates.tsx can stay a plain template registry — a file mixing
 * component definitions with non-component exports breaks Fast Refresh.
 */
export function IconButton<
  T = unknown,
  S extends StrictRJSFSchema = RJSFSchema,
  F extends FormContextType = FormContextType,
>(props: IconButtonProps<T, S, F>) {
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  const { icon, className, uiSchema, registry, iconType, ...btnProps } = props
  return (
    <button
      type="button"
      className={className}
      {...btnProps}
    >
      {icon}
    </button>
  )
}

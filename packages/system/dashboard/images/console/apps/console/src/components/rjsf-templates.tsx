import type {
  IconButtonProps,
  ArrayFieldTemplateItemType,
  TemplatesType,
  FormContextType,
  RJSFSchema,
  StrictRJSFSchema,
  SubmitButtonProps,
} from "@rjsf/utils"
import { CustomObjectFieldTemplate } from "./CustomObjectFieldTemplate.tsx"
import { SourceWidget } from "./SourceWidget.tsx"
import { DynamicOptionsWidget } from "./DynamicOptionsWidget.tsx"
import { AdditionalPropertiesWidget } from "./AdditionalPropertiesWidget.tsx"
import { SensitiveStringWidget } from "./SensitiveStringWidget.tsx"

function IconButton<
  T = any,
  S extends StrictRJSFSchema = RJSFSchema,
  F extends FormContextType = any,
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

const buttonClassName =
  "rounded-md border border-slate-200 bg-white px-3 py-1.5 text-sm font-medium text-slate-600 shadow-sm hover:bg-slate-50 hover:border-slate-300 transition-colors"

const removeButtonClassName =
  "rounded-md border border-red-200 bg-white px-3 py-1.5 text-sm font-medium text-red-600 shadow-sm hover:bg-red-50 hover:border-red-300 transition-colors"

function ArrayFieldItemTemplate<
  T = any,
  S extends StrictRJSFSchema = RJSFSchema,
  F extends FormContextType = any,
>(props: ArrayFieldTemplateItemType<T, S, F>) {
  const { children, hasRemove, index, onDropIndexClick, disabled, readonly, schema } = props
  const isNumeric = (schema as any).type === 'integer' || (schema as any).type === 'number'
  return (
    <div className="array-item-row group flex items-center gap-1.5 mb-1">
      <div className={isNumeric ? 'w-36 shrink-0' : 'flex-1'}>{children}</div>
      {hasRemove && (
        <button
          type="button"
          aria-label="Remove"
          className="opacity-0 group-hover:opacity-100 size-[26px] flex-shrink-0 flex items-center justify-center rounded-full border border-red-200 bg-white text-red-400 text-sm leading-none hover:bg-red-50 hover:text-red-600 hover:border-red-300 disabled:opacity-30 transition-all duration-150"
          onClick={onDropIndexClick(index)}
          disabled={disabled || readonly}
        >
          ×
        </button>
      )}
    </div>
  )
}

export const customTemplates = {
  ObjectFieldTemplate: CustomObjectFieldTemplate,
  ArrayFieldItemTemplate: ArrayFieldItemTemplate,
  ButtonTemplates: {
    AddButton: (props: IconButtonProps) => (
      <IconButton
        {...props}
        icon="+ add"
        className="mt-0.5 flex items-center gap-1 px-2 py-1 text-xs font-medium text-slate-500 hover:text-blue-600 rounded-md border border-dashed border-slate-300 hover:border-blue-400 hover:bg-blue-50/60 bg-white transition-all duration-150 cursor-pointer"
      />
    ),
    RemoveButton: (props: IconButtonProps) => (
      <IconButton {...props} icon="× Remove" className={removeButtonClassName} />
    ),
    CopyButton: (props: IconButtonProps) => (
      <IconButton {...props} icon="Copy" className={buttonClassName} />
    ),
    MoveUpButton: () => null,
    MoveDownButton: () => null,
    SubmitButton: (props: SubmitButtonProps) => (
      <IconButton {...props} icon="Submit" className={buttonClassName} />
    ),
  },
} as const satisfies Partial<TemplatesType>

export const customWidgets = {
  SourceWidget: SourceWidget,
  DynamicOptionsWidget: DynamicOptionsWidget,
  AdditionalPropertiesWidget: AdditionalPropertiesWidget,
  SensitiveStringWidget: SensitiveStringWidget,
}

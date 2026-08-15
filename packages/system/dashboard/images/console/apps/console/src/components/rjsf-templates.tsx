import type {
  IconButtonProps,
  TemplatesType,
  SubmitButtonProps,
} from "@rjsf/utils"
import { CustomObjectFieldTemplate } from "./CustomObjectFieldTemplate.tsx"
import { ArrayFieldItemTemplate } from "./ArrayFieldItemTemplate.tsx"
import { IconButton } from "./IconButton.tsx"
import { SourceWidget } from "./SourceWidget.tsx"
import { DynamicOptionsWidget } from "./DynamicOptionsWidget.tsx"
import { AdditionalPropertiesWidget } from "./AdditionalPropertiesWidget.tsx"
import { SensitiveStringWidget } from "./SensitiveStringWidget.tsx"

const buttonClassName =
  "rounded-md border border-slate-200 bg-white px-3 py-1.5 text-sm font-medium text-slate-600 shadow-sm hover:bg-slate-50 hover:border-slate-300 transition-colors"

const removeButtonClassName =
  "rounded-md border border-red-200 bg-white px-3 py-1.5 text-sm font-medium text-red-600 shadow-sm hover:bg-red-50 hover:border-red-300 transition-colors"

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

import type { TemplatesType } from "@rjsf/utils"
import { CustomObjectFieldTemplate } from "./CustomObjectFieldTemplate.tsx"
import {
  AddButton,
  ArrayFieldItemTemplate,
  CopyButton,
  HiddenButton,
  RemoveButton,
  SubmitButton,
} from "./rjsf-templates.tsx"
import { SourceWidget } from "./SourceWidget.tsx"
import { DynamicOptionsWidget } from "./DynamicOptionsWidget.tsx"
import { AdditionalPropertiesWidget } from "./AdditionalPropertiesWidget.tsx"
import { SensitiveStringWidget } from "./SensitiveStringWidget.tsx"

export const customTemplates = {
  ObjectFieldTemplate: CustomObjectFieldTemplate,
  ArrayFieldItemTemplate: ArrayFieldItemTemplate,
  ButtonTemplates: {
    AddButton,
    RemoveButton,
    CopyButton,
    MoveUpButton: HiddenButton,
    MoveDownButton: HiddenButton,
    SubmitButton,
  },
} as const satisfies Partial<TemplatesType>

export const customWidgets = {
  SourceWidget: SourceWidget,
  DynamicOptionsWidget: DynamicOptionsWidget,
  AdditionalPropertiesWidget: AdditionalPropertiesWidget,
  SensitiveStringWidget: SensitiveStringWidget,
}

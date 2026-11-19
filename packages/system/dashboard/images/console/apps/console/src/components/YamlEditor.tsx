import { useEffect, useRef } from "react"
import Editor, { type OnMount } from "@monaco-editor/react"
import "../lib/monaco-setup.ts"

interface YamlEditorProps {
  value: string
  onChange: (value: string) => void
  readOnly?: boolean
  height?: string | number
}

export function YamlEditor({ value, onChange, readOnly, height = "100%" }: YamlEditorProps) {
  const editorRef = useRef<Parameters<OnMount>[0] | null>(null)

  useEffect(() => {
    const model = editorRef.current?.getModel()
    if (model && model.getValue() !== value) {
      model.setValue(value)
    }
  }, [value])

  return (
    <Editor
      height={height}
      defaultLanguage="yaml"
      theme="vs-light"
      value={value}
      onMount={(editor) => {
        editorRef.current = editor
      }}
      onChange={(next) => onChange(next ?? "")}
      options={{
        readOnly,
        minimap: { enabled: false },
        fontSize: 13,
        scrollBeyondLastLine: false,
        tabSize: 2,
        automaticLayout: true,
        renderLineHighlight: "none",
        lineNumbersMinChars: 3,
      }}
    />
  )
}

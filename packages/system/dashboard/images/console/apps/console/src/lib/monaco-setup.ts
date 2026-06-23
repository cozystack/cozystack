import * as monaco from "monaco-editor"
import { loader } from "@monaco-editor/react"
import EditorWorker from "monaco-editor/esm/vs/editor/editor.worker?worker"
import JsonWorker from "monaco-editor/esm/vs/language/json/json.worker?worker"
import CssWorker from "monaco-editor/esm/vs/language/css/css.worker?worker"
import HtmlWorker from "monaco-editor/esm/vs/language/html/html.worker?worker"
import TsWorker from "monaco-editor/esm/vs/language/typescript/ts.worker?worker"

// Self-host monaco. Without loader.config the @monaco-editor/react default
// loader fetches the whole editor from jsDelivr at runtime, which stalls
// forever on air-gapped or flaky networks. Pointing it at the bundled copy and
// routing the language workers to bundled copies keeps everything on the app's
// own origin.
const WORKERS: Record<string, new () => Worker> = {
  json: JsonWorker,
  css: CssWorker,
  scss: CssWorker,
  less: CssWorker,
  html: HtmlWorker,
  handlebars: HtmlWorker,
  razor: HtmlWorker,
  typescript: TsWorker,
  javascript: TsWorker,
}

self.MonacoEnvironment = {
  getWorker(_workerId: string, label: string): Worker {
    const WorkerCtor = WORKERS[label] ?? EditorWorker
    return new WorkerCtor()
  },
}

loader.config({ monaco })

import { describe, it, expect, vi } from "vitest"

// Mock the heavy/browser-only modules so the setup can be imported in jsdom and
// we can assert how it wires things — without loading real monaco or workers.
const configSpy = vi.fn()
vi.mock("@monaco-editor/react", () => ({ loader: { config: configSpy } }))
vi.mock("monaco-editor", () => ({ editor: {} }))

class EditorWorker {}
class JsonWorker {}
class OtherWorker {}
vi.mock("monaco-editor/esm/vs/editor/editor.worker?worker", () => ({ default: EditorWorker }))
vi.mock("monaco-editor/esm/vs/language/json/json.worker?worker", () => ({ default: JsonWorker }))
vi.mock("monaco-editor/esm/vs/language/css/css.worker?worker", () => ({ default: OtherWorker }))
vi.mock("monaco-editor/esm/vs/language/html/html.worker?worker", () => ({ default: OtherWorker }))
vi.mock("monaco-editor/esm/vs/language/typescript/ts.worker?worker", () => ({
  default: OtherWorker,
}))

interface MonacoEnv {
  getWorker(workerId: string, label: string): Worker
}

describe("monaco-setup", () => {
  it("points @monaco-editor/react at the bundled monaco so it never loads from a CDN", async () => {
    await import("./monaco-setup.ts")
    expect(configSpy).toHaveBeenCalledWith(
      expect.objectContaining({ monaco: expect.anything() }),
    )
  })

  it("routes language workers to the bundled copies and falls back to the editor worker", async () => {
    await import("./monaco-setup.ts")
    const env = (self as unknown as { MonacoEnvironment?: MonacoEnv }).MonacoEnvironment
    if (!env) throw new Error("monaco-setup did not install MonacoEnvironment")
    expect(env.getWorker("x", "json")).toBeInstanceOf(JsonWorker)
    // unknown labels (e.g. yaml) get the base editor worker
    expect(env.getWorker("x", "yaml")).toBeInstanceOf(EditorWorker)
  })
})

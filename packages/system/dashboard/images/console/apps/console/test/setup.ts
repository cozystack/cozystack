import "@testing-library/jest-dom/vitest"
import { afterEach } from "vitest"
import { cleanup } from "@testing-library/react"

// React Testing Library auto-registers cleanup via a global `afterEach`
// hook; that hook only exists when vitest is run with `globals: true`.
// We use explicit imports in tests, so wire cleanup up manually here.
afterEach(() => {
  cleanup()
})

# cozystack-ui

Cozystack Marketplace + Console — a **pure SPA that talks directly to the
Kubernetes API**. No BFF, no backend, no codegen. UI entities are discovered
at runtime from `ApplicationDefinition` CRDs in the cluster.

## Development model

This UI is **developed entirely by Claude**. There is no human author writing
features in parallel. Every file in this repo was produced through a Claude
session and every change should be reviewable as a small, self-contained PR.
Implications:

- Keep modules small and obvious. The next Claude session has no tribal
  knowledge — only what's in code, tests, and this file.
- Prefer convention over abstraction. Three similar widgets are better than a
  premature factory.
- If you're tempted to write a comment explaining *what* the code does, rename
  things instead. Only comment the non-obvious *why* (constraints, k8s quirks,
  workarounds) — and keep it to one short line.
- Don't leave half-finished scaffolding behind. If you start a refactor and
  bail, revert it.

## Stack

- **pnpm workspace** — `apps/console` is the SPA; `packages/{k8s-client,ui,types}` are workspace deps.
- **React 19 + Vite 8 + TypeScript ~6.0** with `module: esnext`,
  `moduleResolution: bundler`, `verbatimModuleSyntax`, `allowImportingTsExtensions`,
  `erasableSyntaxOnly`, `noUnusedLocals`, `noUnusedParameters`.
- **Tailwind 4** via `@tailwindcss/vite` + **Base UI** (`@base-ui/react`) for primitives.
- **React Query** for caching; custom watch layer streams chunked JSON events.
- **RJSF** (`@rjsf/core` + `validator-ajv8`) for application config forms.
- **Vitest + jsdom + @testing-library/react** for tests.
- **Monaco** for YAML view, **noVNC** for the VM console tab.

## Dev loop

```sh
pnpm install
kubectl proxy --port 8001          # required — terminates TLS, proxies /api + /apis
pnpm dev                            # http://localhost:3001
pnpm typecheck                      # tsc --noEmit across the workspace
pnpm test                           # vitest run
pnpm lint                           # eslint
pnpm build                          # tsc check + vite build into apps/console/dist
```

The Vite dev server proxies `/api`, `/apis`, and `/k8s` (VNC WebSocket prefix)
to `kubectl proxy`. In production, nginx (see `Containerfile`) proxies the
same paths to `kubernetes.default.svc` using the pod's service-account token.

## Code style

- **No semicolons.** Match the surrounding code — every file in the tree
  follows this. Don't reformat existing files.
- **Always import with explicit `.ts` / `.tsx` extensions** — required by
  `allowImportingTsExtensions` and the way packages re-export.
- **`import type { ... }` for type-only imports** — required by `verbatimModuleSyntax`.
- **Path aliases**: `@/` → `apps/console/src/`, plus the workspace deps
  `@cozystack/{k8s-client,ui,types}`. Don't reach into `../../packages/...`.
- **No `any`.** Use `unknown` and narrow, or a precise local interface.
  `as any` casts in older files are debt to pay down, not a pattern to extend.
- **No new top-level deps without a reason in the PR description.** This is a
  static SPA that ships to every Cozystack cluster — bundle size matters.

## Architecture rules

1. **Talk to the Kubernetes API directly.** Use `@cozystack/k8s-client`
   (`useK8sList`, `useK8sGet`, `useK8sCreate`, `useK8sUpdate`, `useK8sDelete`).
   Don't add a backend, server route, or proxy of your own.
2. **Discover, don't hardcode.** The marketplace, sidebar, detail pages, and
   forms are all driven from `ApplicationDefinition` resources
   (`cozystack.io/v1alpha1`). Adding a new application kind to Cozystack
   should require zero UI changes — if it doesn't, fix the generic path
   instead of adding a special case.
3. **`useK8sList` already does watches.** It seeds with a LIST then upgrades
   to a chunked-encoding WATCH against the same `resourceVersion`. Don't
   poll. Don't add `refetchInterval`. If you need a one-shot, pass
   `{ watch: false }`.
4. **Tenant scoping.** Most resources live in `tenant-<name>` namespaces.
   Pull the active tenant from `useTenantContext()` — never read the
   namespace from a URL param or guess it.
5. **Auth.** In production the SPA sits behind oauth2-proxy. The client
   relies on cookies forwarded by nginx, and `/oauth2/userinfo` returns the
   logged-in user. There is no token handling in the SPA itself.

## Forms (RJSF) pipeline

Every application's configure form is built from
`ApplicationDefinition.spec.application.openAPISchema` (a JSON-encoded OpenAPI
schema). The pipeline in `apps/console/src/components/SchemaForm.tsx`:

1. `sanitizeSchema` strips Kubernetes-specific extensions
   (`x-kubernetes-int-or-string`, `x-kubernetes-preserve-unknown-fields`) and
   renames `"Chart Values"` → `"Parameters"`.
2. `keysOrderToUiSchema` reads `spec.dashboard.keysOrder` and emits per-level
   `ui:order` arrays.
3. A chain of `addXxxWidgets(schema, uiSchema)` walks the schema and binds
   widgets:
   - any field carrying the `x-cozystack-options` schema keyword → `DynamicOptionsWidget`, a runtime dropdown populated from the cluster's `Option` resource (`core.cozystack.io`) keyed by the keyword's `source`; `addDynamicOptionWidgets` (`lib/dynamic-options.ts`) recurses into `properties`, array `items` and `additionalProperties`, and replaces the former field-name-bound `StorageClassWidget` / `BackupClassWidget` / `VMDiskWidget`.
   - object with `additionalProperties: <schema>` → `AdditionalPropertiesField`
   - credential-shaped fields (`password`, `*token`, `*accessKey`, …) →
     `SensitiveStringWidget` — see `lib/sensitive-fields.ts` and its tests for
     the exact matching rules.
4. Defaults are emitted to the parent once per schema via `getDefaultFormState`
   so the first submit always carries a populated spec.

When you add a new widget binding:

- Add it to the chain in `SchemaForm.tsx` in a deterministic order.
- Walk `properties` *and* `items` for arrays. Do **not** walk
  `oneOf`/`anyOf`/`allOf` unless a real chart needs it — there's a
  "pin broken behaviour" test that documents this gap intentionally.
- Don't mutate the input `uiSchema`. Return a new object. There are tests
  asserting this.

## Project layout

```
apps/console/
  src/
    App.tsx, main.tsx              # entry + routing
    routes/                         # one file per top-level page
      detail/                       # ApplicationDetailPage + tabs (Overview, Workloads, …)
    components/                     # page-level components, form widgets, command palette
    lib/                            # app-definitions, tenant-context, sensitive-fields, …
    hooks/
  test/setup.ts                     # vitest + jest-dom + manual RTL cleanup
packages/
  k8s-client/   # K8sClient (list/get/create/update/patch/delete/watch) + React Query hooks
  ui/           # AppShell, Sidebar, Header, Button, StatusBadge, Spinner, Dropdown, Section
  types/        # ApplicationDefinition, ApplicationInstance, Tenant, TenantNamespace, group/version constants
Containerfile                       # multi-stage build → nginx-unprivileged on :8080
.github/workflows/                  # test.yaml (typecheck + vitest), build.yaml (multi-arch image to ghcr)
```

## Testing

- Tests live next to the code they cover (`SensitiveStringWidget.test.tsx`,
  `sensitive-fields.test.ts`). Co-locate; don't centralise.
- `apps/console/test/setup.ts` wires `@testing-library/jest-dom` and registers
  `afterEach(cleanup)` manually — vitest is **not** run with `globals: true`,
  so `expect`, `describe`, etc. must be imported explicitly.
- When you fix a subtle widget bug, write the test first. The
  `addSensitiveStringWidgets` suite is the model: small focused cases, one
  invariant per `it`, and a "pin broken behaviour" group for known gaps.

## CI

`test.yaml` runs `pnpm typecheck` + `pnpm test` on every push and PR.
`build.yaml` builds and pushes a multi-arch image to
`ghcr.io/<repo>` on `main` and `v*` tags. Don't add other workflows without a
clear reason — keep CI fast.

## What goes in a good PR here

- One feature or one fix per branch. Small enough that a human can review it
  in a few minutes without context.
- A title that follows the conventional-commit style already in use
  (`feat(console): …`, `fix(forms): …`, `fix(external-ips): …`).
- If you touched form logic, add or update tests.
- If you added a new application kind to the sidebar/marketplace, double-check
  that the generic path handles it instead of special-casing.
- Don't bundle drive-by reformatting with a behavioural change.

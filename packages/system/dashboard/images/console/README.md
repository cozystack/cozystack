# cozystack-ui

Cozystack Marketplace and Console UI — a pure SPA that talks directly to the
Kubernetes API. No BFF, no controller-generated configuration: all UI entities
are discovered dynamically from `ApplicationDefinitions` in the cluster.

## Structure

- `apps/console` — React + Vite SPA
- `packages/k8s-client` — fetch-based Kubernetes client with watch
- `packages/ui` — shared UI components (Tailwind + Base UI)
- `packages/types` — shared TypeScript types

## Requirements

- Node.js ≥ 20
- pnpm ≥ 9
- `kubectl proxy` for local development (proxies `/api` and `/apis`)

## Development

```sh
pnpm install
# in one terminal:
kubectl proxy --port 8001
# in another terminal:
pnpm dev
```

The app becomes available at <http://localhost:3001/>.

## Build

```sh
pnpm build
```

The console is built into `apps/console/dist/`.

## Test

```sh
pnpm test
```

Runs the workspace test suites (vitest + jsdom for the console).

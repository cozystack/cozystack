import type { ComponentType } from "react"
import {
  Database,
  Globe,
  HardDrive,
  Info,
  Monitor,
  Network,
  Server,
  Users,
  type LucideIcon,
} from "lucide-react"
import {
  siApachekafka,
  siClickhouse,
  siEtcd,
  siHarbor,
  siKubernetes,
  siMariadb,
  siMongodb,
  siNatsdotio,
  siNginx,
  siOpensearch,
  siPostgresql,
  siPrometheus,
  siQdrant,
  siRabbitmq,
  siRedis,
  siVault,
  siWireguard,
  type SimpleIcon as SimpleIconData,
} from "simple-icons"

/**
 * TODO(bff): move both of these mappings to the server. Ideally each
 * ApplicationDefinition carries a `spec.dashboard.iconSlug` (Simple Icons
 * slug) or `spec.dashboard.iconLucide` (a Lucide name) so the frontend
 * doesn't need a hardcoded table. Until then we keep the mapping here and
 * fall through to the generic Lucide fallback.
 */
const KIND_TO_SIMPLE_ICON: Record<string, SimpleIconData> = {
  // PaaS
  ClickHouse: siClickhouse,
  Harbor: siHarbor,
  Kafka: siApachekafka,
  MariaDB: siMariadb,
  MongoDB: siMongodb,
  NATS: siNatsdotio,
  OpenBAO: siVault,
  Postgres: siPostgresql,
  Qdrant: siQdrant,
  RabbitMQ: siRabbitmq,
  Redis: siRedis,

  // NaaS
  HTTPCache: siNginx,
  VPN: siWireguard,

  // Administration
  Etcd: siEtcd,
  Ingress: siNginx,
  Kubernetes: siKubernetes,
  Monitoring: siPrometheus,
  OpenSearch: siOpensearch,
}

const ICON_BY_SLUG: Record<string, SimpleIconData> = Object.fromEntries(
  Object.values(KIND_TO_SIMPLE_ICON).map((icon) => [icon.slug, icon]),
)

/**
 * Lucide fallbacks for kinds that don't have a canonical brand logo in
 * Simple Icons. These use the same pack as cozyportal-ui.
 */
const KIND_TO_LUCIDE_ICON: Record<string, LucideIcon | ComponentType<{ className?: string }>> = {
  BootBox: Server,
  // Amazon S3's logo was removed from Simple Icons, so the S3-compatible
  // Bucket kind falls back to a generic storage glyph.
  Bucket: Database,
  ExternalDNS: Globe,
  TCPBalancer: Network,
  FoundationDB: Database,
  Info: Info,
  SeaweedFS: Database,
  Tenant: Users,
  VirtualPrivateCloud: Network,
  VMDisk: HardDrive,
  VMInstance: Monitor,
}

export function simpleIconSlug(kind: string): string | undefined {
  return KIND_TO_SIMPLE_ICON[kind]?.slug
}

export function lucideIcon(
  kind: string,
): LucideIcon | ComponentType<{ className?: string }> | undefined {
  return KIND_TO_LUCIDE_ICON[kind]
}

/**
 * Build a monochromatic icon component that renders the Simple Icons SVG as a
 * CSS `mask-image`. The span takes its colour from `currentColor`, so active
 * sidebar items pick up the blue accent and inactive ones stay slate-400 —
 * exactly like the Lucide icons next to them.
 *
 * The SVG is inlined from the bundled simple-icons package as a data URI, so it
 * is served from the app's own origin — nothing is fetched from a CDN.
 */
export function simpleIconComponent(slug: string): ComponentType<{ className?: string }> {
  const icon: SimpleIconData | undefined = ICON_BY_SLUG[slug]
  if (!icon) {
    // Unknown slug — render an empty placeholder rather than throwing.
    return function MissingIcon({ className }) {
      return <span aria-hidden className={className} />
    }
  }
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="${icon.path}"/></svg>`
  const url = `url("data:image/svg+xml,${encodeURIComponent(svg)}")`
  return function SimpleIcon({ className }) {
    return (
      <span
        aria-hidden
        className={className}
        style={{
          display: "inline-block",
          backgroundColor: "currentColor",
          maskImage: url,
          WebkitMaskImage: url,
          maskSize: "contain",
          WebkitMaskSize: "contain",
          maskRepeat: "no-repeat",
          WebkitMaskRepeat: "no-repeat",
          maskPosition: "center",
          WebkitMaskPosition: "center",
        }}
      />
    )
  }
}

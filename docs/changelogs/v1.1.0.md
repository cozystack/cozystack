<!--
https://github.com/cozystack/cozystack/releases/tag/v1.1.0
-->

# Cozystack v1.1.0

Cozystack v1.1.0 delivers a major expansion of the managed application catalog with **OpenBAO** (open-source HashiCorp Vault fork) for secrets management, comprehensive **tiered object storage** with SeaweedFS storage pools, a new bucket **user model** with per-user credentials and S3 login support, **RabbitMQ version selection**, and **MongoDB Grafana dashboards**. The dashboard gains storageClass dropdowns for all stateful apps. This release also incorporates all fixes from the v1.0.x patch series.

## Feature Highlights

### OpenBAO: Managed Secrets Management Service

Cozystack now ships **OpenBAO** as a fully managed PaaS application — an open-source fork of HashiCorp Vault providing enterprise-grade secrets management. Users can deploy OpenBAO instances in standalone mode (single replica with file storage) or in high-availability Raft mode (multiple replicas with integrated Raft consensus), with the mode switching automatically based on the `replicas` field.

Each OpenBAO instance gets TLS enabled by default via cert-manager self-signed certificates, with DNS SANs covering all service endpoints and pod addresses. The Vault injector and CSI provider are intentionally disabled (they are cluster-scoped components not safe for per-tenant use). OpenBAO requires manual initialization and unsealing by design — no auto-unseal is configured.

A full end-to-end E2E test covers the complete lifecycle: deploy, wait for certificate and API readiness, init, unseal, verify, and cleanup. OpenBAO is available in the application catalog for tenant namespaces.

### SeaweedFS Tiered Storage Pools

SeaweedFS now supports **tiered storage pools** — operators can define separate storage pools per disk type (SSD, HDD, NVMe) in the `volume.pools` field (Simple topology) or `volume.zones[name].pools` (MultiZone topology). Each pool creates an additional Volume StatefulSet alongside the default one, with SeaweedFS distinguishing storage via the `-disk=<type>` flag on volume servers.

Each pool automatically generates its own set of COSI resources: a standard `BucketClass`, a `-lock` BucketClass (COMPLIANCE mode, 365-day retention), a read-write `BucketAccessClass`, and a `-readonly` BucketAccessClass. This allows applications to place data on specific storage tiers and request appropriate access policies per pool.

In MultiZone topology, pools are defined per zone and each zone × pool combination creates a dedicated StatefulSet (e.g., `us-east-ssd`, `us-west-hdd`), with nodes selected via `topology.kubernetes.io/zone` labels. Existing deployments with no pools defined produce output identical to previous versions — no migration is required.

### Bucket User Model with S3 Login

The bucket application introduces a new **user model** for access management. Instead of a single implicit BucketAccess resource, operators now define a `users` map where each entry creates a dedicated `BucketAccess` with its own credentials secret and an optional `readonly` flag. The S3 Manager UI has been updated with a login screen that uses per-session credentials from the user's own secret, replacing the previous basic-auth approach.

Two new bucket parameters are available: `locking` provisions from the `-lock` BucketClass (COMPLIANCE mode, 365-day object lock retention) for write-once-read-many use cases, and `storagePool` selects a specific pool's BucketClass for tiered storage placement. The COSI driver has been updated to v0.3.0 to support the new `diskType` parameter.

**⚠️ Breaking change**: The implicit default BucketAccess resource is no longer created. Existing buckets that relied on the single auto-generated BucketAccess will need to explicitly define users in the `users` map after upgrading.

### RabbitMQ Version Selection

RabbitMQ instances now support a configurable **version selector** (`version` field with values: `v4.2`, `v4.1`, `v4.0`, `v3.13`; default `v4.2`). The chart validates the selection at deploy time and uses it to pin the runtime image, giving operators control over the RabbitMQ release channel per instance. An automatic migration backfills the `version` field on all existing RabbitMQ resources to `v4.2`.

## Major Features and Improvements

* **[apps] Add OpenBAO as a managed secrets management service**: Deployed as a PaaS application with standalone (file storage) and HA Raft modes, TLS enabled by default via cert-manager, injector and CSI provider disabled for tenant safety, and a full E2E lifecycle test ([**@lexfrei**](https://github.com/lexfrei) in #2059).

* **[seaweedfs] Add storage pools support for tiered storage**: Added `volume.pools` (Simple) and `volume.zones[name].pools` (MultiZone) for per-disk-type StatefulSets, zone overrides (`nodeSelector`, `storageClass`, `dataCenter`), per-pool COSI BucketClass and BucketAccessClass resources, and bumped seaweedfs-cosi-driver to v0.3.0 ([**@sircthulhu**](https://github.com/sircthulhu) in #2097).

* **[apps][system] Add bucket user model with locking and storage pool selection**: Replaced implicit BucketAccess with per-user `users` map, added `locking` and `storagePool` parameters, renamed COSI BucketClass suffix from `-worm` to `-lock`, added `-readonly` BucketAccessClass for all topologies, and updated S3 Manager with login screen using per-user credentials ([**@IvanHunters**](https://github.com/IvanHunters) in #2119).

* **[rabbitmq] Add version selection for RabbitMQ instances**: Added `version` field (`v4.2`, `v4.1`, `v4.0`, `v3.13`) with chart-level validation, default `v4.2`, and an automatic migration to backfill the field on existing instances ([**@myasnikovdaniil**](https://github.com/myasnikovdaniil) in #2092).

* **[system] Add MongoDB Overview and InMemory Details Grafana dashboards**: Added two comprehensive Grafana dashboards for MongoDB monitoring — Overview (command operations, connections, cursors, query efficiency, write time) and InMemory Details (WiredTiger cache, transactions, concurrency, eviction). Dashboards are registered in `dashboards.list` for automatic GrafanaDashboard CRD generation ([**@IvanHunters**](https://github.com/IvanHunters) in #2158).

* **[dashboard] Add storageClass dropdown for all stateful apps**: Replaced the free-text `storageClass` input with an API-backed dropdown listing available StorageClasses from the cluster. Affects ClickHouse, Harbor, HTTPCache, Kubernetes, MariaDB, MongoDB, NATS, OpenBAO, Postgres, Qdrant, RabbitMQ, Redis, VMDisk (top-level `storageClass`), FoundationDB (`storage.storageClass`), and Kafka (`kafka.storageClass`, `zookeeper.storageClass`) ([**@sircthulhu**](https://github.com/sircthulhu) in #2131).

* **[bucket] Add readonly S3 access credentials**: Added a readonly `BucketAccessClass` to the SeaweedFS COSI chart and updated the bucket application to automatically provision two sets of S3 credentials per bucket: read-write (for UI) and readonly ([**@IvanHunters**](https://github.com/IvanHunters) in #2105).

* **[dashboard] Hide sidebar on cluster-level pages when no tenant selected**: Fixed broken URLs with double `//` on the main cluster page (before tenant selection) by clearing `CUSTOMIZATION_SIDEBAR_FALLBACK_ID` so no sidebar renders when no namespace is selected ([**@sircthulhu**](https://github.com/sircthulhu) in #2106).

* **[cert-manager] Update cert-manager to v1.19.3**: Upgraded cert-manager with new CRDs moved into a dedicated CRD package, added global `nodeSelector` and `hostUsers` (pod user-namespace isolation), and renamed `ServiceMonitor` targetPort default to `http-metrics` ([**@myasnikovdaniil**](https://github.com/myasnikovdaniil) in #2070).

* **[dashboard] Add backupClasses dropdown to Plan/BackupJob forms**: Replaced free-text input for `backupClass` field with an API-backed dropdown populated with available BackupClass resources, making it easier to select the correct backup target ([**@androndo**](https://github.com/androndo) in #2104).

## Fixes

* **[platform] Fix package name conversion in migration script**: Fixed the `migrate-to-version-1.0.sh` script to correctly prepend the `cozystack.` prefix when converting `BUNDLE_DISABLE` and `BUNDLE_ENABLE` package name lists, ensuring packages are properly identified during the v0.41→v1.0 upgrade ([**@myasnikovdaniil**](https://github.com/myasnikovdaniil) in #2144, #2148).

* **[backups] Fix RBAC for backup controllers**: Updated RBAC permissions for the backup strategy controller to support enhanced backup and restore capabilities, including Velero integration and status management ([**@androndo**](https://github.com/androndo) in #2145).

* **[kubernetes] Set explicit MTU for Cilium in tenant clusters**: Set explicit MTU 1350 for Cilium in KubeVirt-based tenant Kubernetes clusters to prevent packet drops caused by VXLAN encapsulation overhead. Cilium's auto-detection does not account for VXLAN overhead (50 bytes) when the VM interface inherits MTU 1400 from the parent OVN/Geneve overlay, causing intermittent connectivity issues and HTTP 499 errors under load ([**@IvanHunters**](https://github.com/IvanHunters) in #2147).

* **[platform] Prevent cozystack-version ConfigMap from deletion**: Added resource protection annotations to prevent the `cozystack-version` ConfigMap from being accidentally deleted, improving platform stability ([**@myasnikovdaniil**](https://github.com/myasnikovdaniil) in #2112, #2114).

* **[installer] Add keep annotation to Namespace and update migration script**: Added `helm.sh/resource-policy: keep` annotation to the `cozy-system` Namespace in the installer Helm chart to prevent Helm from deleting the namespace and all HelmReleases within it when the installer release is removed. The v1.0 migration script is also updated to annotate the namespace and `cozystack-version` ConfigMap before migration ([**@kvaps**](https://github.com/kvaps) in #2122, #2123).

* **[dashboard] Add FlowSchema to exempt BFF from API throttling**: Added a `cozy-dashboard-exempt` FlowSchema to exempt the dashboard Back-End-for-Frontend service account from Kubernetes API Priority and Fairness throttling, preventing 429 errors under load ([**@kvaps**](https://github.com/kvaps) in #2121, #2124).

* **[platform] Suspend cozy-proxy if it conflicts with installer release during migration**: Added a check in the v0.41→v1.0 migration script to detect and suspend the `cozy-proxy` HelmRelease when its `releaseName` is set to `cozystack`, which conflicts with the installer release and would cause `cozystack-operator` deletion during the upgrade ([**@kvaps**](https://github.com/kvaps) in #2128, #2130).

* **[platform] Fix off-by-one error in run-migrations script**: Fixed a bug in the migration runner where the first required migration was always skipped due to an off-by-one error in the migration range calculation ([**@myasnikovdaniil**](https://github.com/myasnikovdaniil) in #2126, #2132).

* **[system] Fix Keycloak proxy configuration for v26.x**: Replaced the deprecated `KC_PROXY=edge` environment variable with `KC_PROXY_HEADERS=xforwarded` and `KC_HTTP_ENABLED=true` in the Keycloak StatefulSet. `KC_PROXY` was removed in Keycloak 26.x, previously causing "Non-secure context detected" warnings and broken cookie handling behind a reverse proxy with TLS termination ([**@sircthulhu**](https://github.com/sircthulhu) in #2125, #2134).

* **[dashboard] Allow clearing instanceType field and preserve newlines in secret copy**: Added `allowEmpty: true` to the `instanceType` field in the VMInstance form so users can explicitly clear it to use custom KubeVirt resources without a named instance type. Also fixed newline preservation when copying secrets with CMD+C ([**@sircthulhu**](https://github.com/sircthulhu) in #2135, #2137).

* **[dashboard] Restore stock-instance sidebars for namespace-level pages**: Restored `stock-instance-api-form`, `stock-instance-api-table`, `stock-instance-builtin-form`, and `stock-instance-builtin-table` sidebar resources that were inadvertently removed in #2106. Without these sidebars, namespace-level pages such as Backup Plans rendered as empty pages ([**@sircthulhu**](https://github.com/sircthulhu) in #2136, #2138).

## System Configuration

* **[platform] Disable private key rotation in CA certs**: Set `rotationPolicy: Never` for all CA/root certificates used by system components (ingress-nginx, linstor, linstor-scheduler, seaweedfs, victoria-metrics-operator, kubeovn-webhook, lineage-controller-webhook, cozystack-api, etcd, linstor API/internal) to prevent trust chain problems when CA certificates are reissued ([**@myasnikovdaniil**](https://github.com/myasnikovdaniil) in #2113).

## Development, Testing, and CI/CD

* **[ci] Add debug improvements for CI tests**: Added extra debug commands for Kubernetes startup diagnostics and improved error output in CI test runs ([**@myasnikovdaniil**](https://github.com/myasnikovdaniil) in #2111).

## Documentation

* **[website] Add object storage guide (pools, buckets, users)**: Added a comprehensive guide covering SeaweedFS object storage configuration including storage pools for tiered storage, bucket creation with access classes, per-user credential management, and credential rotation procedures ([**@sircthulhu**](https://github.com/sircthulhu) in cozystack/website#438).

* **[website] Add Build Your Own Platform (BYOP) guide**: Added a new "Build Your Own Platform" guide and split the installation documentation into platform installation and BYOP sub-pages, with cross-references throughout the documentation ([**@kvaps**](https://github.com/kvaps) in cozystack/website#437).

* **[website] Add white labeling guide**: Added a comprehensive guide for configuring white labeling (branding) in Cozystack v1, covering Dashboard fields (`titleText`, `footerText`, `tenantText`, `logoText`, `logoSvg`, `iconSvg`) and Keycloak fields (`brandName`, `brandHtmlName`). Includes SVG preparation workflow with theme-aware template variables and portable base64 encoding ([**@lexfrei**](https://github.com/lexfrei) in cozystack/website#441).

* **[website] Actualize backup and recovery documentation**: Reworked the backup and recovery docs to be user-focused, separating operator and tenant workflows. Added tenant-facing documentation for `BackupJob` and `Plan` resources and a new Velero administration guide for operators ([**@androndo**](https://github.com/androndo) in cozystack/website#434).

* **[website] Add step to protect namespace before upgrading**: Updated the cluster upgrade guide and v0.41→v1.0 migration guide with a required step to annotate the `cozy-system` namespace and `cozystack-version` ConfigMap with `helm.sh/resource-policy=keep` before running `helm upgrade` ([**@kvaps**](https://github.com/kvaps) in cozystack/website#435).

* **[website] Replace bundles documentation with variants**: Renamed the "Bundles" documentation section to "Variants" to match current Cozystack terminology. Removed deprecated variants and added new ones: `default` and `isp-full-generic` ([**@kvaps**](https://github.com/kvaps) in cozystack/website#433).

* **[website] Fix component values override instructions**: Corrected the component values override documentation to reflect current configuration patterns ([**@kvaps**](https://github.com/kvaps) in cozystack/website#436).

## Breaking Changes & Upgrade Notes

* **[bucket] Bucket user model now requires explicit user definitions**: The implicit default `BucketAccess` resource is no longer created automatically. Existing buckets that relied on a single auto-generated credential secret will need to define users explicitly in the `users` map after upgrading. Each user entry creates its own `BucketAccess` resource and credential secret (optionally with `readonly: true`). The COSI BucketClass suffix has also been renamed from `-worm` to `-lock` ([**@IvanHunters**](https://github.com/IvanHunters) in #2119).

## Contributors

We'd like to thank all contributors who made this release possible:

* [**@androndo**](https://github.com/androndo)
* [**@IvanHunters**](https://github.com/IvanHunters)
* [**@kvaps**](https://github.com/kvaps)
* [**@lexfrei**](https://github.com/lexfrei)
* [**@myasnikovdaniil**](https://github.com/myasnikovdaniil)
* [**@sircthulhu**](https://github.com/sircthulhu)

---

**Full Changelog**: https://github.com/cozystack/cozystack/compare/v1.0.0...v1.1.0


<!--
https://github.com/cozystack/cozystack/releases/tag/v0.36.0
-->


## Feature Highlights

Release v0.36.0 focuses on the stability, observability, and flexible configuration of managed applications.

### Per-Namespace Resource Limits for Tenants

Resource management for Cozystack tenants has received a final patch and is now graduated to a stable feature.
Platform administrators can define explicit CPU, memory, and storage limits for each tenant's namespace 
via the tenant specification.
This prevents any single tenant from consuming more than their share of cluster resources,
ensuring cluster stability and a guaranteed service level for each tenant.

### Kube-OVN Cluster Health Monitor

A new component called the Kube-OVN Plunger continuously monitors the health of the Kube-OVN network's central control cluster.
This external agent gathers OVN cluster status and consensus information, exposing Prometheus metrics and live events stream via SSE.
As a result, it provides much better visibility of the virtual network layer and helps maintain a reliable and observable network in Cozystack.
This change opens the road to automated Kube-OVN database operations and recovery in specific corner cases.

### Configurable CoreDNS Addon for Kubernetes

Cozystack introduces a dedicated CoreDNS addon for managing cluster DNS with greater flexibility.
CoreDNS is now deployed via a Helm chart and can be tuned through custom values in the cluster specification,
including autoscaling, replica count, and adjusting service IP.
CoreDNS can now be configured in the dashboard and using Cozystack API.

### Granular SeaweedFS Service Configuration

The SeaweedFS S3 storage service in Cozystack is now far more configurable at a component level.
The Helm chart for SeaweedFS now includes independent configuration for each component and its resources.
It includes the master nodes, volume servers with support for multiple zones, filers, the backing database, and the S3 gateway.
Administrators can set per-component parameters such as the number of replicas, available CPU, memory, and storage size.

### Server-side Encryption for S3

Cozystack v0.36.0 includes SeaweedFS 3.97, bringing support for server-side encryption of S3 buckets (SSE-C, SSE-KMS, and SSE-S3).

**Breaking change:** upon updating Cozystack, SeaweedFS will be updated to a newer version, and the services specification
will be converted to the new format.

### Custom Resource Profiles for Ingress Controller

NGINX controller is now configurable on a per-replica basis.
Configurations include the ingress controller pods' CPU and memory requests/limits, either with direct values or using one of the available presets.

### Cozystack REST API Documentation

[Cozystack REST API reference](https://cozystack.io/docs/cozystack-api/rest/) is now published on the website.
It includes endpoints and methods for listing, creating, updating, and removing each managed application, defined as Cozystack CRD.


### Built-in LLDP-Based Neighbor Discovery in Talos

Cozystack now includes the LLDPD extension in its Talos OS image, enabling Link Layer Discovery Protocol (LLDP) out of the box.
This means each node can automatically discover and advertise its network neighbors and topology without any manual setup.

### Use external IP for Egress Traffic in VMs

When a virtual machine has an external IP assigned to it, it will now always use it for egress traffic, independently of the external method used.

## Major Features and Improvements

* [talos] Add LLDPD (`ghcr.io/siderolabs/lldpd`) as a built-in system extension, enabling LLDP-based neighbor discovery out of the box. (@lllamnyp in https://github.com/cozystack/cozystack/pull/1351 and https://github.com/cozystack/cozystack/pull/1360)
* [kubernetes] Add a configurable CoreDNS addon with valuesOverride, packaged chart, and managed deployment (metrics, autoscaling, HPA, customizable Service). (@klinch0 in https://github.com/cozystack/cozystack/pull/1362)
* [kube-ovn] Implement the Kube-OVN plunger, an external monitoring agent for the ovn-central cluster. (@lllamnyp in https://github.com/cozystack/cozystack/pull/1380, patched in https://github.com/cozystack/cozystack/pull/1414 and https://github.com/cozystack/cozystack/pull/1418)
* [tenant] Enable per-namespace resource quota settings in tenants, with explicit cpu, memory, and storage values. (@IvanHunters in https://github.com/cozystack/cozystack/pull/1389)
* [seaweedfs] Add detailed resource configuration for each component of the SeaweedFS service. (@klinch0 and @kvaps in https://github.com/cozystack/cozystack/pull/1415)
* [ingress] Enable per-replica resource configuration to the ingress controller. (@kvaps in https://github.com/cozystack/cozystack/pull/1416)
* [virtual-machine] Use external IP for egress traffic with `PortList` method. (@kvaps in https://github.com/cozystack/cozystack/pull/1349)


## Fixes

* [cozy-lib] Fix malformed retrieval of `cozyConfig` in the cozy-lib template. (@lllamnyp in https://github.com/cozystack/cozystack/pull/1348)
* [cozy-lib] Add the missing template `cozy-lib.resources.flatten`. (@kvaps in https://github.com/cozystack/cozystack/pull/1372)
* [cozystack-api] Sanitize the OpenAPI v2 schema. (@kvaps in https://github.com/cozystack/cozystack/pull/1353)
* [kube-ovn] Improve northd leader detection. Patch the northd leader check to test against all endpoints instead of just the first one marked as ready. (@lllamnyp in https://github.com/cozystack/cozystack/pull/1363)
* [seaweedfs] Add a liveness check for the SeaweedFS S3 endpoint to improve health monitoring and enable automatic recovery. (@IvanHunters in https://github.com/cozystack/cozystack/pull/1368)
* [seaweedfs] Resolve race conditions in SeaweedFS. Increase deployment timeouts and set install/upgrade remediation to unlimited retries to improve deployment resilience. (@IvanHunters in https://github.com/cozystack/cozystack/pull/1371)
* [seaweedfs] Resolve connectivity issues in SeaweedFS. Increase Nginx ingress timeouts for SeaweedFS S3 endpoint. (@kvaps in https://github.com/cozystack/cozystack/pull/1386)
* [virtual-machine] Fix the reg ression in VM update hook introduced in https://github.com/cozystack/cozystack/pull/1169. Target the correct API resource and avoid conflicts with KubeVirt resources. (@kvaps in https://github.com/cozystack/cozystack/pull/1376)
* [virtual-machine] Correct app version references in `virtual-machine` and `vm-instance`, ensuring accurate versioning during migrations. (@kvaps in https://github.com/cozystack/cozystack/pull/1378).
* [cozyreport] Fix an error where cozyreport tried to parse non-existent objects and generated garbage output in CI debug logs. (@lllamnyp in https://github.com/cozystack/cozystack/pull/1383)
* [platform] Fix a boolean override bug in Helm merge. ConfigMap values now correctly take precedence over bundle defaults. (@dyudin0821 in https://github.com/cozystack/cozystack/pull/1385)
* [kubernetes] CoreDNS release now installs and stores state in the `kube-system` namespace. (@kvaps in https://github.com/cozystack/cozystack/pull/1395)
* [kubernetes] Expose configuration for CoreDNS, enabling setting the image repository and replica count via `values.yaml`. (@kvaps in https://github.com/cozystack/cozystack/pull/1410)
* [etcd] Ensure that TopologySpreadConstraints consistently target etcd pods. (@kvaps in https://github.com/cozystack/cozystack/pull/1405)
* [tenant] Use force-upgrade for ingress controller charts. (@klinch0 in https://github.com/cozystack/cozystack/pull/1404)
* [cozystack-controller] Fix an RBAC error that prevented the workload labelling feature from working. (@lllamnyp in https://github.com/cozystack/cozystack/pull/1419)
* [seaweedfs] Remove VerticalPodAutoscaler for SeaweedFS. (@kvaps in https://github.com/cozystack/cozystack/pull/1421)


## Dependencies

* Update LINSTOR to v1.31.3. (@kvaps in https://github.com/cozystack/cozystack/pull/1358)
* Update SeaweedFS to v3.97. (@kvaps in https://github.com/cozystack/cozystack/pull/1361 and https://github.com/cozystack/cozystack/pull/1373)
* Update Kube-OVN to 1.14.5. (@lllamnyp in https://github.com/cozystack/cozystack/pull/1363)
* Replace Bitnami images with alternatives in all charts. (@kvaps in https://github.com/cozystack/cozystack/pull/1374)

## Documentation

## Development, Testing, and CI/CD

* [dx] Remove the BUILDER and PLATFORM autodetect logic in Makefiles. (@kvaps in https://github.com/cozystack/cozystack/pull/1391)
* [ci] Use the host buildx config in CI. (@kvaps in https://github.com/cozystack/cozystack/pull/1015)
* [ci] Add `jq` and `git` to the installer image. (@kvaps in https://github.com/cozystack/cozystack/pull/1417)
* [ci] Source the `REGISTRY` environment variable from actions' variables, not secrets, so external pull requests can work. (@lllamnyp in https://github.com/cozystack/cozystack/pull/1423)

---

**Full Changelog**: https://github.com/cozystack/cozystack/compare/v0.35.0...v0.36.0

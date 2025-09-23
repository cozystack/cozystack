# FoundationDB

A managed FoundationDB service for Cozystack.

## Overview

FoundationDB is a distributed database designed to handle large volumes of structured data across clusters of commodity servers. It organizes data as an ordered key-value store and employs ACID transactions for all operations.

This package provides a managed FoundationDB cluster deployment using the FoundationDB Kubernetes Operator.

## Features

- **High Availability**: Multi-instance deployment with automatic failover
- **ACID Transactions**: Full ACID transaction support across the cluster
- **Scalable**: Easily scale storage and compute resources
- **Backup Integration**: Optional S3-compatible backup storage
- **Monitoring**: Built-in monitoring and alerting through WorkloadMonitor
- **Flexible Configuration**: Support for custom FoundationDB parameters

## Configuration

### Basic Configuration

```yaml
# Cluster process configuration
cluster:
  version: "7.3.63"
  processCounts:
    storage: 3           # Number of storage processes (determines cluster size)
    stateless: -1        # Automatically calculated
    cluster_controller: 1
  faultDomain:
    key: "kubernetes.io/hostname"
    valueFrom: "spec.nodeName"
```

### Storage

```yaml
storage:
  size: "16Gi"           # Storage size per instance
  storageClass: ""       # Storage class (optional)
```

### Resources

```yaml
# Use preset sizing
resourcesPreset: "medium"  # small, medium, large, xlarge, 2xlarge

# Or custom resource configuration
resources:
  cpu: "2000m"
  memory: "4Gi"
```

### Backup (Optional)

```yaml
backup:
  enabled: true
  s3:
    bucket: "my-fdb-backups"
    endpoint: "https://s3.amazonaws.com"
    region: "us-east-1"
    credentials:
      accessKeyId: "AKIA..."
      secretAccessKey: "..."
  retentionPolicy: "7d"
```

### Advanced Configuration

```yaml
# Custom FoundationDB parameters
customParameters:
  - "knob_disable_posix_kernel_aio=1"

# Image type (unified is default and recommended for new deployments)
imageType: "unified"

# Enable automatic pod replacements
automaticReplacements: true

# Security context configuration
securityContext:
  runAsUser: 4059
  runAsGroup: 4059
```

## Prerequisites

- FoundationDB Operator must be installed in the cluster
- Sufficient storage and compute resources
- For backups: S3-compatible storage credentials

## Deployment

1. Install the FoundationDB operator (system package)
2. Deploy this application package with your desired configuration
3. The cluster will be automatically provisioned and configured

## Monitoring

This package includes WorkloadMonitor integration for cluster health monitoring and resource tracking. Monitoring can be disabled by setting:

```yaml
monitoring:
  enabled: false
```

## Security

- All containers run with restricted security contexts
- No privilege escalation allowed
- Read-only root filesystem where possible
- Custom security context configurations supported

## Fault Tolerance

FoundationDB is designed for high availability:
- Automatic failure detection and recovery
- Data replication across instances
- Configurable fault domains for rack/zone awareness
- Transaction log redundancy

The included `WorkloadMonitor` is automatically configured based on the `cluster.redundancyMode` value. It sets the `minReplicas` property on the `WorkloadMonitor` resource to ensure the cluster's health status accurately reflects its fault tolerance level. The number of tolerated failures is as follows:
- `single`: 0 failures
- `double`: 1 failure
- `triple` and datacenter-aware modes: 2 failures

For example, with the default configuration (`redundancyMode: double` and 3 storage pods), `minReplicas` will be set to 2.

## Performance Considerations

- Use SSD storage for better performance
- Consider dedicating nodes for storage processes
- Monitor cluster metrics for optimization opportunities
- Scale storage and stateless processes based on workload

## Support

For issues related to FoundationDB itself, refer to the [FoundationDB documentation](https://apple.github.io/foundationdb/).

For Cozystack-specific issues, consult the Cozystack documentation or support channels.

## Parameters

### Common parameters

| Name                                       | Description                                                                                                                                | Type        | Value                    |
| ------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------ | ----------- | ------------------------ |
| `cluster`                                  | Cluster configuration                                                                                                                      | `object`    | `{}`                     |
| `cluster.processCounts`                    | Process counts for different roles                                                                                                         | `object`    | `{}`                     |
| `cluster.processCounts.stateless`          | Number of stateless processes (-1 for automatic)                                                                                           | `int`       | `-1`                     |
| `cluster.processCounts.storage`            | Number of storage processes (determines cluster size)                                                                                      | `int`       | `3`                      |
| `cluster.processCounts.cluster_controller` | Number of cluster controller processes                                                                                                     | `int`       | `1`                      |
| `cluster.version`                          | Version of FoundationDB to use                                                                                                             | `string`    | `7.3.63`                 |
| `cluster.redundancyMode`                   | Database redundancy mode (single, double, triple, three_datacenter, three_datacenter_fallback)                                             | `string`    | `double`                 |
| `cluster.storageEngine`                    | Storage engine (ssd-2, ssd-redwood-v1, ssd-rocksdb-v1, memory)                                                                             | `string`    | `ssd-2`                  |
| `cluster.faultDomain`                      | Fault domain configuration                                                                                                                 | `object`    | `{}`                     |
| `cluster.faultDomain.key`                  | Fault domain key                                                                                                                           | `string`    | `kubernetes.io/hostname` |
| `cluster.faultDomain.valueFrom`            | Fault domain value source                                                                                                                  | `string`    | `spec.nodeName`          |
| `storage`                                  | Storage configuration                                                                                                                      | `object`    | `{}`                     |
| `storage.size`                             | Size of persistent volumes for each instance                                                                                               | `quantity`  | `16Gi`                   |
| `storage.storageClass`                     | Storage class (if not set, uses cluster default)                                                                                           | `string`    | `""`                     |
| `resources`                                | Explicit CPU and memory configuration for each FoundationDB instance. When left empty, the preset defined in `resourcesPreset` is applied. | `*object`   | `{}`                     |
| `resources.cpu`                            | CPU available to each instance                                                                                                             | `*quantity` | `null`                   |
| `resources.memory`                         | Memory (RAM) available to each instance                                                                                                    | `*quantity` | `null`                   |
| `resourcesPreset`                          | Default sizing preset used when `resources` is omitted. Allowed values: `small`, `medium`, `large`, `xlarge`, `2xlarge`.                   | `string`    | `medium`                 |
| `backup`                                   | Backup configuration                                                                                                                       | `object`    | `{}`                     |
| `backup.enabled`                           | Enable backups                                                                                                                             | `bool`      | `false`                  |
| `backup.s3`                                | S3 configuration for backups                                                                                                               | `object`    | `{}`                     |
| `backup.s3.bucket`                         | S3 bucket name                                                                                                                             | `string`    | `""`                     |
| `backup.s3.endpoint`                       | S3 endpoint URL                                                                                                                            | `string`    | `""`                     |
| `backup.s3.region`                         | S3 region                                                                                                                                  | `string`    | `us-east-1`              |
| `backup.s3.credentials`                    | S3 credentials                                                                                                                             | `object`    | `{}`                     |
| `backup.s3.credentials.accessKeyId`        | S3 access key ID                                                                                                                           | `string`    | `""`                     |
| `backup.s3.credentials.secretAccessKey`    | S3 secret access key                                                                                                                       | `string`    | `""`                     |
| `backup.retentionPolicy`                   | Retention policy for backups                                                                                                               | `string`    | `7d`                     |
| `monitoring`                               | Monitoring configuration                                                                                                                   | `object`    | `{}`                     |
| `monitoring.enabled`                       | Enable WorkloadMonitor integration                                                                                                         | `bool`      | `true`                   |


### FoundationDB configuration

| Name                         | Description                               | Type       | Value     |
| ---------------------------- | ----------------------------------------- | ---------- | --------- |
| `customParameters`           | Custom parameters to pass to FoundationDB | `[]string` | `[]`      |
| `imageType`                  | Container image deployment type           | `string`   | `unified` |
| `securityContext`            | Security context for containers           | `object`   | `{}`      |
| `securityContext.runAsUser`  | User ID to run the container              | `int`      | `4059`    |
| `securityContext.runAsGroup` | Group ID to run the container             | `int`      | `4059`    |
| `automaticReplacements`      | Enable automatic pod replacements         | `bool`     | `true`    |


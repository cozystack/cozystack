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
# Number of total instances
replicas: 3

# Cluster process configuration
cluster:
  version: "7.4.1"
  processCounts:
    storage: 3           # Storage processes
    stateless: -1        # Automatically calculated
    cluster_controller: 1
```

### Storage

```yaml
storage:
  size: "16Gi"           # Storage size per instance
  storageClass: ""       # Storage class (optional)
```

### Resources

```yaml
resources:
  preset: "medium"       # small, medium, large, xlarge
  # Custom overrides
  limits:
    cpu: "2000m"
    memory: "4Gi"
  requests:
    cpu: "1000m"
    memory: "2Gi"
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
advanced:
  # Custom FoundationDB parameters
  customParameters:
    - "knob_disable_posix_kernel_aio=1"
  
  # Image type (split recommended for production)
  imageType: "split"
  
  # Enable automatic pod replacements
  automaticReplacements: true
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

## Performance Considerations

- Use SSD storage for better performance
- Consider dedicating nodes for storage processes
- Monitor cluster metrics for optimization opportunities
- Scale storage and stateless processes based on workload

## Support

For issues related to FoundationDB itself, refer to the [FoundationDB documentation](https://apple.github.io/foundationdb/).

For Cozystack-specific issues, consult the Cozystack documentation or support channels.
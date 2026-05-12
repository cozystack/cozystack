# Managed Qdrant Service

Qdrant is a high-performance vector database and similarity search engine designed for AI and machine learning applications. It provides efficient storage and retrieval of high-dimensional vectors with advanced filtering capabilities, making it ideal for recommendation systems, semantic search, and RAG (Retrieval-Augmented Generation) applications.

## Deployment Details

Service deploys Qdrant as a StatefulSet with automatic cluster mode when multiple replicas are configured.

- Docs: https://qdrant.tech/documentation/
- GitHub: https://github.com/qdrant/qdrant

## Parameters

### Common parameters

| Name               | Description                                                                                                                      | Type       | Value      |
| ------------------ | -------------------------------------------------------------------------------------------------------------------------------- | ---------- | ---------- |
| `replicas`         | Number of Qdrant replicas. Cluster mode is automatically enabled when replicas > 1.                                              | `int`      | `1`        |
| `resources`        | Explicit CPU and memory configuration for each Qdrant replica. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`       |
| `resources.cpu`    | CPU available to each replica.                                                                                                   | `quantity` | `""`       |
| `resources.memory` | Memory (RAM) available to each replica.                                                                                          | `quantity` | `""`       |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                          | `string`   | `t1.small` |
| `size`             | Persistent Volume Claim size available for vector data storage.                                                                  | `quantity` | `10Gi`     |
| `storageClass`     | StorageClass used to store the data.                                                                                             | `string`   | `""`       |
| `external`         | Enable external access from outside the cluster.                                                                                 | `bool`     | `false`    |


## Parameter examples and reference

### resources and resourcesPreset

`resources` sets explicit CPU and memory configurations for each replica.
When left empty, the preset defined in `resourcesPreset` is applied.

```yaml
resources:
  cpu: 4000m
  memory: 4Gi
```

`resourcesPreset` sets named CPU and memory configurations for each replica.
This setting is ignored if the corresponding `resources` value is set.

Presets follow a cloud-style `<series>.<size>` naming convention. Five series cover the full CPU-to-memory ratio range (`t1` 1:0.5, `c1` 1:1, `s1` 1:2, `u1` 1:4, `m1` 1:8) and each series ships eight sizes (`nano` through `4xlarge`). The legacy flat names (`nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`) remain accepted as deprecated aliases of their 1:1 instance-type equivalents.

See [`docs/operations/resource-presets.md`](../../../docs/operations/resource-presets.md) for the full size matrix and the legacy-to-instance-type mapping.

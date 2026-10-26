# Managed Kafka Service

## Parameters

### Common parameters

| Name          | Description                                                                                                                                                                                                                                                                                                                                                                                         | Type     | Value   |
| ------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | ------- |
| `external`    | Enable external access from outside the cluster.                                                                                                                                                                                                                                                                                                                                                    | `bool`   | `false` |
| `tls`         | TLS configuration. Strimzi manages the cluster PKI automatically (no cert-manager is involved for this chart): the operator auto-creates `<release>-cluster-ca-cert` and `<release>-clients-ca-cert` secrets, both exposed for client trust setup. The internal TLS listener on 9093 is always on; this toggle only controls the external listener on 9094.                                         | `object` | `{}`    |
| `tls.enabled` | Enable TLS on the external listener. When unset, inherits the value of `external` (TLS is on when external access is enabled). Warning: setting this to false while external is true exposes Kafka over plaintext on a public IP via LoadBalancer. Strimzi does not provide authentication on this listener unless SCRAM, mTLS, or OAuth is separately configured. Use only in controlled networks. | `*bool`  | `null`  |
| `version`     | Kafka version to deploy. Upgrade-only: once a cluster's KRaft metadata is at a given version, Strimzi refuses an in-place downgrade, so lowering this on a running cluster (e.g. v4.3 back to v4.2) leaves the Kafka CR stuck in a reconcile error. Pick the target version at creation and only ever raise it.                                                                                     | `string` | `v4.3`  |


### Application-specific parameters

| Name                   | Description           | Type       | Value  |
| ---------------------- | --------------------- | ---------- | ------ |
| `topics`               | Topics configuration. | `[]object` | `[]`   |
| `topics[i].name`       | Topic name.           | `string`   | `""`   |
| `topics[i].partitions` | Number of partitions. | `int`      | `0`    |
| `topics[i].replicas`   | Number of replicas.   | `int`      | `0`    |
| `topics[i].config`     | Topic configuration.  | `*object`  | `null` |


### Kafka configuration

| Name                          | Description                                                                                              | Type       | Value      |
| ----------------------------- | -------------------------------------------------------------------------------------------------------- | ---------- | ---------- |
| `kafka`                       | Kafka configuration.                                                                                     | `object`   | `{}`       |
| `kafka.replicas`              | Number of Kafka replicas.                                                                                | `int`      | `3`        |
| `kafka.resources`             | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`       |
| `kafka.resources.cpu`         | CPU available to each replica.                                                                           | `quantity` | `""`       |
| `kafka.resources.memory`      | Memory (RAM) available to each replica.                                                                  | `quantity` | `""`       |
| `kafka.resourcesPreset`       | Default sizing preset used when `resources` is omitted.                                                  | `string`   | `c1.small` |
| `kafka.size`                  | Persistent Volume size for Kafka.                                                                        | `quantity` | `10Gi`     |
| `kafka.storageClass`          | StorageClass used to store the Kafka data.                                                               | `string`   | `""`       |
| `kafka.controllerStorageSize` | Persistent Volume size for KRaft controller metadata.                                                    | `quantity` | `5Gi`      |


## Parameter examples and reference

### resources and resourcesPreset

`resources` sets explicit CPU and memory configurations for each replica.
Every resource it leaves unset is taken from the preset defined in `resourcesPreset`.

```yaml
resources:
  cpu: 4000m
  memory: 4Gi
```

`resourcesPreset` sets named CPU and memory configurations for each replica.
This setting is ignored if the corresponding `resources` value is set.

Presets follow a cloud-style `<series>.<size>` naming convention. Five series cover the full CPU-to-memory ratio range (`t1` 1:0.5, `c1` 1:1, `s1` 1:2, `u1` 1:4, `m1` 1:8) and each series ships eight sizes (`nano` through `4xlarge`). The legacy flat names (`nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`) remain accepted as deprecated aliases and keep their original sizes, which do not follow one series: `nano` through `small` equal the `t1` sizes of the same name, while `medium` equals `c1.small` rather than `c1.medium`.

See [`docs/operations/resource-presets.md`](../../../docs/operations/resource-presets.md) for the full size matrix and the legacy-to-instance-type mapping.

### topics

```yaml
topics:
  - name: Results
    partitions: 1
    replicas: 3
    config:
      min.insync.replicas: 2
  - name: Orders
    config:
      cleanup.policy: compact
      segment.ms: 3600000
      max.compaction.lag.ms: 5400000
      min.insync.replicas: 2
    partitions: 1
    replicas: 3
```

## Node pools

The chart runs Kafka in KRaft mode with two `KafkaNodePool` resources, one for brokers and one for controllers. The pools are not regular chart manifests: a `pre-install,pre-upgrade` Helm hook applies them on every install and upgrade, before the main release apply. Every pool that exists today was created by that hook and is absent from every release manifest. Follow a run with `kubectl logs -n <namespace> job/<release>-kafka-migration`.

Each pool gets a short, release-hashed name (`b-<hash>` for brokers, `c-<hash>` for controllers). `KafkaNodePool` names are namespace-unique and each release hashes differently, so several Kafka instances can live in one namespace. The hash keeps the pool name a fixed length, so the derived `<cluster>-<pool>-<id>` pod hostname stays within the 63-character limit even for long release names.

A cluster migrated from ZooKeeper by an earlier chart keeps its broker pool named `kafka`: that pool adopted the cluster's original `<cluster>-kafka-N` brokers and their data. The controller pool pins its node IDs above the broker range (`strimzi.io/next-node-ids`), so the broker pool keeps `[0..replicas-1]`.

### Deletion and PVC retention

Both node pools set `deleteClaim: false`, so deleting a Kafka release intentionally leaves its data PVCs (`data-0-<cluster>-<pool>-N`) behind. The trade-off is that the PVCs keep consuming tenant quota until removed by hand, and recreating a same-named Kafka rebinds the stale volumes, whose on-disk cluster id will not match the new cluster (`InconsistentClusterIdException`, CrashLoop). To truly start over, delete the leftover PVCs before recreating: `kubectl -n <namespace> delete pvc -l strimzi.io/cluster=<release>`.

### Upgrading from ZooKeeper and from Kafka 3.x

The operator behind this chart (Strimzi 1.x) runs neither ZooKeeper nor Kafka 3.x. A ZooKeeper-based instance must reach KRaft on the Cozystack release that ships the ZooKeeper to KRaft migration (v1.7) before the platform moves past it. On the platform variants that run platform migrations, the migration that prepares Strimzi 1.x refuses to run while any ZooKeeper-based Kafka is left, and it moves every instance on `v3.8` or `v3.9` to `v4.3`; Strimzi then rolls the brokers onto Kafka 4.3. Elsewhere those steps are manual. The simplest way is to run `/migrations/60` from this release's `platform-migrations` image with cluster-admin credentials. Outside that image the script, `packages/core/platform/images/migrations/migrations/60`, needs the `lib/` directory next to it and `kubectl`, `jq`, `gzip` and `base64`, plus `STRIMZI_CRD_FILE` pointing at `strimzi-crds-0.51.0.yaml` from the [Strimzi 0.51.0 release](https://github.com/strimzi/strimzi-kafka-operator/releases/tag/0.51.0).

### No downgrade

Neither lowering the `version` field on a running cluster nor rolling the app back to an earlier chart is supported: Strimzi refuses an in-place downgrade of the KRaft metadata version, and an earlier chart renders resources for an operator API this platform no longer serves. Take a backup before upgrading.

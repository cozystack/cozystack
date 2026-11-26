# Managed Kafka Service

## Node pools

The chart runs Kafka in KRaft mode with two `KafkaNodePool` resources, one for brokers and one for controllers. The pools are not regular chart manifests: a `pre-install,pre-upgrade` Helm hook applies them on every install and upgrade, before the main release apply. Every pool that exists today was created by that hook and is absent from every release manifest. Follow a run with `kubectl logs -n <namespace> job/<release>-kafka-migration`.

Each pool gets a short, release-hashed name (`b-<hash>` for brokers, `c-<hash>` for controllers). `KafkaNodePool` names are namespace-unique and each release hashes differently, so several Kafka instances can live in one namespace. The hash keeps the pool name at 10 characters, so the derived `<cluster>-<pool>-<id>` pod hostname grows only with the release name and the node ID. At the 44-character `name` limit it fits the 63-character limit only while every node ID is a single digit; controllers take node IDs from `kafka.replicas` upward.

A cluster migrated from ZooKeeper by an earlier chart keeps its broker pool named `kafka`: that pool adopted the cluster's original `<cluster>-kafka-N` brokers and their data. The controller pool pins its node IDs above the broker range (`strimzi.io/next-node-ids`), so the broker pool keeps `[0..replicas-1]`.

### Deletion and PVC retention

Both node pools set `deleteClaim: false`, so deleting a Kafka release intentionally leaves its data PVCs (`data-0-<cluster>-<pool>-N`) behind. The trade-off is that the PVCs keep consuming tenant quota until removed by hand, and recreating a same-named Kafka rebinds the stale volumes, whose on-disk cluster id will not match the new cluster (`InconsistentClusterIdException`, CrashLoop). To truly start over, delete the leftover PVCs before recreating: `kubectl -n <namespace> delete pvc -l strimzi.io/cluster=<release>`.

### Upgrading from ZooKeeper and from Kafka 3.x

The operator behind this chart (Strimzi 1.x) runs neither ZooKeeper nor Kafka 3.x. A ZooKeeper-based instance must reach KRaft on the Cozystack release that ships the ZooKeeper to KRaft migration (v1.7) before the platform moves past it. On every platform variant, the platform upgrade refuses to switch package sources while Strimzi still serves `v1beta2` and any Kafka lacks confirmed KRaft state. On the variants that run platform migrations, migration 61 checks the same evidence again, including on a repeat run after API conversion. Every Kafka must report a nonempty `kafkaMetadataVersion` and either `kafkaMetadataState: KRaft` or, without a state, `Ready=True` or the `cozystack.io/kraft-verified` annotation that migration 61 records on every Kafka it accepts before it stops the Strimzi operators. A `kafkaMetadataState` other than `KRaft`, a ZooKeeper section or a missing status blocks the upgrade, and the error names the reason for each Kafka. The API conversion deletes `kafkaMetadataState` and may change the spec, and neither invalidates that evidence on a retry. The migration also moves every instance on `v3.8` or `v3.9` to `v4.3`; Strimzi then rolls the brokers onto Kafka 4.3. Elsewhere those steps are manual, and the platform upgrade refuses to start while Strimzi still stores `v1beta2`: before upgrading the platform, run `/migrations/61` from this release's `platform-migrations` image with cluster-admin credentials. That run is needed on every such cluster that has the kafka-operator package installed, through the `paas` bundle or `bundles.enabledPackages`, even if it runs no Kafka: the Strimzi CRDs list `v1beta2` in their stored versions whether or not any object exists. The run stamps platform version 62 in the `cozystack-version` ConfigMap of `cozy-system`, and the migration runner starts from that stamp, so if the cluster later moves to an `isp-*` variant, the platform migrations numbered below 62 do not run there. Before it changes anything, the run checks the Kafkas, their topics and every Strimzi object, and refuses with the name and reason of each one it cannot move; it can be repeated after a failure. It then stops the Strimzi operators and converts with a Job from `quay.io/strimzi/operator:0.51.0`, pinned by digest, so a cluster that cannot reach quay.io needs that image mirrored under the same name and digest before the run. Until the platform upgrade the operators stay stopped, so a broker pod that is evicted or deleted is not recreated, and the HelmRelease of every application moved to `v4.3` reports a values error, because the old chart accepts only `v3.8` and `v3.9`: upgrade the platform right after the run. Outside that image the script, `packages/core/platform/images/migrations/migrations/61`, needs the `lib/` directory next to it and `kubectl`, `jq`, `gzip` and `base64`, plus `STRIMZI_CRD_FILE` pointing at `strimzi-crds-0.51.0.yaml` from the [Strimzi 0.51.0 release](https://github.com/strimzi/strimzi-kafka-operator/releases/tag/0.51.0).

Kafka 4 no longer accepts clients older than Kafka 2.1: upgrade such clients before the platform upgrade. Kafka 4 also removed the `message.format.version`, `message.downconversion.enable` and `message.timestamp.difference.max.ms` topic settings, and it requires `segment.bytes` of at least 1048576 (1 MiB) where Kafka 3.x accepted much smaller values; a broker whose log holds a topic below that does not start. The platform upgrade refuses to start while a Kafka application sets one of them, migration 61 also refuses while a KafkaTopic does, and the chart refuses to render such a topic, so remove the removed settings and raise `segment.bytes` in the `config` of every topic before the upgrade. A topic created through the Kafka admin API without a KafkaTopic is not visible to these checks.

### No downgrade

Neither lowering the `version` field on a running cluster nor rolling the app back to an earlier chart is supported: Strimzi refuses an in-place downgrade of the KRaft metadata version, and an earlier chart renders resources for an operator API this platform no longer serves. Take a backup before upgrading.

## Parameters

### Common parameters

| Name          | Description                                                                                                                                                                                                                                                                                                                                                                                                                                         | Type     | Value   |
| ------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | ------- |
| `external`    | Enable external access from outside the cluster. The external listener (port 9094) accepts only clients that authenticate with SCRAM-SHA-512 as one of the `users`.                                                                                                                                                                                                                                                                                 | `bool`   | `false` |
| `tls`         | TLS configuration. Strimzi manages the cluster PKI automatically (no cert-manager is involved for this chart): the operator auto-creates `<release>-cluster-ca-cert` and `<release>-clients-ca-cert` secrets, both exposed for client trust setup. The internal TLS listener on 9093 is always on; this toggle only controls the external listener on 9094.                                                                                         | `object` | `{}`    |
| `tls.enabled` | Enable TLS on the external listener. When unset, inherits the value of `external` (TLS is on when external access is enabled). The external listener always requires SCRAM-SHA-512 credentials from `users`; setting this to false while external is true sends all data unencrypted over a public IP and exposes the SCRAM authentication exchange to anyone on the path, although not the password itself, so use it only in controlled networks. | `*bool`  | `null`  |
| `version`     | Kafka version to deploy. Upgrade-only: once a cluster's KRaft metadata is at a given version, Strimzi refuses an in-place downgrade, so lowering this on a running cluster (e.g. v4.3 back to v4.2) leaves the Kafka CR stuck in a reconcile error. Pick the target version at creation and only ever raise it.                                                                                                                                     | `string` | `v4.3`  |


### Application-specific parameters

| Name                   | Description                                                                                                                                                                                                                                                                                                                                                     | Type                | Value  |
| ---------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------- | ------ |
| `users`                | Users allowed on the external listener. Each key `<name>` (lowercase letters, digits and `-`) becomes the SCRAM-SHA-512 username `<release>.<name>`, where `<release>` is `kafka-<application name>`, and its generated password is written to the Secret of the same name (keys `password` and `sasl.jaas.config`). Every user can read and write every topic. | `map[string]object` | `{}`   |
| `topics`               | Topics configuration.                                                                                                                                                                                                                                                                                                                                           | `[]object`          | `[]`   |
| `topics[i].name`       | Topic name.                                                                                                                                                                                                                                                                                                                                                     | `string`            | `""`   |
| `topics[i].partitions` | Number of partitions.                                                                                                                                                                                                                                                                                                                                           | `int`               | `0`    |
| `topics[i].replicas`   | Number of replicas.                                                                                                                                                                                                                                                                                                                                             | `int`               | `0`    |
| `topics[i].config`     | Topic configuration.                                                                                                                                                                                                                                                                                                                                            | `*object`           | `null` |


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


### Scheduling parameters

| Name              | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | Type     | Value |
| ----------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | ----- |
| `schedulingClass` | Name of a SchedulingClass CR (cluster-scoped, group cozystack.io) used in place of the tenant's for this application's pods. A pod gets its class when it is created, so running pods keep theirs until they are recreated. Must be the tenant's own class or one listed in the tenant's `allowedSchedulingClasses`. Setting any other value is refused, and the pods of an application whose class is not, or no longer, in that list get the tenant's class. Empty means inherit from the tenant. | `string` | `""`  |


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

See [Resource management](https://cozystack.io/docs/v1/guides/resource-management/) for the full size matrix and the legacy-to-instance-type mapping.

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

## External access and SCRAM-SHA-512

The external listener (port 9094) accepts only clients that authenticate with SCRAM-SHA-512 as one of the `users`. For a user `app` of the application `events`, the User Operator writes the generated password to the Secret `kafka-events.app` in the application's namespace. Its `sasl.jaas.config` key holds a ready-made JAAS line, and the username is the Secret's name.

A client needs:

```properties
bootstrap.servers=<external address of kafka-events-kafka-external-bootstrap>:9094
security.protocol=SASL_SSL
sasl.mechanism=SCRAM-SHA-512
sasl.jaas.config=<the sasl.jaas.config key of the Secret kafka-events.app>
ssl.truststore.type=PEM
ssl.truststore.location=/path/to/ca.crt
```

`ca.crt` comes from the Secret `kafka-events-cluster-ca-cert`: the cluster CA signs the external listener's certificate. With `tls.enabled: false`, use `security.protocol=SASL_PLAINTEXT` and no truststore.

### Upgrading an instance that has external access

Versions before this one accepted anonymous clients on the external listener. The upgrade turns on SCRAM-SHA-512 and creates the declared `users` in the same step, so a client cannot hold credentials before it needs them. Pick one of two sequences.

**Maintenance window.** Declare the `users` and upgrade. External clients are refused from the moment the brokers restart with authentication on until they are given their Secret and the settings above.

**Credentials staged ahead.** The User Operator already runs on the previous version and creates SCRAM credentials for any `KafkaUser`, even while the listener does not ask for them. A cluster administrator creates, for each user, the `KafkaUser` the new chart will render, with the ownership metadata Helm needs to adopt it:

```yaml
apiVersion: kafka.strimzi.io/v1
kind: KafkaUser
metadata:
  name: kafka-events.app
  namespace: <namespace>
  labels:
    strimzi.io/cluster: kafka-events
    app.kubernetes.io/managed-by: Helm
  annotations:
    meta.helm.sh/release-name: kafka-events
    meta.helm.sh/release-namespace: <namespace>
spec:
  authentication:
    type: scram-sha-512
```

On a platform whose Strimzi still serves only `kafka.strimzi.io/v1beta2`, create it with that `apiVersion` instead; the platform upgrade's API conversion carries it over to `v1`.

The administrator then hands each client its Secret and the settings above, kept ready but not applied: a client that offers SASL to the old listener is refused. The same `users` are then declared and the application upgraded; Helm adopts the existing `KafkaUser`, so the password does not change, and each client switches to its new settings once the brokers have restarted.

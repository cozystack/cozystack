# Managed Kafka Service

> Both `kafka.storageClass` and `controller.storageClass` are annotated as immutable in the chart schema. See [`docs/storage-immutability.md`](../../../docs/storage-immutability.md) for the contract and which consumers enforce it.

## Parameters

### Common parameters

| Name                    | Description                                                                                                                                                                                                                                                                                                                                                                                         | Type       | Value   |
| ----------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- | ------- |
| `external`              | Enable external access from outside the cluster.                                                                                                                                                                                                                                                                                                                                                    | `bool`     | `false` |
| `externalIPs`           | Reserved addresses for the endpoints `external: true` publishes. Each entry pins one endpoint to an address held by an IPAddressClaim, so the address survives a rebuild of this application. Empty lets the load balancer assign any free address, as before.                                                                                                                                      | `[]object` | `[]`    |
| `externalIPs[i].claim`  | Name of an IPAddressClaim in this namespace whose address the endpoint should wear. Until the claim binds, the endpoint keeps whatever address the load balancer assigned.                                                                                                                                                                                                                          | `string`   | `""`    |
| `externalIPs[i].target` | Which external endpoint to pin: `bootstrap` (the default) for the bootstrap Service clients connect to first, or `broker-N` for the per-broker Service of broker N. Kafka advertises every broker's own address, so pinning the bootstrap address alone does not make the whole cluster reachable at fixed addresses — pin one entry per broker for that.                                         | `string`   | `""`    |
| `tls`                   | TLS configuration. Strimzi manages the cluster PKI automatically (no cert-manager is involved for this chart): the operator auto-creates `<release>-cluster-ca-cert` and `<release>-clients-ca-cert` secrets, both exposed for client trust setup. The internal TLS listener on 9093 is always on; this toggle only controls the external listener on 9094.                                         | `object`   | `{}`    |
| `tls.enabled`           | Enable TLS on the external listener. When unset, inherits the value of `external` (TLS is on when external access is enabled). Warning: setting this to false while external is true exposes Kafka over plaintext on a public IP via LoadBalancer. Strimzi does not provide authentication on this listener unless SCRAM, mTLS, or OAuth is separately configured. Use only in controlled networks. | `*bool`    | `null`  |


### Application-specific parameters

| Name                              | Description                                                                                                                                                                                                                                                                                                                                                                                                                           | Type                | Value |
| --------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------- | ----- |
| `topics`                          | Topics configuration.                                                                                                                                                                                                                                                                                                                                                                                                                 | `[]object`          | `[]`  |
| `topics[i].name`                  | Topic name.                                                                                                                                                                                                                                                                                                                                                                                                                           | `string`            | `""`  |
| `topics[i].partitions`            | Number of partitions.                                                                                                                                                                                                                                                                                                                                                                                                                 | `int`               | `0`   |
| `topics[i].replicas`              | Number of replicas.                                                                                                                                                                                                                                                                                                                                                                                                                   | `int`               | `0`   |
| `topics[i].config`                | Topic configuration.                                                                                                                                                                                                                                                                                                                                                                                                                  | `object`            | `{}`  |
| `users`                           | Users of the cluster, each backed by a Strimzi KafkaUser. Declaring at least one user turns on SCRAM-SHA-512 authentication on every listener and ACL authorization for the whole cluster, so clients that connected without credentials lose access. The username is `<release>-<key>`; Strimzi generates the password into the Secret of the same name (keys `password` and `sasl.jaas.config`). Keys must be lowercase DNS labels. | `map[string]object` | `{}`  |
| `users[name].acls`                | Access rules of the user. A user without rules can authenticate but is denied everything.                                                                                                                                                                                                                                                                                                                                             | `[]object`          | `[]`  |
| `users[name].acls[i].resource`    | Kind of resource the rule covers.                                                                                                                                                                                                                                                                                                                                                                                                     | `string`            | `""`  |
| `users[name].acls[i].name`        | Name of the topic, consumer group or transactional id the rule covers, or its prefix when `patternType` is `prefix`. Not used for `cluster`.                                                                                                                                                                                                                                                                                          | `string`            | `""`  |
| `users[name].acls[i].patternType` | `literal` (the default) matches `name` exactly, `prefix` matches every resource whose name starts with it.                                                                                                                                                                                                                                                                                                                            | `string`            | `""`  |
| `users[name].acls[i].operations`  | Operations the rule allows on the resource.                                                                                                                                                                                                                                                                                                                                                                                           | `[]string`          | `[]`  |


### Kafka configuration

| Name                     | Description                                                                                              | Type       | Value      |
| ------------------------ | -------------------------------------------------------------------------------------------------------- | ---------- | ---------- |
| `kafka`                  | Kafka configuration.                                                                                     | `object`   | `{}`       |
| `kafka.replicas`         | Number of Kafka replicas.                                                                                | `int`      | `3`        |
| `kafka.resources`        | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`       |
| `kafka.resources.cpu`    | CPU available to each replica.                                                                           | `quantity` | `""`       |
| `kafka.resources.memory` | Memory (RAM) available to each replica.                                                                  | `quantity` | `""`       |
| `kafka.resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                  | `string`   | `c1.small` |
| `kafka.size`             | Persistent Volume size for Kafka.                                                                        | `quantity` | `10Gi`     |
| `kafka.storageClass`     | StorageClass used to store the Kafka data.                                                               | `string`   | `""`       |


### Controller configuration

| Name                          | Description                                                                                                                                      | Type       | Value      |
| ----------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ | ---------- | ---------- |
| `controller`                  | KRaft controller configuration. The controllers hold the cluster metadata that ZooKeeper used to hold; the brokers are configured under `kafka`. | `object`   | `{}`       |
| `controller.replicas`         | Number of KRaft controllers. Use an odd number: the metadata quorum needs a majority of them.                                                    | `int`      | `3`        |
| `controller.resources`        | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied.                                         | `object`   | `{}`       |
| `controller.resources.cpu`    | CPU available to each replica.                                                                                                                   | `quantity` | `""`       |
| `controller.resources.memory` | Memory (RAM) available to each replica.                                                                                                          | `quantity` | `""`       |
| `controller.resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                                          | `string`   | `c1.small` |
| `controller.size`             | Persistent Volume size for the metadata log of each controller.                                                                                  | `quantity` | `5Gi`      |
| `controller.storageClass`     | StorageClass used to store the controller metadata.                                                                                              | `string`   | `""`       |


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

### Authentication and access rules

Listeners are open until the first user is declared. Declaring users switches the whole cluster to authenticated mode: every listener asks for SCRAM-SHA-512 credentials and the simple authorizer enforces the ACLs below, so clients that connected without credentials lose access at that point. The username is `<release>-<key>` and Strimzi writes the generated password into a Secret of the same name, under the keys `password` and `sasl.jaas.config`.

```yaml
users:
  app:
    acls:
    - resource: topic
      name: orders
      operations: [Read, Write, Describe]
    - resource: group
      name: app-
      patternType: prefix
      operations: [Read]
```

Rules only ever allow: a user without rules can authenticate but is denied everything, and the entity operator that reconciles topics and users stays a super user. A rule covers a `topic`, a consumer `group`, a `transactionalId` or the `cluster` itself; `patternType: prefix` matches every name starting with `name`. Mutual TLS and OAuth users are not managed by this chart.

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

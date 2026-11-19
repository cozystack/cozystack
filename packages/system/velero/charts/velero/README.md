# Velero

Velero is an open source tool to safely backup and restore, perform disaster recovery, and migrate Kubernetes cluster resources and persistent volumes.

Velero has two main components: a CLI, and a server-side Kubernetes deployment.

## Installing the Velero CLI

See the different options for installing the [Velero CLI](https://velero.io/docs/v1.13/basic-install/#install-the-cli).

## Installing the Velero server

### Installation Requirements

Kubernetes v1.16+, because this helm chart uses CustomResourceDefinition `apiextensions.k8s.io/v1`. This API version was introduced in Kubernetes v1.16.

### Velero version

This helm chart installs Velero version v1.16 https://velero.io/docs/v1.16/. See the [#Upgrading](#upgrading) section for information on how to upgrade from other versions.

### Provider credentials

When installing using the Helm chart, the provider's credential information will need to be appended into your values. The easiest way to do this is with the `--set-file` argument, available in Helm 2.10 and higher. See your cloud provider's documentation for the contents and creation of the `credentials-velero` file.

### Azure resources

When using the Azure plug-in, requests and limits must be set. See https://github.com/vmware-tanzu/velero/issues/3234 and https://github.com/vmware-tanzu/helm-charts/issues/469 for details.

### Installing

The default configuration values for this chart are listed in values.yaml.

See Velero's full [official documentation](https://velero.io/docs/v1.13/basic-install/). More specifically, find your provider in the Velero list of [supported providers](https://velero.io/docs/v1.13/supported-providers/) for specific configuration information and examples.

#### Set up Helm

See the main [README.md](https://github.com/vmware-tanzu/helm-charts#kubernetes-helm-charts-for-vmware-tanzu).

#### Using Helm 3

##### Option 1) CLI commands

Note: You may add the flag `--set cleanUpCRDs=true` if you want to delete the Velero CRDs after deleting a release.
Please note that cleaning up CRDs will also delete any CRD instance, such as BackupStorageLocation and VolumeSnapshotLocation, which would have to be reconfigured when reinstalling Velero. The backup data in object storage will not be deleted, even though the backup instances in the cluster will.

Specify the necessary values using the --set key=value[,key=value] argument to helm install. For example,

```bash
helm install velero vmware-tanzu/velero \
--namespace <YOUR NAMESPACE> \
--create-namespace \
--set-file credentials.secretContents.cloud=<FULL PATH TO FILE> \
--set configuration.backupStorageLocation[0].name=<BACKUP STORAGE LOCATION NAME> \
--set configuration.backupStorageLocation[0].provider=<PROVIDER NAME> \
--set configuration.backupStorageLocation[0].bucket=<BUCKET NAME> \
--set configuration.backupStorageLocation[0].config.region=<REGION> \
--set configuration.volumeSnapshotLocation[0].name=<VOLUME SNAPSHOT LOCATION NAME> \
--set configuration.volumeSnapshotLocation[0].provider=<PROVIDER NAME> \
--set configuration.volumeSnapshotLocation[0].config.region=<REGION> \
--set initContainers[0].name=velero-plugin-for-<PROVIDER NAME> \
--set initContainers[0].image=velero/velero-plugin-for-<PROVIDER NAME>:<PROVIDER PLUGIN TAG> \
--set initContainers[0].volumeMounts[0].mountPath=/target \
--set initContainers[0].volumeMounts[0].name=plugins
```

Users of zsh might need to put quotes around key/value pairs.

##### Option 2) YAML file

Add/update the necessary values by changing the values.yaml from this repository, then run:

```bash
helm install vmware-tanzu/velero --namespace <YOUR NAMESPACE> -f values.yaml --generate-name
```
##### Upgrade the configuration

If a value needs to be added or changed, you may do so with the `upgrade` command. An example:

```bash
helm upgrade <RELEASE NAME> vmware-tanzu/velero --namespace <YOUR NAMESPACE> --reuse-values --set configuration.backupStorageLocation[0].provider=<NEW PROVIDER>
```

#### Using Helm 2

We're no longer supporting Helm v2 since it was deprecated in November 2020.

##### Upgrade the configuration

If a value needs to be added or changed, you may do so with the `upgrade` command. An example:

```bash
helm upgrade vmware-tanzu/velero <RELEASE NAME> --reuse-values --set configuration.backupStorageLocation[0].provider=<NEW PROVIDER>
```
## Upgrading Chart

### Upgrading to 7.0.0

Delete the CSI plugin. Because the Velero CSI plugin is already merged into the Velero, need to remove the existing CSI plugin InitContainer. Otherwise, the Velero server plugin would fail to start due to same plugin registered twice.
CSI plugin has been merged into velero repo in v1.14 release. It will be installed by default as an internal plugin.

### Upgrading to 6.0.0

This version removes the `nodeAgent.privileged` field, you should use `nodeAgent.containerSecurityContext.privileged` instead

## Repository Maintenance Configuration

Starting from Velero v1.15, you can configure repository maintenance jobs with different resource limits and node affinity settings per repository using a ConfigMap. This feature is supported through the Helm chart.

### Basic Usage

To enable per-repository maintenance configuration, provide repository-specific configurations and provide global configurations that will be applied across all repositories:

```yaml
configuration:
  repositoryMaintenanceJob:
    repositoryConfigData:
      name: "my-repo-maintenance-config"  # Optional, defaults to "velero-repo-maintenance"
      global:
        podResources:
          cpuRequest: "100m"
          cpuLimit: "200m"
          memoryRequest: "100Mi"
          memoryLimit: "200Mi"
        keepLatestMaintenanceJobs: 1
```

### Per-Repository Configuration

You can configure specific settings for individual repositories using repository keys. Repository keys are formed as: `{namespace}-{storageLocation}-{repositoryType}`.

For example, if you have a BackupRepository for namespace `prod` using storage location `s3-backup` with repository type `kopia`, the key would be `prod-s3-backup-kopia`.

```yaml
configuration:
  repositoryMaintenanceJob:
    repositoryConfigData:
      global:
        podResources:
          cpuRequest: "100m"
          cpuLimit: "200m"
          memoryRequest: "100Mi"
          memoryLimit: "200Mi"
      repositories:
        "prod-s3-backup-kopia":
          podResources:
            cpuRequest: "500m"
            cpuLimit: "1000m"
            memoryRequest: "512Mi"
            memoryLimit: "1024Mi"
          loadAffinity:
            - nodeSelector:
                matchLabels:
                  dedicated: "backup"
```

### Node Affinity and Priority Class

You can specify node affinity and priority class for maintenance jobs:

```yaml
configuration:
  repositoryMaintenanceJob:
    repositoryConfigData:
      global:
        podResources:
          cpuRequest: "100m"
          cpuLimit: "200m"
          memoryRequest: "100Mi"
          memoryLimit: "200Mi"
        loadAffinity:
          - nodeSelector:
              matchExpressions:
                - key: "cloud.google.com/machine-family"
                  operator: "In"
                  values: ["e2"]
          - nodeSelector:
              matchExpressions:
                - key: "topology.kubernetes.io/zone"
                  operator: "In"
                  values: ["us-central1-a", "us-central1-b", "us-central1-c"]
        priorityClassName: "low-priority"
```

**Note**: `priorityClassName` is only supported in the global configuration section and applies to all maintenance jobs.

### Backward Compatibility

When `repositoryConfigData.global` and `repositoryConfigData.repositories` are not provided (default), the chart continues to use the legacy global settings:

```yaml
configuration:
  repositoryMaintenanceJob:
    requests:
      cpu: 500m
      memory: 512Mi
    limits:
      cpu: 1000m
      memory: 1024Mi
    latestJobsCount: 3
```

Note: The legacy parameters (`--maintenance-job-cpu-request`, `--maintenance-job-mem-request`, `--maintenance-job-cpu-limit`, `--maintenance-job-mem-limit`) are deprecated in Velero v1.15 and will be removed in v1.17.

For more information, see the [Velero Repository Maintenance documentation](https://velero.io/docs/main/repository-maintenance/).

## Upgrading Velero

### Upgrading to v1.16

The [instructions found here](https://velero.io/docs/v1.16/upgrade-to-1.16/) will assist you in upgrading from version v1.15.x to v1.16.

### Upgrading to v1.15

The [instructions found here](https://velero.io/docs/v1.15/upgrade-to-1.15/) will assist you in upgrading from version v1.14.x to v1.15.

### Upgrading to v1.14

The [instructions found here](https://velero.io/docs/v1.14/upgrade-to-1.14/) will assist you in upgrading from version v1.13.x to v1.14.

### Upgrading to v1.13

The [instructions found here](https://velero.io/docs/v1.13/upgrade-to-1.13/) will assist you in upgrading from version v1.12.x to v1.13.

### Upgrading to v1.12

The [instructions found here](https://velero.io/docs/v1.12/upgrade-to-1.12/) will assist you in upgrading from version v1.11.x to v1.12.

### Upgrading to v1.11

The [instructions found here](https://velero.io/docs/v1.11/upgrade-to-1.11/) will assist you in upgrading from version v1.10.x to v1.11.

### Upgrading to v1.10

The [instructions found here](https://velero.io/docs/v1.10/upgrade-to-1.10/) will assist you in upgrading from version v1.9.x to v1.10.

### Upgrading to v1.9

The [instructions found here](https://velero.io/docs/v1.9/upgrade-to-1.9/) will assist you in upgrading from version v1.8.x to v1.9.

### Upgrading to v1.8

The [instructions found here](https://velero.io/docs/v1.8/upgrade-to-1.8/) will assist you in upgrading from version v1.7.x to v1.8.

### Upgrading to v1.7

The [instructions found here](https://velero.io/docs/v1.7/upgrade-to-1.7/) will assist you in upgrading from version v1.6.x to v1.7.

### Upgrading to v1.6

The [instructions found here](https://velero.io/docs/v1.6/upgrade-to-1.6/) will assist you in upgrading from version v1.5.x to v1.6.

### Upgrading to v1.5

The [instructions found here](https://velero.io/docs/v1.5/upgrade-to-1.5/) will assist you in upgrading from version v1.4.x to v1.5.

### Upgrading to v1.4

The [instructions found here](https://velero.io/docs/v1.4/upgrade-to-1.4/) will assist you in upgrading from version v1.3.x to v1.4.

### Upgrading to v1.3.1

The [instructions found here](https://velero.io/docs/v1.3.1/upgrade-to-1.3/) will assist you in upgrading from version v1.2.0 or v1.3.0 to v1.3.1.

### Upgrading to v1.2.0

The [instructions found here](https://velero.io/docs/v1.2.0/upgrade-to-1.2/) will assist you in upgrading from version v1.0.0 or v1.1.0 to v1.2.0.

### Upgrading to v1.1.0

The [instructions found here](https://velero.io/docs/v1.1.0/upgrade-to-1.1/) will assist you in upgrading from version v1.0.0 to v1.1.0.

## Uninstall Velero

Note: when you uninstall the Velero server, all backups remain untouched.

### Using Helm 3

```bash
helm uninstall <RELEASE NAME> -n <YOUR NAMESPACE>
```
### Note
Since from velero v1.10.0, it has supported both Restic and Kopia to do file-system level backup and restore, some configuration that contains the keyword Restic is not suitable anymore, which means from chart version 3.0.0 is not backward compatible, and we've done a configure filed name validation.

# VM Import

Import virtual machines from VMware vSphere using [Forklift](https://github.com/kubev2v/forklift), the KubeVirt-native migration toolkit.

Forklift supports cold and warm migrations from VMware vSphere to KubeVirt, with automatic guest conversion via virt-v2v (virtio drivers installation, guest OS adaptation).

- Docs: <https://forklift.konveyor.io/>
- GitHub: <https://github.com/kubev2v/forklift>

## How to use

### 1. Create a VMware credentials Secret

Create a Kubernetes Secret containing your vCenter credentials:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: vmware-credentials
  namespace: <your-namespace>
type: Opaque
stringData:
  user: administrator@vsphere.local
  password: your-password
  thumbprint: AA:BB:CC:DD:...
```

The `thumbprint` is the SHA-1 fingerprint of the vCenter SSL certificate. You can retrieve it with:

```bash
openssl s_client -connect vcenter.example.com:443 < /dev/null 2>/dev/null | openssl x509 -fingerprint -noout -sha1
```

### 2. Identify VMs to migrate

You need the managed object reference ID (`vm-XXX`) of each VM to migrate. You can find these in the vSphere client URL or via the API.

### 3. Create the VM Import

Deploy a `vm-import` application with the following values:

```yaml
sourceUrl: "https://vcenter.example.com/sdk"
sourceSecretName: "vmware-credentials"

vms:
  - id: "vm-123"
    name: "my-web-server"
  - id: "vm-456"

storageMap:
  - sourceId: "datastore-1"
    storageClass: "replicated"

networkMap:
  - sourceId: "network-1"
    destinationType: "pod"
```

### 4. Monitor the migration

Track the migration progress through the Forklift resources:

```bash
kubectl get migrations,plans -n <your-namespace>
```

Once completed, the migrated VMs will be available as KubeVirt VirtualMachine resources in the target namespace.

## Parameters

### VMware Source

| Name               | Description                                                                                          | Type     | Value |
| ------------------ | ---------------------------------------------------------------------------------------------------- | -------- | ----- |
| `sourceUrl`        | URL of the VMware vCenter server (e.g. `https://vcenter.example.com/sdk`).                           | `string` | `""`  |
| `sourceSecretName` | Name of the Kubernetes Secret containing VMware credentials (`user`, `password`, `thumbprint` keys). | `string` | `""`  |


### Migration Plan

| Name          | Description                                                           | Type       | Value   |
| ------------- | --------------------------------------------------------------------- | ---------- | ------- |
| `vms`         | List of virtual machines to migrate.                                  | `[]object` | `[]`    |
| `vms[i].id`   | The managed object reference ID of the VM in vSphere (e.g. `vm-123`). | `string`   | `""`    |
| `vms[i].name` | Optional target name for the VM in KubeVirt.                          | `string`   | `""`    |
| `warm`        | Enable warm migration (incremental replication before cutover).       | `bool`     | `false` |


### Network Mapping

| Name                                 | Description                                                                | Type       | Value |
| ------------------------------------ | -------------------------------------------------------------------------- | ---------- | ----- |
| `networkMap`                         | Mapping of source networks to destination networks.                        | `[]object` | `[]`  |
| `networkMap[i].sourceId`             | The managed object reference ID of the source network in vSphere.          | `string`   | `""`  |
| `networkMap[i].destinationType`      | Destination type: `pod` for pod network, or `multus` for a Multus network. | `string`   | `""`  |
| `networkMap[i].destinationName`      | Name of the destination network (required if type is `multus`).            | `string`   | `""`  |
| `networkMap[i].destinationNamespace` | Namespace of the destination network.                                      | `string`   | `""`  |


### Storage Mapping

| Name                         | Description                                                         | Type       | Value |
| ---------------------------- | ------------------------------------------------------------------- | ---------- | ----- |
| `storageMap`                 | Mapping of source datastores to destination StorageClasses.         | `[]object` | `[]`  |
| `storageMap[i].sourceId`     | The managed object reference ID of the source datastore in vSphere. | `string`   | `""`  |
| `storageMap[i].storageClass` | Name of the destination Kubernetes StorageClass.                    | `string`   | `""`  |


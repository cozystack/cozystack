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

### 5. Verify imported VMs

List the imported VMs:

```bash
kubectl get virtualmachines -n <your-namespace>
kubectl get vm -n <your-namespace> -l forklift.konveyor.io/plan=<your-import-name>
```

Check if VMs are labeled for Cozystack adoption:

```bash
kubectl get vm -n <your-namespace> -l cozystack.io/adopted=true
```

## VM Lifecycle and Adoption

### What happens during import?

1. **Forklift Migration**: VMs are migrated from VMware and created as native KubeVirt `VirtualMachine` resources
2. **Automatic Adoption**: If `enableAdoption: true` (default), the VM Adoption Controller automatically:
   - Detects the imported VMs (via `forklift.konveyor.io/plan` label)
   - Extracts VM configuration (instance type, disks, network, resources)
   - Creates a `VMInstance` Cozystack CRD
   - Labels the original VM as `cozystack.io/adopted: "true"`
3. **Dashboard Integration**: The `VMInstance` is managed by Cozystack's controller, which creates a HelmRelease
4. **Dashboard Visibility**: Adopted VMs appear in the Cozystack dashboard as regular VM Instances

### What happens after import?

Imported VMs are:
- ✅ **Fully functional** KubeVirt VirtualMachines
- ✅ **Automatically adopted** as Cozystack `VMInstance` resources (within ~30 seconds)
- ✅ **Manageable** via the Cozystack dashboard with full functionality
- ✅ **Manageable** via `kubectl` and Cozystack APIs
- ✅ **Persistent** - they remain even if you delete the `vm-import` application

### What happens when you delete the vm-import application?

```bash
kubectl delete vmimport <your-import-name> -n <your-namespace>
```

**Resources deleted:**
- ❌ Migration Plan
- ❌ Migration object
- ❌ Network and Storage Maps

**Resources preserved:**
- ✅ **Providers** (source and destination) - kept for reuse in future imports
- ✅ **All imported VMs** - never deleted
- ✅ **All DataVolumes and disks** - never deleted
- ✅ **VM data and state** - fully preserved

**Important**: The imported VMs are **NOT managed by Helm** and are therefore **never deleted** when you remove the `vm-import` application. They remain as independent KubeVirt resources.

### Managing imported VMs

#### Automatic Management (Default with `enableAdoption: true`)

Imported VMs are **automatically adopted** and appear in the Cozystack dashboard with full management capabilities:

- Start/stop/restart VMs
- Access console
- Modify resources (CPU, memory)
- Attach/detach disks
- Configure networking
- Cloud-init customization

The adoption happens automatically within ~30 seconds of the import completing.

#### Manual KubeVirt Management (if `enableAdoption: false`)
If you disable automatic adoption, you can still use `kubectl` and KubeVirt APIs directly:

```bash
# Start/stop VM
kubectl patch vm <vm-name> -n <namespace> --type merge -p '{"spec":{"running":true}}'
kubectl patch vm <vm-name> -n <namespace> --type merge -p '{"spec":{"running":false}}'

# View VM console
virtctl console <vm-name> -n <namespace>

# SSH into VM
virtctl ssh <vm-name> -n <namespace>
```

**Note**: Manual adoption is still possible using the provided script, but it's rarely needed with automatic adoption enabled. See the [VM Adoption Guide](./docs/adoption.md) for details.

### Cleanup

To fully remove imported VMs from your cluster:

```bash
# 1. Delete the vm-import application (if not already done)
kubectl delete vmimport <import-name> -n <namespace>

# 2. Manually delete the VMs
kubectl delete vm -n <namespace> -l forklift.konveyor.io/plan=<import-name>

# 3. (Optional) Delete associated DataVolumes
kubectl delete dv -n <namespace> -l forklift.konveyor.io/plan=<import-name>
```

## Parameters

### VMware Source

| Name               | Description                                                                                          | Type     | Value |
| ------------------ | ---------------------------------------------------------------------------------------------------- | -------- | ----- |
| `sourceUrl`        | URL of the VMware vCenter server (e.g. `https://vcenter.example.com/sdk`).                           | `string` | `""`  |
| `sourceSecretName` | Name of the Kubernetes Secret containing VMware credentials (`user`, `password`, `thumbprint` keys). | `string` | `""`  |


### Migration Plan

| Name             | Description                                                                       | Type       | Value   |
| ---------------- | --------------------------------------------------------------------------------- | ---------- | ------- |
| `vms`            | List of virtual machines to migrate.                                              | `[]object` | `[]`    |
| `vms[i].id`      | The managed object reference ID of the VM in vSphere (e.g. `vm-123`).             | `string`   | `""`    |
| `vms[i].name`    | Optional target name for the VM in KubeVirt.                                      | `string`   | `""`    |
| `warm`           | Enable warm migration (incremental replication before cutover).                   | `bool`     | `false` |
| `enableAdoption` | Automatically label imported VMs for Cozystack adoption and dashboard visibility. | `bool`     | `true`  |


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


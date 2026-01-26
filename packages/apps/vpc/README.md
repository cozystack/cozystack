# VPC

VPC offers a subset of dedicated subnets with networking services related to it.
As the service evolves, it will provide more ways to isolate your workloads.

## Service details

To function, the service requires kube-ovn and multus CNI to be present, so by default it will only work on `paas-full` bundle.
Kube-ovn provides VPC and Subnet resources and performs isolation and networking maintenance such as DHCP. Under the hood it uses ovn virtual routers and virtual switches.
Multus enables a multi-nic capability, so a pod or a VM could have two or more network interfaces.

Currently every workload will have a connection to a default management network which will also have a default gateway, and the majority of traffic will go through it.
VPC subnets are for now an additional dedicated networking spaces.

## Deployment notes

VPC name must be unique within a tenant.
Subnet name and ip address range must be unique within a VPC.
Subnet ip address space must not overlap with the default management network ip address range, subsets of 172.16.0.0/12 are recommended.
Currently there are no fail-safe checks, however they are planned for the future.

Different VPCs may have subnets with overlapping ip address ranges.

A VM or a pod may be connected to multiple secondary Subnets at once. Each secondary connection will be represented as an additional network interface.

## Parameters

### Common parameters

| Name                 | Description                      | Type                | Value   |
| -------------------- | -------------------------------- | ------------------- | ------- |
| `subnets`            | Subnets of a VPC                 | `map[string]object` | `{...}` |
| `subnets[name].cidr` | Subnet CIDR, e.g. 192.168.0.0/24 | `cidr`              | `{}`    |


## Examples
```yaml
apiVersion: apps.cozystack.io/v1alpha1
kind: VirtualPrivateCloud
metadata:
  name: vpc00
spec:
  subnets:
    sub00:
      cidr: 172.16.0.0/24
    sub01:
      cidr: 172.16.1.0/24
    sub02:
      cidr: 172.16.2.0/24
```

# hcloud-cloud-controller-manager Helm Chart

This Helm chart is the recommended installation method for [hcloud-cloud-controller-manager](https://github.com/hetznercloud/hcloud-cloud-controller-manager).

## Quickstart

First, [install Helm 3](https://helm.sh/docs/intro/install/).

The following snippet will deploy hcloud-cloud-controller-manager to the kube-system namespace.

```sh
# Sync the Hetzner Cloud helm chart repository to your local computer.
helm repo add hcloud https://charts.hetzner.cloud
helm repo update hcloud

# Install the latest version of the hcloud-cloud-controller-manager chart.
helm install hccm hcloud/hcloud-cloud-controller-manager -n kube-system

# If you want to install hccm with private networking support (see main Deployment guide for more info).
helm install hccm hcloud/hcloud-cloud-controller-manager -n kube-system --set networking.enabled=true
```

Please note that additional configuration is necessary. See the main [Deployment](https://github.com/hetznercloud/hcloud-cloud-controller-manager#deployment) guide.

If you're unfamiliar with Helm it would behoove you to peep around the documentation. Perhaps start with the [Quickstart Guide](https://helm.sh/docs/intro/quickstart/)?

### Upgrading from static manifests

If you previously installed hcloud-cloud-controller-manager with this command:

```sh
kubectl apply -f https://github.com/hetznercloud/hcloud-cloud-controller-manager/releases/latest/download/ccm.yaml
```

You can uninstall that same deployment, by running the following command:

```sh
kubectl delete -f https://github.com/hetznercloud/hcloud-cloud-controller-manager/releases/latest/download/ccm.yaml
```

Then you can follow the Quickstart installation steps above.

## Configuration

This chart aims to be highly flexible. Please review the [values.yaml](./values.yaml) for a full list of configuration options.

If you've already deployed hccm using the `helm install` command above, you can easily change configuration values:

```sh
helm upgrade hccm hcloud/hcloud-cloud-controller-manager -n kube-system --set monitoring.podMonitor.enabled=true
```

### Multiple replicas / DaemonSet

You can choose between different deployment options. By default the chart will deploy a single replica as a Deployment.

If you want to change the replica count you can adjust the value `replicaCount` inside the helm values.
If you have more than 1 replica leader election will be turned on automatically.

If you want to deploy hccm as a DaemonSet you can set `kind` to `DaemonSet` inside the values.
To adjust on which nodes the DaemonSet should be deployed you can use the `nodeSelector` and `additionalTolerations` values.

# kamaji-crds

![Version: 0.0.0+latest](https://img.shields.io/badge/Version-0.0.0+latest-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: latest](https://img.shields.io/badge/AppVersion-latest-informational?style=flat-square)

Kamaji is the Hosted Control Plane Manager for Kubernetes.

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| Dario Tranchitella | <dario@tranchitella.eu> | <https://clastix.io> |
| Adriano Pezzuto | <me@bsctl.io> | <https://clastix.io> |

## Source Code

* <https://github.com/clastix/kamaji>

[Kamaji](https://github.com/clastix/kamaji) Custom Resource Definitions packaged as Helm Charts.

## How to use this chart

Add `clastix` Helm repository:

    helm repo add clastix https://clastix.github.io/charts

Install the Chart with the release name `kamaji-crds`:

    helm upgrade --install --namespace kamaji-system --create-namespace kamaji-crds clastix/kamaji-crds

Show the status:

    helm status kamaji-crds -n kamaji-system

Upgrade the Chart

    helm upgrade kamaji-crds -n kamaji-system clastix/kamaji-crds

Uninstall the Chart

    helm uninstall kamaji-crds -n kamaji-system

## Customize the installation

There are two methods for specifying overrides of values during Chart installation: `--values` and `--set`.

The `--values` option is the preferred method because it allows you to keep your overrides in a YAML file, rather than specifying them all on the command line. Create a copy of the YAML file `values.yaml` and add your overrides to it.

Specify your overrides file when you install the Chart:

    helm upgrade kamaji-crds --install --namespace kamaji-system --create-namespace clastix/kamaji-crds --values myvalues.yaml

The values in your overrides file `myvalues.yaml` will override their counterparts in the Chart's values.yaml file. Any values in `values.yaml` that weren’t overridden will keep their defaults.

If you only need to make minor customizations, you can specify them on the command line by using the `--set` option. For example:

    helm upgrade kamaji-crds --install --namespace kamaji-system --create-namespace clastix/kamaji-crds --set kamajiCertificateName=kamaji

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| fullnameOverride | string | `""` | Overrides the full name of the resources created by the chart. |
| kamajiCertificateName | string | `"kamaji-serving-cert"` | The cert-manager Certificate resource name, holding the Certificate Authority for webhooks. |
| kamajiNamespace | string | `"kamaji-system"` | The namespace where Kamaji has been installed: required to inject the Certificate Authority for cert-manager. |
| kamajiService | string | `"kamaji-webhook-service"` | The Kamaji webhook Service name. |
| nameOverride | string | `""` | Overrides the name of the chart for resource naming purposes. |

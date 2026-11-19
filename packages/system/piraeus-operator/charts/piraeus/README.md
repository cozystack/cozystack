# Piraeus Operator

Deploys the [Piraeus Operator](https://github.com/piraeusdatastore/piraeus-operator) which deploys and manages a simple
and resilient storage solution for Kubernetes.

The main deployment method for Piraeus Operator switched to [`kustomize`](https://piraeus.io/docs/stable/tutorial/get-started/)
in release `v2.0.0`. This chart is intended for users who want to continue using Helm.

This chart **only** configures the Operator, but does not create the `LinstorCluster` resource creating the actual
storage system. Refer to the [how-to guide](https://piraeus.io/docs/stable/how-to/helm/).

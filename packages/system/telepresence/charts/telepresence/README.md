## Configuration

The following tables lists the configurable parameters of the Telepresence Pro chart and their default values.

| Parameter                   | Description                                                                                                               | Default                    |
|-----------------------------|---------------------------------------------------------------------------------------------------------------------------|----------------------------|
| agent.envoy.logLevel        | The logging level for the traffic-agent Envoy server                                                                      | defaults to agent.logLevel |
| agent.envoy.serverPort      | The server port for the traffic-agent Envoy server                                                                        | 18000                      |
| agent.envoy.adminPort       | The admin port for the traffic-agent Envoy server                                                                         | 19000                      |
| agent.envoy.httpIdleTimeout | The time an Envoy http connection can be idle before it is closed.                                                        | 70s                        |
| httpsProxy.rootCATLSSecret  | The TLS Secret to use when the traffic manager is behind a proxy. Should contain the root CA for the proxy                | `""`                       |
| licenseKey.create           | Create the license key `volume` and `volumeMount`. **Only required for clusters without access to the internet.**         | `false`                    |
| licenseKey.value            | The value of the license key.                                                                                             | `""`                       |
| licenseKey.secret.create    | Define whether you want the license key `Secret` to be managed by the release or not.                                     | `true`                     |
| licenseKey.secret.name      | The name of the `Secret` that Traffic Manager will look for.                                                              | `systema-license`          |
| intercept.disableGlobal     | If set to `true`, the traffic-manager will only allow intercepts that use mechanism `http`.                               | `false`                    |
| systemaHost                 | Host to be used for features requiring extensions (formerly the SYSTEMA_HOST environment variable)                        | `app.getambassador.io`     |
| systemaPort                 | Port to be used with the `systemaHost` for features requiring extensions (formerly the SYSTEMA_HOST environment variable) | `443`                      |

## License Key

Telepresence can create TCP intercepts without a license key. Creating
intercepts based on HTTP headers requires a license key from the Ambassador
Cloud.

In normal environments that have access to the public internet, the Traffic
Manager will automatically connect to the Ambassador Cloud to retrieve a license
key. If you are working in one of these environments, you can safely ignore
these settings in the chart.

If you are running in
an [air gapped cluster](https://www.getambassador.io/docs/telepresence/latest/reference/cluster-config/#air-gapped-cluster),
you will need to configure the Traffic Manager to use a license key you manually
deploy to the cluster.

These notes should help clarify your options for enabling this.

- `licenseKey.create` will **always** create the `volume` and `volumeMount` for
  mounting the `Secret` in the Traffic Managed

- `licenseKey.secret.name` will define the name of the `Secret` that is
  mounted in the Traffic Manager, regardless of it it is created by the chart

- `licenseKey.secret.create` will create a `Secret` with
  ```
  data:
    license: {{.licenseKey.value}}
  ```

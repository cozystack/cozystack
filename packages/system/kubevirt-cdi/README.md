# CDI upload endpoint

The console shows a local `virtctl image-upload` command for an existing upload DataVolume. The browser does not transfer the image. Tenant admins and super-admins can run the command with their own Kubernetes credentials; the machine running virtctl must reach the upload endpoint and trust its certificate.

`uploadProxyURL` overrides the upload URL advertised by CDI. When it is empty, this chart derives `https://cdi-uploadproxy.<root-host>` only when the platform exposes `cdi-uploadproxy` and has a root host. Without those settings, the chart supplies no URL override. The console does not guess a URL from its own browser origin.

The default Ingress and Gateway TLSRoute pass TLS through to CDI. CDI owns the uploadproxy serving certificate and its rotation. This chart does not issue a public certificate or mount a platform wildcard certificate into the proxy. An advertised HTTPS URL establishes neither client trust nor a matching certificate hostname; the derived public hostname may fail TLS verification with CDI's private certificate. See the upstream [CDI certificate guidance](https://kubevirt.io/user-guide/storage/containerized_data_importer/#addressing-certificate-issues-when-uploading). Adding trust alone does not fix a hostname mismatch.

## External TLS termination

1. Install CDI normally and verify that its uploadproxy is ready.
2. Configure an external TLS terminator with a certificate for the upload hostname that the upload client's trust store accepts. Forward the upload API, including its Authorization header, to the existing CDI HTTPS service. Configure streaming body limits and timeouts for disk images. Verify backend TLS using CDI's CA and the backend service hostname; do not disable verification to conceal a certificate mismatch.
3. Verify endpoint reachability and TLS from the machine that will run virtctl.
4. Set `spec.components.kubevirt-cdi.values.uploadProxyURL` on the `cozystack.kubevirt-cdi` Package. This merge fragment preserves its other settings:

```yaml
spec:
  components:
    kubevirt-cdi:
      values:
        uploadProxyURL: https://uploads.example.org
```

The explicit URL wins even when the platform also exposes its standard upload hostname or uses a wildcard certificate. The external terminator and its certificate remain administrator-managed. DNS or certificate issuance failures at that endpoint do not block CDI installation or replace its internal serving certificate. Uploads to an unavailable or untrusted endpoint fail normally; repair the endpoint or point the override at another prepared endpoint. Clearing the override returns to the derived/passthrough behavior and does not create a trusted certificate. The console's command keeps TLS verification enabled.

## Existing disks and upgrades

A VMDisk source is immutable. If a requested source change is rejected, the console displays the retained upload DataVolume even while the VMDisk is NotReady. Restore the source recorded in its `vm-disk.cozystack.io/source` annotation to reconcile the existing disk, or create another disk for a different source. The console never recreates a data-bearing disk automatically.

Upgrades from a release using CDI's own certificate preserve that certificate and mount. A cluster that installed an unreleased build which mounted `cdi-uploadproxy-public-cert` or `cdi-uploadproxy-wildcard-cert` returns to CDI's own mount when the corrected CDI resource reconciles. Recovery can interrupt an upload; wait for the proxy to become ready before starting another transfer. External TLS resources are not changed by this chart.

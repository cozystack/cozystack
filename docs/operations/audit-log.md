# Kubernetes Audit Log

Cozystack attributes every issuance and revocation of a managed application password to the kube-apiserver audit log. A mint is a `create` on the `mint` subresource of a `core.cozystack.io` Credential, which kube-apiserver audits before it proxies the request to the Cozystack API, recording the caller, the time, the account in `objectRef` and the subresource that tells a mint from a revoke. kube-apiserver records neither the body nor the response of a request it proxies to an aggregated API, at any audit level, so the password shown once in the mint response cannot reach the log. The Cozystack API itself runs without audit flags, and it must stay that way or get a policy that keeps `credentials` below `RequestResponse`, because it is the one server that could write the password down.

The `monitoring-agents` package tails `/var/log/audit/kube/*.log` on every node and ships it to the VictoriaLogs instance in the `tenant-root` tenant, tagged `audit.*`, where it is kept for a month by default. Only `tenant-root` can read that store. Every other tenant sees the last ten operations on each of its accounts in the Credential's `status.history`, and each mint and revoke also records a Kubernetes Event on the application, with reason `CredentialIssued` or `CredentialRevoked`, naming the account and the caller.

## Distributions that ship no audit policy

Talos Linux configures kube-apiserver with an audit policy and writes the log under `/var/log/audit/kube/` out of the box, which is where the default fluent-bit input reads it. A cluster built on a distribution that gives kube-apiserver no audit policy records nothing, so attribution is lost silently. Give it the policy below, or merge these rules into the policy it has, and point the log at the directory fluent-bit tails.

```yaml
apiVersion: audit.k8s.io/v1
kind: Policy
omitStages:
  - RequestReceived
rules:
  # Who issued or revoked which account, and when. Metadata is all there is to
  # record for a proxied request, and all there must ever be for this resource.
  - level: Metadata
    resources:
      - group: core.cozystack.io
        resources:
          - credentials
          - credentials/mint
          - credentials/revoke
  # Account Secrets and the tenantsecrets view carry stored password forms;
  # Metadata keeps their data out of the log.
  - level: Metadata
    resources:
      - group: ""
        resources:
          - secrets
      - group: core.cozystack.io
        resources:
          - tenantsecrets
  - level: Metadata
```

```
--audit-policy-file=/etc/kubernetes/audit-policy.yaml
--audit-log-path=/var/log/audit/kube/kube-apiserver.log
--audit-log-maxage=30
--audit-log-maxbackup=10
--audit-log-maxsize=100
```

The log path has to be a file under the node's `/var/log/audit/kube/` directory, because the fluent-bit DaemonSet mounts the node's `/var/log` and tails that glob. A distribution that writes the log elsewhere needs `fluent-bit.config.inputs` in the `monitoring-agents` values changed to match.

## Finding an issuance

In VictoriaLogs, the stream fields of the audit output include `verb` and `user_username`, and the record keeps the whole audit event, so a mint is found by its subresource:

```
objectRef.subresource:mint AND objectRef.resource:credentials
```

The matching record names the Credential in `objectRef.name`, the caller in `user.username`, the impersonated user where Kubernetes impersonation was used, the source address, the response code and the authorization decision. The user is a person only when the dashboard signs in through OIDC; without it, the dashboard acts as the tenant ServiceAccount.

## Shipping the log to a SIEM

The fluent-bit outputs of `monitoring-agents` are one string, so replacing `config.outputs` to add a destination drops the VictoriaLogs outputs with it. Add an output under `fluent-bit.extraOutputs` instead, which is appended after them. The audit records carry the tag `audit.*`:

```yaml
fluent-bit:
  extraOutputs: |
    [OUTPUT]
        Name forward
        Match audit.*
        Host siem.example.org
        Port 24224
        tls on
```

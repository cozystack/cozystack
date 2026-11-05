# Worker package admission integration

This opt-in test starts disposable kube-apiserver/etcd processes, a separately identified Cozystack API executable, and the production worker controller/webhook executable. It installs the kept WorkerClass CRD and the package's rendered ServiceAccount, RBAC, built-ins, Deployment, Service and webhook registrations. The package process uses its rendered ServiceAccount token, not the test administrator's credentials.

```bash
KUBEBUILDER_ASSETS=/path/to/approved/local/assets COZY_API_BINARY=/path/to/cozystack-api COZY_API_REVISION=<exact-api-source-sha> EXPECT_STORAGE_FIX=1 make test-worker-class-admission
```

Build the API variant independently and record its exact source revision. `EXPECT_STORAGE_FIX=1` expects stale resourceVersion conflicts to remain conflicts and a concurrent spec change to rerun admission. With that flag absent, the test pins the known earlier storage failures and logs them as `KNOWN-FAIL`; a successful harness result does not turn those observations into passing storage guarantees. The worker package depends on the separate generic update-concurrency correction before those guarantees can be claimed. No worker-specific core hook is used.

The final `workerClassName` ApplicationDefinition and canonical `kubernetesnodeses.apps.cozystack.io` resource are taken from this repository. Coverage includes CREATE/SSA create, omission and explicit-empty UPDATEs through PUT/JSON PATCH/merge PATCH/SSA, fixed artifact conflicts, deleting-class refusal, real finalizers and recreated UIDs, denied class/reference-store reads, TLS errors/rotation, and the production unregister command.

The control plane and child processes have bounded lifetimes and cleanup. Set `WORKERCLASS_TEST_OUTPUT` to an owned directory to retain logs; otherwise the test uses a temporary directory. Its private kubeconfigs and serving keys are removed after shutdown. It does not read an existing kubeconfig or build a container image.

Topology limits are explicit: the isolated host uses endpoint routing for APIService aggregation, and the APIService backend skips serving-certificate verification. Package admission uses a TLS recording relay solely to observe AdmissionReviews and inject transport/conflict faults; it forwards to the production handlers without implementing mutation, lookup or validation. The webhook URL is a local override of the rendered Service reference. Serving certificates are supplied by the harness in place of cert-manager issuance, so this does not prove CA injection, Service DNS, Pod networking or cert-manager reconciliation. The rendered pre-delete hook's actual executable/arguments are invoked, but there is no scheduler/Job controller running its Pod. Production image execution, HA and issuer behavior require their own environment coverage.

Set `WORKERCLASS_LEGACY_CHART` to an independently extracted older chart to exercise the hoster restoration command in preview and apply modes. That case installs minimal discovery-only CAPI/Cilium resource schemas for server-backed Helm lookups; it does not start those providers or boot workers. The standard `cozystack-values` Secret is supplied as namespace render context. The test uses a valid synthetic tenant namespace, rather than bypassing the chart's tenant-identifier checks.

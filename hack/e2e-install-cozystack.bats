#!/usr/bin/env bats

@test "Deploy cilium-leak-healer watchdog (best-effort)" {
  # Interim mitigation for the Cilium in-memory endpoint leak (cilium/cilium#38313
  # class): a deleted pod's endpoint is orphaned in the agent's registry, IPAM
  # re-hands its IP to a new pod, and the agent then rejects the new sandbox with
  # "IP <X> is already in use" until an agent restart. The watchdog runs as an
  # in-cluster Job that surgically evicts only the orphaned endpoint, covering
  # install and (the Job outlives this file) the whole app suite.
  #
  # This is a real @test, NOT a bats setup_file hook: the e2e runner
  # (hack/cozytest.sh) only executes @test functions — it never invokes
  # setup_file/teardown_file — so a hook would silently never run. It is placed
  # first so the watchdog is up before the install churn. Best-effort: the leak's
  # primary trigger (VPA Auto eviction) is removed separately by the
  # monitoring/etcd VPA->Initial fixes, so this is only the reactive net for
  # residual churn and must never fail the suite. The heal logic stays in
  # hack/e2e-cilium-endpoint-leak-healer.sh (single source of truth), shipped to
  # the pod via a ConfigMap built here. Remove this test, that script, and
  # hack/e2e-cilium-leak-healer.yaml once a fixed Cilium ships.
  kubectl create configmap cilium-leak-healer -n kube-system \
    --from-file=heal.sh=hack/e2e-cilium-endpoint-leak-healer.sh \
    --dry-run=client -o yaml | kubectl apply -f - || true
  kubectl apply -f hack/e2e-cilium-leak-healer.yaml || true
  # Confirm it landed (visible in the cozytest.sh trace); never fail on a band-aid.
  if kubectl -n kube-system get job cilium-leak-healer >/dev/null 2>&1; then
    echo "cilium-leak-healer Job created"
  else
    echo "WARNING: cilium-leak-healer Job NOT created — watchdog inactive this run"
  fi
}

@test "Required installer chart exists" {
  if [ ! -f packages/core/installer/Chart.yaml ]; then
    echo "Missing: packages/core/installer/Chart.yaml" >&2
    exit 1
  fi
}

@test "Pre-pull platform images" {
  # Cluster-member workloads (OVN raft, LINSTOR) fail if replicas start at
  # different times due to image-pull stagger across nodes. Pre-pull these
  # images to every node so all replicas start with images already cached.
  #
  # Source images directly from the rendered charts so version bumps stay in
  # sync automatically. yq walks every PodSpec-shaped object and emits the
  # images of each container — this scopes the result to images the kubelet
  # actually pulls (skips configmap fields and CRD examples that happen to
  # contain an `image:` key). Add a chart here when a new peer-sensitive
  # workload is found.
  # Stage each render AND the yq filter through tmp files instead of
  # piping. Two constraints stack here: `set -x` would expand any
  # `var=$(helm ...)` capture into the trace and balloon CI logs, and
  # `set -o pipefail` is unavailable because hack/cozytest.sh runs under
  # /bin/sh which is dash on Ubuntu CI. Redirection keeps each step as a
  # standalone command — set -e catches a failure at any stage (helm
  # render, yq filter, prepull) without needing pipefail and without
  # leaking rendered YAML into the trace.
  local kubeovn_yaml linstor_yaml certmanager_yaml images_list
  kubeovn_yaml=$(mktemp)
  linstor_yaml=$(mktemp)
  certmanager_yaml=$(mktemp)
  images_list=$(mktemp)
  helm template packages/system/kubeovn > "$kubeovn_yaml"
  helm template packages/system/linstor > "$linstor_yaml"
  helm template packages/system/cert-manager > "$certmanager_yaml"
  yq -N '
      (..|select(has("containers"))|.containers[]|.image),
      (..|select(has("initContainers"))|.initContainers[]|.image)
    ' "$kubeovn_yaml" "$linstor_yaml" "$certmanager_yaml" > "$images_list"
  hack/e2e-prepull-images.sh < "$images_list"
  rm -f "$kubeovn_yaml" "$linstor_yaml" "$certmanager_yaml" "$images_list"
}

@test "Install Cozystack" {
  # Install cozy-installer chart (operator installs CRDs on startup via --install-crds)
  helm upgrade installer packages/core/installer \
    --install \
    --namespace cozy-system \
    --create-namespace \
    --set cozystackOperator.helmReleaseInterval=30s \
    --wait \
    --timeout 2m

  # The pre-install hook (cozy-system-labeler) must have stamped the PSA and
  # cozystack identity labels onto cozy-system. Operator pods need
  # enforce=privileged for hostNetwork=true; a silent regression in the hook
  # would let helm install succeed but break operator admission downstream.
  kubectl get ns cozy-system -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}' | grep -qx privileged
  kubectl get ns cozy-system -o jsonpath='{.metadata.labels.cozystack\.io/system}' | grep -qx true

  # Verify the operator deployment is available
  kubectl wait deployment/cozystack-operator -n cozy-system --timeout=1m --for=condition=Available

  # Wait for operator to install CRDs (happens at startup before reconcile loop).
  # kubectl wait fails immediately if the CRD does not exist yet, so poll until it appears first.
  timeout 120 sh -ec 'until kubectl wait crd/packages.cozystack.io --for=condition=Established --timeout=10s 2>/dev/null; do sleep 2; done'
  timeout 120 sh -ec 'until kubectl wait crd/packagesources.cozystack.io --for=condition=Established --timeout=10s 2>/dev/null; do sleep 2; done'

  # Wait for operator to create the platform PackageSource
  timeout 120 sh -ec 'until kubectl get packagesource cozystack.cozystack-platform >/dev/null 2>&1; do sleep 2; done'

  # Create platform Package with isp-full variant
  kubectl apply -f - <<EOF
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: cozystack.cozystack-platform
spec:
  variant: isp-full
  components:
    platform:
      values:
        networking:
          podCIDR: "10.244.0.0/16"
          podGateway: "10.244.0.1"
          serviceCIDR: "10.96.0.0/16"
          joinCIDR: "100.64.0.0/16"
        publishing:
          host: "example.org"
          apiServerEndpoint: "https://192.168.123.10:6443"
        bundles:
          enabledPackages:
            - cozystack.external-dns-application
EOF

  # Launch storage + LB configuration in the background. It waits for its
  # own prerequisites (linstor-controller deploy, MetalLB CRDs) and finishes
  # while the parallel HR wait below is still running, so the cost overlaps
  # with the platform reconcile instead of compounding it.
  hack/e2e-post-install-prep.sh > /tmp/post-install-prep.log 2>&1 &
  POST_PREP_PID=$!

  # Wait until HelmReleases appear & reconcile them
  timeout 180 sh -ec 'until [ $(kubectl get hr -A --no-headers 2>/dev/null | wc -l) -gt 10 ]; do sleep 1; done'
  # TODO(e2e-replace-fixed-timeouts): genuine sleep. The threshold of 10 is a
  # heuristic for "enough HRs visible to start waiting"; the snapshot below
  # uses whatever HRs have appeared by then. There is no objective k8s API
  # signal for "all platform HRs have been emitted" without hard-coding the
  # expected list, so the 5s pad lets a few late-arrivals join the snapshot.
  sleep 5
  # Pacing only: names every HR that timed out in the trace; the authoritative
  # gate re-lists below, covering HRs created after this snapshot (#2822).
  kubectl wait hr --all -A --timeout=15m --for=condition=ready || true

  echo "Waiting for post-install-prep to complete"
  if ! wait $POST_PREP_PID; then
    cat /tmp/post-install-prep.log >&2
    echo "post-install-prep failed" >&2
    exit 1
  fi
  cat /tmp/post-install-prep.log

  # Fail the test if any HelmRelease is not Ready. Wait again on a fresh
  # listing so HelmReleases created after the snapshot above are gated too;
  # the window absorbs momentary Unknown flaps from drift reconciles.
  if ! kubectl wait hr --all -A --timeout=15m --for=condition=ready; then
    kubectl get hr -A || true
    # kubectl's STATUS column truncates long messages; dump the full Ready
    # condition per non-ready HR so the real error (e.g. a rejected CRD) is
    # visible in the test output instead of only inside the cozyreport.
    kubectl get hr -A --no-headers | awk '$4 != "True"' | while read -r ns name _; do
      echo "--- Non-ready HelmRelease: $ns/$name" >&2
      kubectl get hr -n "$ns" "$name" -o jsonpath='{range .status.conditions[*]}{.type}={.status} reason={.reason}: {.message}{"\n"}{end}' >&2 || true
    done
    echo "Some HelmReleases failed to reconcile" >&2
    exit 1
  fi
}

@test "Wait for Cluster‑API provider deployments" {
  # Wait for Cluster‑API provider deployments
  timeout 120 sh -ec 'until kubectl get deploy -n cozy-cluster-api capi-controller-manager capi-kamaji-controller-manager capi-kubeadm-bootstrap-controller-manager capi-operator-cluster-api-operator capk-controller-manager >/dev/null 2>&1; do sleep 1; done'
  kubectl wait deployment/capi-controller-manager deployment/capi-kamaji-controller-manager deployment/capi-kubeadm-bootstrap-controller-manager deployment/capi-operator-cluster-api-operator deployment/capk-controller-manager -n cozy-cluster-api --timeout=2m --for=condition=available
}

@test "Check Cozystack API service" {
  timeout 60 sh -ec 'until kubectl get apiservices/v1alpha1.apps.cozystack.io apiservices/v1alpha1.core.cozystack.io >/dev/null 2>&1; do sleep 2; done'
  kubectl wait --for=condition=Available apiservices/v1alpha1.apps.cozystack.io apiservices/v1alpha1.core.cozystack.io --timeout=2m
}

@test "Configure Tenant and wait for applications" {
  # Patch root tenant and wait for its releases

  kubectl patch tenants/root -n tenant-root --type merge -p '{"spec":{"host":"example.org","ingress":true,"monitoring":true,"etcd":true,"isolated":true, "seaweedfs": true}}'

  timeout 60 sh -ec 'until kubectl get hr -n tenant-root etcd ingress monitoring seaweedfs tenant-root >/dev/null 2>&1; do sleep 1; done'
  # tenant-root parent HR only flips Ready after every child HR is Ready,
  # so listing all four top-level children plus the parent gives precise
  # failure messages without redundant separate waits. seaweedfs now
  # installs as a serial chain seaweedfs-db (CNPG bootstrap) ->
  # seaweedfs-system (master raft quorum) -> seaweedfs wrapper, which
  # pushes the parent's Ready flip to ~5-6 min; tenant-root HR.spec.timeout
  # is 15m and this 10m wait stays inside it.
  kubectl wait hr/etcd hr/ingress hr/monitoring hr/seaweedfs hr/tenant-root \
    -n tenant-root --timeout=10m --for=condition=ready


  # Expose Cozystack services through ingress
  kubectl patch package cozystack.cozystack-platform --type merge -p '{"spec":{"components":{"platform":{"values":{"publishing":{"exposedServices":["api","dashboard","cdi-uploadproxy","vm-exportproxy","keycloak"]}}}}}}'

  # NGINX ingress controller
  timeout 60 sh -ec 'until kubectl get deploy root-ingress-controller -n tenant-root >/dev/null 2>&1; do sleep 1; done'
  kubectl wait deploy/root-ingress-controller -n tenant-root --timeout=5m --for=condition=available

  # etcd statefulset
  timeout 60 sh -ec 'until kubectl get sts/etcd -n tenant-root >/dev/null 2>&1; do sleep 2; done'
  kubectl wait sts/etcd -n tenant-root --for=jsonpath='{.status.readyReplicas}'=3 --timeout=5m

  # VictoriaMetrics components
  timeout 60 sh -ec 'until kubectl get vmalert/vmalert-shortterm -n tenant-root >/dev/null 2>&1; do sleep 2; done'
  timeout 60 sh -ec 'until kubectl get vmalertmanager/alertmanager -n tenant-root >/dev/null 2>&1; do sleep 2; done'
  kubectl wait vmalert/vmalert-shortterm vmalertmanager/alertmanager -n tenant-root --for=jsonpath='{.status.updateStatus}'=operational --timeout=15m
  timeout 60 sh -ec 'until kubectl get vlclusters/generic -n tenant-root >/dev/null 2>&1; do sleep 2; done'
  kubectl wait vlclusters/generic -n tenant-root --for=jsonpath='{.status.updateStatus}'=operational --timeout=5m
  timeout 60 sh -ec 'until kubectl get vmcluster/shortterm vmcluster/longterm -n tenant-root >/dev/null 2>&1; do sleep 2; done'
  kubectl wait vmcluster/shortterm vmcluster/longterm -n tenant-root --for=jsonpath='{.status.updateStatus}'=operational --timeout=5m

  # Grafana
  timeout 60 sh -ec 'until kubectl get clusters.postgresql.cnpg.io/grafana-db -n tenant-root >/dev/null 2>&1; do sleep 2; done'
  kubectl wait clusters.postgresql.cnpg.io/grafana-db -n tenant-root --for=condition=ready --timeout=5m
  timeout 60 sh -ec 'until kubectl get deploy/grafana-deployment -n tenant-root >/dev/null 2>&1; do sleep 2; done'
  kubectl wait deploy/grafana-deployment -n tenant-root --for=condition=available --timeout=5m

  # Verify Grafana via ingress
  ingress_ip=$(kubectl get svc root-ingress-controller -n tenant-root -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
  if ! curl -sS -k "https://${ingress_ip}" -H 'Host: grafana.example.org' --max-time 30 | grep -q Found; then
    echo "Failed to access Grafana via ingress at ${ingress_ip}" >&2
    exit 1
  fi
}

@test "Keycloak OIDC stack is healthy" {
  # keycloakInternalUrl makes oauth2-proxy skip OIDC discovery and route
  # backend calls (token/jwks/userinfo/logout) through the in-cluster
  # keycloak Service. Without it the dashboard gatekeeper crashloops on
  # DNS lookup of keycloak.example.org -- the e2e host placeholder does
  # not resolve, and under Flux v2.8 kstatus the gatekeeper Deployment
  # then flips to Failed and stalls the dashboard HelmRelease.
  kubectl patch package cozystack.cozystack-platform --type merge -p '{"spec":{"components":{"platform":{"values":{"authentication":{"oidc":{"enabled":true,"keycloakInternalUrl":"http://keycloak-http.cozy-keycloak.svc:8080/realms/cozy"}}}}}}}'

  timeout 120 sh -ec 'until kubectl get hr -n cozy-keycloak keycloak keycloak-configure keycloak-operator >/dev/null 2>&1; do sleep 1; done'
  kubectl wait hr/keycloak hr/keycloak-configure hr/keycloak-operator -n cozy-keycloak --timeout=10m --for=condition=ready
}

@test "Aggregated API rejects Tenant name with dashes" {
  # Regression guard: the tenant Helm chart's tenant.name helper splits the
  # Release.Name on "-" and fails unless the result is exactly
  # ["tenant", "<name>"]. The aggregated API must catch tenant names
  # containing dashes up-front with a tenant-specific error, instead of
  # silently accepting the Application and letting Flux fail later.

  # Defensive cleanup: if a prior regression left foo-bar in the cluster,
  # remove it before exercising the validation so we are not observing
  # stale state. Safe even in the happy path because of --ignore-not-found.
  kubectl delete tenants.apps.cozystack.io foo-bar -n tenant-root --ignore-not-found

  # Preflight: tenant-root is created by earlier tests in this suite. Fail
  # loudly if it is missing so this test does not silently trigger an
  # unrelated "namespace not found" error and misreport as a pass.
  kubectl get namespace tenant-root

  # --validate=ignore forces kubectl to skip client-side OpenAPI validation
  # and send the payload straight to the aggregated API. This guarantees the
  # server-side name check runs and the error we grep for is the tenant
  # contract error, not a kubectl schema rejection. (--validate=false is the
  # deprecated alias.)
  local output rc
  # Run the apply in its own subshell so we can capture BOTH stdout+stderr
  # AND the exit code explicitly, without `|| true` swallowing a real failure
  # mode (e.g. network error, auth failure) that should also fail the test.
  output=$(
    kubectl apply --validate=ignore -f - 2>&1 <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Tenant
metadata:
  name: foo-bar
  namespace: tenant-root
spec: {}
EOF
  ) && rc=0 || rc=$?
  echo "kubectl apply exit=$rc, output=$output"
  # kubectl MUST have failed: success would mean validation regressed.
  [ "$rc" -ne 0 ]
  # Assert the tenant-specific message is present (distinguishes from
  # generic DNS-1035 errors and from network/auth failures).
  echo "$output" | grep -q "tenant names must"
  # And assert kubectl did NOT report creation — if validation regressed
  # into a "warn" variant, the server could still accept the object.
  ! echo "$output" | grep -qi "created"

  # Post-condition cleanup: even though we expect validation to reject the
  # create, removing foo-bar unconditionally keeps the cluster clean for
  # subsequent tests in case validation regresses and the object is created.
  kubectl delete tenants.apps.cozystack.io foo-bar -n tenant-root --ignore-not-found
}

@test "Create tenant with isolated mode enabled" {
  kubectl -n tenant-root get tenants.apps.cozystack.io test ||
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Tenant
metadata:
  name: test
  namespace: tenant-root
spec:
  etcd: false
  host: ""
  ingress: false
  isolated: true
  monitoring: false
  resourceQuotas:
    cpu: "60"
    memory: "128Gi"
    storage: "200Gi"
  seaweedfs: false
EOF
  timeout 60 sh -ec 'until kubectl get hr/tenant-test -n tenant-root >/dev/null 2>&1; do sleep 2; done'
  kubectl wait hr/tenant-test -n tenant-root --timeout=1m --for=condition=ready
  timeout 60 sh -ec 'until kubectl get namespace tenant-test >/dev/null 2>&1; do sleep 2; done'
  kubectl wait namespace tenant-test --timeout=20s --for=jsonpath='{.status.phase}'=Active
  # Wait for ResourceQuota to appear and assert values
  timeout 60 sh -ec 'until [ "$(kubectl get quota -n tenant-test --no-headers 2>/dev/null | wc -l)" -ge 1 ]; do sleep 1; done'
  kubectl get quota -n tenant-test \
    -o jsonpath='{range .items[*]}{.spec.hard.requests\.memory}{" "}{.spec.hard.requests\.storage}{"\n"}{end}' \
    | grep -qx '137438953472 200Gi'

  # Assert LimitRange defaults for containers
  kubectl get limitrange -n tenant-test \
  -o jsonpath='{range .items[*].spec.limits[*]}{.default.cpu}{" "}{.default.memory}{" "}{.defaultRequest.cpu}{" "}{.defaultRequest.memory}{"\n"}{end}' \
  | grep -qx '250m 128Mi 25m 128Mi'
}

@test "Deletion-protection VAP denies delete on labeled cozystack-version ConfigMap" {
  # Locks down the contract delivered by packages/core/platform/templates/
  # deletion-protection.yaml: a DELETE on any object carrying
  # platform.cozystack.io/no-delete=true is rejected by the
  # ValidatingAdmissionPolicy with the documented message, and the bypass
  # path (remove the label, then delete) succeeds.
  #
  # This covers every regression the PR is meant to prevent in a single
  # pass: capability gate inverted, binding objectSelector mistyped,
  # validationActions flipped Deny→Warn, expression flipped false→true,
  # label-key drift between the binding and the manifests.

  # Preflight: VAP requires Kubernetes 1.30+. Skip on older clusters so
  # the suite stays green where the capability gate intentionally elides
  # the policy. Detect by attempting to fetch the policy by name; if the
  # API is present, the resource will be retrievable, otherwise kubectl
  # exits non-zero on an unknown resource type.
  if ! kubectl api-resources --api-group=admissionregistration.k8s.io \
       2>/dev/null | grep -qw validatingadmissionpolicies; then
    skip "ValidatingAdmissionPolicy API not available on this cluster"
  fi
  kubectl get validatingadmissionpolicy cozystack-no-delete-guardrail

  # The cozystack-version ConfigMap is created with the no-delete label
  # baked in by the chart (templates/cozystack-version.yaml) and is
  # backfilled by the migration on upgrades. Asserting the label is the
  # precondition for the deny check below: if the label is gone the deny
  # will not fire and the test would misreport as a pass on a regressed
  # binding.
  kubectl get configmap cozystack-version -n cozy-system \
    -o jsonpath='{.metadata.labels.platform\.cozystack\.io/no-delete}' \
    | grep -qx 'true'

  # The actual deny check. Capture both stdout+stderr and exit code so a
  # network/auth failure does not silently look like a deny success.
  local output rc
  output=$(kubectl delete configmap cozystack-version -n cozy-system 2>&1) \
    && rc=0 || rc=$?
  echo "kubectl delete exit=$rc, output=$output"
  # Delete MUST have failed: success means the VAP regressed.
  [ "$rc" -ne 0 ]
  # Assert the user-facing deny message is the one this PR ships — guards
  # against expression flipped to "true" or message reworded away from the
  # documented bypass. The CEL message is on one line in the api-server
  # response, so grep for the literal substring with the --namespace flag.
  echo "$output" | grep -q 'Deletion blocked: object carries platform.cozystack.io/no-delete=true'
  echo "$output" | grep -q -- '--namespace'

  # And confirm the ConfigMap is still there — a partial deny that races
  # tombstone creation would also be a regression.
  kubectl get configmap cozystack-version -n cozy-system >/dev/null

  # Bypass path: remove the label, delete must succeed. Re-stamp the label
  # afterward so the cluster ends the test in the same state it started.
  kubectl label configmap cozystack-version -n cozy-system \
    platform.cozystack.io/no-delete- --overwrite
  # Stash the data so we can reconstruct after delete.
  local version
  version=$(kubectl get configmap cozystack-version -n cozy-system \
    -o jsonpath='{.data.version}')
  kubectl delete configmap cozystack-version -n cozy-system
  ! kubectl get configmap cozystack-version -n cozy-system 2>/dev/null
  # Reconstruct: declarative apply matches the chart template at
  # packages/core/platform/templates/cozystack-version.yaml — same label set
  # AND the helm.sh/resource-policy: keep annotation that pins the ConfigMap
  # across helm uninstall.
  cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: cozystack-version
  namespace: cozy-system
  labels:
    platform.cozystack.io/no-delete: "true"
  annotations:
    helm.sh/resource-policy: keep
data:
  version: "${version}"
EOF
}

# System Component Memory Limits

Cozystack defaults a memory limit for containers in its own system namespaces, and sets explicit requests and limits on the node DaemonSets. That coverage is namespace-wide rather than universal, because it lands at admission: Talos-managed static pods never pass through it, and the operator withholds it from a namespace where defaulting would stop a pod being admitted. This page explains why that is not merely resource hygiene, how to tune it, and what it deliberately does not cover.

## Why a memory limit, and not a PriorityClass

Talos Linux v1.12 introduced a userspace OOM handler, enabled by default, that reacts to memory pressure before the kernel OOM killer does. Its only input from the machine is its own config document; everything else it reads straight out of cgroupfs, and it holds no Kubernetes client at all. `PriorityClass`, `system-node-critical` included, therefore has no bearing on which cgroup it picks.

Setting a priority on a system component to stop these kills looks like a fix and changes nothing about them. Take that claim narrowly, because priority is far from inert elsewhere: it still drives scheduler preemption, kubelet eviction ordering, and the `oom_score_adj` kubelet hands the kernel OOM killer. None of those three is what kills these pods. The one ordering input this handler has is the Kubernetes **QoS class**, inferred from the cgroup path rather than from any pod object, a different axis from priority, so a `system-node-critical` pod with no memory limit is an ordinary candidate like any other.

The handler scores each pod cgroup with a CEL expression. The default in Talos v1.13.6, the version Cozystack currently ships, is:

```cel
memory_max.hasValue() ? 0.0 :
  {Besteffort: 1.0, Burstable: 0.5, Guaranteed: 0.0, Podruntime: 0.0, System: 0.0}[class] *
    double(memory_current.orValue(0u))
```

Both CEL blocks on this page are the shipped defaults at that tag, `DefaultOOMCgroupRankingExpression` and `DefaultOOMTriggerExpression` in `pkg/machinery/constants/constants.go`; cross-check them against the constant at the tag rather than against a rendered configuration reference, because the `OOMConfig` doc comments point at the constant instead of inlining its value.

Any cgroup scoring zero is dropped from the candidate set entirely. A pod whose containers all carry a memory limit has `memory.max` set on its pod cgroup, scores `0.0`, and can never be selected. A pod without one stays a candidate no matter how little memory it is using.

Three consequences follow, and all three are easy to get wrong:

- Only a **limit** grants immunity. A memory **request** merely moves the pod from BestEffort to Burstable, which under the v1.13.6 default `strictCgroupClassOrdering: true` means "killed second" rather than "not killed"; Burstable cgroups are considered only once no BestEffort one is eligible, and the score then breaks ties within the class. That setting arrived in v1.13.4; on v1.13.0 through v1.13.3 the score alone decided, so a large Burstable pod could outrank a small BestEffort one.
- **Every** container in the pod needs a limit, init containers included. Kubelet only sets pod-level `memory.max` when all of them have one, so a single limit-free sidecar puts the whole pod back in the candidate set.
- CPU limits are irrelevant here. The ranking expression is given the cgroup's path, its QoS class, and `memory.max`, `memory.current` and `memory.peak`, with no CPU information of any kind.

Victim selection is also decoupled from the trigger. The cgroup that caused the pressure and the cgroup that gets `SIGKILL`ed are unrelated by design. In practice the pods carrying limits are overwhelmingly tenant workloads (managed applications are sized through resource presets, and a tenant namespace with `resourceQuotas` configured gets a default limit of its own), while system components carried none until the change this page describes. That inverts the intended order: a tenant workload thrashing against its own multi-gigabyte limit drives node-wide memory PSI, and `metallb-speaker` or `linstor-satellite`, using a few dozen megabytes and entirely uninvolved, is killed for it, repeatedly, until the pressure subsides.

## What Cozystack does

**A default LimitRange in every system namespace.** The `cozystack-operator` maintains a `LimitRange` named `cozystack-system-defaults` in each namespace it reconciles whose name does not begin with `tenant-`, defaulting container memory for anything that declares none. This is the layer that actually closes the problem, because it covers current components, components added later, and containers whose upstream chart exposes no `resources` knob.

**Explicit requests and limits on node DaemonSets.** Charts additionally set real values on the DaemonSets that run on every node: the cilium agent and its init containers, metallb speaker, the frr-k8s controller and its sidecars, the linstor satellite along with plunger and drbd-logger, virt-handler, fluent-bit, node-exporter, multus and its init container, velero node-agent, and all three kubevirt-csi-node containers. Requests there are fitted to observed usage, which is a real signal for the scheduler; the LimitRange cannot supply that, because it applies one number to every container in the namespace and so has to keep its default request deliberately tiny. kube-ovn is absent from that list because its vendored chart already sets both on its own containers.

**One JVM carries a request and no chart-level limit on purpose.** `linstor-controller` is a Java service, and on a JVM a `memory.max` is not only a ceiling on process RSS: it is the figure container-aware heap sizing reads, so a chart-level ceiling shrinks the heap rather than merely capping the process. It therefore declares requests only in `packages/system/linstor/values.yaml`, pinned with `notExists` on the limit in `packages/system/linstor/tests/cluster_test.yaml`, so re-adding a guessed ceiling fails the suite rather than passing quietly. Be precise about what that buys it, because the namespace LimitRange still defaults a limit: the pod does get `memory.max` and does leave the victim set, at the namespace-wide default rather than at a figure fitted to the workload. Replacing that with a fitted ceiling needs a measured peak plus an explicit heap share for the JVM rather than a guessed value, which is why the chart states none today.

The operator's LimitRange stops at the `tenant-` prefix, so tenant namespaces never receive it. A tenant namespace gets a default of its own only from the tenant chart's `tenant-range-limits`, which is rendered only when `resourceQuotas` is configured on the tenant and is empty by default. A tenant workload left without a memory limit therefore stays an eviction candidate, which is the upstream design working as intended and is what restores the ordering described above.

## Tuning

Two installer values, both empty by default so the operator's own defaults apply:

| Value | Operator flag | Default |
|---|---|---|
| `cozystackOperator.systemNamespaceMemoryLimit` | `--system-namespace-memory-limit` | `4Gi` |
| `cozystackOperator.systemNamespaceMemoryRequest` | `--system-namespace-memory-request` | `32Mi` |

The limit is a ceiling, not a reservation, so it is deliberately set far above real usage: the point is that `memory.max` exists, not that it binds. Raising it is close to free; lowering it is where the risk lives.

**The limit must stay above the largest memory request declared in any system namespace.** A container whose own request is above the defaulted limit is rejected at admission, so before applying the operator scans the namespace's live pods and withholds the LimitRange where it finds a container requesting more than the limit and declaring no limit of its own, logging the workload, container, request and limit. Only a statically declared request reaches that state. `LimitRanger` is an in-tree admission plugin and runs well ahead of every mutating webhook in the API server's plugin order, so a webhook raising a request later sees a container that already carries the defaulted limit, and VPA in particular preserves the request-to-limit ratio it finds and so rescales that limit rather than leaving it behind. Lowering the limit too far therefore costs protection rather than availability, with one exception the live-pod scan cannot see: a workload with no pod running at scan time is invisible to it, and the LimitRange stays in place until that pod is finally created and rejected. The check below reads workload templates as well as live pods for exactly that reason, and it needs both halves, because a template is the only place a workload with no pods yet appears while a live pod is the only place a container still running from before the LimitRange was applied appears. The operator marks exactly the namespaces it treats as system with `cozystack.io/system=true`, the same condition under which it creates the LimitRange:

```bash
(
  set -eu -o pipefail
  namespaces=$(kubectl get namespaces -l cozystack.io/system=true -o jsonpath='{.items[*].metadata.name}')
  [ -n "$namespaces" ] || { echo "no namespace carries cozystack.io/system=true" >&2; exit 1; }
  for ns in $namespaces; do
    kubectl get pods,deployments,daemonsets,statefulsets,jobs,cronjobs -n "$ns" -o json
  done | jq -r '
    .items[]
    | .metadata.namespace as $ns
    | "\(.kind)/\(.metadata.name)" as $owner
    | (.spec.template.spec // .spec.jobTemplate.spec.template.spec // .spec) as $pod
    | (($pod.containers // []) + ($pod.initContainers // []))[]
    | select(.resources.requests.memory != null and .resources.limits.memory == null)
    | "\($ns)\t\($owner)\t\(.name)\t\(.resources.requests.memory)"' | sort -u
)
```

Each line names the namespace, the object the container came from, the container and its request. A `Deployment/...` line is a workload whose pods need not exist yet; a `Pod/...` line is a container running right now with no limit of its own, which in a namespace that already has the LimitRange means a pod admitted before it was applied. Containers already declaring a memory limit are left out, because a LimitRange default never reaches them, and a pod whose template lives in a custom resource shows up only once the workload it generates does. Quantities print as declared, so compare the largest by hand rather than trusting the sort. The subshell is what makes an empty result trustworthy: an unreadable cluster or a missing label exits non-zero instead of printing nothing and reading as a clean bill of health.

**Keep the request small.** The operator always pairs the default limit with a default request, because a `LimitRange` that sets `default` without `defaultRequest` makes each container's request equal its limit and reserves the whole ceiling at schedule time. Note that leaving `systemNamespaceMemoryRequest` empty does not produce that state: an empty installer value omits the flag, which selects the operator's own `32Mi`. The knob is there to be raised or lowered, and clearing it is not a way to switch the request off.

The operator also refuses to start when the request exceeds the limit. A `LimitRange` whose `defaultRequest` is above its `default` is rejected by the API server, which would wedge namespace reconciliation for every system package, so the check happens at startup rather than at apply time.

Setting the limit to `0` disables the feature and removes the LimitRanges the operator previously created, so the knob is reversible. Leaving it empty is not the same thing: that selects the operator's `4Gi` default.

A LimitRange only mutates at admission. Existing pods keep running without limits until they restart, so the protection lands progressively as workloads roll rather than the moment the setting is applied.

## Verifying

Confirm a pod cgroup actually carries `memory.max`, which is the property that matters rather than the QoS class. Talos runs kubelet with the `cgroupfs` driver on a unified cgroup v2 hierarchy, so a pod cgroup is a directory named for the pod UID, dashes and all:

```bash
(
  set -eu
  POD_UID=$(kubectl get pod <pod> -n <ns> -o jsonpath='{.metadata.uid}')
  NODE=$(kubectl get pod <pod> -n <ns> -o jsonpath='{.spec.nodeName}')
  NODE_IP=$(kubectl get node "$NODE" -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}')
  case $(kubectl get pod <pod> -n <ns> -o jsonpath='{.status.qosClass}') in
    Guaranteed) QOS_DIR= ;;
    Burstable)  QOS_DIR=burstable/ ;;
    BestEffort) QOS_DIR=besteffort/ ;;
    *)          echo "could not read the pod's QoS class" >&2; exit 1 ;;
  esac
  talosctl -n "$NODE_IP" read "/sys/fs/cgroup/kubepods/${QOS_DIR}pod$POD_UID/memory.max"
)
```

A byte count rather than the literal `max` means the pod is out of the victim set. Note that `talosctl -n` takes a machine address rather than a Kubernetes node name, which is why the address is looked up instead of being reused from `nodeName`.

The `case` is there because the QoS segment of that path is not uniform. BestEffort and Burstable pods live under `kubepods/besteffort/` and `kubepods/burstable/`, but there is no `guaranteed` directory: kubelet creates QoS-level cgroups for those two classes only, so a Guaranteed pod sits directly at `kubepods/pod<uid>`. Reading `.status.qosClass` picks the right one of the three rather than assuming Burstable, and a class that cannot be read stops the command instead of sending it to a path that does not exist. To see the layout for yourself, list the pod directories two levels down:

```bash
talosctl -n "$NODE_IP" ls -d 2 -t d /sys/fs/cgroup/kubepods
```

`talosctl read` wants an `os:admin` talosconfig and a single node per call. Reading the same file through `kubectl debug node/$NODE --profile=sysadmin` under `/host/sys/fs/cgroup` also works, but it depends on the kubelet, on scheduling, and on a namespace that admits a privileged pod, all shakier than the Talos API in precisely the situation that makes you run this check.

Inspect what the handler has actually killed:

```bash
talosctl -n <node> get oomactions -o yaml
```

Each entry carries the score the victim was selected on, the command lines of the processes in it, and a dump of the trigger context it fired under. Ask for `-o yaml`: the default table shows only a score, and its `Time` column reads a field the spec never sets, so it renders empty. This is a ring buffer of the last 50 actions held in memory, so it does not survive a `machined` restart and is no substitute for an alert, but it is still the better of the two signals here, being structured and complete where the logs are neither.

Once every eligible pod in a namespace carries a limit, the healthy signature is the controller still triggering under pressure and finding nothing it is allowed to kill:

```bash
talosctl -n <node> logs controller-runtime | grep OOMController
```

Both halves of that signature, `OOM controller triggered` and then `no eligible cgroup to kill`, are emitted at the default log level, so neither needs a debug flag. Note that the structured fields are encoded as JSON rather than logfmt, so the second line reads `no eligible cgroup to kill {"component": "controller-runtime", "controller": "runtime.OOMController", "ranked": 0}`. Grep for `"ranked"` or for the message text; `ranked=` matches nothing and reads as though the handler were not running. The trigger firing is not itself a fault.

`talosctl dmesg` carries the same lines and is the more common reflex, but it is the weaker source: that path strips log levels and timestamps, truncates at 976 bytes, and suppresses the first four occurrences of each ranking error, precisely the lines worth having if scoring ever misbehaves.

## What this does not cover

**Kubernetes static pods.** `kube-apiserver`, `kube-controller-manager` and `kube-scheduler` are managed by Talos rather than by any chart. A `cozystack-system-defaults` LimitRange does reach `kube-system`, since `cozystack-scheduler` installs there, but it cannot touch them: a LimitRange defaults resources at API-server admission, and kubelet builds static pods straight from files on disk without ever passing through it. Sizing those is a Talos machine-config matter.

**A namespace where defaulting would break admission.** Where the operator finds a live container requesting more than the configured limit and declaring no limit of its own, it withholds the LimitRange from that namespace and retracts any it applied earlier, so nothing there is defaulted at all. The condition is logged with the workload, container, request and limit. No system chart comes close to the `4Gi` default, so this is a consequence of lowering the knob rather than something to expect out of the box.

**A handful of vendored containers have no upstream `resources` knob.** The four frr-k8s `cp-*` init containers, `cozy-proxy`, whose chart exposes no resources value at all, and two kube-ovn init containers, are covered by the namespace LimitRange and nothing else. Mind the reach of the kube-ovn pair, because it is not confined to the node DaemonSets: `install-cni` runs only on the `kube-ovn-cni` DaemonSet, but `hostpath-init` runs on six workloads that are as much control-plane as node, three DaemonSets (`ovs-ovn`, `kube-ovn-pinger`, `kube-ovn-cni`) and three Deployments (`ovn-central`, `kube-ovn-controller`, `kube-ovn-monitor`), with a seventh, `ovn-ic-controller`, when `func.ENABLE_IC` is set. That is enough for OOM immunity, since the defaulted limit is what puts `memory.max` on the cgroup, but their request is then the generic default rather than a figure fitted to measured usage, so the scheduler gets a weaker signal for them than for the DaemonSets above. Lifting that needs changes upstream.

**Optional components** `hami` and `kilo` carry no chart-level values, on the grounds that inventing numbers for components with no usage measurements behind them is worse than the blanket default.

## When the trigger itself is the problem

Giving system components limits stops them being *victims*. It does not stop the handler *triggering*, and a node under genuine sustained pressure will keep firing. The v1.13.6 default trigger is:

```cel
(multiply_qos_vectors(d_qos_memory_full_total, {System: 8.0, Podruntime: 4.0}) > 3000.0 &&
 multiply_qos_vectors(qos_memory_full_avg10, {System: 1.0, Podruntime: 1.0}) > 5.0 &&
 time_since_trigger > duration("5s")) ||
(memory_full_avg10 > 75.0 && time_since_trigger > duration("10s"))
```

The first clause is QoS-aware, which is what keeps unrelated pressure from firing it, and it reached that shape over three changes rather than one: [siderolabs/talos#12602](https://github.com/siderolabs/talos/pull/12602) replaced the original global-PSI trigger with the per-QoS form in v1.13.0, [#12632](https://github.com/siderolabs/talos/pull/12632) added the `qos_memory_full_avg10 > 5.0` conjunct to make it less sensitive, and [#13725](https://github.com/siderolabs/talos/pull/13725) added the `5s` cooldown in v1.13.6. The second clause is a global-PSI backstop that is still cause-blind, and a single workload thrashing inside its own cgroup limit can drive root `memory_full` above 75 while the node has free RAM. The `10s` term makes that clause fire at most once per 10 seconds, which is a useful fingerprint when reading the controller log.

If a cluster trips the backstop persistently, it can be relaxed through an `OOMConfig` machine-config document, at the cost of raising the last-resort guard before the kernel OOM killer takes over. The document also accepts `cgroupRankingExpression`, `strictCgroupClassOrdering` and `sampleInterval`; anything left out keeps its default, so overriding the trigger alone is enough here:

```yaml
apiVersion: v1alpha1
kind: OOMConfig
triggerExpression: |-
  (multiply_qos_vectors(d_qos_memory_full_total, {System: 8.0, Podruntime: 4.0}) > 3000.0 &&
   multiply_qos_vectors(qos_memory_full_avg10, {System: 1.0, Podruntime: 1.0}) > 5.0 &&
   time_since_trigger > duration("5s")) ||
  (memory_full_avg60 > 90.0 && time_since_trigger > duration("60s"))
```

Prefer fixing the workload generating the pressure, once you have established there is one. Persistent triggering has more than one cause and the trigger context in `oomactions` is what distinguishes them: the first clause weighs only `System` and `Podruntime` pressure, so a node whose kubelet, container runtime or static pods are squeezed keeps triggering with no pod anywhere near its own ceiling, while the second is the cause-blind global backstop that one cgroup reclaiming against its limit can drive on its own. Read the context first, and raise a limit only where a cgroup sitting at its ceiling is what the numbers actually show.

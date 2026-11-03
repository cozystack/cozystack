# System Component Memory Limits

Cozystack defaults container memory in eligible system namespaces and sets explicit requests and limits on the node DaemonSets. This page explains why that is not merely resource hygiene, how to tune it, and what it deliberately does not cover.

## Why a memory limit, and not a PriorityClass

Talos Linux v1.12 introduced a userspace OOM handler, enabled by default, that reacts to memory pressure before the kernel OOM killer does. Its only input from the machine is its own config document; everything else it reads straight out of cgroupfs, and it holds no Kubernetes client at all. `PriorityClass` — `system-node-critical` included — therefore has no bearing on which cgroup it picks.

Setting a priority on a system component to stop these kills looks like a fix and changes nothing about them. Take that claim narrowly, because priority is far from inert elsewhere: it still drives scheduler preemption, kubelet eviction ordering, and the `oom_score_adj` kubelet hands the kernel OOM killer. None of those three is what kills these pods. The one ordering input this handler has is the Kubernetes **QoS class**, inferred from the cgroup path rather than from any pod object — a different axis from priority, so a `system-node-critical` pod with no memory limit is an ordinary candidate like any other.

The handler scores each pod cgroup with a CEL expression. The default in Talos v1.14.2, the version Cozystack currently ships for host nodes, is:

```
memory_max.hasValue() ? 0.0 :
  {Besteffort: 1.0, Burstable: 0.5, Guaranteed: 0.0, Podruntime: 0.0, System: 0.0}[class] *
    double(memory_current.orValue(0u))
```

Both default blocks on this page, this one and the trigger [below](#when-the-trigger-itself-is-the-problem), are the shipped defaults at that tag: `DefaultOOMCgroupRankingExpression` and `DefaultOOMTriggerExpression` in `pkg/machinery/constants/constants.go`. Cross-check them against the constant at the tag rather than against a rendered configuration reference. The rendered `OOMConfig` reference does build its expressions from those same constants, through the `examples:` annotation on the document type, but what it renders is one example document rather than a table of defaults: it re-serialises the expressions with different parenthesisation, and it sets `sampleInterval: 100ms` against a `DefaultOOMSampleInterval` of `500ms`, with nothing marking which of its fields are shipped defaults and which are illustration.

Any cgroup scoring zero is dropped from the candidate set entirely. A pod whose containers all carry a memory limit has `memory.max` set on its pod cgroup, scores `0.0`, and can never be selected. A pod without one stays a candidate no matter how little memory it is using.

Three consequences follow, and all three are easy to get wrong:

- Only a **limit** grants immunity. A memory **request** merely moves the pod from BestEffort to Burstable, which under the default `strictCgroupClassOrdering: true` means "killed second" rather than "not killed" — Burstable cgroups are considered only once no BestEffort one is eligible, and the score then breaks ties within the class. That setting arrived in v1.13.4; on v1.13.0 through v1.13.3 the score alone decided, so a large Burstable pod could outrank a small BestEffort one.
- **Every** container in the pod needs a limit, init containers included. Kubelet only sets pod-level `memory.max` when all of them have one, so a single limit-free sidecar puts the whole pod back in the candidate set.
- CPU limits are irrelevant here. The ranking expression is given the cgroup's path, its QoS class, and `memory.max`, `memory.current` and `memory.peak` — no CPU information of any kind.

Victim selection is also decoupled from the trigger. The cgroup that caused the pressure and the cgroup that gets `SIGKILL`ed are unrelated by design. In practice the pods carrying limits are overwhelmingly tenant workloads — managed applications are sized through resource presets, and a tenant namespace with `resourceQuotas` configured gets a default limit of its own — while system components carried none until the change this page describes. That inverts the intended order: whatever fires the trigger, `metallb-speaker` or `linstor-satellite`, using a few dozen megabytes and entirely uninvolved, is the one killed for it, repeatedly, until the pressure subsides. On Talos v1.13.x a tenant workload thrashing against its own multi-gigabyte limit is enough to fire it, through the node-wide PSI clause described [below](#when-the-trigger-itself-is-the-problem).

## What Cozystack does

**A default LimitRange in eligible system namespaces.** The `cozystack-operator` maintains a `LimitRange` named `cozystack-system-defaults` in each namespace it reconciles whose name does not begin with `tenant-`, defaulting container memory for anything that declares none. It covers containers whose upstream chart exposes no `resources` knob. Namespaces with a foreign memory policy or an unresolved safety hold are excluded.

**Explicit requests and limits on node DaemonSets.** Charts additionally set real values on the DaemonSets that run on every node — the cilium agent and its init containers, metallb speaker, the frr-k8s controller and its sidecars, the linstor satellite along with plunger and drbd-logger, virt-handler, fluent-bit, node-exporter, multus and its init container, velero node-agent, and all three kubevirt-csi-node containers. Requests there are fitted to observed usage, which is a real signal for the scheduler; the LimitRange cannot supply that, because it applies one number to every container in the namespace and so has to keep its default request deliberately tiny. kube-ovn is absent from that list because its vendored chart already sets both on its own containers; its init containers are a separate gap, covered [below](#what-this-does-not-cover).

**One JVM carries a request and no chart-level limit on purpose.** `linstor-controller` is a Java service, and on a JVM a `memory.max` is not only a ceiling on process RSS: it is the figure container-aware heap sizing reads, so a chart-level ceiling shrinks the heap rather than merely capping the process. It therefore declares requests only in `packages/system/linstor/values.yaml`, pinned with `notExists` on the limit in `packages/system/linstor/tests/cluster_test.yaml`, so re-adding a guessed ceiling fails the suite rather than passing quietly. Be precise about what that buys it, because the namespace LimitRange still defaults a limit wherever it is in place: the pod then does get `memory.max` and does leave the victim set, at the namespace-wide default rather than at a figure fitted to the workload. Replacing that with a fitted ceiling needs a measured peak plus an explicit heap share for the JVM rather than a guessed value, which is why the chart states none today.

The operator's LimitRange stops at the `tenant-` prefix, so tenant namespaces never receive it. A tenant namespace gets a default of its own only from the tenant chart's `tenant-range-limits`, which is rendered only when `resourceQuotas` is configured on the tenant and is empty by default. A tenant workload left without a memory limit therefore stays an eviction candidate, which is the upstream design working as intended and is what restores the ordering described above.

## Tuning

Two installer values, both empty by default so the operator's own defaults apply:

| Value | Operator flag | Default |
|---|---|---|
| `cozystackOperator.systemNamespaceMemoryLimit` | `--system-namespace-memory-limit` | `32Gi` |
| `cozystackOperator.systemNamespaceMemoryRequest` | `--system-namespace-memory-request` | `32Mi` |

The limit is a ceiling, not a reservation, and the default is deliberately far above any real working set. The handler discards a cgroup for carrying a `memory.max` at all, whatever its value, so what the default buys is immunity rather than a fitted ceiling. That is also why it is set so high: the scan described below sees *declared requests*, never a container's actual resident set, so a ceiling low enough to bind is a ceiling that can convert a rare pressure-driven kill into a deterministic one for any component whose real usage nobody measured. Raising it is close to free; lowering it is where the risk lives, and it is worth doing per component in that component's own chart before doing it fleet-wide here.

**The limit must stay above the largest memory request declared by a system-namespace container that carries no memory limit of its own.** A container whose own request is above the defaulted limit is rejected at admission, and the pod simply will not start. `LimitRanger` is an in-tree admission plugin and runs well ahead of every mutating webhook in the API server's plugin order, so a request a webhook raises later is checked against a container that already carries the defaulted limit. VPA keeps the ratio between request and limit under `controlledValues: RequestsAndLimits`, which is the upstream default and which no VPA in this repository overrides, so it rescales that limit along with the request. A VPA set to `RequestsOnly`, or any other webhook that raises a request and leaves the limit alone, would leave the defaulted limit behind and get the pod rejected, and nothing can list that in advance; the `RequestsOnly` case is [listed below](#what-this-does-not-cover) as outside the scan. What can be listed in advance is a request declared on the workload itself, so list those before lowering the limit, workload templates as well as live pods, because a template is the only place a workload with no pods yet appears while a live pod is the only place a container still running from before the LimitRange was applied appears. The operator marks exactly the namespaces it treats as system with `cozystack.io/system=true`, the same condition under which it creates the LimitRange:

```bash
(
  set -eu -o pipefail
  namespaces=$(kubectl get namespaces -l cozystack.io/system=true -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
  [ -n "$namespaces" ] || { echo "no namespace carries cozystack.io/system=true" >&2; exit 1; }
  printf '%s\n' "$namespaces" | while read -r ns; do
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

Setting the limit to `0` disables the feature, so the knob is reversible. Leaving it empty is not the same thing: that selects the operator's `32Gi` default.

On the next Package reconcile after the feature is disabled, the operator lists LimitRanges cluster-wide by `app.kubernetes.io/managed-by: cozystack-package-controller` and removes every object it owns, including leftovers in namespaces no active Package targets. A same-named LimitRange without that ownership label is left alone. The same ownership boundary applies while the feature is enabled: a foreign object already holding the `cozystack-system-defaults` name is neither overwritten nor adopted, even if it contains only CPU or storage policy, so that namespace receives no Cozystack memory default until the name is free.

## Withholding and acknowledging a default

**An unsafe default stays withheld until a cluster administrator acknowledges it.** Before applying, the operator scans Deployments, StatefulSets, DaemonSets, CronJobs, Jobs and Pods, including init containers, for requests above the configured limit without an explicit limit. A visible blocker withdraws the managed LimitRange and records a hold on the Namespace. Removing a Pod or template does not release the hold: the same empty snapshot can occur between deletion and recreation by another controller.

Lowering a previously applied default also creates a hold, even if no blocker is visible. LimitRanger persists its default on admitted Pods, so a stored limit cannot prove that the workload's source declares one. The operator remembers the previous limit independently of the LimitRange and does not infer permission to tighten from a Pod's `limits` field or `kubernetes.io/limit-ranger` annotation.

The cluster-scoped Namespace carries `operator.cozystack.io/system-memory-state`, a JSON object with `version`, `lastLimit`, and, when held, `hold`, `target` and `reason`. This is operator-managed safety state. It survives controller restart, removal of the LimitRange, removal of the Package target, and disabling the feature. Do not remove or edit it to clear a hold: doing so discards the evidence protecting future Pod admission. Invalid state blocks new defaults until a cluster administrator repairs it.

Inspect the hold and the required acknowledgement:

```bash
kubectl get namespace <namespace> -o json | jq -r '
  .metadata.annotations["operator.cozystack.io/system-memory-state"]
  | fromjson | {reason, target, acknowledgement: (.hold + ":" + .target)}'
```

Before acknowledging, inspect the source of every affected workload, including custom resources and static-pod manifests that this scanner cannot read. Give the affected container an explicit limit at least as large as its request, lower its request only when appropriate, or raise the configured default. A currently running Pod's defaulted limit is not that source. Also check actual memory usage: admission safety does not make a small runtime ceiling safe.

Copy the exact `acknowledgement` value reported for the current target into a separate annotation:

```bash
kubectl annotate namespace <namespace> \
  operator.cozystack.io/system-memory-ack='<hold>:<target>' --overwrite
```

This requires permission to patch the cluster-scoped Namespace; a tenant's namespaced RoleBinding does not grant it. The operator grants no additional permission for acknowledgement. A token authorizes only its hold generation and canonical target quantity. Changing the configured target rotates the hold, even when returning to an earlier value. The operator consumes a matching token once; if a concrete request-above-default blocker still exists, the token is rejected and removed, and the hold stays. Acknowledgement never overrides a foreign LimitRange.

After successful acknowledgement and a clean scan, the configured default is applied. Existing Pods keep their resources; the new default reaches later admissions. Package reconciles run after ordinary events and are additionally scheduled every five minutes, so acknowledgement and workload changes also progress on a quiet cluster, subject to controller queue and API availability. The reason and required token are visible on the Namespace and in operator logs:

```bash
kubectl -n cozy-system logs deploy/cozystack-operator | grep "withholding the default container memory limit"
```

A hold remains after disable/re-enable, and changing the configured value while held requires acknowledging the new token. The operator also removes its LimitRange when no active Package targets the namespace, considering all Packages sharing it; the Namespace safety state remains for a later reinstall. Setting the limit to zero removes managed defaults even if their safety state is invalid.

**A namespace another LimitRange already governs is left alone.** If a system namespace — `kube-system` included — already carries a LimitRange that says anything about memory, the operator writes nothing there and withdraws anything it wrote earlier. Any memory-bearing field counts, at either the `Container` or the `Pod` scope: a second `default` leaves the effective ceiling to the order `LimitRanger` happens to iterate them, a `max` below this default or a `min` above the default request rejects the pod outright, and a `maxLimitRequestRatio` rejects it too, since a 32Gi default against a 32Mi default request is a ratio of 1024:1. The `Pod` scope is included because the collision there is not decidable in advance at all — whether a per-container default breaches a pod-wide bound depends on how many containers a not-yet-created pod will have. LimitRanges that bound only cpu or storage say nothing about memory and coexist normally. Note that withdrawing hands the namespace to the administrator's policy in full: if that LimitRange defaults container memory below some container's own request, the resulting admission rejection is theirs to resolve, and it is one that would have occurred without Cozystack in the picture at all.

A LimitRange only mutates at admission. Existing pods keep running without limits until they restart, so the protection lands progressively as workloads roll rather than the moment the setting is applied.

## Verifying

Confirm a pod cgroup actually carries `memory.max` — this is the property that matters, not the QoS class. Talos runs kubelet with the `cgroupfs` driver on a unified cgroup v2 hierarchy, so a pod cgroup is a directory named for the pod UID, dashes and all:

```bash
POD_UID=$(kubectl get pod <pod> -n <ns> -o jsonpath='{.metadata.uid}')
NODE=$(kubectl get pod <pod> -n <ns> -o jsonpath='{.spec.nodeName}')
NODE_IP=$([ -z "$NODE" ] || kubectl get node "$NODE" -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}')
(
  set -eu
  [ -n "$POD_UID" ] && [ -n "$NODE_IP" ] || { echo "could not resolve the pod UID or the node address" >&2; exit 1; }
  case $(kubectl get pod <pod> -n <ns> -o jsonpath='{.status.qosClass}') in
    Guaranteed) QOS_DIR= ;;
    Burstable)  QOS_DIR=burstable/ ;;
    BestEffort) QOS_DIR=besteffort/ ;;
    *)          echo "could not read the pod's QoS class" >&2; exit 1 ;;
  esac
  talosctl -n "$NODE_IP" read "/sys/fs/cgroup/kubepods/${QOS_DIR}pod$POD_UID/memory.max"
)
```

A byte count rather than the literal `max` means the pod is out of the victim set. Note that `talosctl -n` takes a machine address rather than a Kubernetes node name, which is why the address is looked up instead of being reused from `nodeName`. The three lookups sit outside the subshell so that `NODE` and `NODE_IP` are still set for the two commands further down, while `set -eu` and the `exit` stay inside it and so cannot take an interactive shell down with them. They are guarded explicitly rather than left to `set -e`, because `kubectl get -o jsonpath` exits zero and prints nothing when the field it asks for is absent, which is the ordinary state of a pod that has not been scheduled yet.

The `case` is there because the QoS segment of that path is not uniform. BestEffort and Burstable pods live under `kubepods/besteffort/` and `kubepods/burstable/`, but there is no `guaranteed` directory: kubelet creates QoS-level cgroups for those two classes only, so a Guaranteed pod sits directly at `kubepods/pod<uid>`. Reading `.status.qosClass` picks the right one of the three rather than assuming Burstable, and a class that cannot be read stops the command instead of sending it to a path that does not exist. To see the layout for yourself, list the pod directories two levels down:

```bash
talosctl -n "$NODE_IP" ls -d 2 -t d /sys/fs/cgroup/kubepods
```

`talosctl read` wants an `os:admin` talosconfig and a single node per call. Reading the same file through `kubectl debug node/$NODE --profile=sysadmin` under `/host/sys/fs/cgroup` also works, but it depends on the kubelet, on scheduling, and on a namespace that admits a privileged pod — all shakier than the Talos API in precisely the situation that makes you run this check.

Inspect what the handler has actually killed:

```bash
talosctl -n <node> get oomactions -o yaml
```

Each entry carries the score the victim was selected on, the command lines of the processes in it, and a dump of the trigger context it fired under. Ask for `-o yaml`: the default table shows only a score, and its `Time` column reads a field the spec never sets, so it renders empty. This is a ring buffer of the last 50 actions held in memory, so it does not survive a `machined` restart and is no substitute for an alert — but it is still the better of the two signals here, being structured and complete where the logs are neither.

Once every eligible pod in a namespace carries a limit, the healthy signature is the controller still triggering under pressure and finding nothing it is allowed to kill:

```bash
talosctl -n <node> logs controller-runtime | grep OOMController
```

Both halves of that signature — `OOM controller triggered`, then `no eligible cgroup to kill` — are emitted at the default log level, so neither needs a debug flag. Note that the structured fields are encoded as JSON rather than logfmt, so the second line reads `no eligible cgroup to kill {"component": "controller-runtime", "controller": "runtime.OOMController", "ranked": 0}`. Grep for `"ranked"` or for the message text; `ranked=` matches nothing and reads as though the handler were not running. The trigger firing is not itself a fault.

`talosctl dmesg` carries the same lines and is the more common reflex, but it is the weaker source: that path strips log levels and timestamps, truncates at 976 bytes, and suppresses the first four occurrences of each ranking error — precisely the lines worth having if scoring ever misbehaves.

## What this does not cover

**Kubernetes static pods.** `kube-apiserver`, `kube-controller-manager` and `kube-scheduler` are managed by Talos rather than by any chart. A `cozystack-system-defaults` LimitRange does reach `kube-system`, since `cozystack-scheduler` installs there, but it cannot touch them: a LimitRange defaults resources at API-server admission, and kubelet builds static pods straight from files on disk without ever passing through it. Sizing those is a Talos machine-config matter. Their mirror pods pass admission. An already persisted mirror Pod with an oversized request and no limit creates a hold, and lowering the previous default requires acknowledgement. A mirror Pod rejected before persistence provides no scan evidence; its absence does not mean the static manifest is safe.

**Future Pods outside the scanned templates.** A custom controller can change its desired request without producing a Deployment, StatefulSet, DaemonSet, CronJob or Job. If its first replacement Pod is rejected at admission, there is no new Pod for this scanner to inspect. An ownerless ReplicaSet at zero replicas has the same gap. A `RequestsOnly` VPA can likewise introduce an unseen request after defaulting. The hold prevents automatic lowering and automatic recovery after an observed blocker, but does not validate arbitrary future custom-resource changes. Configure explicit resources for those workloads and inspect their admission errors.

**A handful of vendored containers with no upstream `resources` knob** — the four frr-k8s `cp-*` init containers, `cozy-proxy`, whose chart exposes no resources value at all, and two kube-ovn init containers. These are covered by the namespace LimitRange and nothing else. Mind the reach of the kube-ovn pair, because it is not confined to the node DaemonSets: `install-cni` runs only on the `kube-ovn-cni` DaemonSet, but in the default render `hostpath-init` runs on six workloads that are as much control-plane as node, three DaemonSets (`ovs-ovn`, `kube-ovn-pinger`, `kube-ovn-cni`) and three Deployments (`ovn-central`, `kube-ovn-controller`, `kube-ovn-monitor`), with a seventh, `ovn-ic-controller`, when `func.ENABLE_IC` is set. That is enough for OOM immunity, since the defaulted limit is what puts `memory.max` on the cgroup, but their request is then the generic default rather than a figure fitted to measured usage, so the scheduler gets a weaker signal for them than for the DaemonSets above. Lifting that needs changes upstream.

**Optional components** `hami` and `kilo` carry no chart-level values, on the grounds that inventing numbers for components with no usage measurements behind them is worse than the blanket default.

## When the trigger itself is the problem

Giving system components limits stops them being *victims*. It does not stop the handler *triggering*, and a node under genuine sustained pressure will keep firing. The v1.14.2 default trigger is:

```
(multiply_qos_vectors(d_qos_memory_full_total, {System: 8.0, Podruntime: 4.0}) > 3000.0 &&
 multiply_qos_vectors(qos_memory_full_avg10, {System: 1.0, Podruntime: 1.0}) > 5.0 &&
 time_since_trigger > duration("5s"))
```

It fires only on memory stalls in the `System` and `Podruntime` cgroups, so pressure confined to pods does not trip it. It reached that shape over four changes: [siderolabs/talos#12602](https://github.com/siderolabs/talos/pull/12602) replaced the original global-PSI trigger with the per-QoS form in v1.13.0, [#12632](https://github.com/siderolabs/talos/pull/12632) added the `qos_memory_full_avg10 > 5.0` conjunct to make it less sensitive, [#13725](https://github.com/siderolabs/talos/pull/13725) added the `5s` cooldown in v1.13.6, and [#13895](https://github.com/siderolabs/talos/pull/13895) dropped a second clause, `memory_full_avg10 > 75.0 && time_since_trigger > duration("10s")`, in v1.14.0. That clause was a cause-blind global-PSI backstop: a single workload thrashing inside its own cgroup limit could drive root `memory_full` above 75 while the node had free RAM. Any node still running a v1.13 release carries it, tenant Kubernetes workers included. A tenant worker's machine config is rendered by the `kubernetes-nodes` chart, which accepts no extra documents, so the workaround below is for host nodes only.

On a v1.13 host node the backstop can be removed without upgrading, through an `OOMConfig` machine-config document that sets the v1.14 trigger — at the cost of the last-resort guard before the kernel OOM killer takes over. The document also accepts `cgroupRankingExpression`, `strictCgroupClassOrdering` and `sampleInterval`; anything left out keeps its default, so overriding the trigger alone is enough here:

```yaml
apiVersion: v1alpha1
kind: OOMConfig
triggerExpression: |-
  (multiply_qos_vectors(d_qos_memory_full_total, {System: 8.0, Podruntime: 4.0}) > 3000.0 &&
   multiply_qos_vectors(qos_memory_full_avg10, {System: 1.0, Podruntime: 1.0}) > 5.0 &&
   time_since_trigger > duration("5s"))
```

Prefer fixing the workload that is generating the pressure. Persistent triggering on v1.14 means the system and runtime cgroups keep stalling for memory, and capping the pod that is growing into the node's memory addresses the cause rather than the symptom. On v1.13 it can also mean a pod sitting at its own memory ceiling and reclaiming constantly, which raising that pod's limit addresses.

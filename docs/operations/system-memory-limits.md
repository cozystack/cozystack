# System Component Memory Limits

Cozystack gives every container in a system namespace a memory limit, and sets explicit requests and limits on the node DaemonSets. This page explains why that is not merely resource hygiene, how to tune it, and what it deliberately does not cover.

## Why a memory limit, and not a PriorityClass

Talos Linux v1.12 introduced a userspace OOM handler, enabled by default, that reacts to memory pressure before the kernel OOM killer does. Its only input from the machine is its own config document; everything else it reads straight out of cgroupfs, and it holds no Kubernetes client at all. `PriorityClass` — `system-node-critical` included — therefore has no bearing on which cgroup it picks.

Setting a priority on a system component to stop these kills looks like a fix and changes nothing about them. Take that claim narrowly, because priority is far from inert elsewhere: it still drives scheduler preemption, kubelet eviction ordering, and the `oom_score_adj` kubelet hands the kernel OOM killer. None of those three is what kills these pods. The one ordering input this handler has is the Kubernetes **QoS class**, inferred from the cgroup path rather than from any pod object — a different axis from priority, so a `system-node-critical` pod with no memory limit is an ordinary candidate like any other.

The handler scores each pod cgroup with a CEL expression. The default in Talos v1.13.6, the version Cozystack currently ships, is:

```
memory_max.hasValue() ? 0.0 :
  {Besteffort: 1.0, Burstable: 0.5, Guaranteed: 0.0, Podruntime: 0.0, System: 0.0}[class] *
    double(memory_current.orValue(0u))
```

Any cgroup scoring zero is dropped from the candidate set entirely. A pod whose containers all carry a memory limit has `memory.max` set on its pod cgroup, scores `0.0`, and can never be selected. A pod without one stays a candidate no matter how little memory it is using.

Three consequences follow, and all three are easy to get wrong:

- Only a **limit** grants immunity. A memory **request** merely moves the pod from BestEffort to Burstable, which under the v1.13.6 default `strictCgroupClassOrdering: true` means "killed second" rather than "not killed" — Burstable cgroups are considered only once no BestEffort one is eligible, and the score then breaks ties within the class. That setting arrived in v1.13.4; on v1.13.0 through v1.13.3 the score alone decided, so a large Burstable pod could outrank a small BestEffort one.
- **Every** container in the pod needs a limit, init containers included. Kubelet only sets pod-level `memory.max` when all of them have one, so a single limit-free sidecar puts the whole pod back in the candidate set.
- CPU limits are irrelevant here. The ranking expression is given the cgroup's path, its QoS class, and `memory.max`, `memory.current` and `memory.peak` — no CPU information of any kind.

Victim selection is also decoupled from the trigger. The cgroup that caused the pressure and the cgroup that gets `SIGKILL`ed are unrelated by design. In practice the pods carrying limits are overwhelmingly tenant workloads — managed applications are sized through resource presets, and a tenant namespace with `resourceQuotas` configured gets a default limit of its own — while system components carried none until the change this page describes. That inverts the intended order: a tenant workload thrashing against its own multi-gigabyte limit drives node-wide memory PSI, and `metallb-speaker` or `linstor-satellite`, using a few dozen megabytes and entirely uninvolved, is killed for it, repeatedly, until the pressure subsides.

## What Cozystack does

**A default LimitRange in every system namespace.** The `cozystack-operator` maintains a `LimitRange` named `cozystack-system-defaults` in each namespace it reconciles whose name does not begin with `tenant-`, defaulting container memory for anything that declares none. This is the layer that actually closes the problem, because it covers current components, components added later, and containers whose upstream chart exposes no `resources` knob.

**Explicit requests and limits on node DaemonSets.** Charts additionally set real values on the DaemonSets that run on every node — the cilium agent and its init containers, metallb speaker, the frr-k8s controller and its sidecars, the linstor satellite along with plunger and drbd-logger, virt-handler, fluent-bit, node-exporter, multus and its init container, velero node-agent, and all three kubevirt-csi-node containers. Requests there are fitted to observed usage, which is a real signal for the scheduler; the LimitRange cannot supply that, because it applies one number to every container in the namespace and so has to keep its default request deliberately tiny. kube-ovn is absent from that list because its vendored chart already sets both.

The operator's LimitRange stops at the `tenant-` prefix, so tenant namespaces never receive it. A tenant namespace gets a default of its own only from the tenant chart's `tenant-range-limits`, which is rendered only when `resourceQuotas` is configured on the tenant and is empty by default. A tenant workload left without a memory limit therefore stays an eviction candidate, which is the upstream design working as intended and is what restores the ordering described above.

## Tuning

Two installer values, both empty by default so the operator's own defaults apply:

| Value | Operator flag | Default |
|---|---|---|
| `cozystackOperator.systemNamespaceMemoryLimit` | `--system-namespace-memory-limit` | `4Gi` |
| `cozystackOperator.systemNamespaceMemoryRequest` | `--system-namespace-memory-request` | `32Mi` |

The limit is a ceiling, not a reservation, so it is deliberately set far above real usage — the point is that `memory.max` exists, not that it binds. Raising it is close to free; lowering it is where the risk lives.

**The limit must stay above the largest memory request in any system namespace.** A defaulted limit below a container's own request is rejected at admission, and the pod simply will not start. List the requests before lowering it — the operator marks exactly the namespaces it treats as system with `cozystack.io/system=true`, the same condition under which it creates the LimitRange:

```bash
for ns in $(kubectl get ns -l cozystack.io/system=true -o jsonpath='{.items[*].metadata.name}'); do
  kubectl get pods -n "$ns" -o json | jq -r --arg ns "$ns" '
    .items[]
    | [.spec.containers[], (.spec.initContainers // [])[]][]
    | select(.resources.requests.memory != null)
    | "\($ns)\t\(.name)\t\(.resources.requests.memory)"'
done | sort -u
```

**Keep the request small.** The operator always pairs the default limit with a default request, because a `LimitRange` that sets `default` without `defaultRequest` makes each container's request equal its limit and reserves the whole ceiling at schedule time. Note that leaving `systemNamespaceMemoryRequest` empty does not produce that state: an empty installer value omits the flag, which selects the operator's own `32Mi`. The knob is there to be raised or lowered, and clearing it is not a way to switch the request off.

The operator also refuses to start when the request exceeds the limit. A `LimitRange` whose `defaultRequest` is above its `default` is rejected by the API server, which would wedge namespace reconciliation for every system package, so the check happens at startup rather than at apply time.

Setting the limit to `0` disables the feature and removes the LimitRanges the operator previously created, so the knob is reversible. Leaving it empty is not the same thing — that selects the operator's `4Gi` default.

A LimitRange only mutates at admission. Existing pods keep running without limits until they restart, so the protection lands progressively as workloads roll rather than the moment the setting is applied.

## Verifying

Confirm a pod cgroup actually carries `memory.max` — this is the property that matters, not the QoS class. Talos runs kubelet with the `cgroupfs` driver on a unified cgroup v2 hierarchy, so a pod cgroup is a directory named for the pod UID, dashes and all:

```bash
POD_UID=$(kubectl get pod <pod> -n <ns> -o jsonpath='{.metadata.uid}')
NODE=$(kubectl get pod <pod> -n <ns> -o jsonpath='{.spec.nodeName}')
NODE_IP=$(kubectl get node "$NODE" -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}')
talosctl -n "$NODE_IP" read "/sys/fs/cgroup/kubepods/burstable/pod$POD_UID/memory.max"
```

A byte count rather than the literal `max` means the pod is out of the victim set. Note that `talosctl -n` takes a machine address rather than a Kubernetes node name, which is why the address is looked up instead of being reused from `nodeName`.

Mind the QoS segment of that path, and do not extrapolate it. BestEffort and Burstable pods live under `kubepods/besteffort/` and `kubepods/burstable/`, but there is no `guaranteed` directory: kubelet creates QoS-level cgroups for those two classes only, so a Guaranteed pod sits directly at `kubepods/pod<uid>`. To avoid guessing, list the pod directories two levels down instead:

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

**Kubernetes static pods.** `kube-apiserver`, `kube-controller-manager` and `kube-scheduler` are managed by Talos rather than by any chart. A `cozystack-system-defaults` LimitRange does reach `kube-system`, since `cozystack-scheduler` installs there, but it cannot touch them: a LimitRange defaults resources at API-server admission, and kubelet builds static pods straight from files on disk without ever passing through it. Sizing those is a Talos machine-config matter.

**A handful of vendored containers with no upstream `resources` knob** — the four frr-k8s `cp-*` init containers, the kube-ovn `hostpath-init` and `install-cni` init containers, and `cozy-proxy`, whose chart exposes no resources value at all. These are covered by the namespace LimitRange and nothing else. That is enough for OOM immunity, since the defaulted limit is what puts `memory.max` on the cgroup, but their request is then the generic default rather than a figure fitted to measured usage, so the scheduler gets a weaker signal for them than for the DaemonSets above. Lifting that needs changes upstream.

**Optional components** `hami` and `kilo` carry no chart-level values, on the grounds that inventing numbers for components with no usage measurements behind them is worse than the blanket default.

## When the trigger itself is the problem

Giving system components limits stops them being *victims*. It does not stop the handler *triggering*, and a node under genuine sustained pressure will keep firing. The v1.13.6 default trigger is:

```
(multiply_qos_vectors(d_qos_memory_full_total, {System: 8.0, Podruntime: 4.0}) > 3000.0 &&
 multiply_qos_vectors(qos_memory_full_avg10, {System: 1.0, Podruntime: 1.0}) > 5.0 &&
 time_since_trigger > duration("5s")) ||
(memory_full_avg10 > 75.0 && time_since_trigger > duration("10s"))
```

The first clause is QoS-aware, which is what keeps unrelated pressure from firing it, and it reached that shape over three changes rather than one: [siderolabs/talos#12602](https://github.com/siderolabs/talos/pull/12602) replaced the original global-PSI trigger with the per-QoS form in v1.13.0, [#12632](https://github.com/siderolabs/talos/pull/12632) added the `qos_memory_full_avg10 > 5.0` conjunct to make it less sensitive, and [#13725](https://github.com/siderolabs/talos/pull/13725) added the `5s` cooldown in v1.13.6. The second clause is a global-PSI backstop that is still cause-blind, and a single workload thrashing inside its own cgroup limit can drive root `memory_full` above 75 while the node has free RAM. The `10s` term makes that clause fire at most once per 10 seconds, which is a useful fingerprint when reading the controller log.

If a cluster trips the backstop persistently, it can be relaxed through an `OOMConfig` machine-config document — at the cost of raising the last-resort guard before the kernel OOM killer takes over. The document also accepts `cgroupRankingExpression`, `strictCgroupClassOrdering` and `sampleInterval`; anything left out keeps its default, so overriding the trigger alone is enough here:

```yaml
apiVersion: v1alpha1
kind: OOMConfig
triggerExpression: |-
  (multiply_qos_vectors(d_qos_memory_full_total, {System: 8.0, Podruntime: 4.0}) > 3000.0 &&
   multiply_qos_vectors(qos_memory_full_avg10, {System: 1.0, Podruntime: 1.0}) > 5.0 &&
   time_since_trigger > duration("5s")) ||
  (memory_full_avg60 > 90.0 && time_since_trigger > duration("60s"))
```

Prefer fixing the workload that is generating the pressure. Persistent triggering means a pod is sitting at its memory ceiling and reclaiming constantly, and raising that pod's limit addresses the cause rather than the symptom.

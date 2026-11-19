# SeaweedFS volume size limit — lowering it on an existing instance

New SeaweedFS instances start with `master.volumeSizeLimitMB: 400`. An instance that existed before that default was lowered keeps 1000 unless its limit was already set: on a variant that runs platform migrations, migration 57 wrote 1000 into every instance that had not chosen one, because lowering it on an instance that holds data is not free. The one exception is a release whose `master` is not an object, which no v1 chart renders; it is covered below. The `default` variant runs none, so there that step is yours, before the upgrade, as described under [Before upgrading the platform](#before-upgrading-the-platform). A value the new chart refuses, anything but a whole number from 1 to 30000, counts as not chosen and was replaced with 1000, which is what the previous chart ran in its place. A value from 1 to 30000 that was set before the upgrade stays, and since the previous chart ignored it, it takes effect with the upgrade. Where the migration runs, it names each such instance in its Job's log, but that Job runs as part of the upgrade, too late to act on, so find the ones whose limit it changes beforehand, as described under [Before upgrading the platform](#before-upgrading-the-platform). The value lives in the instance's own spec, so a tool that replaces the whole resource from a manifest without the field drops it and the instance takes 400. If an instance is managed from a manifest, keep `master.volumeSizeLimitMB` in that manifest. Every volume already at or above the new limit leaves the master's writable set on the next heartbeat, so every bucket whose volumes have all crossed that line needs a grow before its next write lands. This page is the check to run before setting a lower limit on such an instance, and the way back if a lowered instance stops taking writes. What decides the outcome is free space measured against what the buckets on each server will claim, not the size of the volume. An instance that comes out short keeps serving reads and loses no data, but the affected buckets stop taking writes until an operator intervenes.

An instance left at 1000 still has more room than it used to. At `replicationFactor: 2` a grow now claims three volumes rather than six, so a bucket's budget there is 3000 MB instead of 6000 and the instance has room for roughly twice as many new buckets as it had. The same change costs write spread: a bucket that grows gets three writable volumes instead of six, and a volume server takes the writes into each volume one at a time, so a heavily parallel writer spreads over fewer volumes until the bucket grows again. The growth count is not a field, so there is no setting that takes this back. At any other replication factor the batch is unchanged.

**Leave the limit at 1000 if the instance runs `topology: MultiZone`, or if more of its volume servers share one disk type than its `replicationFactor`.** The master places a new volume by disk type, not by group: it picks among all servers of the requested type with room, whether they belong to the default group or to a pool, and it treats an empty `diskType` and `hdd` as the same type. Under `MultiZone` it also grows each zone on its own. In both cases it can put a new volume on a server where the bucket that asked for it has nothing yet, which a per-server listing cannot account for. So count the servers of all groups together for each disk type, taking `volume.diskType` for the default group and `diskType` for each pool. The shipped layout, two volume servers at `replicationFactor: 2` and no pool, is one the check covers, and `kubectl get statefulset` in the instance's namespace shows how many servers each group runs.

## Before upgrading the platform

An instance whose limit was already set to a valid value other than 1000, a whole number from 1 to 30000, changes limit with the upgrade itself. List those instances, and the releases that need a closer look, while the previous release still runs; this needs `jq`:

<!-- migration-57-listing -->
```sh
kubectl get helmreleases.helm.toolkit.fluxcd.io -A -o json \
  -l apps.cozystack.io/application.kind=SeaweedFS,apps.cozystack.io/application.group=apps.cozystack.io \
  | jq -r '"instances \(.items | length)",
      (.items[] | "\(.metadata.namespace) \(.metadata.name)" as $id | .spec.values as $s
       | [(.spec.valuesFrom // [])[] | select(. != {kind: "Secret", name: "cozystack-values"})] as $extra
       | (if $extra != [] then "valuesfrom \($id) \($extra | tojson)" else empty end),
         (if $s != null and ($s | type) != "object" then "unreadable \($id) \($s | tojson)"
          elif $s.master != null and ($s.master | type) != "object" then "unrenderable \($id) \($s.master | tojson)"
          else $s.master.volumeSizeLimitMB as $v
          | select(($v | type) == "number" and $v == ($v | floor) and $v >= 1 and $v <= 30000 and $v != 1000)
          | "changes \($id) \($v)" end)),
      "complete \(.items | length)"'
```

**The last line must read `complete` with the same number as the first.** If it is missing or `jq` printed an error, `jq` stopped at a release it could not read, and the list says nothing about the releases after it, whatever it printed: stop.

**The `instances` count, less the number of `unreadable` lines, must equal the number of instances `kubectl get seaweedfses.apps.cozystack.io -A` lists.** The API leaves out a release it cannot convert and only logs it, and a release whose `spec.values` is not an object is one it cannot convert. Any other difference means the labels in this command are not the ones the API selects instances by, or the API is leaving out a release for another reason: stop and find out why before reading the list.

Every other line names one release, by namespace and name after its first word:

- `changes`: the upgrade changes its limit to the number at the end of the line. Run [the check](#the-check) on it with `LIMIT` set to that number.
- `unreadable`: its `spec.values` is not an object, and the line shows what it holds. helm-controller ignores such values and renders the chart's defaults, so the instance runs at 1000 today. The migration, or the step below on the `default` variant, pins it there and replaces what it held, which drops nothing that applies.
- `unrenderable`: its `master` is not an object, and the line shows what it holds. No v1 chart renders that, so the release does not reconcile today, and nothing pins it. When you repair `master`, set `volumeSizeLimitMB` in the same edit, or the instance comes up at 400.
- `valuesfrom`: it also takes values from the entries on the line, besides the platform's `cozystack-values` Secret. helm-controller merges `spec.values` over them, so a `master.volumeSizeLimitMB` an entry sets without `targetPath` does not apply, and one set through `targetPath` can, depending on the entries before it. If an entry sets it that way, check the instance with that value as for a `changes` line.

For a `changes` line:

- Below 1000 the upgrade lowers the limit, which is what the rest of this page is about.
- Above 1000 it raises the limit. Existing buckets keep writing, but every volume below the new limit is charged up to that limit, so the room new buckets grow into shrinks, possibly to none. Measured on a local SeaweedFS 4.31 setup: a server holding seven nearly empty volumes had no free slot left once the limit was raised tenfold, and a new bucket was refused while the existing one kept writing; setting the limit back let the new bucket grow. The budget is an upper bound here too, and what the `df` figure has left after it, divided by the limit, is roughly how many volumes new buckets can still get, a batch for each.

If one comes out short and you cannot free space before the upgrade, set its limit to 1000 in its HelmRelease with the same patch the migration applies, taking `NS` and `NAME` from its line in the list:

```sh
kubectl -n "$NS" patch helmreleases.helm.toolkit.fluxcd.io "$NAME" --type=merge \
  -p '{"spec":{"values":{"master":{"volumeSizeLimitMB":1000}}}}'
```

The running chart ignores the value, and the new one applies it, so the instance stays at 1000. Patch the HelmRelease, not the SeaweedFS resource. The API applies a patch to the instance as it reads it, with every default filled in, and writes that whole spec back into `spec.values`, so every field left at its default would stop following the chart's default. The HelmRelease patch writes this one key, as the migration does.

On the `default` variant, which installs packages by hand and runs no platform migrations, nothing writes 1000 into the instances that have not chosen a limit, and the upgrade would lower every one of them whose `master` is an object to 400. Before upgrading, apply the migration's patch yourself. It writes only `master.volumeSizeLimitMB` into each instance's HelmRelease, and only where no valid value is set:

<!-- migration-57-pin -->
```sh
kubectl get helmreleases.helm.toolkit.fluxcd.io -A -o json \
  -l apps.cozystack.io/application.kind=SeaweedFS,apps.cozystack.io/application.group=apps.cozystack.io \
  | jq -r '"instances \(.items | length)",
      (.items[] | "\(.metadata.namespace) \(.metadata.name)" as $id | .spec.values as $s
       | [(.spec.valuesFrom // [])[] | select(. != {kind: "Secret", name: "cozystack-values"})] as $extra
       | (if $extra != [] then "valuesfrom \($id) \($extra | tojson)" else empty end),
         (if $s != null and ($s | type) != "object" then "unreadable \($id) \($s | tojson)", "pin \($id)"
          elif $s.master != null and ($s.master | type) != "object" then "unrenderable \($id) \($s.master | tojson)"
          else $s.master.volumeSizeLimitMB as $v
          | select(($v | type) != "number" or $v != ($v | floor) or $v < 1 or $v > 30000)
          | "pin \($id)" end)),
      "complete \(.items | length)"' \
  | while read -r what ns name detail; do
      case "$what" in
        pin) ;;
        instances|complete) echo "$what $ns"; continue ;;
        *) echo "$what $ns $name $detail"; continue ;;
      esac
      echo "pinning $ns/$name"
      kubectl -n "$ns" patch helmreleases.helm.toolkit.fluxcd.io "$name" --type=merge \
        -p '{"spec":{"values":{"master":{"volumeSizeLimitMB":1000}}}}' </dev/null \
        || { echo "stopped: patching $ns/$name failed"; break; }
    done
```

**The last line must read `complete` with the same number as the first.** If it is missing, `jq` printed an error, or a `stopped` line appears, the loop ended early, and the releases after that point were neither listed nor pinned, whatever the lines above say and whatever the exit status: stop, fix the cause and run it again. The loop stops at the first failed patch so that this one line covers both. The `unreadable`, `unrenderable` and `valuesfrom` lines mean what they mean in the listing above, and an `unreadable` release is pinned like the others. Then run the command a second time. It must print no `pinning` and no `unreadable` line, which shows that every release it can pin now holds a value the upgrade keeps, and the `instances` count must now equal the number the API lists, since the patch made every `unreadable` release readable. An `unrenderable` line stays until that release is repaired as described above.

## Why free space is the thing to measure

A volume server started with `-max 0` derives its slot count from disk. A volume past the limit is charged no headroom, while one below it is charged all of its unfilled room, so once a bucket's volumes have all gone oversized the slots open to it are free space, less the unfilled room of every volume still below the limit, divided by the limit: at 400, 400 MB of such space is one slot.

One slot serves one bucket, and the buckets compete. A grow claims a batch of three volumes at once and takes as many as are available, and the volumes it creates are empty, so they charge their own headroom straight back and the next bucket finds nothing. Measured on a two-bucket instance with two free slots: the first bucket to write took both, and the second stayed refused afterwards with the disk still reporting a quarter of itself free. A bucket that gets only part of its batch keeps those volumes and writes into them; the check below still budgets a whole batch for it on every volume server, which is an upper bound.

Buckets that keep writing are not free either. Each of their volumes still below the limit is charged its unfilled room, up to a whole limit, and a bucket grows several volumes at a time, six at the upstream count this platform used to run and three now, so one that has barely been written to can hold 2400 MB of that on a server. Measured on a local SeaweedFS 4.31 setup with the limit and batch scaled down: a server with free space for a batch per bucket refused the grow of the one bucket that needed it, because a second, nearly empty bucket's six volumes had taken the room, and the same free space without the second bucket let the grow through.

The master also grows some buckets without being asked. Every few minutes it gives two new volumes to any bucket with no writable volume at or below 90 percent of the limit, and at 400 that line is 360 MB where at 1000 it is 900, so a bucket whose volumes all sit between 360 and 400 MB is grown once the limit is lowered, even though it can still write. Measured on the same setup: a bucket with every volume in that band, and no writes at all after the limit moved, got two new volumes on each server within one pass, while a bucket with a roomy volume was left alone. Two is the master's own step. A client whose assign requests ask for a writable volume count changes it for that bucket, as a filer path rule with `volumeGrowthCount` does, and nothing in this platform asks for one: the COSI driver writes only a bucket's disk type and replication into its rule.

Three is the batch at the shipped `replicationFactor: 2`, the only arm the platform tunes. At `replicationFactor: 1` the master uses its untouched batch of seven. At 3 the batch is three again, and above 3 it is one, so three over-estimates there rather than under-estimating.

The volume server's own `minFreeSpacePercent` reserve, five percent of what the filesystem reports, only comes into it on a server whose writable volumes have all passed the limit, since nothing there is charged headroom. Where five percent is at least the limit, which for 400 holds from 8000 MiB of filesystem upward and so on the shipped 10Gi volume, such a server refuses a grow only once its disk is down to the reserve; where it is less, the server can refuse one with up to a whole limit still free. So on such a server the reserve decides what an operator sees, not whether the instance is safe. On any other server the headroom charged to its volumes decides, and a refusal can come with the disk far from full whatever the reserve. Either way, deleting a bucket gives back a slot for each volume it held on a server, however little it stored, since the unfilled room charged to those volumes leaves with them; only a server already down to its reserve, where every volume is read-only and charged nothing, gets back no more than the freed bytes.

## The check

Run it on the instance before changing its limit. No volume size exempts an instance from this check: a large volume carries more buckets, and the budget scales with them. Size does decide a different cost, which this check does not measure and `df` does not show: a volume server keeps two descriptors open per volume, and the volume count follows stored bytes divided by the limit, so a server reaches its open-file limit sooner; at a size limit of 400 and an open-file limit of 65536, that is somewhere around 12.5 TiB stored. The open-file limit comes from the node's container runtime, not from this chart, and this cost arrives as the instance stores more, not when the size limit is lowered. Checking an instance that turns out to be clear costs a minute; skipping one that is not costs its writes. `$NS` throughout is the namespace the instance runs in; `kubectl get seaweedfses.apps.cozystack.io -A` lists every instance with its namespace.

Count the volume servers first, because a match that finds nothing prints nothing, and nothing looks exactly like every server being clear:

```sh
kubectl -n "$NS" get pods -o name | grep '/seaweedfs-volume' | grep -vc resize-hook
```

**That number must be the volume servers you expect this instance to run, and if it is zero, stop here.** Zero, or fewer than you expect, means the name match does not fit this instance and the loop below would report success by saying nothing. The `resize-hook` exclusion is there because the chart's PVC-resize Job is named `seaweedfs-volume-resize-hook` and matches the prefix without being a volume server; a count higher than you expect means something else does too, and the loop would try to read a path that pod does not have. `kubectl -n "$NS" get statefulset` lists what should be there if you are not sure. Do not swap the name match for `app.kubernetes.io/component=volume`: that label carries the pool or zone key, so it reads `volume-east` on a zoned instance and no pod there carries the bare value.

Then read, on each, its free space and the most its volumes can claim of it. Set `LIMIT` to the value you mean to set, and `BATCH` to the growth batch for the instance's `replicationFactor`: 3 at 2 or above, 7 at 1. The figures in the text around it are for a limit of 400.

```sh
LIMIT=400
BATCH=3
for p in $(kubectl -n "$NS" get pods -o name | grep '/seaweedfs-volume' | grep -v resize-hook); do
  echo "$p"
  kubectl -n "$NS" exec "$p" -- df -m /data1
  kubectl -n "$NS" exec -i "$p" -- sh -s -- "$BATCH" "$LIMIT" <<'EOF'
find /data1 -name '*.dat' -exec ls -ln {} + | awk -v batch="$1" -v limit="$2" '
  BEGIN { full = limit * 1048576; roomy_max = full * 0.9 }
  { n = $NF; sub(/.*\//, "", n); c = n
    if (!sub(/_[0-9]+\.dat$/, "", c)) c = "(default)"
    files++
    if (!(c in seen)) { seen[c] = 1; order[++cols] = c }
    if ($5 < full) { small[c]++; smalls++ }
    if ($5 <= roomy_max) roomy[c] = 1 }
  END { for (i = 1; i <= cols; i++) { c = order[i]; need += small[c] + ((c in roomy) ? 0 : batch) }
        printf "volume files %d, below the limit %d, collections %d, budget %d MB\n", files, smalls, cols, need * limit }'
EOF
done
```

`/data1` is the mount on every render path, since each `dataDirs` entry the chart writes is named `data1`, and the volume server writes its files straight into it as `<bucket>_<id>.dat`, or `<id>.dat` for the unnamed default collection. You should get one block per server, as many as the count, each with a `df` line and a budget line.

**Read `volume files` before anything else.** It counts every volume on that server, from the same listing the budget is computed from, so a server that holds any bucket's data cannot show zero there. If every server shows zero on an instance that stores data, the listing is not reading the volume files and a budget of 0 MB means nothing: stop. Zero `below the limit` is a legitimate answer on its own.

**A server is clear when its available `df` figure is at least its budget, and the instance is clear when every server is.** For an instance the top of this page leaves to the check, a server's budget is an upper bound on what the new limit claims of its free space when it lands: one limit for each volume below it, since that is the most such a volume can be charged, and a whole batch for each collection with no volume at or below 90 percent of the limit, 360 MB at 400, since that collection is grown whether or not it writes. `BATCH` is at least the two volumes the master's own growth adds. The listing reads `.dat` sizes only, while the volume server charges each volume the room left after its `.dat` and `.idx` together, so leaving the index out can only overstate the charge. The master draws that 90 percent line against the `.dat` length the volume server reports, rounded up to the 8-byte needle padding, so a file within seven bytes of the line can fall on the other side of it; it also adds bytes it has handed out but not yet seen written, so run the check while the instance is not being written to heavily. The master also counts only writable volumes as roomy, and the listing cannot tell which ones are read-only: a server under its five percent reserve has made all of its volumes read-only, and an operator can mark one by hand. If either applies to a server, take its budget as `LIMIT` × (`BATCH` × collections + volumes below the limit) MB, from the same line, instead. If a server falls short, free space as described below before lowering the limit, or leave it at 1000. A server that clears one limit but not its budget is the trap this check exists for: the first bucket to write grows into what there is, and the ones behind it stay refused while `df` still shows room.

## If the check comes out short

Delete buckets the tenant no longer needs, then re-run the check. A deleted bucket gives back a slot for every volume it held, including one it barely wrote to. Deleting objects inside a bucket helps less and later: that room comes back only after a vacuum, and only for what was deleted.

## Lowering the limit

Set the new value in the instance's HelmRelease, with the patch used above and for the same reason; the master StatefulSet rolls with it:

```sh
kubectl -n "$NS" patch helmreleases.helm.toolkit.fluxcd.io "$NAME" --type=merge \
  -p '{"spec":{"values":{"master":{"volumeSizeLimitMB":400}}}}'
```

`NAME` is the instance's name, which its HelmRelease carries unchanged. The tenant module creates its instance under the fixed name `seaweedfs`; an instance created directly against the API can carry another one.

## If a lowered instance stops taking writes

The symptom is some buckets writing and others failing on the same instance, while the disk reads as far from full and the master logs a grow that places nothing. The ones that write are those whose grow went through first; the rest are behind them in the same queue for the same free space. No data was lost on the way in, since the volumes stay readable throughout. Freeing space helps as described above, takes effect on the next heartbeat and restarts nothing. Setting the limit back to 1000, with the same HelmRelease patch and 1000 in place of 400, frees every bucket at once, and it rewrites the master's command line, so the master StatefulSet rolls; give it a window.

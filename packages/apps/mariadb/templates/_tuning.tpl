{{/*
  Derive my.cnf sizing from the instance's own CPU, memory and volume.

  Every value below is either a memory allocation or a thread count, so it
  belongs to the pod rather than to the host the config was first written
  for. Settings that encode durability or storage behaviour — flush method,
  flush_log_at_trx_commit, io_capacity, fast_shutdown — are deliberately not
  derived here: the chart cannot infer them from the pod's size.

  join_buffer_size is the one allocation left hardcoded, at the 2M the chart
  has always shipped, eight times the server's own default. It is per-join
  rather than per-connection and is left for a change that can measure the
  effect; on a small instance it is the largest allocation this helper does
  not account for.

  The memory limit is read through the same cozy-lib helper that fills the
  MariaDB CR's resource stanza, so `resources` overrides and `resourcesPreset`
  resolve exactly as they do there.

  Two version constraints shape the clamps, both checked against every server
  this chart ships (10.6.24, 10.11.15, 11.4.9, 11.8.5):

  - InnoDB refuses to start when innodb_buffer_pool_size is too small at the
    default 16K page size. The exact minimum moved: 10.6 demands 5 MiB, the
    later lines 6 MiB. The 8Mi floor clears both.
  - On the 10.6 line the buffer pool is allocated in innodb_buffer_pool_chunk_size
    units, which default to 128 MiB there, and a request above one chunk is
    rounded *up* to a whole number of chunks. Rounding the computed size down to
    a 128 MiB multiple keeps 10.6 from being handed more buffer pool than its
    memory limit allows. It is a no-op on the other shipped versions: 10.8
    autosized the chunk to a 64th of the pool, and 10.11.12, 11.4.6 and 11.8.2
    deprecated and ignore it outright, all at or below 10.11.15, 11.4.9 and
    11.8.5. Rounding down unconditionally costs those versions a little pool
    whenever the memory limit is at least 256 MiB and not a multiple of it —
    the rounding applies to half the limit, and below one whole chunk it does
    not fire at all — in exchange for one effective size across every version
    the chart ships.

  Where the clamps sit relative to what the chart used to ship, since an
  upgrade moves an existing instance onto them:

  - Most caps are the previously shipped value, so an instance large enough to
    reach one is sized as it is today.
  - key_buffer_size is capped at 128M against the 1024M shipped before, and
    floored at 8M against the server's own 128M default. Both ends are
    deliberate — see the key cache note below — and both lower the allocation
    on every instance.
  - The IO threads floor at 2 against a server default of 4, so a one-core
    instance gets fewer than an untuned server would start. That is the point:
    the floor follows the CPU limit, not the host.
  - The buffer pool has no cap at all: it is the setting this whole helper
    exists to derive, and pinning it to the old 60G would keep the largest
    instances sized for a host they do not run on. An instance with 128Gi or
    more of memory is the single case where anything here goes up rather than
    down, to half of a limit the instance already has.
*/}}
{{- define "mariadb.tuning" }}
{{-   $mib := 1048576 }}
{{-   $resources := include "cozy-lib.resources.defaultingSanitize" (list .Values.resourcesPreset .Values.resources $) | fromYaml }}
{{-   $memB := include "cozy-lib.resources.toFloat" $resources.limits.memory | float64 | int64 }}
{{-   $cpu := include "cozy-lib.resources.toFloat" $resources.limits.cpu | float64 }}
{{-   $volB := include "cozy-lib.resources.toFloat" (.Values.size | toString) | float64 | int64 }}
{{-   $memMi := div $memB $mib }}
{{-   $cores := ceil $cpu | int64 }}

{{/*
  Buffer pool: half of the memory limit. The classic "70-80% of RAM" advice
  assumes a dedicated host where the remainder is page cache and overshooting
  merely swaps. Under a cgroup limit the same budget also has to cover the
  per-connection buffers, temp tables, the key cache and mysqld's own heap,
  and exceeding it is an OOM kill rather than swapping, so half is the share
  that leaves room for everything else.
*/}}
{{-   $poolB := div $memB 2 }}
{{-   if ge $poolB (mul 128 $mib) }}
{{-     $poolB = mul (div $poolB (mul 128 $mib)) (mul 128 $mib) }}
{{-   end }}
{{-   $poolB = max $poolB (mul 8 $mib) }}

{{/*
  Redo log: bounded by the buffer pool it buffers dirty pages for, and by a
  small fraction of the data volume it is preallocated on. The floor is the
  server's own 96M default; the ceiling is the 4096M the chart shipped before.
  Changing this on an existing database is safe on every shipped version —
  InnoDB replays the existing log during crash recovery and only then rewrites
  it at the new size, after both a clean and an unclean shutdown.
*/}}
{{-   $logB := min $poolB (div $volB 20) }}
{{-   $logB = min (max $logB (mul 96 $mib)) (mul 4096 $mib) }}

{{/*
  MyISAM key cache. Allocated eagerly at startup, and close to dead weight
  here because MariaDB's own system tables are Aria rather than MyISAM, so it
  is held to a small share of memory and never above the 128M server default.
*/}}
{{-   $keyB := min (max (div $memB 32) (mul 8 $mib)) (mul 128 $mib) }}

{{/*
  Connections and the per-connection buffers behind them. The floor is stock
  MariaDB's own 151, so the chart is never more restrictive than an untuned
  server; the ceiling is the 4096 shipped before.
*/}}
{{-   $maxConnections := min (max (div $memMi 4) 151) 4096 }}

{{/*
  tmp_table_size only takes effect up to max_heap_table_size, which the chart
  does not set and which defaults to 16M, so today this scales a value whose
  effective ceiling is its own floor. It is derived anyway so that raising
  max_heap_table_size later is a one-line change rather than a re-derivation,
  and because the 512M shipped before was inert for exactly the same reason.
*/}}
{{-   $tmpTableB := min (max (div $memB 32) (mul 16 $mib)) (mul 512 $mib) }}
{{-   $readRndB := min (max (div $memB 256) 262144) (mul 16 $mib) }}
{{-   $tableOpenCache := min (max (mul $memMi 4) 2000) 40714 }}

{{/*
  Thread counts follow the CPU limit; ceilings are the previously shipped
  values. Only the IO threads are live today: thread_pool_size applies solely
  under thread_handling=pool-of-threads, and the chart leaves the server on its
  default of one-thread-per-connection, so this — like the 24 shipped before —
  is carried for the day the chart switches rather than for effect now.
*/}}
{{-   $threadPoolSize := min (max $cores 1) 24 }}
{{-   $ioThreads := min (max $cores 2) 12 }}

{{-   dict
        "bufferPoolSizeMi" (div $poolB $mib)
        "logFileSizeMi"    (div $logB $mib)
        "keyBufferSizeMi"  (div $keyB $mib)
        "tmpTableSizeMi"   (div $tmpTableB $mib)
        "readRndBufferKi"  (div $readRndB 1024)
        "maxConnections"   $maxConnections
        "tableOpenCache"   $tableOpenCache
        "threadPoolSize"   $threadPoolSize
        "ioThreads"        $ioThreads
      | toYaml }}
{{- end }}

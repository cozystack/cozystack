{{- /* seaweedfs.renamedVolumePrefix — reconstruct the name 4.31 gives the volume
   component of the `<name>-system` release, i.e. the RELEASE-NAMED generation the
   fullnameOverride pin exists to avoid.

   Input: the `<name>-system` release name, as a bare string.
     {{ include "seaweedfs.renamedVolumePrefix" "foo-system" }} -> foo-system-seaweedfs-volume

   This replays two upstream helpers, so it must track them if the vendored chart
   changes (hack/seaweedfs-guard-parity.bats pins the two copies of the guard
   against each other; charts/seaweedfs/templates/shared/_helpers.tpl is the
   source of truth for the rules below):

     seaweedfs.fullname      — with no fullnameOverride, the release name, plus
                               `-<chart name>` when the release name does not
                               already contain it; truncated to 63.
     seaweedfs.componentName — truncates the fullname to (62 - len(suffix)) before
                               appending `-<suffix>`, so for `volume` the fullname
                               is cut to 56. An instance name >= ~40 chars
                               therefore loses `seaweedfs` from the tail, which is
                               why a `contains "seaweedfs"` name filter cannot see
                               its claims and this reconstruction can.

   The guard deliberately does NOT call seaweedfs.fullname directly: that helper
   reads .Values.fullnameOverride, which this chart PINS to `seaweedfs`, so it
   returns the chart-named generation — the opposite of what is wanted here.

   ACCEPTED LIMIT — zone/pool volume components. MultiZone (and Simple-with-
   pools) tenants get per-group components with suffix `volume-<key>`, cut at
   (62 - len(suffix)), i.e. SHORTER than the 56 this helper reconstructs. The
   prefixes only diverge when the fullname exceeds that shorter cut: for the
   supported instance (the tenant module hardcodes the name `seaweedfs`,
   fullname 16 chars) that takes a zone/pool key of ~40+ characters, and for
   the default `volume` group it can never happen (its suffix IS the 56 cut).
   A guard prefix that diverges means a release-named zone component the guard
   cannot see. Decided 2026-07-17 (1.6 triage): accepted and documented in
   docs/operations/seaweedfs-431-rename-recovery.md (Scope) rather than
   reconstructed per-key — instance names are fixed by the tenant module and
   absurd keys are the only reachable trigger. Revisit if instance naming is
   ever opened up or a key-length clamp lands in extra/seaweedfs. */}}
{{- define "seaweedfs.renamedVolumePrefix" -}}
{{- $release := . -}}
{{- $full := $release -}}
{{- if not (contains "seaweedfs" $release) -}}
{{-   $full = printf "%s-seaweedfs" $release -}}
{{- end -}}
{{- $full = $full | trunc 63 | trimSuffix "-" -}}
{{- printf "%s-volume" ($full | trunc 56 | trimSuffix "-") -}}
{{- end -}}

{{- define "cozy-lib.network.defaultDisableLoadBalancerNodePorts" }}
{{/* Default behavior prior to introduction */}}
{{-   `true` }}
{{- end }}

{{/* 
Invoke as {{ include "cozy-lib.network.disableLoadBalancerNodePorts" $ }}.
Detects whether the current load balancer class requires nodeports to function
correctly. Currently just checks if Hetzner's RobotLB is enabled, which does
require nodeports, and so, returns `false`. Otherwise assumes that metallb is
in use and returns `true`.
*/}}

{{- define "cozy-lib.network.disableLoadBalancerNodePorts" }}
{{-   include "cozy-lib.loadCozyConfig" (list "" .) }}
{{-   $cozyConfig := index . "cozyConfig" }}
{{-   if not $cozyConfig }}
{{-     include "cozy-lib.network.defaultDisableLoadBalancerNodePorts" . }}
{{-   else }}
{{-     $components := $cozyConfig.components | default dict }}
{{-     $robotlb := index $components "hetzner-robotlb" | default dict }}
{{-     $robotlbEnabled := $robotlb.enabled | default false }}
{{-     not $robotlbEnabled }}
{{-   end }}
{{- end }}

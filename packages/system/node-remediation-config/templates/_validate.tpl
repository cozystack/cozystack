{{- /*
The fence-agents operator runs in safety-mode: Redfish only, TLS verified
against bmc.caBundle, an explicit ComputerSystem per node, action off. Its
webhook refuses a template outside that contract, but only once the release
reaches the cluster, so the same contract is checked here, where the message
names the values key to fix.

values.schema.json refuses a value of the wrong kind or shape under its own
key before any template runs; what remains here are the rules between keys
and the cases the schema cannot see: a key whose value is null is dropped
before validation, so a missing parent map is caught here.
*/ -}}
{{- define "node-remediation-config.validate" -}}
{{- $v := .Values -}}
{{- if not (kindIs "map" $v.bmc) -}}
{{- fail "nodeRemediation.bmc must be a map: caBundle, credentialsSecretName, systemsUri, port and nodes live under it" -}}
{{- end -}}
{{- if or (not (kindIs "map" $v.bmc.nodes)) (eq (len $v.bmc.nodes) 0) -}}
{{- fail "nodeRemediation.bmc.nodes must map every Node name to its BMC: a node without an entry can never be fenced, so NodeHealthCheck would select it and release nothing" -}}
{{- end -}}
{{- include "node-remediation-config.validatePort" (list "nodeRemediation.bmc.port" (include "node-remediation-config.scalar" (list $v.bmc "port"))) -}}
{{- $shared := include "node-remediation-config.scalar" (list $v.bmc "credentialsSecretName") -}}
{{- $own := 0 -}}
{{- range $name, $node := $v.bmc.nodes -}}
{{- $address := include "node-remediation-config.scalar" (list $node "address") -}}
{{- if or (contains "/" $address) (contains "@" $address) (contains "?" $address) (contains "#" $address) (contains "%" $address) (contains "\\" $address) -}}
{{- fail (printf "nodeRemediation.bmc.nodes.%s.address %q must be a host or IP only; the scheme is always https, the port goes to port and the ComputerSystem to systemsUri" $name $address) -}}
{{- end -}}
{{- /* A colon is a port unless the value looks like an IPv6 literal: hex groups with at least two colons, dots for an IPv4-mapped tail. This is a pre-filter; the operator's template webhook parses the address and has the final word. */ -}}
{{- if and (contains ":" $address) (not (regexMatch "^[0-9A-Fa-f.]*(:[0-9A-Fa-f.]*){2,}$" $address)) -}}
{{- fail (printf "nodeRemediation.bmc.nodes.%s.address %q must not carry a port; the port goes to port, and an IPv6 literal is checked once more by the operator's webhook at install" $name $address) -}}
{{- end -}}
{{- include "node-remediation-config.validatePort" (list (printf "nodeRemediation.bmc.nodes.%s.port" $name) (include "node-remediation-config.scalar" (list $node "port"))) -}}
{{- /* Either the node or bmc names the ComputerSystem; the schema checks the shape of each, this checks that one of them is set. */ -}}
{{- if eq (include "node-remediation-config.systemsUri" (list $node $v.bmc)) "" -}}
{{- fail (printf "nodeRemediation.bmc.nodes.%s: systemsUri \"\" must name one ComputerSystem member, /redfish/v1/Systems/<id>; set it on the node or as bmc.systemsUri, fence_redfish would otherwise take the last system the BMC lists" $name) -}}
{{- end -}}
{{- if ne (include "node-remediation-config.scalar" (list $node "credentialsSecretName")) "" -}}
{{- $own = add1 $own -}}
{{- end -}}
{{- end -}}
{{- if and (eq $shared "") (ne (int $own) (len $v.bmc.nodes)) -}}
{{- fail "nodeRemediation.bmc.credentialsSecretName is required, or every node names its own credentialsSecretName: the fence-agents operator refuses a node without exactly one Secret" -}}
{{- end -}}
{{- if and (ne $shared "") (ne (int $own) 0) -}}
{{- fail "nodeRemediation.bmc: either credentialsSecretName for all nodes or a credentialsSecretName on every node, not both; the fence-agents operator refuses a node that is given two Secrets" -}}
{{- end -}}
{{- /* The schema admits any duration; a zero one would remediate a node the moment it reports Ready=Unknown, with no tolerance for a short partition. */ -}}
{{- if regexMatch "^(0+(h|m|s))+$" (toString $v.unhealthyAfter) -}}
{{- fail (printf "nodeRemediation.unhealthyAfter %q must be above zero: a node would be remediated the moment it reports Ready=Unknown, before a short partition can clear" (toString $v.unhealthyAfter)) -}}
{{- end -}}
{{- end -}}

{{- /*
A values scalar as text: nil and a missing key become "", a number its
decimal form. The values of a Package travel as JSON, so a number written in
YAML arrives as a float64; only an integral one in a range the integer text
can carry becomes that text. A fraction or an out-of-range value is handed
on unchanged, so that whoever reads it refuses it instead of a truncated
number passing as a count.
*/ -}}
{{- define "node-remediation-config.scalar" -}}
{{- $map := index . 0 -}}
{{- $key := index . 1 -}}
{{- if and (kindIs "map" $map) (hasKey $map $key) (not (kindIs "invalid" (index $map $key))) -}}
{{- $value := index $map $key -}}
{{- if and (kindIs "float64" $value) (eq $value (floor $value)) (le $value 1.0e15) (ge $value -1.0e15) -}}
{{- printf "%d" (int64 $value) -}}
{{- else -}}
{{- toString $value -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- /* The schema bounds an integer port and admits a string of digits; the range of the string form is checked here, named by the key it was written under. An empty port is the Redfish default. */ -}}
{{- define "node-remediation-config.validatePort" -}}
{{- $key := index . 0 -}}
{{- $port := index . 1 -}}
{{- if and (ne $port "") (or (lt (atoi $port) 1) (gt (atoi $port) 65535)) -}}
{{- fail (printf "%s %q must be an integer between 1 and 65535" $key $port) -}}
{{- end -}}
{{- end -}}

{{- define "node-remediation-config.port" -}}
{{- $node := index . 0 -}}
{{- $bmc := index . 1 -}}
{{- $port := include "node-remediation-config.scalar" (list $node "port") -}}
{{- if eq $port "" -}}
{{- $port = include "node-remediation-config.scalar" (list $bmc "port") -}}
{{- end -}}
{{- $port -}}
{{- end -}}

{{- define "node-remediation-config.systemsUri" -}}
{{- $node := index . 0 -}}
{{- $bmc := index . 1 -}}
{{- $uri := include "node-remediation-config.scalar" (list $node "systemsUri") -}}
{{- if eq $uri "" -}}
{{- $uri = include "node-remediation-config.scalar" (list $bmc "systemsUri") -}}
{{- end -}}
{{- $uri -}}
{{- end -}}

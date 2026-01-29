{{- define "cozy-lib.rbac.accessLevelMap" }}
view: 0
use: 1
admin: 2
super-admin: 3
{{- end }}

{{- define "cozy-lib.rbac.accessLevelToInt" }}
{{-   $accessMap := include "cozy-lib.rbac.accessLevelMap" "" | fromYaml }}
{{-   $accessLevel := dig . -1 $accessMap | int }}
{{-   if eq $accessLevel -1 }}
{{-     printf "encountered access level of %s, allowed values are %s" . ($accessMap | keys) | fail }}
{{-   end }}
{{-   $accessLevel }}
{{- end }}

{{- define "cozy-lib.rbac.accessLevelsAtOrAbove" }}
{{-   $minLevelInt := include "cozy-lib.rbac.accessLevelToInt" . | int }}
{{-   range $k, $v := (include "cozy-lib.rbac.accessLevelMap" "" | fromYaml) }}
{{-     if ge (int $v) $minLevelInt }}
- {{ $k }}
{{-     end }}
{{-   end }}
{{- end }}

{{- define "cozy-lib.rbac.allParentTenantsAndThis" }}
{{-   if not (hasPrefix "tenant-" .) }}
{{-     printf "'%s' is not a valid tenant identifier" . | fail }}
{{-   end }}
{{-   $parts := append (splitList "-" .) "" }}
{{-   $tenants := list }}
{{-   range untilStep 2 (len $parts) 1 }}
{{-     $tenants = append $tenants (slice $parts 0 . | join "-") }}
{{-   end }}
{{-   range $tenants }}
- {{ . }}
{{-   end }}
{{-   if not (eq . "tenant-root") }}
- tenant-root
{{-   end }}
{{- end }}

{{- define "cozy-lib.rbac.groupSubject" -}}
- kind: Group
  name: {{ . }}
  apiGroup: rbac.authorization.k8s.io
{{- end }}

{{- define "cozy-lib.rbac.serviceAccountSubject" -}}
- kind: ServiceAccount
  name: {{ . }}
  namespace: {{ . }}
{{- end }}

{{- /* 
  A helper function to get a list of groups that should have access, given a
  minimal access level and the tenant. Invoked as:
  {{ include "cozy-lib.rbac.subjectsForTenantAndAccessLevel" (list "use" $) }}
  For an example input of (list "use" $) and a .Release.Namespace of
  tenant-abc-def it will return:
  ---
  - kind: Group
    name: tenant-abc-admin
    apiGroup: rbac.authorization.k8s.io
  - kind: Group
    name: tenant-abc-def-admin
    apiGroup: rbac.authorization.k8s.io
  - kind: Group
    name: tenant-abc-super-admin
    apiGroup: rbac.authorization.k8s.io
  - kind: Group
    name: tenant-abc-def-super-admin
    apiGroup: rbac.authorization.k8s.io
  - kind: Group
    name: tenant-abc-use
    apiGroup: rbac.authorization.k8s.io
  - kind: Group
    name: tenant-abc-def-use
    apiGroup: rbac.authorization.k8s.io

  in other words, all roles including use and higher and for tenant-abc-def, as
  well as all parent, grandparent, etc. tenants.
*/}}
{{- define "cozy-lib.rbac.subjectsForTenantAndAccessLevel" }}
{{-   include "cozy-lib.checkInput" . }}
{{-   $level := index . 0 }}
{{-   $tenant := index . 1 }}
{{-   $levels := include "cozy-lib.rbac.accessLevelsAtOrAbove" $level | fromYamlArray }}
{{-   $tenants := include "cozy-lib.rbac.allParentTenantsAndThis" $tenant | fromYamlArray }}
{{-   range $t := $tenants }}
{{-     include "cozy-lib.rbac.serviceAccountSubject" $t }}{{ printf "\n" }}
{{-     range $l := $levels }}
{{-       include "cozy-lib.rbac.groupSubject" (printf "%s-%s" $t $l) }}{{ printf "\n" }}
{{-     end }}
{{-   end}}
{{- end }}

{{- define "cozy-lib.rbac.subjectsForTenant" }}
{{-   include "cozy-lib.checkInput" . }}
{{-   $level := index . 0 }}
{{-   $tenant := index . 1 }}
{{-   $tenants := include "cozy-lib.rbac.allParentTenantsAndThis" $tenant | fromYamlArray }}
{{-   range $t := $tenants }}
{{-     include "cozy-lib.rbac.groupSubject" (printf "%s-%s" $t $level) }}{{ printf "\n" }}
{{-   end}}
{{- end }}

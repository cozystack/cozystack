{{- /* Realms published on the login host in the narrowed (ingress.adminHost)
       mode, as a YAML list — callers parse it with fromYamlArray.

       Enumerating them keeps the login host off realm master, which a bare
       /realms prefix publishes along with every other realm the deployment
       happens to have. It is one hostname, not the whole admin plane: the
       admin route still carries master, and expose-ingress-admin defaults to
       the public Gateway/ingressClass, so where that route attaches and which
       hostnames the edge accepts decide the rest.

       The charset guard mirrors authentication.oidc.realmName in
       packages/core/platform: the name is used verbatim as a URL path
       segment. */}}
{{- define "keycloak.publishedRealms" -}}
{{- $realmPattern := "^[a-zA-Z0-9][a-zA-Z0-9._-]*$" }}
{{- $platformRealm := dig "oidc-realm-name" "cozy" (.Values._cluster | default dict) | toString }}
{{- $extraRealms := .Values.ingress.exposedRealms }}
{{- /* kindIs "invalid" is the unset key; anything else that is not a list is
       an operator mistake, including the falsy scalars false and 0. */}}
{{- if and (not (kindIs "invalid" $extraRealms)) (not (kindIs "slice" $extraRealms)) }}
{{-   fail (printf "ingress.exposedRealms must be a list of realm names (got %s), e.g. exposedRealms: [cozy-admin]" (kindOf $extraRealms)) }}
{{- end }}
{{- range $realm := $extraRealms }}
{{-   if not (kindIs "string" $realm) }}
{{-     fail (printf "ingress.exposedRealms entries must be strings (got %s); quote the realm name" (kindOf $realm)) }}
{{-   end }}
{{-   if not (regexMatch $realmPattern $realm) }}
{{-     fail (printf "ingress.exposedRealms: realm %q is invalid: use only letters, digits, '.', '_' and '-'" $realm) }}
{{-   end }}
{{-   if eq $realm "master" }}
{{-     fail "ingress.exposedRealms must not list master: it holds the Keycloak super-admin, whose login page and token endpoint would then accept those credentials on the public login hostname" }}
{{-   end }}
{{- end }}
{{- if not (regexMatch $realmPattern $platformRealm) }}
{{-   fail (printf "the platform realm name (authentication.oidc.realmName) %q is invalid: use only letters, digits, '.', '_' and '-'" $platformRealm) }}
{{- end }}
{{- if eq $platformRealm "master" }}
{{-   fail "the platform realm (authentication.oidc.realmName) must not be master: it holds the Keycloak super-admin, whose login page and token endpoint would then accept those credentials on the public login hostname" }}
{{- end }}
{{- /* The platform realm is what every component's issuer URL points at, so it
       is published whether or not exposedRealms names it. */}}
{{- concat (list $platformRealm) ($extraRealms | default list) | uniq | toYaml }}
{{- end }}

{{- /* Paths the login host serves in the narrowed mode, as a YAML list of
       {value, exact}.

       Each realm gets an exact path plus a prefix with the trailing slash,
       because ingress-nginx renders `pathType: Prefix` as a plain nginx
       prefix location (buildLocation() in
       internal/ingress/controller/template/template.go) rather than the
       element-wise match the Ingress API specifies — a lone /realms/cozy
       would also serve /realms/cozy-admin there. The pair is equivalent to
       the element-wise semantics on a conformant controller and on Gateway
       API, so it is emitted for both. */}}
{{- define "keycloak.loginPaths" -}}
{{- range $realm := include "keycloak.publishedRealms" . | fromYamlArray }}
- value: {{ printf "/realms/%s" $realm | quote }}
  exact: true
- value: {{ printf "/realms/%s/" $realm | quote }}
  exact: false
{{- end }}
- value: "/resources"
  exact: false
- value: "/.well-known"
  exact: false
{{- end }}

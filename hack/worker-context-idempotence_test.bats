#!/usr/bin/env bats

load test_helper

@test "worker family resolution remains stable across repeated consumer includes" {
  chart_dir="$(mktemp -d)"
  python3 -I - "$(pwd)" "${chart_dir}" <<'PY'
import json, pathlib, shutil, sys
root, tmp = map(pathlib.Path, sys.argv[1:])
chart = tmp / 'chart'
shutil.copytree(root / 'packages/apps/kubernetes-nodes', chart, symlinks=False)
shutil.rmtree(chart / 'tests')
(chart / 'tests').mkdir()
(chart / 'templates/idempotence.yaml').write_text('''{{- include "kubernetes-nodes.workerContext" . -}}
{{- $first := deepCopy .Values -}}
{{- include "kubernetes-nodes.workerContext" . -}}
{{- $second := deepCopy .Values -}}
{{- $class := include "kubernetes-nodes.workerClass" . | fromYaml -}}
{{- $bootstrap := "kubernetes.talosConfigTemplateSpec" -}}
{{- if eq $class.bootstrap "kairos" -}}
{{- $bootstrap = "kubernetes.kairosConfigTemplateSpec" -}}
{{- end -}}
{{- $firstBootstrap := include $bootstrap (dict "root" . "group" .Values "class" $class) -}}
{{- include "kubernetes-nodes.workerContext" . -}}
{{- $secondBootstrap := include $bootstrap (dict "root" . "group" .Values "class" $class) -}}
{{- include "kubernetes-nodes.workerContext" . -}}
apiVersion: v1
kind: ResolutionStability
family: {{ $class.bootstrap | quote }}
stable: {{ and (deepEqual $first $second) (deepEqual $second .Values) }}
bootstrapStable: {{ eq $firstBootstrap $secondBootstrap }}
talosEmpty: {{ empty .Values.talos }}
modulesPresent: {{ hasKey .Values "kernelModules" }}
''')
base={'cluster':'k','version':'v1.35','instanceType':'','resources':{'cpu':'2','memory':'4Gi'},'_cluster':{'cluster-domain':'cozy.local'}}
tests=[]
for family in ['talos','kairos']:
    values=dict(base,workerClassName=family)
    assertions=[{'equal':{'path':'family','value':family}},{'equal':{'path':'stable','value':True}},{'equal':{'path':'bootstrapStable','value':True}},{'equal':{'path':'talosEmpty','value':family=='kairos'}},{'equal':{'path':'modulesPresent','value':False}}]
    tests.append({'it':'repeated '+family+' resolution and bootstrap consumption','set':values,'asserts':assertions})
(chart / 'tests/idempotence_test.yaml').write_text(json.dumps({'suite':'Repeated worker consumer resolution','templates':['templates/idempotence.yaml'],'release':{'name':'kubernetes-nodes-k-md0','namespace':'tenant-test'},'tests':tests}))
PY
  rc=0
  helm unittest "${chart_dir}/chart" || rc=$?
  python3 -I - "${chart_dir}" <<'PY'
import pathlib, shutil, sys
shutil.rmtree(pathlib.Path(sys.argv[1]))
PY
  [ "${rc}" -eq 0 ]
}

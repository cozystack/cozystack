#!/usr/bin/env bats

load test_helper

@test "Helm resolves the same independent WorkerClass contract corpus as admission" {
  chart_dir="$(mktemp -d)"
  python3 -I - "$(pwd)" "${chart_dir}" <<'PY'
import json, pathlib, shutil, sys
root, tmp = map(pathlib.Path, sys.argv[1:])
chart = tmp / 'chart'
shutil.copytree(root / 'packages/apps/kubernetes-nodes', chart, symlinks=False)
shutil.rmtree(chart / 'tests')
(chart / 'tests').mkdir()
(chart / 'templates/worker-resolution-probe.yaml').write_text('''{{- include "kubernetes-nodes.workerContext" . -}}
{{- $class := include "kubernetes-nodes.workerClass" . | fromYaml -}}
{{- $source := "kairos" -}}
{{- $version := "" -}}
{{- $schematic := "" -}}
{{- $names := list -}}
{{- $parameters := dict -}}
{{- if eq $class.bootstrap "talos" -}}
{{- if eq .Values.substrate "proxmox" -}}
{{- $source = "proxmox" -}}
{{- $version = .Values.talos.version -}}
{{- $schematic = .Values.talos.schematicID -}}
{{- else -}}
{{- $out := dict -}}
{{- include "kubernetes-nodes.resolveOsImage" (dict "osImage" .Values.osImage "talos" .Values.talos "out" $out "groupName" "md0") -}}
{{- $source = ternary "builtin" "factory" $out.clone -}}
{{- $version = $out.version -}}
{{- $schematic = $out.schematicID -}}
{{- end -}}
{{- range (include "kubernetes-nodes.kernelModules" (dict "group" .Values "groupName" "md0") | fromYamlArray) -}}
{{- $names = append $names .name -}}
{{- if .parameters -}}{{- $_ := set $parameters .name .parameters -}}{{- end -}}
{{- end -}}
{{- end -}}
apiVersion: v1
kind: WorkerResolution
metadata:
  name: resolution
version: {{ $version | quote }}
schematicID: {{ $schematic | quote }}
source: {{ $source | quote }}
modules: {{ $names | toJson }}
moduleParameters: {{ $parameters | toJson }}
mirrors: {{ .Values.talos.registryMirrors | default dict | toJson }}
''')
cases = json.loads((root / 'internal/kubernetesworker/config/testdata/resolution.json').read_text())
tests=[]
for case in cases:
    values={'cluster':'k','instanceType':'','resources':{'cpu':'2','memory':'4Gi'},'_cluster':{'cluster-domain':'cozy.local'}}
    values.update(case['pool'])
    test={'it':case['name'],'set':values}
    if case.get('class') is not None:
        test['kubernetesProvider']={'scheme':{'cozystack.io/v1alpha1/WorkerClass':{'gvr':{'group':'cozystack.io','version':'v1alpha1','resource':'workerclasses'},'namespaced':False}},'objects':[{'apiVersion':'cozystack.io/v1alpha1','kind':'WorkerClass','metadata':{'name':'custom'},'spec':case['class']}]}
    if 'error' in case:
        test['asserts']=[{'failedTemplate':{'errorPattern':case['error']}}]
    else:
        test['asserts']=[{'equal':{'path':field,'value':case.get(field,'')}} for field in ('version','schematicID','source')]
        test['asserts'].append({'equal':{'path':'modules','value':case['modules']}})
        for field in ('mirrors','moduleParameters'):
            if field in case:
                test['asserts'].append({'equal':{'path':field,'value':case[field]}})
    tests.append(test)
(chart / 'tests/contract_test.yaml').write_text(json.dumps({'suite':'WorkerClass contract corpus','templates':['templates/worker-resolution-probe.yaml'],'release':{'name':'kubernetes-nodes-k-md0','namespace':'tenant-test'},'tests':tests}))
PY
  rc=0
  helm unittest "${chart_dir}/chart" || rc=$?
  python3 -I - "${chart_dir}" <<'PY'
import shutil, sys
shutil.rmtree(sys.argv[1])
PY
  [ "${rc}" -eq 0 ]
}

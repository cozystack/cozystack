#!/usr/bin/env bats

load test_helper

@test "Kairos taint count follows the resolved bootstrap family and shipped CRD limit" {
  limit="$(yq --output-format=json 'select(.kind == "CustomResourceDefinition" and .metadata.name == "kairosconfigtemplates.bootstrap.cluster.x-k8s.io") | .spec.versions[] | select(.name == "v1beta2") | .schema.openAPIV3Schema.properties.spec.properties.template.properties.spec.properties.kubeadm.properties.joinConfiguration.properties.nodeRegistration.properties.taints.maxItems' packages/system/capi-providers-bootstrap/files/bootstrap-components-kairos.yaml)"
  [ "$limit" = 100 ]
  python3 -I - "$PWD" "$BATS_TEST_TMPDIR/chart" "$limit" <<'PY'
import json
from pathlib import Path
import shutil
import sys

root, chart, limit = Path(sys.argv[1]), Path(sys.argv[2]), int(sys.argv[3])
shutil.copytree(root / 'packages/apps/kubernetes-nodes', chart, symlinks=False)
scheme = {
    'cozystack.io/v1alpha1/WorkerClass': {'gvr': {'group': 'cozystack.io', 'version': 'v1alpha1', 'resource': 'workerclasses'}, 'namespaced': False},
    'cluster.x-k8s.io/v1beta1/MachineSet': {'gvr': {'group': 'cluster.x-k8s.io', 'version': 'v1beta1', 'resource': 'machinesets'}, 'namespaced': True},
    'infrastructure.cluster.x-k8s.io/v1alpha1/KubevirtMachineTemplate': {'gvr': {'group': 'infrastructure.cluster.x-k8s.io', 'version': 'v1alpha1', 'resource': 'kubevirtmachinetemplates'}, 'namespaced': True},
}
objects = [
    {'kind': 'MachineSet', 'apiVersion': 'cluster.x-k8s.io/v1beta1', 'metadata': {'name': 's', 'namespace': 's'}, 'spec': {}},
    {'kind': 'KubevirtMachineTemplate', 'apiVersion': 'infrastructure.cluster.x-k8s.io/v1alpha1', 'metadata': {'name': 's', 'namespace': 's'}, 'spec': {}},
    {'kind': 'WorkerClass', 'apiVersion': 'cozystack.io/v1alpha1', 'metadata': {'name': 'custom-worker'}, 'spec': {'bootstrap': 'kairos'}},
]
tests = []
for name in ['kairos', 'custom-worker', 'talos']:
    for count in ([limit, limit + 1] if name != 'talos' else [limit + 1]):
        test = {
            'it': name + ' taints ' + str(count),
            'template': 'templates/nodegroup.yaml',
            'set': {'workerClassName': name, 'taints': [{'key': 'example.com/t' + str(i), 'effect': 'NoSchedule'} for i in range(count)]},
        }
        if name == 'custom-worker':
            test['kubernetesProvider'] = {'scheme': scheme, 'objects': objects}
        if name != 'talos' and count > limit:
            test['asserts'] = [{'failedTemplate': {'errorPattern': 'taints:.*Kairos.*100'}}]
        else:
            test['documentSelector'] = {'path': 'kind', 'value': 'MachineDeployment'}
            test['asserts'] = [{'equal': {'path': 'spec.template.spec.bootstrap.configRef.kind', 'value': 'TalosConfigTemplate' if name == 'talos' else 'KairosConfigTemplate'}}]
        tests.append(test)
suite = {
    'suite': 'bootstrap taint count boundary',
    'templates': ['templates/nodegroup.yaml'],
    'release': {'name': 'kubernetes-nodes-myk8s-md0', 'namespace': 'tenant-test'},
    'set': {'cluster': 'myk8s', '_cluster': {'cluster-domain': 'cozy.local'}, 'version': 'v1.35', 'instanceType': '', 'resources': {'cpu': '2', 'memory': '4Gi'}},
    'tests': tests,
}
(chart / 'tests/taint_count_boundary_test.yaml').write_text(json.dumps(suite))
PY
  run helm unittest --file tests/taint_count_boundary_test.yaml "$BATS_TEST_TMPDIR/chart"
  printf '%s\n' "$output"
  [ "$status" -eq 0 ]
}

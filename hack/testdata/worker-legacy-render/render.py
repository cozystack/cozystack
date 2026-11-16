import hashlib
import json
import pathlib
import shutil
import subprocess
import sys

root, fixtures, temp = map(pathlib.Path, sys.argv[1:])
chart = temp / 'chart'
shutil.copytree(root / 'packages/apps/kubernetes-nodes', chart, symlinks=False)
shutil.rmtree(chart / 'tests')
(chart / 'tests').mkdir()
tests = []
for case in json.loads((fixtures / 'cases.json').read_text()):
    settings = {'cluster': 'k', 'version': 'v1.35', 'instanceType': '', 'resources': {'cpu': '2', 'memory': '4Gi'}, 'diskSize': '20Gi', 'storageClass': 'replicated', 'roles': ['ingress-nginx'], '_cluster': {'cluster-domain': 'cozy.local'}}
    settings.update(case['values'])
    scheme = {
        'cluster.x-k8s.io/v1beta1/MachineSet': {'gvr': {'group': 'cluster.x-k8s.io', 'version': 'v1beta1', 'resource': 'machinesets'}, 'namespaced': True},
        'infrastructure.cluster.x-k8s.io/v1alpha1/KubevirtMachineTemplate': {'gvr': {'group': 'infrastructure.cluster.x-k8s.io', 'version': 'v1alpha1', 'resource': 'kubevirtmachinetemplates'}, 'namespaced': True},
        'infrastructure.cluster.x-k8s.io/v1alpha1/ProxmoxMachineTemplate': {'gvr': {'group': 'infrastructure.cluster.x-k8s.io', 'version': 'v1alpha1', 'resource': 'proxmoxmachinetemplates'}, 'namespaced': True},
        'cdi.kubevirt.io/v1beta1/DataVolume': {'gvr': {'group': 'cdi.kubevirt.io', 'version': 'v1beta1', 'resource': 'datavolumes'}, 'namespaced': True},
    }
    objects = [{'apiVersion': key.rsplit('/', 1)[0], 'kind': key.rsplit('/', 1)[1], 'metadata': {'name': 'scheme-only', 'namespace': 'scheme-only'}, 'spec': {}} for key in scheme]
    objects.append({'apiVersion': 'cdi.kubevirt.io/v1beta1', 'kind': 'DataVolume', 'metadata': {'name': 'talos-worker-ce4c980550dd2ab1b17bbf2b08801c7eb59418eafe8f279833297925d67c7515-v1.13.6', 'namespace': 'cozy-public'}, 'spec': {'storage': {'storageClassName': 'replicated', 'resources': {'requests': {'storage': '6Gi'}}}}, 'status': {'phase': 'Succeeded'}})
    tests.append({'it': case['name'], 'set': settings, 'kubernetesProvider': {'scheme': scheme, 'objects': objects}, 'asserts': [{'matchSnapshot': {}}]})
suite = {'suite': 'legacy effective render corpus', 'templates': ['templates/nodegroup.yaml', 'templates/talos-reconcile-job.yaml', 'templates/image-factory-ca.yaml', 'templates/pre-delete-unpin.yaml'], 'release': {'name': 'kubernetes-nodes-k-md0', 'namespace': 'tenant-test'}, 'tests': tests}
(chart / 'tests/legacy_test.yaml').write_text(json.dumps(suite))
subprocess.run(['helm', 'unittest', '--update-snapshot', str(chart)], check=True, stdout=sys.stderr)
raw = subprocess.check_output(['yq', '--output-format=json', '.', str(chart / 'tests/__snapshot__/legacy_test.yaml.snap')])
rendered = json.loads(raw)
digests = {name: hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest() for name, value in rendered.items()}
print(json.dumps(digests, indent=2, sort_keys=True))

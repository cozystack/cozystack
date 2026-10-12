"""Check effective permissions of the rendered provisioner service account."""
import json
import subprocess
import unittest
from pathlib import Path

CHART = Path(__file__).resolve().parents[1]


def render(namespace, settings):
    command = ["helm", "template", "test", str(CHART), "--namespace", namespace]
    for value in settings:
        command.extend(["--set", value])
    output = subprocess.run(command, check=True, capture_output=True, text=True).stdout
    parsed = subprocess.run(["yq", "-o=json", "-I=0", ".", "-"], input=output,
                            check=True, capture_output=True, text=True).stdout
    return [obj for line in parsed.splitlines() if (obj := json.loads(line))]


def permits(objects, namespace, service_account, verb, resource, target_namespace="", group=""):
    for binding in objects:
        kind = binding["kind"]
        if kind not in ("RoleBinding", "ClusterRoleBinding"):
            continue
        if kind == "RoleBinding" and binding["metadata"]["namespace"] != target_namespace:
            continue
        subject = {"kind": "ServiceAccount", "name": service_account, "namespace": namespace}
        if subject not in binding["subjects"]:
            continue
        ref = binding["roleRef"]
        for role in objects:
            if role["kind"] != ref["kind"] or role["metadata"]["name"] != ref["name"]:
                continue
            if ref["kind"] == "Role" and role["metadata"]["namespace"] != binding["metadata"]["namespace"]:
                continue
            for rule in role["rules"]:
                if all(value in rule.get(key, []) or "*" in rule.get(key, [])
                       for key, value in (("verbs", verb), ("resources", resource), ("apiGroups", group))):
                    return True
    return False


class LocalPVPermissions(unittest.TestCase):
    def test_lifecycle_and_namespace_boundary(self):
        for namespace, settings in (("cozy-local-storage", []), ("custom-storage", [
                "localpv-provisioner.fullnameOverride=custom-provisioner",
                "localpv-provisioner.serviceAccount.name=custom-account"])):
            with self.subTest(namespace=namespace):
                objects = render(namespace, settings)
                deployment = next(o for o in objects if o["kind"] == "Deployment")
                account = deployment["spec"]["template"]["spec"]["serviceAccountName"]
                allow = lambda *args: permits(objects, namespace, account, *args)
                for verb, resource, target, group in [
                    ("get", "nodes", "", ""), ("list", "nodes", "", ""),
                    ("list", "persistentvolumes", "", ""), ("watch", "persistentvolumes", "", ""),
                    ("create", "persistentvolumes", "", ""), ("delete", "persistentvolumes", "", ""),
                    ("patch", "persistentvolumes", "", ""),
                    ("list", "persistentvolumeclaims", "tenant-a", ""),
                    ("watch", "persistentvolumeclaims", "tenant-a", ""),
                    ("update", "persistentvolumeclaims", "tenant-a", ""),
                    ("get", "storageclasses", "", "storage.k8s.io"),
                    ("list", "storageclasses", "", "storage.k8s.io"),
                    ("watch", "storageclasses", "", "storage.k8s.io"),
                    ("create", "events", "tenant-a", ""), ("patch", "events", "tenant-a", ""),
                    ("get", "pods", namespace, ""), ("create", "pods", namespace, ""),
                    ("delete", "pods", namespace, ""),
                    ("get", "leases", namespace, "coordination.k8s.io"),
                    ("create", "leases", namespace, "coordination.k8s.io"),
                    ("update", "leases", namespace, "coordination.k8s.io"),
                    ("get", "configmaps", namespace, ""), ("create", "configmaps", namespace, ""),
                    ("patch", "configmaps", namespace, ""),
                ]:
                    self.assertTrue(allow(verb, resource, target, group), (verb, resource, target))
                for verb, resource, target, group in [
                    ("delete", "namespaces", "", ""), ("create", "namespaces", "", ""),
                    ("get", "secrets", namespace, ""), ("list", "secrets", "tenant-a", ""),
                    ("create", "pods", "tenant-a", ""), ("delete", "pods", "tenant-a", ""),
                    ("create", "pods", "kube-system", ""),
                    ("patch", "configmaps", "tenant-a", ""),
                    ("update", "leases", "tenant-a", "coordination.k8s.io"),
                    ("delete", "storageclasses", "", "storage.k8s.io"),
                    ("delete", "persistentvolumeclaims", "tenant-a", ""),
                    ("create", "customresourcedefinitions", "", "apiextensions.k8s.io"),
                    ("delete", "blockdevices", "", "openebs.io"),
                ]:
                    self.assertFalse(allow(verb, resource, target, group), (verb, resource, target))

    def test_external_rbac_is_not_granted(self):
        objects = render("custom-storage", ["localpv-provisioner.rbac.create=false"])
        self.assertFalse(any(o["kind"] in ("Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding")
                             for o in objects))


if __name__ == "__main__":
    unittest.main()

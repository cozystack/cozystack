import copy
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).resolve().parents[1] / "hack" / "piraeus-webhooks.sh"
CONFIG = "piraeus-operator-validating-webhook-configuration"
MARK = "linstor.cozystack.io/relaxed-webhooks"

# kubectl stand-in: serves the configuration and the webhook Service's
# EndpointSlices from a JSON state file, applies JSON patches to it the way
# the apiserver does (a failed test op rejects the whole patch), and logs
# every call.
FAKE_KUBECTL = r'''
import json, os, sys
state_path, log_path = os.environ["FAKE_STATE"], os.environ["FAKE_LOG"]
args = sys.argv[1:]
with open(log_path, "a") as log:
    log.write(json.dumps(args) + "\n")
with open(state_path) as f:
    state = json.load(f)

def opt(name):
    return args[args.index(name) + 1]

def pointer(path):
    return [part.replace("~1", "/").replace("~0", "~") for part in path.split("/")[1:]]

def walk(doc, parts):
    for part in parts:
        doc = doc[int(part)] if isinstance(doc, list) else doc[part]
    return doc

if args[:2] == ["get", "validatingwebhookconfiguration"]:
    if state["config"] is not None and args[2] == state["config"]["metadata"]["name"]:
        print(json.dumps(state["config"]))
    sys.exit(0)
if args[:2] == ["get", "endpointslices"]:
    service = opt("--namespace") + "/" + opt("--selector").split("=", 1)[1]
    endpoints = [{"addresses": ["10.0.0.1"], "conditions": {"ready": ready}}
                 for ready in state["endpoints"].get(service, [])]
    print(json.dumps({"items": [{"endpoints": endpoints}] if endpoints else []}))
    sys.exit(0)
if args[:2] == ["patch", "validatingwebhookconfiguration"]:
    assert opt("--type") == "json"
    doc = state["config"]
    if state.get("rewrite_before_patch"):
        doc["webhooks"].reverse()
    for op in json.loads(opt("--patch")):
        parts = pointer(op["path"])
        parent, key = walk(doc, parts[:-1]), parts[-1]
        if isinstance(parent, list):
            key = int(key)
        if op["op"] == "test":
            if parent[key] != op["value"]:
                sys.exit("test failed: " + op["path"])
        elif op["op"] in ("add", "replace"):
            parent[key] = op["value"]
        elif op["op"] == "remove":
            del parent[key]
    state["config"] = doc
    with open(state_path, "w") as f:
        json.dump(state, f)
    sys.exit(0)
sys.exit("unexpected kubectl call: %s" % args)
'''


def webhook(name, group, resource):
    # As packages/system/piraeus-operator/charts/piraeus renders them.
    return {
        "name": name,
        "admissionReviewVersions": ["v1"],
        "clientConfig": {"service": {"name": "piraeus-operator-webhook-service", "namespace": "cozy-linstor",
                                     "path": "/validate-" + resource}},
        "failurePolicy": "Fail",
        "rules": [{"apiGroups": [group], "apiVersions": ["v1"], "operations": ["CREATE", "UPDATE"],
                   "resources": [resource]}],
        "sideEffects": "None",
        "timeoutSeconds": 2,
    }


def configuration():
    return {
        "apiVersion": "admissionregistration.k8s.io/v1",
        "kind": "ValidatingWebhookConfiguration",
        "metadata": {"name": CONFIG,
                     "annotations": {"cert-manager.io/inject-ca-from": "cozy-linstor/piraeus-operator"}},
        "webhooks": [
            webhook("vlinstorcluster.kb.io", "piraeus.io", "linstorclusters"),
            webhook("vlinstornodeconnection.kb.io", "piraeus.io", "linstornodeconnections"),
            webhook("vlinstorsatellite.kb.io", "piraeus.io", "linstorsatellites"),
            webhook("vlinstorsatelliteconfiguration.kb.io", "piraeus.io", "linstorsatelliteconfigurations"),
            webhook("vstorageclass.kb.io", "storage.k8s.io", "storageclasses"),
        ],
    }


STOPPED = {}
RUNNING = {"cozy-linstor/piraeus-operator-webhook-service": [True]}
RELAXED = ["vlinstorcluster.kb.io", "vlinstorsatelliteconfiguration.kb.io"]


@unittest.skipUnless(shutil.which("jq"), "jq is required")
class PiraeusWebhooksTest(unittest.TestCase):
    def setUp(self):
        self.dir = Path(self.enterContext(tempfile.TemporaryDirectory()))
        kubectl = self.dir / "kubectl"
        kubectl.write_text("#!/bin/sh\nexec %s %s \"$@\"\n" % (sys.executable, self.dir / "fake.py"))
        kubectl.chmod(0o755)
        (self.dir / "fake.py").write_text(FAKE_KUBECTL)
        self.state = self.dir / "state.json"
        self.log = self.dir / "log"
        self.log.touch()

    def given(self, config, endpoints):
        self.state.write_text(json.dumps({"config": config, "endpoints": endpoints}))

    def run_script(self, phase):
        env = dict(os.environ, PATH="%s:%s" % (self.dir, os.environ["PATH"]), PIRAEUS_WEBHOOKS=CONFIG,
                   FAKE_STATE=str(self.state), FAKE_LOG=str(self.log))
        return subprocess.run(["sh", str(SCRIPT), phase], env=env, capture_output=True, text=True)

    def config(self):
        return json.loads(self.state.read_text())["config"]

    def policies(self):
        return {w["name"]: w["failurePolicy"] for w in self.config()["webhooks"]}

    def patches(self):
        return [json.loads(line) for line in self.log.read_text().splitlines() if json.loads(line)[0] == "patch"]

    def test_relax_ignores_only_the_kinds_the_chart_renders_when_unserved(self):
        self.given(configuration(), STOPPED)
        result = self.run_script("relax")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.policies(), {
            "vlinstorcluster.kb.io": "Ignore",
            "vlinstornodeconnection.kb.io": "Fail",
            "vlinstorsatellite.kb.io": "Fail",
            "vlinstorsatelliteconfiguration.kb.io": "Ignore",
            "vstorageclass.kb.io": "Fail",
        })
        self.assertEqual(self.config()["metadata"]["annotations"][MARK], ",".join(RELAXED))
        self.assertEqual(self.config()["metadata"]["annotations"]["cert-manager.io/inject-ca-from"],
                         "cozy-linstor/piraeus-operator")

    def test_relax_counts_not_ready_endpoints_as_unserved(self):
        self.given(configuration(), {"cozy-linstor/piraeus-operator-webhook-service": [False]})
        self.assertEqual(self.run_script("relax").returncode, 0)
        self.assertEqual(self.policies()["vlinstorcluster.kb.io"], "Ignore")

    def test_relax_leaves_served_webhooks_alone(self):
        self.given(configuration(), RUNNING)
        result = self.run_script("relax")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.patches(), [])
        self.assertEqual(self.config(), configuration())

    def test_restore_returns_the_configuration_as_it_was(self):
        self.given(configuration(), STOPPED)
        self.assertEqual(self.run_script("relax").returncode, 0)
        result = self.run_script("restore")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.config(), configuration())

    def test_restore_works_with_the_operator_still_stopped(self):
        # hold and adopt finish with the operator stopped; the release must
        # leave Fail behind all the same.
        self.given(configuration(), STOPPED)
        self.run_script("relax")
        self.assertEqual(self.run_script("restore").returncode, 0)
        self.assertNotIn("Ignore", self.policies().values())

    def test_relax_on_a_configuration_without_annotations(self):
        config = configuration()
        del config["metadata"]["annotations"]
        self.given(config, STOPPED)
        self.assertEqual(self.run_script("relax").returncode, 0)
        self.assertEqual(self.config()["metadata"]["annotations"], {MARK: ",".join(RELAXED)})
        self.assertEqual(self.run_script("restore").returncode, 0)
        self.assertEqual(self.config()["metadata"].get("annotations"), {})

    def test_retried_relax_keeps_the_record(self):
        # An upgrade that fails after the pre hook is retried; the second relax
        # finds the webhooks already relaxed and must not forget them.
        self.given(configuration(), STOPPED)
        self.run_script("relax")
        self.assertEqual(self.run_script("relax").returncode, 0)
        self.assertEqual(self.config()["metadata"]["annotations"][MARK], ",".join(RELAXED))
        self.run_script("restore")
        self.assertEqual(self.config(), configuration())

    def test_relax_restores_what_a_failed_upgrade_left_once_served_again(self):
        self.given(configuration(), STOPPED)
        self.run_script("relax")
        state = json.loads(self.state.read_text())
        state["endpoints"] = RUNNING
        self.state.write_text(json.dumps(state))
        self.assertEqual(self.run_script("relax").returncode, 0)
        self.assertEqual(self.config(), configuration())

    def test_restore_leaves_a_policy_set_by_hand(self):
        config = configuration()
        config["webhooks"][3]["failurePolicy"] = "Ignore"
        self.given(config, STOPPED)
        self.assertEqual(self.run_script("restore").returncode, 0)
        self.assertEqual(self.patches(), [])
        self.assertEqual(self.policies()["vlinstorsatelliteconfiguration.kb.io"], "Ignore")

    def test_restore_only_what_it_relaxed(self):
        config = configuration()
        config["webhooks"][3]["failurePolicy"] = "Ignore"
        self.given(config, STOPPED)
        self.run_script("relax")
        self.assertEqual(self.config()["metadata"]["annotations"][MARK], "vlinstorcluster.kb.io")
        self.run_script("restore")
        self.assertEqual(self.policies()["vlinstorcluster.kb.io"], "Fail")
        self.assertEqual(self.policies()["vlinstorsatelliteconfiguration.kb.io"], "Ignore")

    def test_patch_is_refused_when_the_webhooks_moved(self):
        # The index each op names is checked against the webhook's name, so a
        # configuration rewritten in between is not patched at the wrong entry.
        self.given(configuration(), STOPPED)
        self.run_script("relax")
        state = json.loads(self.state.read_text())
        before = copy.deepcopy(state["config"])
        state["rewrite_before_patch"] = True
        self.state.write_text(json.dumps(state))
        self.assertNotEqual(self.run_script("restore").returncode, 0)
        self.assertEqual(self.config(), before)

    def test_missing_configuration_is_not_an_error(self):
        self.given(None, STOPPED)
        for phase in ("relax", "restore"):
            with self.subTest(phase=phase):
                result = self.run_script(phase)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(self.patches(), [])

    def test_unknown_phase_fails(self):
        self.given(configuration(), STOPPED)
        self.assertEqual(self.run_script("loosen").returncode, 2)


if __name__ == "__main__":
    unittest.main()

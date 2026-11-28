import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).resolve().parents[1] / "hack" / "blockstor-controllerconfig.sh"
RESOURCE = "controllerconfigs.blockstor.org"
PATCH = {"spec": {"passphraseSecretRef": {"key": "MASTER_PASSPHRASE", "name": "linstor-passphrase"}}}
MANIFEST = """apiVersion: blockstor.org/v1alpha1
kind: ControllerConfig
metadata:
  name: default
spec:
  passphraseSecretRef:
    name: linstor-passphrase
    key: MASTER_PASSPHRASE
"""

# kubectl stand-in: serves ControllerConfig/default from a JSON state file,
# applies merge patches to it the way the apiserver does (RFC 7386), refuses
# a create of an object that exists, and logs every call. Any other call, a
# server-side apply among them, fails the script.
FAKE_KUBECTL = r'''
import json, os, sys
state_path, log_path = os.environ["FAKE_STATE"], os.environ["FAKE_LOG"]
args = sys.argv[1:]
with open(log_path, "a") as log:
    log.write(json.dumps(args) + "\n")
with open(state_path) as f:
    state = json.load(f)

def opt(name):
    for arg in args:
        if arg.startswith(name + "="):
            return arg.split("=", 1)[1]
    return args[args.index(name) + 1]

def merge(target, patch):
    if not isinstance(patch, dict):
        return patch
    target = dict(target) if isinstance(target, dict) else {}
    for key, value in patch.items():
        if value is None:
            target.pop(key, None)
        else:
            target[key] = merge(target.get(key), value)
    return target

def save():
    with open(state_path, "w") as f:
        json.dump(state, f)

resource = "controllerconfigs.blockstor.org"
if args[:3] == ["get", resource, "default"]:
    if state.get("get_error"):
        sys.exit(state["get_error"])
    assert "--ignore-not-found" in args
    if state["object"] is not None:
        print("controllerconfig.blockstor.org/default")
    sys.exit(0)
if args[:3] == ["patch", resource, "default"]:
    assert opt("--type") == "merge"
    if state["object"] is None:
        sys.exit("NotFound")
    state["object"] = merge(state["object"], json.loads(opt("--patch")))
    save()
    sys.exit(0)
if args[:1] == ["create"] and opt("-f") == "-":
    if state["object"] is not None:
        sys.exit("AlreadyExists")
    state["created"] = sys.stdin.read()
    state["object"] = {"created": True}
    save()
    sys.exit(0)
sys.exit("unexpected kubectl call: %s" % args)
'''

# ControllerConfig/default as a client-side `kubectl apply` of linstor-migrate's
# output leaves it: LINSTOR's cluster-wide DRBD options in extraProps, owned
# by kubectl-client-side-apply, which also owns the last-applied annotation.
# This is the shape a server-side apply of passphraseSecretRef alone empties.
MIGRATED = {
    "apiVersion": "blockstor.org/v1alpha1",
    "kind": "ControllerConfig",
    "metadata": {
        "name": "default",
        "annotations": {"kubectl.kubernetes.io/last-applied-configuration": "{}"},
        "managedFields": [{"manager": "kubectl-client-side-apply", "operation": "Update"}],
    },
    "spec": {
        "extraProps": {
            "DrbdOptions/Net/timeout": "100",
            "DrbdOptions/Net/verify-alg": "crc32c",
            "DrbdOptions/Resource/on-no-data-accessible": "suspend-io",
        },
        "drbdOptions": {"net": {"connect-int": "15"}},
        "passphraseSecretRef": {"name": "set-by-hand", "key": "OTHER"},
    },
}


class ControllerConfigHookTest(unittest.TestCase):
    def setUp(self):
        self.dir = Path(self.enterContext(tempfile.TemporaryDirectory()))
        kubectl = self.dir / "kubectl"
        kubectl.write_text("#!/bin/sh\nexec %s %s \"$@\"\n" % (sys.executable, self.dir / "fake.py"))
        kubectl.chmod(0o755)
        (self.dir / "fake.py").write_text(FAKE_KUBECTL)
        self.state = self.dir / "state.json"
        self.log = self.dir / "log"
        self.log.touch()

    def given(self, obj, **extra):
        self.state.write_text(json.dumps(dict(object=obj, **extra)))

    def run_script(self):
        env = dict(os.environ, PATH="%s:%s" % (self.dir, os.environ["PATH"]), PATCH=json.dumps(PATCH),
                   MANIFEST=MANIFEST, FAKE_STATE=str(self.state), FAKE_LOG=str(self.log))
        return subprocess.run(["sh", "-e", str(SCRIPT)], env=env, capture_output=True, text=True)

    def result(self):
        return json.loads(self.state.read_text())

    def calls(self):
        return [json.loads(line)[0] for line in self.log.read_text().splitlines()]

    def test_keeps_every_field_it_does_not_set(self):
        self.given(json.loads(json.dumps(MIGRATED)))
        run = self.run_script()
        self.assertEqual(run.returncode, 0, run.stderr)
        obj = self.result()["object"]
        self.assertEqual(obj["spec"]["extraProps"], MIGRATED["spec"]["extraProps"])
        self.assertEqual(obj["spec"]["drbdOptions"], MIGRATED["spec"]["drbdOptions"])
        self.assertEqual(obj["metadata"], MIGRATED["metadata"])
        self.assertEqual(self.calls(), ["get", "patch"])

    def test_wins_over_a_ref_set_by_hand(self):
        self.given(json.loads(json.dumps(MIGRATED)))
        run = self.run_script()
        self.assertEqual(run.returncode, 0, run.stderr)
        self.assertEqual(self.result()["object"]["spec"]["passphraseSecretRef"],
                         {"name": "linstor-passphrase", "key": "MASTER_PASSPHRASE"})

    def test_creates_a_missing_object(self):
        self.given(None)
        run = self.run_script()
        self.assertEqual(run.returncode, 0, run.stderr)
        self.assertEqual(self.result()["created"].rstrip("\n"), MANIFEST.rstrip("\n"))
        self.assertEqual(self.calls(), ["get", "create"])

    # The CRD may not be served yet right after a first install: the Job must
    # fail and be retried, not create blindly or report success.
    def test_fails_when_the_object_cannot_be_read(self):
        self.given(None, get_error="the server could not find the requested resource")
        run = self.run_script()
        self.assertNotEqual(run.returncode, 0)
        self.assertEqual(self.calls(), ["get"])


if __name__ == "__main__":
    unittest.main()

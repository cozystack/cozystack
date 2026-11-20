from pathlib import Path
import shutil
import subprocess
import unittest

CHART = Path(__file__).resolve().parents[1]

# Every template that renders a piraeus.io object, or something one of them
# references. While blockstorMigration is set the release must give piraeus
# nothing new: the runbook stops the operator, and once it runs again it has
# to find LINSTOR's shape and keep linstor-csi on LINSTOR until the value is
# cleared. helm-unittest asserts on one render at a time, so the comparison
# across renders lives here.
PIRAEUS_TEMPLATES = [
    "templates/cluster.yaml",
    "templates/satellites-cozy.yaml",
    "templates/satellites-plunger.yaml",
    "templates/satellites-reloader.yaml",
    "templates/satellites-talos.yaml",
    "templates/plunger/configmap-scripts.yaml",
    "templates/linstor-api-tls.yaml",
    "templates/linstor-internal-tls.yaml",
    "templates/podscrape.yaml",
]


def render(*values):
    args = ["helm", "template", "linstor", str(CHART), "--namespace", "cozy-linstor",
            "--api-versions", "operator.victoriametrics.com/v1beta1"]
    for value in values:
        args += ["--set", value]
    out = subprocess.run(args, check=True, capture_output=True, text=True).stdout
    sources = [f"# Source: cozy-linstor/{template}" for template in PIRAEUS_TEMPLATES]
    docs = [doc.removeprefix("---\n") for doc in out.split("\n---\n")]
    return [doc for doc in docs if doc.split("\n", 1)[0] in sources]


@unittest.skipUnless(shutil.which("helm"), "helm is required")
class MigrationLeavesPiraeusAlone(unittest.TestCase):
    def test_hold_and_adopt_render_piraeus_as_linstor(self):
        linstor = render("backend=linstor")
        for step in ("hold", "adopt"):
            with self.subTest(step=step):
                self.assertEqual(render("backend=blockstor", f"blockstorMigration={step}"), linstor)

    def test_completed_migration_changes_piraeus(self):
        self.assertNotEqual(render("backend=blockstor"), render("backend=linstor"))


if __name__ == "__main__":
    unittest.main()

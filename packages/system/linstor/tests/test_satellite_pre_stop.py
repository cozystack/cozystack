import copy
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import unittest
from unittest.mock import patch


REAL_RUN = subprocess.run
SCRIPT = Path(__file__).resolve().parents[1] / "hack" / "satellite-pre-stop.py"
SPEC = importlib.util.spec_from_file_location("satellite_pre_stop", SCRIPT)
hook = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(hook)


def resource(name="r1", role="Secondary", opened=False):
    return {"name": name, "role": role, "devices": [{"volume": 0, "open": opened}]}


class SatellitePreStopTest(unittest.TestCase):
    def setUp(self):
        self.run = self.enterContext(patch.object(hook.subprocess, "run"))
        self.enterContext(patch.object(hook.sys, "argv", ["hook", "--execute"]))
        self.clock = self.enterContext(patch.object(hook.time, "monotonic", return_value=0))
        self.events = self.enterContext(patch.object(hook, "log"))

    def status(self, *resources):
        return subprocess.CompletedProcess([], 0, json.dumps(resources), "")

    def test_rechecks_unused_secondary_before_down(self):
        self.run.side_effect = [self.status(resource()), self.status(resource()), subprocess.CompletedProcess([], 0)]
        hook.main()
        self.assertEqual([call.args[0] for call in self.run.call_args_list], [
            ["/usr/sbin/drbdsetup", "status", "--json"],
            ["/usr/sbin/drbdsetup", "status", "r1", "--json"],
            ["/usr/sbin/drbdsetup", "down", "r1"],
        ])
        self.assertEqual(self.run.call_args.kwargs["timeout"], 10)
        self.assertEqual([call.args[0] for call in self.events.call_args_list], ["down-start", "down-finished", "finished"])

    def test_keeps_primary_even_when_not_open_and_keeps_open_secondary(self):
        self.run.return_value = self.status(resource(role="Primary"), resource("r2", opened=True))
        hook.main()
        self.assertEqual(self.run.call_count, 1)
        self.assertEqual([call.args[0] for call in self.events.call_args_list], ["skip-in-use", "skip-in-use", "finished"])

    def test_handles_multiple_unused_resources_without_a_fixed_inventory(self):
        first, second = resource("pvc-a"), resource("volume.2_test")
        self.run.side_effect = [self.status(first, second), self.status(first), subprocess.CompletedProcess([], 0),
                                self.status(second), subprocess.CompletedProcess([], 0)]
        hook.main()
        down = [call.args[0] for call in self.run.call_args_list if call.args[0][1] == "down"]
        self.assertEqual(down, [["/usr/sbin/drbdsetup", "down", "pvc-a"], ["/usr/sbin/drbdsetup", "down", "volume.2_test"]])

    def test_keeps_resource_when_any_volume_is_open(self):
        current = resource()
        current["devices"].append({"volume": 1, "open": True})
        self.run.return_value = self.status(current)
        hook.main()
        self.assertEqual(self.run.call_count, 1)

    def test_state_change_between_reads_prevents_down(self):
        for changed in [resource(role="Primary"), resource(opened=True)]:
            with self.subTest(changed=changed):
                self.run.reset_mock()
                self.run.side_effect = [self.status(resource()), self.status(changed)]
                hook.main()
                self.assertEqual(self.run.call_count, 2)
                self.assertEqual(self.events.call_args_list[-2].args[0], "skip-state-changed")

    def test_recheck_must_identify_exact_resource(self):
        for reply in [self.status(), self.status(resource("other")), self.status(resource(), resource("other"))]:
            with self.subTest(reply=reply):
                self.run.reset_mock()
                self.run.side_effect = [self.status(resource()), reply]
                with self.assertRaisesRegex(ValueError, "resource-read-mismatch"):
                    hook.main()
                self.assertEqual(self.run.call_count, 2)

    def test_ambiguous_status_prevents_all_down_actions(self):
        invalid = [None, {}, [None], [resource(), resource()], [resource(role="Unknown")]]
        for field, value in [("name", "--all"), ("name", "r1;id"), ("devices", []), ("devices", None)]:
            current = resource()
            current[field] = value
            invalid.append([current])
        for field, value in [("open", None), ("open", "false"), ("open", 0), ("volume", -1), ("volume", True)]:
            current = resource()
            current["devices"][0][field] = value
            invalid.append([current])
        current = resource()
        current["devices"][0].pop("open")
        invalid.append([current])
        current = resource()
        current["devices"].append(copy.deepcopy(current["devices"][0]))
        invalid.append([current])
        for value in invalid:
            with self.subTest(value=value):
                self.run.reset_mock()
                self.run.return_value = subprocess.CompletedProcess([], 0, json.dumps(value), "")
                with self.assertRaises(ValueError):
                    hook.main()
                self.assertEqual(self.run.call_count, 1)

    def test_status_error_or_timeout_aborts(self):
        self.run.return_value = subprocess.CompletedProcess([], 1, "", "failed")
        with self.assertRaisesRegex(RuntimeError, "status-command-failed"):
            hook.main()
        self.assertEqual(self.run.call_count, 1)
        self.run.reset_mock()
        self.run.side_effect = subprocess.TimeoutExpired("status", 4)
        with self.assertRaises(subprocess.TimeoutExpired):
            hook.main()
        self.assertEqual(self.run.call_count, 1)

    def test_down_failure_or_timeout_stops_before_next_resource(self):
        for failure in [subprocess.CompletedProcess([], 1), subprocess.TimeoutExpired("down", 10)]:
            with self.subTest(failure=failure):
                self.run.reset_mock()
                self.run.side_effect = [self.status(resource(), resource("r2")), self.status(resource()), failure]
                with self.assertRaises((RuntimeError, subprocess.TimeoutExpired)):
                    hook.main()
                self.assertEqual(self.run.call_count, 3)

    def test_abort_logs_bounded_status_diagnostics_without_down(self):
        stderr = "discarded-prefix" + "x" * 2048 + "permission denied"
        self.run.return_value = subprocess.CompletedProcess([], 12, "", stderr)
        with patch.object(hook, "connect_container_stdout"):
            self.assertEqual(hook.run_hook(), 1)
        self.assertEqual(self.run.call_count, 1)
        self.assertEqual(self.run.call_args.kwargs["errors"], "replace")
        self.events.assert_called_once_with(
            "abort-no-further-actions", error_type="RuntimeError",
            error="status-command-failed (exit=12): " + stderr[-2048:],
        )

    def test_abort_logs_down_diagnostics_and_stops_before_next_resource(self):
        stderr = "discarded-prefix" + "x" * 2048 + "device busy"
        self.run.side_effect = [self.status(resource(), resource("r2")), self.status(resource()),
                                subprocess.CompletedProcess([], 17, stderr=stderr)]
        with patch.object(hook, "connect_container_stdout"):
            self.assertEqual(hook.run_hook(), 1)
        self.assertEqual(self.run.call_count, 3)
        self.assertEqual(self.run.call_args.kwargs["stderr"], subprocess.PIPE)
        self.assertTrue(self.run.call_args.kwargs["text"])
        self.assertEqual(self.run.call_args.kwargs["errors"], "replace")
        self.events.assert_called_with(
            "abort-no-further-actions", error_type="RuntimeError",
            error="down-command-failed (exit=17): " + stderr[-2048:],
        )

    def test_abort_logs_partial_timeout_stderr_for_status_and_down(self):
        stderr = b"discarded-prefix" + b"x" * 2048 + b"device busy\xff"
        for command in ["status", "down"]:
            with self.subTest(command=command):
                self.run.reset_mock()
                self.events.reset_mock()
                failure = subprocess.TimeoutExpired(command, 4, stderr=stderr)
                responses = [failure] if command == "status" else [
                    self.status(resource(), resource("r2")), self.status(resource()), failure,
                ]
                self.run.side_effect = responses
                with patch.object(hook, "connect_container_stdout"):
                    self.assertEqual(hook.run_hook(), 1)
                self.assertEqual(self.run.call_count, len(responses))
                self.events.assert_called_with(
                    "abort-no-further-actions", error_type="TimeoutExpired",
                    error=str(failure), stderr=stderr.decode("utf-8", errors="replace")[-2048:],
                )

    def test_deadline_prevents_further_down_and_bounds_command_timeout(self):
        self.clock.side_effect = [0, 60]
        self.run.side_effect = [self.status(resource()), self.status(resource())]
        with self.assertRaisesRegex(RuntimeError, "hook-deadline"):
            hook.main()
        self.assertEqual(self.run.call_count, 1)
        self.clock.side_effect = [0, 57, 58]
        self.run.side_effect = [self.status(resource()), self.status(resource()), subprocess.CompletedProcess([], 0)]
        hook.main()
        self.assertEqual(self.run.call_args_list[-2].kwargs["timeout"], 3)
        self.assertEqual(self.run.call_args.kwargs["timeout"], 2)

    def test_deadline_stops_rechecks_when_resources_keep_becoming_busy(self):
        self.clock.side_effect = [0, 1, 60]
        self.run.side_effect = [self.status(resource(), resource("r2")), self.status(resource(opened=True))]
        with self.assertRaisesRegex(RuntimeError, "hook-deadline"):
            hook.main()
        self.assertEqual(self.run.call_count, 2)

    def test_default_dry_run_never_downs(self):
        hook.sys.argv = ["hook"]
        self.run.return_value = self.status(resource())
        hook.main()
        self.assertEqual(self.run.call_count, 2)
        self.assertEqual(self.events.call_args_list[-2].args[0], "would-down")

    def test_unknown_argument_prevents_even_status(self):
        hook.sys.argv = ["hook", "--force"]
        with self.assertRaisesRegex(ValueError, "unsupported-arguments"):
            hook.main()
        self.run.assert_not_called()

    def test_chart_command_form_passes_execute_as_the_only_argument(self):
        # The chart renders `python3 -c <script> --execute`; CPython sets
        # sys.argv[0] to "-c" for that form, so main() receives ["--execute"].
        probe = REAL_RUN([sys.executable, "-c", "import sys; print(sys.argv)", "--execute"],
                         capture_output=True, text=True, check=True)
        self.assertEqual(probe.stdout.strip(), "['-c', '--execute']")
        hook.sys.argv = ["-c", "--execute"]
        self.run.return_value = self.status()
        hook.main()
        self.events.assert_called_once_with("finished", execute=True, resources=0)

    def test_script_name_placeholder_before_execute_is_rejected(self):
        hook.sys.argv = ["-c", "hook", "--execute"]
        with self.assertRaisesRegex(ValueError, "unsupported-arguments"):
            hook.main()
        self.run.assert_not_called()

    def test_empty_resource_list_is_a_noop(self):
        self.run.return_value = self.status()
        hook.main()
        self.assertEqual(self.run.call_count, 1)
        self.events.assert_called_once_with("finished", execute=True, resources=0)

    def test_logs_to_java_in_same_container_not_shared_pid_one(self):
        files = {"/proc/self/cgroup": "satellite", "/proc/1/cgroup": "sandbox", "/proc/1/comm": "pause",
                 "/proc/2/cgroup": "other", "/proc/2/comm": "java", "/proc/3/cgroup": "satellite", "/proc/3/comm": "java"}
        def read(path):
            if path not in files:
                raise FileNotFoundError(path)
            return io.StringIO(files[path])
        with patch("builtins.open", side_effect=read), patch.object(hook.os, "listdir", return_value=["1", "2", "3", "4", "self"]), \
                patch.object(hook.os, "open", return_value=100) as fd, patch.object(hook.os, "dup2") as dup, patch.object(hook.os, "close") as close:
            hook.connect_container_stdout()
            self.assertEqual(fd.call_args.args[0], "/proc/3/fd/1")
            self.assertEqual([call.args for call in dup.call_args_list], [(100, 1), (100, 2)])
            close.assert_called_once_with(100)


if __name__ == "__main__":
    unittest.main()

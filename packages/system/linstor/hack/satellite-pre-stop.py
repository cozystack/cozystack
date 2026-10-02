import json
import os
import re
import subprocess
import sys
import time


def stderr_tail(value):
    """Keep the last 2048 characters, including diagnostics from timeouts."""
    if isinstance(value, bytes):
        value = value.decode("utf-8", errors="replace")
    return (value or "")[-2048:]


def connect_container_stdout():
    # Piraeus shares the pod PID namespace, so /proc/1 belongs to the sandbox.
    with open("/proc/self/cgroup") as stream:
        cgroup = stream.read()
    candidates = []
    for pid in os.listdir("/proc"):
        if not pid.isdigit():
            continue
        try:
            with open("/proc/" + pid + "/cgroup") as stream:
                same_container = stream.read() == cgroup
            with open("/proc/" + pid + "/comm") as stream:
                is_java = stream.read().strip() == "java"
            if same_container and is_java:
                candidates.append(pid)
        except OSError:
            continue
    if len(candidates) != 1:
        raise RuntimeError("unique-satellite-java-required")
    fd = os.open("/proc/" + candidates[0] + "/fd/1", os.O_WRONLY | os.O_APPEND)
    try:
        os.dup2(fd, 1)
        os.dup2(fd, 2)
    finally:
        os.close(fd)


def log(event, **fields):
    print(json.dumps(dict(event=event, **fields), sort_keys=True), flush=True)


def parse_status(text):
    data = json.loads(text)
    if type(data) is not list:
        raise ValueError("status-list-required")
    result = {}
    for resource in data:
        if type(resource) is not dict:
            raise ValueError("resource-object-required")
        name = resource.get("name")
        if type(name) is not str or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,254}", name):
            raise ValueError("invalid-resource-name")
        if name in result or resource.get("role") not in ("Primary", "Secondary"):
            raise ValueError("duplicate-name-or-unknown-role")
        devices = resource.get("devices")
        if type(devices) is not list or not devices:
            raise ValueError("devices-required")
        volumes = set()
        for device in devices:
            if type(device) is not dict or type(device.get("open")) is not bool:
                raise ValueError("explicit-open-boolean-required")
            volume = device.get("volume")
            if type(volume) is not int or volume < 0 or volume in volumes:
                raise ValueError("invalid-volume")
            volumes.add(volume)
        result[name] = resource
    return result


def eligible(resource):
    # A pod restart must leave mounted volumes and their Primary role intact.
    return resource["role"] == "Secondary" and all(device["open"] is False for device in resource["devices"])


def read_status(name=None, timeout=4):
    args = ["/usr/sbin/drbdsetup", "status"]
    if name is not None:
        args.append(name)
    args.append("--json")
    result = subprocess.run(args, capture_output=True, text=True, errors="replace", timeout=timeout)
    if result.returncode != 0:
        raise RuntimeError("status-command-failed (exit=%s): %s" % (result.returncode, stderr_tail(result.stderr)))
    return parse_status(result.stdout)


def main():
    if sys.argv[1:] not in ([], ["--execute"]):
        raise ValueError("unsupported-arguments")
    execute = sys.argv[1:] == ["--execute"]
    deadline = time.monotonic() + 60
    initial = read_status()
    for name, resource in initial.items():
        if not eligible(resource):
            log("skip-in-use", resource=name, role=resource["role"])
            continue
        remaining = deadline - time.monotonic()
        if remaining < 1:
            raise RuntimeError("hook-deadline")
        current = read_status(name, timeout=min(4, remaining))
        if set(current) != {name}:
            raise ValueError("resource-read-mismatch")
        if not eligible(current[name]):
            log("skip-state-changed", resource=name)
            continue
        if not execute:
            log("would-down", resource=name)
            continue
        remaining = deadline - time.monotonic()
        if remaining < 1:
            raise RuntimeError("hook-deadline")
        log("down-start", resource=name)
        result = subprocess.run(
            ["/usr/sbin/drbdsetup", "down", name],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
            text=True,
            errors="replace",
            timeout=min(10, remaining),
        )
        if result.returncode != 0:
            raise RuntimeError("down-command-failed (exit=%s): %s" % (result.returncode, stderr_tail(result.stderr)))
        log("down-finished", resource=name)
    log("finished", execute=execute, resources=len(initial))


def run_hook():
    try:
        connect_container_stdout()
        main()
    except Exception as error:
        details = {"error_type": type(error).__name__, "error": str(error)}
        if isinstance(error, subprocess.TimeoutExpired):
            details["stderr"] = stderr_tail(error.stderr)
        log("abort-no-further-actions", **details)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(run_hook())

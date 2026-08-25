#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Cozystack E2E provisioning — CONTAINER lane
#
# The counterpart to hack/e2e-prepare-cluster.bats. That file boots three QEMU
# guests; this one takes over after hack/e2e-container-up.sh has started three
# Talos containers on the runner's docker daemon and attached this sandbox to
# their network. Everything from bootstrap onward is deliberately the same shape
# as the QEMU lane so the two stay comparable, and both hand off an identical
# cluster to hack/e2e-install-cozystack.bats.
#
# Why the split: containers cannot nest inside the sandbox the way QEMU guests
# do, so the nodes must be siblings on the host daemon for hack/e2e-compose.yaml's
# /dev and /run/udev binds to resolve against the real host devtmpfs. Only the
# host-side half needs a docker socket; this half needs only the network.
#
# Addressing, CPU and memory match the QEMU lane exactly, so the MetalLB
# 192.168.123.200-250 pool and hack/e2e-post-install-prep.sh are unchanged.
# The one deliberate difference is the API endpoint: container mode has no ARP
# VIP, so .11 stands in for the QEMU lane's 192.168.123.10.
# -----------------------------------------------------------------------------

@test "Talos API is reachable on all three container nodes" {
  # e2e-container-up.sh has already started them; this is the handoff check, and
  # it is a wait rather than a probe because container start is asynchronous.
  if ! timeout 180 sh -ec 'until nc -nz 192.168.123.11 50000 && nc -nz 192.168.123.12 50000 && nc -nz 192.168.123.13 50000; do sleep 1; done'; then
    echo "Talos API never came up on 192.168.123.11-13:50000." >&2
    echo "The sandbox reaches the nodes over the compose network it was attached to;" >&2
    echo "if this times out, check that hack/e2e-container-up.sh ran and that the" >&2
    echo "sandbox is on the cozy-e2e_talos network." >&2
    exit 1
  fi
}

@test "Host kernel modules are usable from inside the nodes" {
  # THE trap of this lane. machine.kernel.modules is a silent no-op in container
  # mode -- kernel_module_spec.go returns early on ModeContainer with no error
  # and no event -- so a lane that carried the QEMU patch over would look like it
  # worked and fail much later as a CNI or storage error. e2e-container-up.sh
  # loads these on the host; this asserts the nodes actually see them, which is
  # the property that matters and is not implied by the host check alone.
  for mod in openvswitch zfs; do
    if ! talosctl read /proc/modules -n 192.168.123.11 -e 192.168.123.11 | grep -q "^${mod} "; then
      echo "Kernel module '${mod}' is not visible from srv1." >&2
      echo "Container nodes share the host kernel and Talos cannot load modules in" >&2
      echo "container mode, so this must be loaded on the host before they start." >&2
      exit 1
    fi
  done
}

@test "/dev/kvm is present inside the nodes" {
  # KubeVirt's device plugin needs it, and its presence is what makes the tenant
  # workers run under real KVM rather than TCG emulation.
  if ! talosctl ls /dev/kvm -n 192.168.123.11 -e 192.168.123.11 >/dev/null 2>&1; then
    echo "/dev/kvm is missing inside srv1 — KubeVirt cannot advertise devices.kubevirt.io/kvm." >&2
    echo "hack/e2e-compose.yaml binds the host /dev; check the host exposes /dev/kvm." >&2
    exit 1
  fi
}

@test "Bootstrap Talos cluster" {
  # Retry for up to 120s: talosctl bootstrap refuses with "time is not in sync
  # yet" until NTP converges on a freshly started node. This is the same
  # allowance the QEMU lane makes, and it is a genuine infrastructure wait, not
  # a retry around product logic.
  #
  # If it never converges, the usual cause is that the nodes cannot reach the
  # internet at all rather than slow NTP -- most often because docker sets
  # `-P FORWARD DROP` and the compose bridge's traffic is being dropped.
  if ! timeout 120 sh -ec 'until talosctl bootstrap -n 192.168.123.11 -e 192.168.123.11; do sleep 2; done'; then
    echo "talosctl bootstrap never succeeded." >&2
    echo "If the error was 'time is not in sync yet', the nodes likely have no egress:" >&2
    echo "check 'iptables -S FORWARD' on the host for a DROP policy over the compose bridge." >&2
    talosctl dmesg -n 192.168.123.11 -e 192.168.123.11 || true
    exit 1
  fi
}

@test "Wait until etcd is healthy" {
  # Same 5m budget and the same reason as the QEMU lane: a 3-node etcd converges
  # through Talos's serialized learner promotion, so the third member is only
  # promoted once the second has caught up.
  if ! timeout 300 sh -ec 'until talosctl etcd members -n 192.168.123.11,192.168.123.12,192.168.123.13 -e 192.168.123.11 >/dev/null 2>&1; do sleep 1; done'; then
    talosctl dmesg -n 192.168.123.11,192.168.123.12,192.168.123.13 -e 192.168.123.11 || true
    exit 1
  fi
  timeout 60 sh -ec 'while talosctl etcd members -n 192.168.123.11,192.168.123.12,192.168.123.13 -e 192.168.123.11 2>&1 | grep -q "rpc error"; do sleep 1; done'
}

@test "Retrieve kubeconfig and register all three nodes" {
  rm -f kubeconfig
  # No ARP VIP in container mode, so the endpoint is a node IP rather than the
  # QEMU lane's 192.168.123.10.
  talosctl kubeconfig kubeconfig -e 192.168.123.11 -n 192.168.123.11

  if ! timeout 120 sh -ec 'until [ $(kubectl get node --no-headers | wc -l) -eq 3 ]; do sleep 1; done'; then
    echo "Fewer than three nodes registered:" >&2
    kubectl get nodes -o wide >&2 || true
    exit 1
  fi
}

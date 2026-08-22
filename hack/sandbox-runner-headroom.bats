#!/usr/bin/env bats
# Asserts that the e2e sandbox never fills a CI runner to its nominal capacity:
# every workflow job that boots the sandbox must run on a runner class with
# strictly more vCPUs and strictly more memory than the sandbox's QEMU guests
# demand.
#
# The sandbox boots a fixed set of QEMU management nodes, each with its own
# `-smp` and `-m`, and those resources are committed for the whole run. The
# host needs cycles and memory for its own work on top of that, so a 1:1
# commit leaves nothing to give it. That is an arithmetic statement about the
# configuration, and it is the entire basis for this check.
#
# Nothing enforced that pairing before this file: the guest demand lives in a
# bats file and the runner classes live in four workflows, so raising `-smp`
# or adding a lane on a smaller runner drifted them apart silently. That is how
# one lane came to sit on a 32-vCPU class while another ran the same sandbox on
# a 24-vCPU one.
#
# The scope is that drift and nothing more, and the limit is worth stating
# because the check is easy to over-read. It compares numbers. It attributes no
# red run to a runner class, predicts nothing about how fast a lane runs, and a
# green result means only that a lane is not oversubscribed on paper. Whether a
# lane is fast enough also depends on what else shares the host, which none of
# these files describe. Memory is compared because the same arithmetic applies
# to it and the numbers are already in the strings being read, not because a
# class that violates it exists today.
#
# Both sides are derived, never listed. The guest demand is read out of
# hack/e2e-prepare-cluster.bats by multiplying each qemu launch's `-smp` and
# `-m` by the iteration count of the `for` loop it sits in, so moving any of
# those moves this check with it. The job list is read out of .github/workflows
# by finding the jobs that invoke the `prepare-env` / `prepare-cluster` make
# targets, so a fifth lane written as an ordinary job is covered the day it is
# added rather than the day someone remembers to extend a literal list here.
#
# The floor of that derivation is the workflow file itself, and it is worth
# naming rather than leaving as an implied promise of total coverage. A lane
# that reached the sandbox through a composite action would carry the make
# target inside the action and only `runs-on` in the workflow, so neither the
# marker sweep nor the coverage cross-check would see it. Nothing in the tree
# is written that way today. A workflow reached through `workflow_call` is a
# different case and is covered, because the called file carries both its own
# `runs-on` and its own marker. Covering a composite action means teaching both
# legs to follow `uses:`, which is a larger change than widening a pattern.
#
# A derivation that cannot read its input has two ways out, and only one of
# them is safe. Guessing produces a number smaller than the truth, which passes
# a runner that cannot hold the sandbox; stopping produces a red naming the
# line. This one stops. The guest-side shapes in the list below are pinned by
# the fixture tests at the end of this file; the last bullet is a runner-side
# refusal, which only the live tree exercises.
#
#   - a line naming qemu-system-x86_64 with no literal `-smp <n>` or no literal
#     `-m <n>`, which covers `-smp cpus=8` and `-m size=24576` as well as an
#     absent flag;
#   - such a line outside any @test block, where no loop count applies to it;
#   - a @test block still open at end of file, whose contents were never
#     accounted for;
#   - a `for` opener the strict pattern does not match, counted separately from
#     the ones it does so that `do` on the following line is refused instead of
#     silently multiplying by one;
#   - a `for` list holding anything but literal words, so `$(seq 1 3)` is
#     refused rather than counted as three whitespace tokens by coincidence;
#   - a `while` or `until` loop in the same @test block as a qemu launch, since
#     its iteration count is not a static property at all;
#   - more than one qemu launch or more than one `for` loop in a single @test
#     block;
#   - a sandbox job whose `runs-on` does not carry an `oracle-vm-<n>cpu-<m>gb`
#     class.
#
# Several of those are scoped to the @test block rather than to real nesting,
# and say so in their messages, because this file refuses rather than parses
# shell. A poll loop added beside a qemu launch trips the while/until refusal
# without enclosing anything; moving it into its own @test block is the answer,
# and the message says that rather than leaving the reader to derive it. The
# loop multiplier is block-scoped for the same reason: a strict `for` anywhere
# in the block multiplies the launch whether or not it encloses it. That
# over-counts rather than under-counts, so the worst it yields is a false red,
# and the one-loop-per-block refusal bounds how far it can go.
#
# The qemu match is deliberately any line naming `qemu-system-x86_64`, which is
# wider than a launch. Narrowing it to lines that begin with the binary would
# skip a launch written as `sudo qemu-system-x86_64` or with a leading `env`,
# and a skipped launch lowers the demand this compares, which is the one
# direction that must never happen quietly. So the width stays and a line that
# names the binary without a literal `-smp`/`-m` is refused by name.
#
# Unit note for the memory leg: qemu's `-m` is MiB, and the runner class names
# its memory in `gb`. This reads that `gb` as GiB, which is the permissive
# direction, not the safe one: if the vendor means GB, the runner holds less
# than assumed and this leg lets through a class it should not. It is written
# that way because the inequality it asserts holds under either reading for the
# classes in .github/actionlint.yaml, so the looser unit costs nothing today.
#
# The coverage cross-check exists for the other rot direction. Matching jobs on
# a make-target name says nothing when the name changes or the YAML is
# reindented: the parser simply finds fewer jobs and the survivors still pass.
# So every workflow file that drives the sandbox make targets at all must yield
# at least one matched job, which turns a half-broken parser red instead of
# quietly narrowing what is checked.
#
# Harness note: the CI path is hack/cozytest.sh, NOT real bats. There is no
# `run`, `$status`, `$output`, `skip`, or setup()/teardown(); each test runs as
# a shell function under `set -eu -x`, so a non-zero exit is the failure. A
# `!`-negated command never trips errexit, so every assertion below is written
# as an explicit `if ... return 1`. A top-level helper function would have
# `return 0` injected before its closing brace by the runner's awk, so nothing
# is factored into one. The same awk pass injects that line before any bare `}`
# at column 0 and reads any line starting with `@test ` as a block header, so
# the fixtures below are built with `printf` rather than heredocs: a heredoc
# holding either shape would be rewritten as if it were code. Paths are
# repo-root-relative.
#
# Run with: hack/cozytest.sh hack/sandbox-runner-headroom.bats

# The guest-side derivation, held in one place so the check that runs against
# the live tree and the fixtures that pin its refusals cannot drift apart.
# Prints the vCPU total then the MiB total on success, one ERROR line
# otherwise. `exit` inside an awk rule still runs END, so the error path raises
# a flag END reads first: stdout is the only channel that survives to a caller.
GUEST_DEMAND_AWK='
  /^@test / {
    if (inblk) { print "ERROR unclosed @test block above line " NR; failed = 1; exit 1 }
    inblk = 1; nfor = 0; nforraw = 0; nloop = 0; forcount = 0
    nqemu = 0; smp = ""; mem = ""; baditem = ""
    next
  }
  inblk && /^}/ {
    if (nqemu > 0) {
      blk = " in the @test block ending at line " NR
      if (nqemu > 1) { print "ERROR " nqemu " lines naming qemu-system-x86_64" blk; failed = 1; exit 1 }
      if (smp == "") { print "ERROR line naming qemu-system-x86_64 with no literal -smp <n>" blk; failed = 1; exit 1 }
      if (mem == "") { print "ERROR line naming qemu-system-x86_64 with no literal -m <n>" blk; failed = 1; exit 1 }
      if (nloop > 0) { print "ERROR while/until loop in the same @test block as a qemu launch" blk; failed = 1; exit 1 }
      if (nforraw > nfor) { print "ERROR for opener the strict pattern could not read" blk; failed = 1; exit 1 }
      if (nfor > 1) { print "ERROR " nfor " for-loops" blk; failed = 1; exit 1 }
      if (baditem != "") { print "ERROR non-literal item \"" baditem "\" in the for list" blk; failed = 1; exit 1 }
      mult = (nfor == 1 ? forcount : 1)
      total_cpu += smp * mult
      total_mem += mem * mult
      launches += 1
    }
    inblk = 0
    next
  }
  !inblk && /qemu-system-x86_64/ {
    print "ERROR line naming qemu-system-x86_64 outside any @test block at line " NR
    failed = 1
    exit 1
  }
  inblk {
    line = $0
    if (line ~ /^[[:space:]]*(while|until)[[:space:]]/) nloop += 1
    if (line ~ /^[[:space:]]*for[[:space:]]/) {
      nforraw += 1
      if (line ~ /^[[:space:]]*for[[:space:]]+[A-Za-z_][A-Za-z0-9_]*[[:space:]]+in[[:space:]]+[^;]+;[[:space:]]*do[[:space:]]*$/) {
        sub(/^[[:space:]]*for[[:space:]]+[A-Za-z_][A-Za-z0-9_]*[[:space:]]+in[[:space:]]+/, "", line)
        sub(/;[[:space:]]*do[[:space:]]*$/, "", line)
        sub(/^[[:space:]]+/, "", line)
        sub(/[[:space:]]+$/, "", line)
        nfor += 1
        forcount = split(line, items, /[[:space:]]+/)
        for (k = 1; k <= forcount; k++) {
          if (items[k] !~ /^[A-Za-z0-9_.-]+$/) baditem = items[k]
        }
      }
      line = $0
    }
    if (line ~ /qemu-system-x86_64/) {
      nqemu += 1
      if (match(line, /-smp[[:space:]]+[0-9]+([[:space:]]|$)/)) {
        smp = substr(line, RSTART, RLENGTH)
        sub(/-smp[[:space:]]+/, "", smp)
        sub(/[^0-9]/, "", smp)
      }
      if (match(line, /-m[[:space:]]+[0-9]+([[:space:]]|$)/)) {
        mem = substr(line, RSTART, RLENGTH)
        sub(/-m[[:space:]]+/, "", mem)
        sub(/[^0-9]/, "", mem)
      }
    }
  }
  END {
    if (failed) exit 1
    if (inblk) { print "ERROR @test block still open at end of file"; exit 1 }
    if (launches == 0) { print "ERROR no qemu-system-x86_64 launch found"; exit 1 }
    print total_cpu
    print total_mem
  }
'

@test "every workflow job that boots the e2e sandbox leaves spare host capacity" {
  sandbox_file=hack/e2e-prepare-cluster.bats
  workflow_dir=.github/workflows

  # `|| true` because the assignment inherits awk's status, and under errexit a
  # refusal would kill the test right here, before the branch below can print
  # the sentence that says which shapes the parser needs.
  guest=$(awk "$GUEST_DEMAND_AWK" "$sandbox_file") || true

  guest_vcpus=$(printf '%s\n' "$guest" | sed -n 1p)
  guest_mib=$(printf '%s\n' "$guest" | sed -n 2p)
  for value in "$guest_vcpus" "$guest_mib"; do
    case "$value" in
      ""|*[!0-9]*)
        echo "could not read the sandbox guest demand from $sandbox_file:" >&2
        printf '%s\n' "$guest" >&2
        echo "every qemu-system-x86_64 launch must carry a literal '-smp <n>' and '-m <n>', sit inside a closed @test block, and be enclosed by at most one literal 'for VAR in ...; do' loop." >&2
        return 1
        ;;
    esac
  done
  if [ "$guest_vcpus" -le 0 ] || [ "$guest_mib" -le 0 ]; then
    echo "the sandbox guest demand read from $sandbox_file is $guest_vcpus vCPU / $guest_mib MiB" >&2
    return 1
  fi

  # ── Runner side. One line per job that drives the sandbox make targets:
  # workflow path, job name, the vCPU and GiB of its runner class (or NONE for
  # both) and the raw runs-on value. Comment lines are dropped before the
  # marker match, so prose naming e2e-prepare-cluster.bats above a job cannot
  # enrol it. Both globs are passed unquoted: if either matches nothing awk
  # gets a literal path and exits 2, which errexit turns into a red without the
  # named line the refusals above carry. Louder than they are, and still the
  # safe direction, since no workflow extension drops out of the sweep quietly.
  sandbox_jobs=$(awk '
    function class_of(s,   n) {
      if (match(s, /oracle-vm-[0-9]+cpu-[0-9]+gb/)) {
        n = substr(s, RSTART, RLENGTH)
        sub(/^oracle-vm-/, "", n)
        sub(/cpu-/, " ", n)
        sub(/gb$/, "", n)
        return n
      }
      return "NONE NONE"
    }
    function flush() {
      if (job != "" && sandbox) print wf, job, class_of(ro), ro
      job = ""; ro = ""; sandbox = 0
    }
    FNR == 1 { flush(); wf = FILENAME }
    /^  [A-Za-z0-9_-]+:[[:space:]]*$/ {
      flush()
      job = $0
      sub(/^[[:space:]]+/, "", job)
      sub(/:[[:space:]]*$/, "", job)
      next
    }
    job != "" {
      if ($0 ~ /^[[:space:]]*#/) next
      if ($0 ~ /^    runs-on:/) {
        ro = $0
        sub(/^[[:space:]]*runs-on:[[:space:]]*/, "", ro)
      }
      if ($0 ~ /prepare-env/ || $0 ~ /prepare-cluster/) sandbox = 1
    }
    END { flush() }
  ' "$workflow_dir"/*.yaml "$workflow_dir"/*.yml)

  if [ -z "$sandbox_jobs" ]; then
    echo "no job in $workflow_dir invokes the sandbox make targets" >&2
    echo "this check matches jobs on 'prepare-env' / 'prepare-cluster'; if those targets were renamed, rename them here too." >&2
    return 1
  fi

  # ── Coverage cross-check, before the comparison it guards. A file that runs
  # anything under packages/core/testing is a sandbox lane, so failing to match
  # a job in it means the parser above lost the file, not that the file is
  # innocent.
  for wf in "$workflow_dir"/*.yaml "$workflow_dir"/*.yml; do
    [ -e "$wf" ] || continue
    if grep -v '^[[:space:]]*#' "$wf" | grep -q 'packages/core/testing'; then
      if printf '%s\n' "$sandbox_jobs" | grep -qF "$wf "; then
        continue
      fi
      echo "$wf runs the packages/core/testing make targets but no job in it matched the sandbox marker" >&2
      echo "the job parser reads job names at two-space indent and runs-on at four; a reindent or a renamed target breaks it silently." >&2
      return 1
    fi
  done

  violations=
  while read -r wf job cpus gib raw; do
    if [ "$cpus" = "NONE" ]; then
      echo "job '$job' in $wf boots the e2e sandbox but its runs-on names no oracle-vm-<n>cpu-<m>gb class: ${raw:-<empty>}" >&2
      echo "this check cannot size a runner it cannot name, and guessing one would be worse than stopping." >&2
      return 1
    fi
    runner_mib=$((gib * 1024))
    if [ "$guest_vcpus" -ge "$cpus" ]; then
      violations="$violations
  $wf job '$job': $guest_vcpus guest vCPU on a ${cpus}-vCPU runner ($raw)"
    fi
    if [ "$guest_mib" -ge "$runner_mib" ]; then
      violations="$violations
  $wf job '$job': $guest_mib MiB of guest memory on a ${gib}-GiB runner ($raw)"
    fi
  done <<EOF
$sandbox_jobs
EOF

  if [ -n "$violations" ]; then
    echo "the e2e sandbox demands $guest_vcpus guest vCPU and $guest_mib MiB (from $sandbox_file)," >&2
    echo "which leaves no host headroom on these runners:" >&2
    printf '%s\n' "$violations" >&2
    echo "Move the job to a larger runner class, or lower the sandbox's -smp / -m." >&2
    return 1
  fi
}

# ── Fixtures for the guest-side derivation.
#
# The check above passes on the live tree whether or not the refusals still
# work, so loosening the awk would be a silent no-op without these. Each one
# feeds a synthetic file to the same program the check uses and asserts on the
# value it prints.

@test "guest demand multiplies -smp and -m by the loop count" {
  fixdir=$(mktemp -d)
  printf '@test "boot" {\n  for i in 1 2 3; do\n    qemu-system-x86_64 -smp 8 -m 24576 -display none\n  done\n}\n' > "$fixdir/f.bats"
  demand=$(awk "$GUEST_DEMAND_AWK" "$fixdir/f.bats" 2>&1) || true
  rm -rf "$fixdir"
  cpu=$(printf '%s\n' "$demand" | sed -n 1p)
  mib=$(printf '%s\n' "$demand" | sed -n 2p)
  if [ "$cpu" != 24 ] || [ "$mib" != 73728 ]; then
    echo "expected 24 vCPU / 73728 MiB, got: [$demand]" >&2
    return 1
  fi
}

@test "guest demand counts a launch outside any loop exactly once" {
  fixdir=$(mktemp -d)
  printf '@test "boot" {\n  qemu-system-x86_64 -smp 8 -m 24576 -display none\n}\n' > "$fixdir/f.bats"
  demand=$(awk "$GUEST_DEMAND_AWK" "$fixdir/f.bats" 2>&1) || true
  rm -rf "$fixdir"
  cpu=$(printf '%s\n' "$demand" | sed -n 1p)
  mib=$(printf '%s\n' "$demand" | sed -n 2p)
  if [ "$cpu" != 8 ] || [ "$mib" != 24576 ]; then
    echo "expected 8 vCPU / 24576 MiB, got: [$demand]" >&2
    return 1
  fi
}

@test "guest demand refuses a qemu line outside any @test block" {
  fixdir=$(mktemp -d)
  printf 'boot_node() {\n  qemu-system-x86_64 -smp 4 -m 8192\n}\n@test "boot" {\n  for i in 1 2 3; do\n    qemu-system-x86_64 -smp 8 -m 24576\n  done\n}\n' > "$fixdir/f.bats"
  demand=$(awk "$GUEST_DEMAND_AWK" "$fixdir/f.bats" 2>&1) || true
  rm -rf "$fixdir"
  case "$demand" in
    ERROR*outside\ any\ @test\ block*) : ;;
    *) echo "expected a refusal naming the out-of-block line, got: [$demand]" >&2; return 1 ;;
  esac
}

@test "guest demand refuses a @test block left open at end of file" {
  fixdir=$(mktemp -d)
  printf '@test "boot" {\n  for i in 1 2 3; do\n    qemu-system-x86_64 -smp 8 -m 24576\n  done\n' > "$fixdir/f.bats"
  demand=$(awk "$GUEST_DEMAND_AWK" "$fixdir/f.bats" 2>&1) || true
  rm -rf "$fixdir"
  case "$demand" in
    ERROR*still\ open\ at\ end\ of\ file*) : ;;
    *) echo "expected a refusal naming the unclosed block, got: [$demand]" >&2; return 1 ;;
  esac
}

@test "guest demand refuses a launch with no literal -smp" {
  fixdir=$(mktemp -d)
  printf '@test "boot" {\n  for i in 1 2 3; do\n    qemu-system-x86_64 -smp cpus=8 -m 24576\n  done\n}\n' > "$fixdir/f.bats"
  demand=$(awk "$GUEST_DEMAND_AWK" "$fixdir/f.bats" 2>&1) || true
  rm -rf "$fixdir"
  case "$demand" in
    ERROR*no\ literal\ -smp*) : ;;
    *) echo "expected a refusal naming -smp, got: [$demand]" >&2; return 1 ;;
  esac
}

@test "guest demand refuses a launch with no literal -m" {
  fixdir=$(mktemp -d)
  printf '@test "boot" {\n  for i in 1 2 3; do\n    qemu-system-x86_64 -smp 8 -m size=24576\n  done\n}\n' > "$fixdir/f.bats"
  demand=$(awk "$GUEST_DEMAND_AWK" "$fixdir/f.bats" 2>&1) || true
  rm -rf "$fixdir"
  case "$demand" in
    ERROR*no\ literal\ -m*) : ;;
    *) echo "expected a refusal naming -m, got: [$demand]" >&2; return 1 ;;
  esac
}

@test "guest demand refuses a for opener the strict pattern cannot read" {
  fixdir=$(mktemp -d)
  printf '@test "boot" {\n  for i in 1 2 3\n  do\n    qemu-system-x86_64 -smp 8 -m 24576\n  done\n}\n' > "$fixdir/f.bats"
  demand=$(awk "$GUEST_DEMAND_AWK" "$fixdir/f.bats" 2>&1) || true
  rm -rf "$fixdir"
  case "$demand" in
    ERROR*for\ opener*) : ;;
    *) echo "expected a refusal naming the for opener, got: [$demand]" >&2; return 1 ;;
  esac
}

@test "guest demand refuses a for list that is not literal words" {
  fixdir=$(mktemp -d)
  printf '@test "boot" {\n  for i in $(seq 1 3); do\n    qemu-system-x86_64 -smp 8 -m 24576\n  done\n}\n' > "$fixdir/f.bats"
  demand=$(awk "$GUEST_DEMAND_AWK" "$fixdir/f.bats" 2>&1) || true
  rm -rf "$fixdir"
  case "$demand" in
    ERROR*non-literal\ item*) : ;;
    *) echo "expected a refusal naming the non-literal item, got: [$demand]" >&2; return 1 ;;
  esac
}

@test "guest demand refuses a while loop beside a qemu launch" {
  fixdir=$(mktemp -d)
  printf '@test "boot" {\n  i=1\n  while [ $i -le 3 ]; do\n    qemu-system-x86_64 -smp 8 -m 24576\n  done\n}\n' > "$fixdir/f.bats"
  demand=$(awk "$GUEST_DEMAND_AWK" "$fixdir/f.bats" 2>&1) || true
  rm -rf "$fixdir"
  case "$demand" in
    ERROR*while/until\ loop*) : ;;
    *) echo "expected a refusal naming the while loop, got: [$demand]" >&2; return 1 ;;
  esac
}

@test "guest demand refuses two qemu lines in one @test block" {
  fixdir=$(mktemp -d)
  printf '@test "boot" {\n  for i in 1 2 3; do\n    qemu-system-x86_64 -smp 8 -m 24576\n    qemu-system-x86_64 -smp 4 -m 8192\n  done\n}\n' > "$fixdir/f.bats"
  demand=$(awk "$GUEST_DEMAND_AWK" "$fixdir/f.bats" 2>&1) || true
  rm -rf "$fixdir"
  case "$demand" in
    ERROR*lines\ naming\ qemu-system-x86_64*) : ;;
    *) echo "expected a refusal naming the second launch, got: [$demand]" >&2; return 1 ;;
  esac
}

@test "guest demand refuses two for loops in one @test block" {
  fixdir=$(mktemp -d)
  printf '@test "boot" {\n  for j in a b; do\n    echo "$j"\n  done\n  for i in 1 2 3; do\n    qemu-system-x86_64 -smp 8 -m 24576\n  done\n}\n' > "$fixdir/f.bats"
  demand=$(awk "$GUEST_DEMAND_AWK" "$fixdir/f.bats" 2>&1) || true
  rm -rf "$fixdir"
  case "$demand" in
    ERROR*for-loops*) : ;;
    *) echo "expected a refusal naming the second loop, got: [$demand]" >&2; return 1 ;;
  esac
}

@test "guest demand refuses a file with no qemu launch at all" {
  fixdir=$(mktemp -d)
  printf '@test "nothing" {\n  true\n}\n' > "$fixdir/f.bats"
  demand=$(awk "$GUEST_DEMAND_AWK" "$fixdir/f.bats" 2>&1) || true
  rm -rf "$fixdir"
  case "$demand" in
    ERROR*no\ qemu-system-x86_64\ launch\ found*) : ;;
    *) echo "expected a refusal naming the absent launch, got: [$demand]" >&2; return 1 ;;
  esac
}

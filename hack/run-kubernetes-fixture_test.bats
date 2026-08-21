#!/usr/bin/env bats
# Pins what the tenant Kubernetes fixture in hack/e2e-chainsaw/_lib/run-kubernetes.sh
# renders, so the worker sizing an experiment varies through the environment
# cannot move the default a run without that environment gets.
#
# The three sizing values in that fixture read from the environment with the
# figures they used to carry as their defaults. That shape has one failure the
# eye does not catch: a default written `${VAR-2}` instead of `${VAR:-2}` still
# renders 2 for an unset variable and renders nothing at all for one set to the
# empty string. Empty is not how an omitted dispatch input arrives -- these knobs
# declare defaults and arrive carrying them -- it is what an operator clearing
# the field sends, and it is also what a sandbox started by hand can have. The
# recorded render below is the discriminator, and the empty-value case is tested
# separately from the unset one for that reason.
#
# What the comparison covers is the whole manifest and not the three lines: a
# knob that reached a neighbouring field, or a heredoc whose quoting changed so
# that some other expansion stopped happening, both show up as a diff here.
#
# What it does not cover is the content of the two blocks the fixture composes
# from its caller. `talos_block` and `ouroboros_addon` are rendered as fixed
# placeholders, so this file says nothing about either, and changing them leaves
# it green. That is deliberate: those blocks move for reasons of their own and
# pinning them here would make this file red for changes it has no opinion on.
#
# The recorded render is regenerated, not edited, whenever the fixture changes
# on purpose:
#
#   sh hack/run-kubernetes-fixture-render.sh hack/e2e-chainsaw/_lib/run-kubernetes.sh \
#     > hack/testdata/run-kubernetes-fixture/default-render.yaml

FIXTURE_SRC=hack/e2e-chainsaw/_lib/run-kubernetes.sh
FIXTURE_GOLDEN=hack/testdata/run-kubernetes-fixture/default-render.yaml
FIXTURE_RENDER=hack/run-kubernetes-fixture-render.sh

# Renders the fixture heredoc. Arguments after the work directory are passed to
# `env`, so a caller sets the knobs for one render without leaving them set for
# the next test in the file. The values the fixture takes from its caller are
# the renderer's, not this file's, so that a regenerated recording and a
# rendered one are the same bytes.
#
# The closing brace is indented on purpose: hack/cozytest.sh rewrites a line
# that is exactly `}` into the end of a @test, which appends `return 0` to
# whatever it closes and would make this helper always report success.
cozy_render_fixture() {
  _rf_src=$1
  _rf_work=$2
  shift 2
  # Any knob already exported into the shell running this suite is cleared
  # first, so "no knob is set" means it here too. Derived from the environment
  # rather than listed, so a knob added later is cleared without an edit.
  _rf_clear=
  for _rf_name in $(env | sed -n 's/^\(COZY_E2E_[A-Z0-9_]*\)=.*/\1/p'); do
    _rf_clear="${_rf_clear} -u ${_rf_name}"
  done
  # Unquoted on purpose: the accumulated -u flags have to reach env as words.
  # shellcheck disable=SC2086
  env ${_rf_clear} "$@" sh "${FIXTURE_RENDER}" "${_rf_src}" >"${_rf_work}/out" 2>"${_rf_work}/err"
  }

@test "the fixture renders byte for byte as recorded when no knob is set" {
  tmp=$(mktemp -d)
  cozy_render_fixture "${FIXTURE_SRC}" "${tmp}"
  diff -u "${FIXTURE_GOLDEN}" "${tmp}/out"
}

@test "a knob set to the empty string renders the recorded default" {
  tmp=$(mktemp -d)
  cozy_render_fixture "${FIXTURE_SRC}" "${tmp}" \
    COZY_E2E_WORKER_VCPU= \
    COZY_E2E_WORKER_POD_CPU_LIMIT= \
    COZY_E2E_WORKER_POD_CPU_REQUEST=
  diff -u "${FIXTURE_GOLDEN}" "${tmp}/out"
}

@test "each knob reaches its own field in the rendered manifest" {
  tmp=$(mktemp -d)
  cozy_render_fixture "${FIXTURE_SRC}" "${tmp}" \
    COZY_E2E_WORKER_VCPU=4 \
    COZY_E2E_WORKER_POD_CPU_LIMIT=16 \
    COZY_E2E_WORKER_POD_CPU_REQUEST=250m
  grep -q '^        cpu: 4$' "${tmp}/out"
  grep -q '^      podCpuLimit: 16$' "${tmp}/out"
  grep -q '^      podCpuRequest: 250m$' "${tmp}/out"
}

@test "setting the knobs changes those three lines and nothing else" {
  tmp=$(mktemp -d)
  cozy_render_fixture "${FIXTURE_SRC}" "${tmp}" \
    COZY_E2E_WORKER_VCPU=4 \
    COZY_E2E_WORKER_POD_CPU_LIMIT=16 \
    COZY_E2E_WORKER_POD_CPU_REQUEST=250m
  diff "${FIXTURE_GOLDEN}" "${tmp}/out" >"${tmp}/diff" || true
  # Three changed lines are six diff lines plus three `---` separators and three
  # `NcN` headers. Counting the changed content rather than the diff's shape
  # keeps this insensitive to which diff implementation ran.
  # `|| :` because grep exits 1 when it counts nothing, and a zero count is a
  # legitimate reading here -- it is the one that says the knobs did not land.
  changed=$(grep -c '^[<>]' "${tmp}/diff" || :)
  [ "${changed}" = 6 ]
  grep -q '^< *cpu: 1$' "${tmp}/diff"
  grep -q '^> *cpu: 4$' "${tmp}/diff"
  grep -q '^< *podCpuLimit: 2$' "${tmp}/diff"
  grep -q '^> *podCpuLimit: 16$' "${tmp}/diff"
  grep -q '^< *podCpuRequest: 100m$' "${tmp}/diff"
  grep -q '^> *podCpuRequest: 250m$' "${tmp}/diff"
}

@test "the renderer refuses a source whose fixture heredoc it cannot find" {
  tmp=$(mktemp -d)
  printf 'echo not the fixture\n' >"${tmp}/src.sh"
  rc=0
  sh "${FIXTURE_RENDER}" "${tmp}/src.sh" >"${tmp}/out" 2>"${tmp}/err" || rc=$?
  [ "${rc}" -ne 0 ]
  grep -q 'no fixture heredoc' "${tmp}/err"
}

@test "the renderer refuses a fixture heredoc with no body" {
  tmp=$(mktemp -d)
  printf '  kubectl apply -f - <<EOF\nEOF\n' >"${tmp}/src.sh"
  rc=0
  sh "${FIXTURE_RENDER}" "${tmp}/src.sh" >"${tmp}/out" 2>"${tmp}/err" || rc=$?
  [ "${rc}" -ne 0 ]
  grep -q 'empty' "${tmp}/err"
}

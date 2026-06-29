#!/bin/sh
# Make every private cozystack/* OCIR repository public via the OCI API.
#
# Why: a first-party image's OCIR repository is created on its FIRST push, and
# OCIR defaults new repositories to PRIVATE. CI's e2e cluster pulls images
# anonymously (no pull secret), so a brand-new image's repository returns 403
# until it is flipped public. This script performs that flip idempotently for
# the whole cozystack/ namespace, so a newly introduced first-party image needs
# no manual intervention.
#
# Auth: the docker OCIR_TOKEN used for `docker login` is a registry auth token
# and CANNOT call the OCI control-plane API. Repository visibility is set via
# OCI-API auth (instance principal, or an API signing key in OCI_CLI_* env),
# selected by OCI_CLI_AUTH (default: instance_principal). The caller provides
# working auth plus an IAM `manage repos` policy on the compartment.
#
# Best-effort by design: a transient API/install failure emits a ::warning::
# and exits 0 rather than failing the build — the worst case is the
# pre-existing "new repo stays private" state, never a regression on an
# unrelated PR.
#
# Env:
#   OCIR_COMPARTMENT_OCID  (required) compartment holding the cozystack/* repos
#   OCI_CLI_AUTH           (optional) auth method, default instance_principal
#   OCIR_REPO_PREFIX       (optional) repo display-name prefix, default cozystack/
#
# Run the unit test with: hack/cozytest.sh hack/ocir-make-public_test.bats
set -eu

: "${OCIR_COMPARTMENT_OCID:?OCIR_COMPARTMENT_OCID must be set}"
export OCI_CLI_AUTH="${OCI_CLI_AUTH:-instance_principal}"
PREFIX="${OCIR_REPO_PREFIX:-cozystack/}"

command -v jq >/dev/null 2>&1 || {
	echo "::warning::jq not found; skipping OCIR repository visibility update"
	exit 0
}

# Ephemeral runners may not carry the oci CLI; install under $HOME (no sudo),
# idempotent. Install failure is non-fatal — the step is opt-in convenience.
if ! command -v oci >/dev/null 2>&1; then
	if curl -fsSL https://raw.githubusercontent.com/oracle/oci-cli/master/scripts/install/install.sh -o /tmp/install-oci.sh \
		&& bash /tmp/install-oci.sh --accept-all-defaults \
			--install-dir "$HOME/.oci-cli" --exec-dir "$HOME/.oci-cli/bin" >/dev/null 2>&1; then
		PATH="$HOME/.oci-cli/bin:$PATH"
		export PATH
	fi
	rm -f /tmp/install-oci.sh
fi
command -v oci >/dev/null 2>&1 || {
	echo "::warning::oci CLI unavailable; skipping OCIR repository visibility update"
	exit 0
}

# List every repository in the compartment. Best-effort: a transient list error
# must not fail the build.
if ! items="$(oci artifacts container repository list \
	--compartment-id "$OCIR_COMPARTMENT_OCID" --all 2>/tmp/ocir-list.err)"; then
	echo "::warning::OCIR repository list failed; new repos may stay private:"
	cat /tmp/ocir-list.err >&2 2>/dev/null || true
	exit 0
fi

# Select private repositories under the cozystack/ display-name prefix. OCIR
# display names are everything after <region>.ocir.io/<tenancy-namespace>/, so
# images pushed to iad.ocir.io/<ns>/cozystack/<img> are named cozystack/<img>.
# `// ""` guards an item without a display-name: jq would otherwise error on
# `null | startswith`, and (no pipefail) that non-zero would abort the step,
# defeating the best-effort design.
ids="$(printf '%s' "$items" | jq -r --arg p "$PREFIX" \
	'.data.items[]? | select(((.["display-name"] // "") | startswith($p)) and (.["is-public"] == false)) | .id')"

if [ -z "$ids" ]; then
	echo "No private ${PREFIX}* repositories to update."
	exit 0
fi

echo "$ids" | while IFS= read -r id; do
	[ -n "$id" ] || continue
	echo "::notice::Setting OCIR repository ${id} public"
	oci artifacts container repository update --repository-id "$id" --is-public true --force >/dev/null \
		|| echo "::warning::Failed to set ${id} public"
done

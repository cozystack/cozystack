# shellcheck shell=sh
# Shared helper for stamping the cozystack-version ConfigMap during migrations.
#
# The ConfigMap MUST carry the platform.cozystack.io/no-delete=true label so the
# cozystack-no-delete-guardrail ValidatingAdmissionPolicy guards it against
# deletion. Migrations apply it under the default kubectl field manager; a
# label-less apply by that same manager would strip the label (the failure mode
# migration 42 first fixed). templates/cozystack-version.yaml renders the
# labeled ConfigMap only on first install (lookup-if-not), so every upgrade-time
# stamp must re-emit the label by hand.
#
# Centralizing the manifest here means migration 45+ cannot drift back to a
# label-less apply: each migration sources this file and calls the helper
# instead of copy-pasting a heredoc.
#
# Sourced, not executed. Each migrations/<N> script does:
#   . "$(dirname "$0")/lib/cozystack-version.sh"
# and run-migrations.sh sources /migrations/lib/cozystack-version.sh.

# render_cozystack_version_manifest <version>
# Emit the labeled cozystack-version ConfigMap manifest on stdout. Pure (no
# kubectl), so it is unit-testable in isolation. The namespace defaults to
# cozy-system and is overridable via $NAMESPACE.
render_cozystack_version_manifest() {
  : "${1:?render_cozystack_version_manifest: version argument required}"
  cat <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: cozystack-version
  namespace: ${NAMESPACE:-cozy-system}
  labels:
    platform.cozystack.io/no-delete: "true"
data:
  version: "$1"
EOF
}

# stamp_cozystack_version <version>
# Render and apply the labeled manifest under the default kubectl field manager.
stamp_cozystack_version() {
  # Guard the version here too, not only in render: without pipefail a render
  # failure would not abort the pipeline and kubectl would apply empty input.
  : "${1:?stamp_cozystack_version: version argument required}"
  render_cozystack_version_manifest "$1" | kubectl apply --filename -
}

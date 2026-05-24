#!/usr/bin/env bash
#
# Regression test for the containerd registry config_path sed lines in
# templates/cluster.yaml preKubeadmCommands.
#
# Prevents two failure modes:
#   1. The sed lines in this script drift from the ones in cluster.yaml
#      (drift detection via grep -F against the template).
#   2. A future containerd release renames the registry plugin section or
#      changes its default quoting, so the sed silently stops matching
#      and per-registry mirroring under /etc/containerd/certs.d breaks
#      (functional check via running the seds against minimal fixtures
#      of the containerd 1.x and 2.x default configs).

set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
template="$script_dir/../templates/cluster.yaml"
fixtures_dir="$script_dir/fixtures"

# Pick a GNU-compatible sed; the cluster.yaml sed uses \s and \( ... \)
# which behave differently on BSD sed (macOS default).
if sed --version >/dev/null 2>&1; then
  SED=sed
elif command -v gsed >/dev/null 2>&1; then
  SED=gsed
elif [[ "${CI:-}" == "true" ]]; then
  echo "FAIL: CI must have GNU sed; refusing to skip the regression test"
  exit 1
else
  echo "SKIP: GNU sed not found (install via 'brew install gnu-sed' on macOS)"
  exit 0
fi

sed_legacy='sed -i '\''/\[plugins\.[^]]*grpc\.v1\.cri[^]]*registry\]/,/^\[/ s|^\(\s*config_path\s*=\s*\).*|\1"/etc/containerd/certs.d"|'\'' /etc/containerd/config.toml'
sed_new='sed -i '\''/\[plugins\.[^]]*io\.containerd\.cri\.v1\.images[^]]*registry\]/,/^\[/ s|^\(\s*config_path\s*=\s*\).*|\1"/etc/containerd/certs.d"|'\'' /etc/containerd/config.toml'

# Drift check: the strings above must appear verbatim in cluster.yaml.
for line in "$sed_legacy" "$sed_new"; do
  if ! grep -qF -- "$line" "$template"; then
    echo "FAIL: sed line drifted from $template:"
    echo "  expected: $line"
    exit 1
  fi
done

# Extract just the inline sed script (between the outer single quotes) for
# applying via $SED -e.
script_legacy=$(printf '%s' "$sed_legacy" | $SED -n "s/^sed -i '\\(.*\\)' \\/etc\\/containerd\\/config.toml$/\\1/p")
script_new=$(printf '%s' "$sed_new" | $SED -n "s/^sed -i '\\(.*\\)' \\/etc\\/containerd\\/config.toml$/\\1/p")

if [[ -z "$script_legacy" || -z "$script_new" ]]; then
  echo "FAIL: could not parse sed scripts from the template lines"
  exit 1
fi

# Functional check: run both seds against each fixture, assert the
# config_path under the registry section was rewritten exactly once.
for fixture in "$fixtures_dir"/containerd-1x.toml "$fixtures_dir"/containerd-2x.toml; do
  name=$(basename "$fixture")
  patched=$($SED -e "$script_legacy" -e "$script_new" "$fixture")

  count=$(printf '%s\n' "$patched" | grep -cE '^[[:space:]]*config_path = "/etc/containerd/certs.d"$' || true)
  if [[ "$count" -ne 1 ]]; then
    echo "FAIL: $name: expected exactly 1 patched config_path line, got $count"
    echo "--- patched output ---"
    printf '%s\n' "$patched"
    exit 1
  fi
done

echo "OK: containerd config_path sed lines patch both 1.x and 2.x configs"

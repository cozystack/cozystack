#!/usr/bin/env bash
# Export the GitHub rulesets that apply to a repository as normalized JSON, so a
# change to them is a reviewable diff. docs/github-settings.md describes the
# settings and the procedure around this script.
#
# The files are a record, not a source of truth: nothing applies them back.
# GitHub holds the live settings, an org admin changes them in the UI, and this
# script refreshes the files afterwards.
#
# Usage: hack/export-rulesets.sh [owner/repo]
#
# Run it with a token that has admin on the repository. The API returns
# `bypass_actors` to admins only, and an export without it would drop who may
# bypass each rule without saying so, so the script refuses to write anything
# when a ruleset comes back without that field.
#
# Org-level rulesets are read through the repository, which returns the rules
# and the ref conditions but not which repositories the ruleset targets. That
# part lives in the org settings and is not exported.
set -euo pipefail

repo="${1:-cozystack/cozystack}"
cd "$(dirname "${BASH_SOURCE[0]}")/.."
out=.github/rulesets

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/repo" "$tmp/org"

rows="$(gh api --paginate "repos/${repo}/rulesets" --jq '.[] | [.id, .source_type, .name] | @tsv')"
if [ -z "$rows" ]; then
  echo "no rulesets returned for ${repo}; refusing to export an empty set" >&2
  exit 1
fi

while IFS=$'\t' read -r id source_type name; do
  case "$source_type" in
    Repository) dir=repo ;;
    Organization) dir=org ;;
    *) echo "ruleset '${name}' has unexpected source_type '${source_type}'" >&2; exit 1 ;;
  esac

  slug="$(printf '%s' "$name" | tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9]+/-/g; s/^-+|-+$//g')"
  file="$tmp/$dir/$slug.json"
  if [ -e "$file" ]; then
    echo "two ${dir} rulesets map to the file name '${slug}.json'" >&2
    exit 1
  fi

  body="$(gh api "repos/${repo}/rulesets/${id}")"
  if ! printf '%s' "$body" | jq -e 'has("bypass_actors")' >/dev/null; then
    echo "ruleset '${name}' came back without bypass_actors; run with a token that has admin on ${repo}" >&2
    exit 1
  fi

  # An allow-list rather than a delete-list: a field GitHub adds later stays
  # out of the files until someone decides it belongs in them.
  printf '%s' "$body" | jq -S '{name, target, enforcement, conditions, rules, bypass_actors}' >"$file"
done <<<"$rows"

rm -rf "${out:?}/repo" "${out:?}/org"
mkdir -p "$out"
cp -R "$tmp/repo" "$out/repo"
cp -R "$tmp/org" "$out/org"
echo "exported to ${out}: $(find "$tmp/repo" -name '*.json' | wc -l) repository, $(find "$tmp/org" -name '*.json' | wc -l) organization"

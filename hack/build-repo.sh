#!/bin/sh
set -e

REPO_NAME="$(basename "$PWD")"
OUT="../../_out/repos/$REPO_NAME"
TMP="../../_out/repos/$REPO_NAME/historical"

if [ ! -f "versions_map" ]; then
  echo "Script should be called from packages/<repo>!" >&2
  exit 1
fi

(set -x; rm -rf "$OUT")
(set -x; mkdir -p "$OUT")

while read chart version commit; do
  if [ $commit = "HEAD" ]; then
    (set -x; helm package -d "$OUT" "$chart")
    continue
  fi
  mkdir -p "$TMP/$chart-$version"
  (set -x; git archive "$commit" "$chart" | tar -xf- --strip-components=1 -C "$TMP/$chart-$version")

  if [ -d "$TMP/$chart-$version/charts" ]; then
    (set -x; find "$TMP/$chart-$version/charts" -type l -maxdepth 1 -mindepth 1) | awk -F/ '{print $NF}' | while read library_chart; do
      (set -x; rm -f "$TMP/$chart-$version/charts/$library_chart")
      (set -x; cd ../library && git archive "$commit" "$library_chart" | tar -xf- -C "$TMP/$chart-$version/charts/")
    done
  fi
  rm -rf "$tmp"
done < versions_map

(set -x; helm package -d "$OUT" $(find . "$TMP" -mindepth 2 -maxdepth 2 -name Chart.yaml | awk 'sub("/Chart.yaml", "")' | sort -V))

(set -x; cd "$OUT" && helm repo index . --url "http://cozystack.cozy-system.svc/repos/$REPO_NAME")

(set -x; rm -rf "$TMP")

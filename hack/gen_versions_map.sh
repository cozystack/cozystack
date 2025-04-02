#!/bin/sh
set -e

file=versions_map

charts=$(find . -mindepth 2 -maxdepth 2 -name Chart.yaml | awk 'sub("/Chart.yaml", "")')

new_map=$(
  for chart in $charts; do
    awk '/^name:/ {chart=$2} /^version:/ {version=$2} END{printf "%s %s %s\n", chart, version, "HEAD"}' "$chart/Chart.yaml"
  done
)

if [ ! -f "$file" ] || [ ! -s "$file" ]; then
  echo "$new_map" > "$file"
  exit 0
fi

miss_map=$(echo "$new_map" | awk 'NR==FNR { nm[$1 " " $2] = $3; next } { if (!($1 " " $2 in nm)) print $1, $2, $3}' - "$file")

# search accross all tags sorted by version
search_commits=$(git ls-remote --tags origin | grep 'refs/tags/v' | sort -k2,2 -rV | awk '{print $1}')
# add latest main commit to search
search_commits="${search_commits} $(git rev-parse "origin/main")"

resolved_miss_map=$(
  echo "$miss_map" | while read -r chart version commit; do
    # if version is found in HEAD, it's HEAD
    if grep -q "^version: $version$" ./${chart}/Chart.yaml; then
      echo "$chart $version HEAD"
      continue
    fi

    # if commit is not HEAD, check if it's valid
    if [ $commit != "HEAD" ]; then
      if ! git show "${commit}:./${chart}/Chart.yaml" 2>/dev/null | grep -q "^version: $version$"; then
        echo "Commit $commit for $chart $version is not valid" >&2
        exit 1
      fi

      commit=$(git rev-parse --short "$commit")
      echo "$chart $version $commit"
      continue
    fi

    # if commit is HEAD, but version is not found in HEAD, check all tags
    found_tag=""
    for tag in $search_commits; do
      if git show "${tag}:./${chart}/Chart.yaml" 2>/dev/null | grep -q "^version: $version$"; then
        found_tag=$(git rev-parse --short "${tag}")
        break
      fi
    done
    
    if [ -z "$found_tag" ]; then
      echo "Can't find $chart $version in any version tag or in the latest main commit" >&2
      exit 1
    fi
    
    echo "$chart $version $found_tag"
  done
)

printf "%s\n" "$new_map" "$resolved_miss_map" | sort -k1,1 -k2,2 -V | awk '$1' > "$file"

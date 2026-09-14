#!/usr/bin/env bash
# discover.sh — Discover all components across cozystack org.
# Outputs: workspace/discovery.json with repos, deps, images.
set -euo pipefail

ORG="cozystack"
WORKSPACE="${1:-workspace}"
OUTFILE="$WORKSPACE/discovery.json"
CLONE_DIR="$WORKSPACE/repos"
TMP_DIR="$WORKSPACE/tmp-discover"

mkdir -p "$CLONE_DIR" "$TMP_DIR"

echo "==> Discovering repos in $ORG..."

# Get all non-fork, non-archived repos
gh api --paginate "/orgs/$ORG/repos?per_page=100" \
  --jq '.[] | select(.fork == false and .archived == false) | .full_name' \
  > "$TMP_DIR/repos.txt" 2>/dev/null

REPO_COUNT=$(wc -l < "$TMP_DIR/repos.txt")
echo "   Found $REPO_COUNT repos"

# --- Process each repo ---

REPO_INDEX=0
while IFS= read -r repo; do
  repo_name=$(basename "$repo")
  repo_dir="$CLONE_DIR/$repo_name"
  repo_tmp="$TMP_DIR/$repo_name"
  mkdir -p "$repo_tmp"

  REPO_INDEX=$((REPO_INDEX + 1))
  echo "==> [$REPO_INDEX/$REPO_COUNT] Processing $repo..."

  # Shallow clone if not exists
  if [ ! -d "$repo_dir" ]; then
    git clone --depth 1 --quiet "https://github.com/$repo.git" "$repo_dir" 2>/dev/null || {
      echo "   WARN: cannot clone $repo, skipping"
      echo "$repo" > "$repo_tmp/repo.txt"
      touch "$repo_tmp/go_deps.txt" "$repo_tmp/dockerfile_images.txt" "$repo_tmp/helm_images.txt" "$repo_tmp/packages.txt"
      continue
    }
  fi

  echo "$repo" > "$repo_tmp/repo.txt"

  # Go deps (direct only)
  if [ -f "$repo_dir/go.mod" ]; then
    grep -E '^\t[a-zA-Z]' "$repo_dir/go.mod" 2>/dev/null \
      | grep -v '// indirect' \
      | awk '{print $1 " " $2}' \
      > "$repo_tmp/go_deps.txt" 2>/dev/null || true
  else
    touch "$repo_tmp/go_deps.txt"
  fi

  # Dockerfile base images
  find "$repo_dir" -name 'Dockerfile*' -type f 2>/dev/null \
    | xargs grep -hi '^FROM' 2>/dev/null \
    | sed 's/^FROM\s*//i; s/\s*[Aa][Ss]\s.*$//' \
    | grep -v '^\$' \
    | grep -v '^scratch$' \
    | sort -u \
    > "$repo_tmp/dockerfile_images.txt" 2>/dev/null || touch "$repo_tmp/dockerfile_images.txt"

  # Helm images from values.yaml (using Python for reliable YAML parsing)
  find "$repo_dir" -name 'values.yaml' -type f 2>/dev/null \
    | xargs -I{} python3 -c "
import yaml, sys
try:
    with open('{}') as f:
        data = yaml.safe_load(f)
    if not isinstance(data, dict):
        sys.exit(0)
    def find_images(d):
        if not isinstance(d, dict):
            return
        # Check image: string field
        img = d.get('image', '')
        if isinstance(img, str) and '/' in img and '{{' not in img and img.strip():
            print(img.strip())
        # Check image: {repository:, tag:} pattern
        if isinstance(img, dict):
            repo = img.get('repository', '')
            tag = img.get('tag', '')
            if repo and '{{' not in str(repo):
                if tag and '{{' not in str(tag):
                    print(f'{repo}:{tag}')
                elif ':' in str(repo):
                    print(repo)
        # Check repository: + tag: at current level
        repo = d.get('repository', '')
        tag = d.get('tag', '')
        if isinstance(repo, str) and repo and '{{' not in repo and '/' in repo:
            if isinstance(tag, str) and tag and '{{' not in tag:
                print(f'{repo}:{tag}')
            elif ':' in repo:
                print(repo)
        for v in d.values():
            if isinstance(v, dict):
                find_images(v)
            elif isinstance(v, list):
                for item in v:
                    if isinstance(item, dict):
                        find_images(item)
    find_images(data)
except Exception:
    pass
" 2>/dev/null | sort -u > "$repo_tmp/helm_images_raw.txt" 2>/dev/null || true

  # Also grep for hardcoded image: lines in templates (non-templated)
  grep -rh 'image:' "$repo_dir" --include='*.yaml' --include='*.yml' 2>/dev/null \
    | grep -v '#' \
    | grep -v '{{' \
    | sed 's/.*image:\s*["'"'"']*//; s/["'"'"']*\s*$//' \
    | grep -E '^[a-zA-Z0-9].*/' \
    | sort -u \
    >> "$repo_tmp/helm_images_raw.txt" 2>/dev/null || true

  sort -u "$repo_tmp/helm_images_raw.txt" > "$repo_tmp/helm_images.txt" 2>/dev/null || touch "$repo_tmp/helm_images.txt"

  # Packages (for cozystack main repo)
  if [ "$repo_name" = "cozystack" ]; then
    for pkg_dir in "$repo_dir"/packages/*/; do
      [ -d "$pkg_dir" ] || continue
      pkg_type=$(basename "$pkg_dir")
      for app_dir in "$pkg_dir"/*/; do
        [ -d "$app_dir" ] || continue
        app_name=$(basename "$app_dir")

        # Dockerfile images in this package
        find "$app_dir" -name 'Dockerfile*' -type f 2>/dev/null \
          | xargs grep -hi '^FROM' 2>/dev/null \
          | sed 's/^FROM\s*//i; s/\s*[Aa][Ss]\s.*$//' \
          | grep -v '^\$' \
          | grep -v '^scratch$' \
          | sort -u \
          | while read -r img; do
              echo "$pkg_type/$app_name|dockerfile|$img"
            done >> "$repo_tmp/packages.txt" 2>/dev/null || true

        # Helm images from values.yaml in this package
        find "$app_dir" -name 'values.yaml' -type f 2>/dev/null \
          | xargs -I{} python3 -c "
import yaml, sys
try:
    with open('{}') as f:
        data = yaml.safe_load(f)
    if not isinstance(data, dict):
        sys.exit(0)
    def find_images(d):
        if not isinstance(d, dict):
            return
        img = d.get('image', '')
        if isinstance(img, str) and '/' in img and '{{' not in img and img.strip():
            print(img.strip())
        if isinstance(img, dict):
            repo = img.get('repository', '')
            tag = img.get('tag', '')
            if repo and '{{' not in str(repo):
                if tag and '{{' not in str(tag):
                    print(f'{repo}:{tag}')
        repo = d.get('repository', '')
        tag = d.get('tag', '')
        if isinstance(repo, str) and repo and '{{' not in repo and '/' in repo:
            if isinstance(tag, str) and tag and '{{' not in tag:
                print(f'{repo}:{tag}')
        for v in d.values():
            if isinstance(v, dict):
                find_images(v)
            elif isinstance(v, list):
                for item in v:
                    if isinstance(item, dict):
                        find_images(item)
    find_images(data)
except Exception:
    pass
" 2>/dev/null | sort -u | while read -r img; do
            echo "$pkg_type/$app_name|helm|$img"
          done >> "$repo_tmp/packages.txt" 2>/dev/null || true
      done
    done
  fi
  [ -f "$repo_tmp/packages.txt" ] || touch "$repo_tmp/packages.txt"

done < "$TMP_DIR/repos.txt"

# --- Assemble JSON ---

echo "==> Assembling discovery.json..."

python3 << 'PYEOF'
import json, os

tmp_dir = os.environ.get("TMP_DIR", "workspace/tmp-discover")
outfile = os.environ.get("OUTFILE", "workspace/discovery.json")
workspace = os.environ.get("WORKSPACE", "workspace")

repos = []
for entry in sorted(os.listdir(tmp_dir)):
    entry_dir = os.path.join(tmp_dir, entry)
    if not os.path.isdir(entry_dir) or entry == ".":
        continue
    repo_file = os.path.join(entry_dir, "repo.txt")
    if not os.path.exists(repo_file):
        continue

    with open(repo_file) as f:
        repo_name = f.read().strip()

    def read_lines(filename):
        path = os.path.join(entry_dir, filename)
        if not os.path.exists(path):
            return []
        with open(path) as f:
            return [l.strip() for l in f if l.strip()]

    go_deps = []
    for line in read_lines("go_deps.txt"):
        parts = line.split()
        if len(parts) >= 2:
            go_deps.append({"module": parts[0], "version": parts[1]})

    repo_data = {
        "repo": repo_name,
        "go_deps": go_deps,
        "dockerfile_images": read_lines("dockerfile_images.txt"),
        "helm_images": read_lines("helm_images.txt"),
    }

    packages = []
    for line in read_lines("packages.txt"):
        parts = line.split("|")
        if len(parts) == 3:
            packages.append({"component": parts[0], "source": parts[1], "image": parts[2]})
    if packages:
        repo_data["packages"] = packages

    repos.append(repo_data)

output = {"repos": repos}
with open(outfile, "w") as f:
    json.dump(output, f, indent=2)

# Collect unique images, filtering out unresolvable entries
import re

def is_valid_image(img):
    """Filter out images that can't be pulled/scanned."""
    if '$' in img or '${' in img:
        return False
    if img.startswith('--'):
        return False
    if '{{' in img or '}}' in img:
        return False
    if '/' not in img and ':' not in img:
        return False
    if '/' not in img and ':' in img:
        name = img.split(':')[0]
        if name in ('alpine', 'redis', 'nginx', 'postgres', 'ubuntu', 'debian', 'busybox', 'node', 'golang', 'python'):
            return True
        return False
    return True

images = set()
skipped = 0
for repo in repos:
    for img in repo.get("dockerfile_images", []):
        if is_valid_image(img):
            images.add(img)
        else:
            skipped += 1
    for img in repo.get("helm_images", []):
        if is_valid_image(img):
            images.add(img)
        else:
            skipped += 1
    for pkg in repo.get("packages", []):
        if is_valid_image(pkg["image"]):
            images.add(pkg["image"])
        else:
            skipped += 1

images_file = os.path.join(workspace, "images-to-scan.txt")
with open(images_file, "w") as f:
    for img in sorted(images):
        f.write(img + "\n")

print(f"Total repos: {len(repos)}")
print(f"Total Go dependencies: {sum(len(r.get('go_deps', [])) for r in repos)}")
print(f"Total unique images to scan: {len(images)} ({skipped} invalid entries filtered)")
PYEOF

echo "==> Discovery complete: $OUTFILE"

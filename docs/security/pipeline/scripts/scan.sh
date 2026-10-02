#!/usr/bin/env bash
# scan.sh — Run Trivy scans on discovered repos and images.
# Reads discovery output, produces workspace/scan-results.json
set -euo pipefail

WORKSPACE="${1:-workspace}"
DISCOVERY="$WORKSPACE/discovery.json"
IMAGES_FILE="$WORKSPACE/images-to-scan.txt"
CLONE_DIR="$WORKSPACE/repos"
RESULTS_DIR="$WORKSPACE/scan-results"
OUTFILE="$WORKSPACE/scan-results.json"

# The Phase-3 merge runs as a heredoc Python subprocess and reads these paths from
# the environment; a subprocess only inherits EXPORTED variables. Without this the
# merge silently falls back to its hardcoded workspace/... defaults, so any run
# invoked with a non-default WORKSPACE would merge the wrong directory.
export WORKSPACE DISCOVERY IMAGES_FILE CLONE_DIR RESULTS_DIR OUTFILE

mkdir -p "$RESULTS_DIR"

if [ ! -f "$DISCOVERY" ]; then
  echo "ERROR: $DISCOVERY not found. Run discover.sh first." >&2
  exit 1
fi

# Check trivy is available
if ! command -v trivy &>/dev/null; then
  echo "ERROR: trivy not found. Install it: https://aquasecurity.github.io/trivy/" >&2
  exit 1
fi

echo "==> Starting vulnerability scans..."
SCAN_FAILURES=0

# --- Phase 1: Scan repos (go.mod, Dockerfiles, lock files) ---

REPO_NAMES=$(python3 -c "
import json
with open('$DISCOVERY') as f:
    data = json.load(f)
for r in data['repos']:
    name = r['repo'].split('/')[-1]
    # Only scan repos that have dependency files
    if r.get('go_deps') or r.get('dockerfile_images'):
        print(name)
")

for repo_name in $REPO_NAMES; do
  repo_dir="$CLONE_DIR/$repo_name"
  result_file="$RESULTS_DIR/repo-$repo_name.json"

  if [ ! -d "$repo_dir" ]; then
    echo "   WARN: $repo_dir not found, skipping"
    continue
  fi

  echo "   Scanning repo: $repo_name..."
  trivy fs "$repo_dir" \
    --format json \
    --severity CRITICAL,HIGH,MEDIUM,LOW \
    --scanners vuln \
    --quiet \
    > "$result_file" 2>"$result_file.err" || {
      echo "   ERROR: trivy fs failed for $repo_name (see $result_file.err)" >&2
      # Mark the target as ERRORED, not clean — a failed scan must not be merged
      # into the aggregate as "no findings".
      echo '{"__scan_error__": true, "Results": []}' > "$result_file"
      SCAN_FAILURES=$((SCAN_FAILURES + 1))
    }
done

# --- Phase 2: Scan Docker images ---

if [ -f "$IMAGES_FILE" ]; then
  total_images=$(wc -l < "$IMAGES_FILE" | tr -d ' ')
  echo "==> Scanning $total_images Docker images..."
  current=0

  while IFS= read -r image; do
    [ -z "$image" ] && continue
    current=$((current + 1))

    # Sanitize image name for filename
    safe_name=$(echo "$image" | sed 's|[/:]|_|g')
    result_file="$RESULTS_DIR/image-$safe_name.json"

    echo "   [$current/$total_images] Scanning image: $image..."
    trivy image "$image" \
      --format json \
      --severity CRITICAL,HIGH,MEDIUM,LOW \
      --scanners vuln \
      --quiet \
      --timeout 5m \
      > "$result_file" 2>"$result_file.err" || {
        echo "   ERROR: trivy image failed for $image (see $result_file.err)" >&2
        echo '{"__scan_error__": true, "Results": []}' > "$result_file"
        SCAN_FAILURES=$((SCAN_FAILURES + 1))
      }
  done < "$IMAGES_FILE"
fi

if [ "$SCAN_FAILURES" -gt 0 ]; then
  # Do NOT abort here. On production data a fixed set of references is unscannable
  # by construction (Helm values parsed into names carrying no registry) and fails
  # every run; aborting before the merge would stop all CVE reporting for the whole
  # org, every scheduled run, until someone fixes them. Instead merge what
  # succeeded, record the failure count in scan-results.json, and let report.py
  # fail the run at the very end — so it still goes red without discarding the
  # aggregate (including the CRITICALs) that was collected.
  echo "WARNING: $SCAN_FAILURES scan target(s) failed — the aggregate will be marked INCOMPLETE." >&2
  echo "         Investigate the *.err files in $RESULTS_DIR." >&2
fi

# --- Phase 3: Merge all results into one file ---

echo "==> Merging scan results..."

python3 << 'PYEOF'
import json, os, sys, re

results_dir = os.environ.get("RESULTS_DIR", "workspace/scan-results")
discovery_file = os.environ.get("DISCOVERY", "workspace/discovery.json")
outfile = os.environ.get("OUTFILE", "workspace/scan-results.json")

# Load discovery data to map images -> repos
with open(discovery_file) as f:
    discovery = json.load(f)

# Build image -> repos mapping
image_to_repos = {}
# Track which images are build/dev only (golang, node, etc. in Dockerfiles)
import re
DEV_IMAGE_RE = re.compile(r'^(--platform=\S+\s+)?(docker\.io/)?(library/)?(golang|node|python|rust|maven|gradle)[:/]', re.I)

dev_only_images = set()
for repo in discovery["repos"]:
    repo_name = repo["repo"]
    for img in repo.get("dockerfile_images", []):
        image_to_repos.setdefault(img, set()).add(repo_name)
        if DEV_IMAGE_RE.match(img):
            dev_only_images.add(img)
    for img in repo.get("helm_images", []):
        image_to_repos.setdefault(img, set()).add(repo_name)
    for pkg in repo.get("packages", []):
        image_to_repos.setdefault(pkg["image"], set()).add(repo_name)
        # Also track the specific component path
        image_to_repos.setdefault(pkg["image"], set()).add(
            f'{repo_name}:{pkg["component"]}'
        )
        # Mark testing components as dev
        if any(p in pkg["component"] for p in ("testing", "hack/", "test/", "e2e/")):
            dev_only_images.add(pkg["image"])

all_vulns = {}  # CVE-ID -> details
scan_errors = 0  # targets that failed to scan (marker files), carried into the output

for filename in sorted(os.listdir(results_dir)):
    if not filename.endswith(".json"):
        continue

    filepath = os.path.join(results_dir, filename)
    try:
        with open(filepath) as f:
            data = json.load(f)
    except (json.JSONDecodeError, FileNotFoundError):
        continue

    # A failed scan writes {"__scan_error__": true, "Results": []}. Count it and do
    # NOT merge it as a clean target — a failed scan is not "zero findings".
    if data.get("__scan_error__"):
        scan_errors += 1
        print(f"   WARN: skipping errored scan result {filename}", file=sys.stderr)
        continue

    # Determine source (repo name or image name)
    source = filename.replace(".json", "")
    if source.startswith("repo-"):
        source_type = "repo"
        source_name = source[5:]  # strip "repo-"
    elif source.startswith("image-"):
        source_type = "image"
        source_name = source[6:]  # strip "image-"
    else:
        continue

    results = data.get("Results", [])
    for result in results:
        target = result.get("Target", "")
        vuln_type = result.get("Type", "")
        for vuln in result.get("Vulnerabilities", []):
            cve_id = vuln.get("VulnerabilityID", "")
            if not cve_id:
                continue

            severity = vuln.get("Severity", "UNKNOWN")
            pkg_name = vuln.get("PkgName", "")
            installed_ver = vuln.get("InstalledVersion", "")
            fixed_ver = vuln.get("FixedVersion", "")
            title = vuln.get("Title", "")
            description = vuln.get("Description", "")[:500]
            refs = vuln.get("References", [])[:5]
            cvss_score = ""
            if vuln.get("CVSS"):
                for provider, scores in vuln["CVSS"].items():
                    if "V3Score" in scores:
                        cvss_score = str(scores["V3Score"])
                        break

            # Determine affected repos and whether this source is dev-only
            affected_repos = set()
            source_is_dev = False
            if source_type == "repo":
                affected_repos.add(f"cozystack/{source_name}")
            else:
                # Map image back to repos
                for img, repos in image_to_repos.items():
                    img_safe = re.sub(r'[/:]', '_', img)
                    if img_safe == source_name:
                        affected_repos.update(repos)
                        if img in dev_only_images:
                            source_is_dev = True

            if cve_id not in all_vulns:
                all_vulns[cve_id] = {
                    "cve_id": cve_id,
                    "severity": severity,
                    "cvss_score": cvss_score,
                    "package": pkg_name,
                    "installed_version": installed_ver,
                    "fixed_version": fixed_ver,
                    "title": title,
                    "description": description,
                    "references": refs,
                    "affected_repos": sorted(affected_repos),
                    "source_type": source_type,
                    "dev_only": source_is_dev,
                }
            else:
                # Merge affected repos
                existing = all_vulns[cve_id]
                existing["affected_repos"] = sorted(
                    set(existing["affected_repos"]) | affected_repos
                )
                # If any non-dev source contributes, mark as not dev-only
                if not source_is_dev:
                    existing["dev_only"] = False
                # Prefer higher severity
                sev_order = {"CRITICAL": 4, "HIGH": 3, "MEDIUM": 2, "LOW": 1, "UNKNOWN": 0}
                if sev_order.get(severity, 0) > sev_order.get(existing["severity"], 0):
                    existing["severity"] = severity

# Sort by severity
sev_order = {"CRITICAL": 0, "HIGH": 1, "MEDIUM": 2, "LOW": 3, "UNKNOWN": 4}
vulns_list = sorted(all_vulns.values(), key=lambda v: (sev_order.get(v["severity"], 9), v["cve_id"]))

output = {
    "scan_date": __import__("datetime").datetime.utcnow().isoformat() + "Z",
    "total_vulnerabilities": len(vulns_list),
    "scan_errors": scan_errors,
    "by_severity": {},
    "vulnerabilities": vulns_list,
}
for v in vulns_list:
    sev = v["severity"]
    output["by_severity"][sev] = output["by_severity"].get(sev, 0) + 1

with open(outfile, "w") as f:
    json.dump(output, f, indent=2)

print(f"Total unique CVEs: {len(vulns_list)}")
for sev in ["CRITICAL", "HIGH", "MEDIUM", "LOW"]:
    count = output["by_severity"].get(sev, 0)
    if count:
        print(f"  {sev}: {count}")
if scan_errors:
    print(f"INCOMPLETE: {scan_errors} target(s) failed to scan (recorded as scan_errors)")
PYEOF

echo "==> Scan complete: $OUTFILE"

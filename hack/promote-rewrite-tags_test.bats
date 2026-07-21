#!/usr/bin/env bats
# Tests for hack/promote-rewrite-tags.sh — the rc->stable tag string rewrite.
#
# Guards the regression that shipped a half-promoted release: the rewrite lived
# inline in .github/workflows/promote-rc.yaml and globbed the depth-2
# values.yaml plus packages/apps/kubernetes/images/*.tag alone, on the premise
# that the kubernetes app was the only one whose .tag files carry the cozystack
# version. Nine other .tag files and one stamped template do, so v1.6.0 was
# staged with 33 refs still reading v1.6.0-rc.4. Being workflow-inline, there
# was nothing to unit test and the miss was only observable by cutting a
# release.
#
# The central test therefore round-trips against the REAL tree and — crucially —
# discovers ref-bearing files BY CONTENT rather than by asking the enumeration
# under test where to look. A fixture built from image_ref_files() could only
# ever confirm that the enumeration agrees with itself; grepping for the
# registry prefix finds files it has never heard of, which is the whole failure
# mode. Stamp wider than the rewrite, then assert the rewrite cleaned all of it.
#
# Harness note: the CI path is hack/cozytest.sh, NOT real bats. cozytest.sh's
# awk parser recognizes only @test blocks and a bare `}` on its own line; there
# is no `run`, `$status`, `$output`, `skip`, or setup()/teardown(). Each test
# runs as a shell function under `set -eu -x`, so a non-zero exit aborts the
# test (that is the exit-0 assertion). A test that expects a non-zero exit must
# capture it with `|| rc=$?` so the harness's `set -e` does not abort first.
# Paths are repo-root-relative: BATS_TEST_DIRNAME is unset here and would abort
# the whole suite under `set -u`.
#
# Run with: hack/cozytest.sh hack/promote-rewrite-tags_test.bats

@test "rewrite leaves no rc reference anywhere in the tree" {
  tmp=$(mktemp -d)
  RC=9.9.9-rc.9

  # Build the fixture from files discovered by CONTENT, independent of
  # hack/lib/image-refs.sh. Excluding charts/ mirrors the script's own scope:
  # vendored upstream chart values are not stamped by the build.
  # --exclude='*.md' keeps documentation examples (which carry placeholder or
  # historical versions) out of the fixture, matching the postcondition.
  # Match 'cozystack/cozystack' WITHOUT a host prefix or trailing slash. The
  # host is not always contiguous with the repository: keycloak-operator splits
  # it into a sibling `registry: ghcr.io` + `repository:
  # cozystack/cozystack/keycloak-operator`, and kubeovn puts it in
  # `global.registry.address: ghcr.io/cozystack/cozystack` with `repository:
  # kubeovn`. A 'ghcr\.io/cozystack/cozystack/' pattern silently skips both
  # files, shrinking the fixture and under-testing the split shapes.
  for f in $(grep -rIl --exclude-dir=charts --exclude='*.md' 'cozystack/cozystack' packages/); do
    mkdir -p "$tmp/$(dirname "$f")"
    cp "$f" "$tmp/$f"
  done

  # Stamp a synthetic rc version onto every VERSION-LINE ref, modelling what an
  # rc build actually produces. The tag must be exactly vX.Y.Z immediately
  # followed by @, which is the shape the build stamps; that anchor is what
  # keeps the fixture faithful rather than merely aggressive:
  #   - kamaji's v0.19.0-cozystack.0@ does not match (a '-' follows the patch),
  #     and must not — it is component-versioned and no promotion rewrites it
  #   - a ':<chart-version>' placeholder in a Dockerfile comment does not match
  #     (no digest follows), and must not — nothing ever stamps a version there
  # Stamping either would make the test demand a rewrite that would itself be a
  # bug, and both were flagged by the postcondition when this sed was looser.
  # Stamp both the combined form and the split form (a bare `tag:` key whose
  # value is a version, as cilium and kubeovn write it), so the fixture
  # exercises every shape the enumeration claims to cover rather than only the
  # single-string one.
  for f in $(grep -rIl 'cozystack/cozystack' "$tmp/packages"); do
    sed -i -E "s|(cozystack/cozystack/[A-Za-z0-9._-]+):v[0-9]+\.[0-9]+\.[0-9]+@|\1:v${RC}@|g" "$f"
    sed -i -E "s|^([[:space:]]*tag:[[:space:]]*)v[0-9]+\.[0-9]+\.[0-9]+([[:space:]]*)$|\1v${RC}\2|" "$f"
  done

  # Sanity: the fixture must actually contain the rc string, otherwise the
  # assertion below passes vacuously and the test guards nothing.
  before=$(grep -rIl -- "$RC" "$tmp/packages" | wc -l | tr -d ' ')
  [ "$before" -gt 0 ]

  hack/promote-rewrite-tags.sh "$RC" 9.9.9 "$tmp/packages" >"$tmp/log" 2>&1 || {
    echo "--- promote-rewrite-tags.sh output ---" >&2; cat "$tmp/log" >&2; rm -rf "$tmp"; return 1
  }

  after=$(grep -rIl -- "$RC" "$tmp/packages" | wc -l | tr -d ' ')
  if [ "$after" -ne 0 ]; then
    echo "files still carrying $RC after the rewrite:" >&2
    grep -rIl -- "$RC" "$tmp/packages" >&2
    rm -rf "$tmp"
    return 1
  fi
  echo "stamped $before files, all rewritten"
  rm -rf "$tmp"
}

@test "rewrite covers .tag files outside packages/apps/kubernetes" {
  # The specific blind spot that shipped. Pinned as its own test so a future
  # narrowing of the glob fails with a message naming the cause, rather than
  # only tripping the broad round-trip above.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/packages/system/monitoring/images"
  echo 'ghcr.io/cozystack/cozystack/grafana:v9.9.9-rc.9@sha256:'"$(printf 'a%.0s' $(seq 64))" \
    > "$tmp/packages/system/monitoring/images/grafana.tag"

  hack/promote-rewrite-tags.sh 9.9.9-rc.9 9.9.9 "$tmp/packages" >/dev/null 2>&1 || {
    rm -rf "$tmp"; return 1
  }

  got=$(cat "$tmp/packages/system/monitoring/images/grafana.tag")
  case "$got" in
    *:v9.9.9@sha256:*) ;;
    *) echo "expected v9.9.9, got: $got" >&2; rm -rf "$tmp"; return 1 ;;
  esac
  rm -rf "$tmp"
}

@test "rewrite covers a ref stamped into a declared template" {
  # Storage shape 3 (IMAGE_REF_EXTRA_FILES). multus sed's its ref into a
  # vendored upstream manifest, so it is reachable by neither glob.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/packages/system/multus/templates"
  printf '          image: ghcr.io/cozystack/cozystack/multus-cni:v9.9.9-rc.9@sha256:%s\n' \
    "$(printf 'b%.0s' $(seq 64))" > "$tmp/packages/system/multus/templates/multus-daemonset-thick.yml"

  hack/promote-rewrite-tags.sh 9.9.9-rc.9 9.9.9 "$tmp/packages" >/dev/null 2>&1 || {
    rm -rf "$tmp"; return 1
  }

  grep -q ':v9.9.9@sha256:' "$tmp/packages/system/multus/templates/multus-daemonset-thick.yml" || {
    echo "multus template not rewritten" >&2; rm -rf "$tmp"; return 1
  }
  rm -rf "$tmp"
}

@test "component-versioned and third-party tags are left alone" {
  # kamaji is versioned by its upstream component (v0.19.0-cozystack.N) and
  # busybox is third-party; neither rides the cozystack version line, so a
  # promotion must not touch either. Rewriting them would be a bug introduced
  # by an over-eager fix to the one this file guards.
  #
  # The rc version is chosen to SHARE A PREFIX with kamaji's tag (0.19.0), so
  # the test is not vacuous: an implementation matching loosely on the X.Y.Z
  # part, or anchoring on anything short of the full "X.Y.Z-rc.N" string, would
  # corrupt v0.19.0-cozystack.0 here and fail. An earlier revision used an rc
  # version sharing no substring with either input, which made the assertion
  # true by construction and tested nothing.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/packages/system/capi/images" "$tmp/packages/apps/kubernetes/images"
  kamaji='ghcr.io/cozystack/cozystack/cluster-api-control-plane-provider-kamaji:v0.19.0-cozystack.0@sha256:c'
  busybox='docker.io/library/busybox:1.37.0@sha256:d'
  echo "$kamaji" > "$tmp/packages/system/capi/images/kamaji.tag"
  echo "$busybox" > "$tmp/packages/apps/kubernetes/images/busybox.tag"

  hack/promote-rewrite-tags.sh 0.19.0-rc.1 0.19.0 "$tmp/packages" >/dev/null 2>&1 || {
    rm -rf "$tmp"; return 1
  }

  [ "$(cat "$tmp/packages/system/capi/images/kamaji.tag")" = "$kamaji" ]
  [ "$(cat "$tmp/packages/apps/kubernetes/images/busybox.tag")" = "$busybox" ]
  rm -rf "$tmp"

  # Known and accepted limitation, stated here so it is a decision rather than
  # an oversight: the rewrite is a substring replace scoped to enumerated
  # files, so a third-party or component-versioned tag carrying the EXACT
  # cozystack rc string would also be rewritten. No such collision exists (a
  # component tag is X.Y.Z-cozystack.N and third-party tags are upstream's),
  # and making the rewrite ownership-aware is not possible with a substring
  # pass while the host may live in a sibling `registry:` key.
}

@test "a legitimate rc mention outside image position does not fail promotion" {
  # The fail-OPEN direction. Every other postcondition test asserts it fires;
  # this asserts it does not fire on prose, lockfiles or dependency pins that
  # merely contain an "X.Y.Z-rc.N" string.
  #
  # These are real: packages/apps/kubernetes/images/kubevirt-csi-driver/go.sum
  # pins github.com/golang/protobuf v1.4.0-rc.2, and v1.4.0-rc.2 is a cozystack
  # rc that was actually cut — an unfiltered postcondition would have aborted
  # that promotion on a Go checksum line. metallb_test.yaml names v1.5.0-rc.2
  # in a comment, and the console pnpm-lock.yaml carries rolldown@1.0.0-rc.15.
  tmp=$(mktemp -d)

  # A fresh copy per version. Reusing one tree would run iterations 2 and 3
  # against a tree the previous rewrite already mutated, so each would be
  # testing a slightly different input than the one it names.
  for v in 1.4.0-rc.2 1.5.0-rc.2 1.4.0-rc.4; do
    rm -rf "$tmp/packages"
    cp -a packages "$tmp/packages"
    rc=0
    hack/promote-rewrite-tags.sh "$v" "${v%%-*}" "$tmp/packages" >"$tmp/log" 2>&1 || rc=$?
    if [ "$rc" -ne 0 ]; then
      echo "false positive: promotion of $v was aborted by the postcondition" >&2
      cat "$tmp/log" >&2
      rm -rf "$tmp"
      return 1
    fi
  done
  rm -rf "$tmp"
}

@test "an unreadable file fails the promotion rather than being skipped" {
  # A read error is not "no match". Skipping it would leave a ref unrewritten
  # while the run still reported success — the same silent-skip shape as the
  # enumeration bug this script exists to fix.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/packages/system/x/images"
  echo 'ghcr.io/cozystack/cozystack/x:v9.9.9-rc.9@sha256:a' > "$tmp/packages/system/x/images/x.tag"
  chmod 000 "$tmp/packages/system/x/images/x.tag"

  rc=0
  hack/promote-rewrite-tags.sh 9.9.9-rc.9 9.9.9 "$tmp/packages" >"$tmp/log" 2>&1 || rc=$?

  # Restore the mode first so the cleanup and any harness diagnostics work.
  chmod 644 "$tmp/packages/system/x/images/x.tag"
  if [ "$rc" -eq 0 ]; then
    echo "expected a non-zero exit for an unreadable file, got 0" >&2
    rm -rf "$tmp"; return 1
  fi
  grep -q 'is not readable' "$tmp/log" || {
    echo "expected the unreadable-file diagnostic; got:" >&2; cat "$tmp/log" >&2
    rm -rf "$tmp"; return 1
  }
  rm -rf "$tmp"
}

@test "collect_image_refs emits every first-party ref present in the tree" {
  # The completeness guard the collector lacked. The rewrite got "scan wider
  # than you rewrite" via its postcondition; the retag and the mirror got a
  # wider enumeration and nothing that would notice a NEW blind spot.
  #
  # Without this, a first-party ref added at an unenumerated path on a tag
  # carrying no version string (flux-plunger and keycloak-operator are
  # latest@-pinned today, so the profile exists) is never mirrored, and the
  # published nightly points at the private build registry — which the
  # contract calls the worst of the three failure modes.
  #
  # The oracle applies the SAME shape knowledge (collect_refs_from_file) to an
  # INDEPENDENTLY discovered file list. That is the right axis: what drifted
  # historically is which files each consumer visits, not how a ref is spelled
  # inside one. Shape coverage is pinned separately by the per-shape test
  # below, so DELETING a shape still fails something even though both sides of
  # this comparison would move together.
  #
  # Deleting a shape, and also narrowing one: the per-shape test asserts the
  # exact canonical repo@digest, so a shape that loses its `registry` rejoin
  # (the very regression shape 3 was added for) fails there too. What this
  # comparison itself contributes is only file coverage — for shape behaviour
  # it is inert, since both sides share the parser.
  #
  # An earlier revision derived the expected set from a grep for a CONTIGUOUS
  # 'ghcr.io/cozystack/cozystack/...@sha256:' string. That was blind to every
  # split-host shape — cilium, keycloak-operator, kubeovn, the OCI artifact and
  # seven others, 12 of 43 first-party refs — including keycloak-operator,
  # which this test's own rationale names as the motivating profile.
  . hack/lib/image-refs.sh
  tmp=$(mktemp -d)

  canon() { sed -E 's|^[^ ]*=||; s|:[^/@]*@|@|' | grep '^ghcr\.io/cozystack/cozystack/' | sort -u; }

  collect_image_refs packages | canon > "$tmp/collected"
  # Pre-filter to files that mention the registry at all before parsing them.
  # Parsing every YAML under packages/ costs ~30s; this is the same file set
  # for the purpose of finding FIRST-PARTY refs (canon discards everything
  # else anyway) and runs in about a second. The marker covers the split forms
  # too, so keycloak-operator and kubeovn are not filtered out.
  grep -rIlE --exclude-dir=charts --exclude='*.md' \
    -e 'cozystack/cozystack' packages/ \
    | grep -E '\.(yaml|yml|tag)$' \
    | while IFS= read -r f; do collect_refs_from_file "$f"; done | canon > "$tmp/present"

  missed=$(comm -13 "$tmp/collected" "$tmp/present")
  if [ -n "$missed" ]; then
    echo "first-party refs present in the tree but NOT collected:" >&2
    printf '%s\n' "$missed" >&2
    echo "Declare their file in IMAGE_REF_EXTRA_FILES in hack/lib/image-refs.sh." >&2
    rm -rf "$tmp"
    return 1
  fi
  rm -rf "$tmp"
}

@test "every file carrying a first-party ref marker is enumerated or allowlisted" {
  # File-level backstop for the class no YAML oracle can reach: a ref inside a
  # file yq cannot parse. Helm templates are full of {{ }} and fail to parse
  # entirely, so a first-party ref planted in one is invisible to the
  # shape-based completeness check above — that is exactly how a reviewer's
  # split-host fixture survived every test.
  #
  # The markers are keyed to the JOIN SEMANTICS _collect_yaml_shapes
  # implements, not to the exact spellings that happen to be in the tree today.
  # An earlier revision keyed them to the latter and was evaded three ways, each
  # a shape the collector fully supports: `registry: ghcr.io/cozystack/cozystack`
  # + `repository: <name>` (kubeovn's layout under the key name keycloak-operator
  # and ingress-nginx use — marker 1 needed a trailing slash a complete-value
  # host does not have), `registry: ghcr.io/cozystack` + `repository:
  # cozystack/<name>` (a host+org split, live precedent at
  # packages/system/fluxcd/values.yaml), and a single-quoted `repository:`
  # (the old marker admitted only double quotes). All three carried a
  # version-free `latest@sha256:` tag — the exact profile this check exists for
  # — and passed the entire suite.
  #
  # Coverage is in fact exhaustive, not merely broad. The collector joins
  # `registry + "/" + repository`, so a split can only fall on a path-separator
  # boundary of the full ref, giving four possible splits — and markers 1-3
  # cover all four. A split at any other point (say `registry: ghcr.io/cozy` +
  # `repository: stack/cozystack/x`) does not need covering because it does not
  # produce a first-party ref: it joins to ghcr.io/cozy/stack/cozystack/x,
  # which every consumer's ownership filter discards anyway.
  #
  # Widening further would cost real precision for nothing: a bare
  # `cozystack/cozystack` marker matches 65 files and would need 23 allowlist
  # entries for Makefiles, Dockerfiles and helm-unittest fixtures.
  . hack/lib/image-refs.sh
  tmp=$(mktemp -d)

  image_ref_files packages | sort > "$tmp/enumerated"
  grep -rIlE --exclude-dir=charts --exclude='*.md' \
    -e 'ghcr\.io/cozystack/cozystack/' \
    -e "['\"]?(repository|image)['\"]?[[:space:]]*:[[:space:]]*['\"]?cozystack/" \
    -e "['\"]?(registry|address)['\"]?[[:space:]]*:[[:space:]]*['\"]?ghcr\.io/cozystack" \
    packages/ | sort > "$tmp/marked"

  # Known carriers that are deliberately NOT enumerated. Each needs a reason;
  # this list is the place a new exception has to be argued for, rather than
  # discovered during a release.
  #   - talos-csr-signer/Dockerfile: a ':<chart-version>' placeholder in build
  #     instructions, not a runtime ref — nothing stamps a version there.
  #   - capi-providers-cpprovider/files/control-plane-components.yaml: the
  #     documented gzip gap. The chart ships files/components.gz built from it,
  #     so rewriting only this copy would diverge the two. kamaji is
  #     component-versioned, so no tag rewrite is owed.
  cat > "$tmp/allow" <<'ALLOW'
packages/apps/kubernetes/images/talos-csr-signer/Dockerfile
packages/system/capi-providers-cpprovider/files/control-plane-components.yaml
ALLOW
  sort -o "$tmp/allow" "$tmp/allow"

  unexplained=$(comm -13 "$tmp/enumerated" "$tmp/marked" | comm -13 "$tmp/allow" -)
  if [ -n "$unexplained" ]; then
    echo "files carry a first-party image ref but are neither enumerated nor allowlisted:" >&2
    printf '%s\n' "$unexplained" >&2
    echo "Declare each in IMAGE_REF_EXTRA_FILES (hack/lib/image-refs.sh), or add it to the" >&2
    echo "allowlist in this test WITH A REASON if it is not a runtime image ref." >&2
    rm -rf "$tmp"
    return 1
  fi
  rm -rf "$tmp"
}

@test "collect_refs_from_file understands every YAML shape" {
  # Pins shape coverage directly, so deleting a shape fails here even though
  # the completeness oracle above shares this code and would move with it.
  . hack/lib/image-refs.sh
  tmp=$(mktemp -d)
  D=$(printf 'a%.0s' $(seq 64))

  printf 'image: ghcr.io/cozystack/cozystack/one:v1@sha256:%s\n' "$D" > "$tmp/s1.yaml"
  # Tagless: a digest-only pin is an ordinary Helm spelling. Narrowing shape 1
  # to require a ':tag' before the digest was otherwise undetected.
  printf 'image: ghcr.io/cozystack/cozystack/onebare@sha256:%s\n' "$D" > "$tmp/s1b.yaml"
  # Shape 2 WITH a sibling registry: the previous fixture kept the host inside
  # repository, so shape 2 losing its registry rejoin was invisible here and
  # caught only by a single nightly-mirror fixture (no real package uses shape
  # 2 with a split host, so promote-retag misses it too).
  printf 'image:\n  registry: ghcr.io\n  repository: cozystack/cozystack/two\n  tag: v1\n  digest: "sha256:%s"\n' "$D" > "$tmp/s2.yaml"
  printf 'image:\n  registry: ghcr.io\n  repository: cozystack/cozystack/three\n  tag: "v1@sha256:%s"\n' "$D" > "$tmp/s3.yaml"
  # Shape 4 carries a digest-pinned sibling OUTSIDE global.images. The rule is
  # deliberately scoped to .global.images[] because the host is a
  # document-level key: binding it to any map in the file would staple
  # kubeovn's registry onto unrelated repositories and manufacture owned refs
  # that were never built. Broadening the rule to `..` otherwise passed every
  # suite, because asserting only that the expected ref appears cannot see an
  # unexpected one — hence the negative assertion below.
  # Two digest-pinned decoys, deliberately at different depths: one INSIDE
  # `global` but outside `global.images`, one outside `global` entirely.
  # Narrowing the negative to the outer decoy alone is not enough — mutating
  # the rule to `.global | ..` (rather than a document-wide `..`) then still
  # passes, while happily stapling the registry onto the inner map.
  printf 'global:\n  registry:\n    address: ghcr.io/cozystack/cozystack\n  images:\n    - repository: four\n      tag: "v1@sha256:%s"\n  sidecar:\n    repository: someone-elses/inner\n    tag: "v1@sha256:%s"\nunrelated:\n  image:\n    repository: someone-elses/thing\n    tag: "v1@sha256:%s"\n' "$D" "$D" "$D" > "$tmp/s4.yaml"
  printf 'x:\n  platformSourceUrl: oci://ghcr.io/cozystack/cozystack/five\n  platformSourceRef: digest=sha256:%s\n' "$D" > "$tmp/s5.yaml"
  printf 'ghcr.io/cozystack/cozystack/six:v1@sha256:%s\n' "$D" > "$tmp/s6.tag"

  # Assert the EXACT canonical repo@digest, not a repository substring. A
  # substring match leaves the digest untested, and since the completeness
  # oracle shares this code, both sides of that comparison move together when
  # the digest handling breaks: corrupting the last hex digit of every emitted
  # digest previously left the rewrite, retag and mirror suites entirely green.
  # The digest is the only part that decides which bytes get retagged, so it is
  # the part most worth pinning.
  for n in one onebare two three four five six; do
    case "$n" in one) f=s1.yaml ;; onebare) f=s1b.yaml ;; two) f=s2.yaml ;;
                 three) f=s3.yaml ;; four) f=s4.yaml ;; five) f=s5.yaml ;;
                 six) f=s6.tag ;; esac
    want="ghcr.io/cozystack/cozystack/${n}@sha256:${D}"
    # Canonicalize away the cosmetic :tag that shapes 1 and 6 carry through.
    got=$(collect_refs_from_file "$tmp/$f" | sed -E 's|:[^/@]*@|@|' | grep "cozystack/${n}@" || true)
    if [ "$got" != "$want" ]; then
      echo "shape for '$n' ($f):" >&2
      echo "  want: $want" >&2
      echo "  got:  ${got:-<nothing>}" >&2
      rm -rf "$tmp"; return 1
    fi
  done

  # Negative: the document-level host must not be stapled onto a map outside
  # global.images. Asserting only that the expected ref appears cannot catch
  # over-collection, and a manufactured "owned" ref would be retagged and
  # mirrored to a destination nothing ever built.
  if collect_refs_from_file "$tmp/s4.yaml" | grep -q 'cozystack/someone-elses'; then
    echo "shape 4 stapled the global registry onto a map outside global.images:" >&2
    collect_refs_from_file "$tmp/s4.yaml" >&2
    rm -rf "$tmp"; return 1
  fi
  rm -rf "$tmp"
}

@test "a declared extra file actually yields its ref, not just its filename" {
  # image_ref_files listing a path proves only that the path is visited.
  # Declaring an extra whose ref the parser cannot reach leaves it enumerated
  # but uncollected, and both completeness checks pass — the marker check
  # compares filenames, and the oracle shares the same parser. Assert the ref
  # itself comes out.
  #
  # The fixture is an unparseable Helm template — the case parsing alone cannot
  # reach — carrying a contiguous ref, single-quoted so the scrape's
  # quote-tolerance is exercised too.
  #
  # Two things this test got wrong before, both of which made it pass while the
  # thing it guards was broken. Its fixture was a SPLIT-host ref, which a
  # textual scrape can never reconstruct: only the contiguous `tag` value is
  # recoverable, so ref_repo() reduces it to `latest` and every consumer drops
  # it at the ownership filter — enumerated-but-uncollected, exactly the
  # conflation this test exists to catch. And it asserted `grep -q
  # "@sha256:$D"`, which any token containing the digest satisfies. Gutting the
  # scrape to emit a bare `@sha256:<digest>` with no repository left the whole
  # suite green. Assert the exact canonical ref instead.
  . hack/lib/image-refs.sh
  tmp=$(mktemp -d)
  D=$(printf 'b%.0s' $(seq 64))
  mkdir -p "$tmp/system/awkward/templates"
  {
    echo '{{- if .Values.enabled }}'
    echo 'spec:'
    printf "  image: 'ghcr.io/cozystack/cozystack/awkward:latest@sha256:%s'\n" "$D"
    echo '{{- end }}'
  } > "$tmp/system/awkward/templates/thing.yaml"

  IMAGE_REF_EXTRA_FILES="system/awkward/templates/thing.yaml"
  image_ref_files "$tmp" | grep -q 'system/awkward/templates/thing.yaml' || {
    echo "declared extra was not enumerated" >&2; rm -rf "$tmp"; return 1
  }
  if ! collect_image_refs "$tmp" | sed -E 's|:[^/@]*@|@|' \
       | grep -qx "ghcr.io/cozystack/cozystack/awkward@sha256:${D}"; then
    echo "declared extra was enumerated but yielded no usable ref:" >&2
    collect_image_refs "$tmp" >&2
    rm -rf "$tmp"; return 1
  fi
  rm -rf "$tmp"

  # Recorded limitation, so the comment above does not overclaim: a SPLIT-host
  # ref in an unparseable file is recoverable by neither route — parsing fails
  # on the template, and a scrape cannot rejoin `registry` to `repository`. The
  # file-level marker check is what covers that case, by refusing to let such a
  # file go undeclared in the first place.
}

@test "the cozystack-packages OCI artifact is collected" {
  # Shape 5 (platformSourceUrl/platformSourceRef). Deleting that rule left
  # every suite green, so stable promotion could silently stop retagging the
  # packages artifact — the one object the installer resolves to find
  # everything else.
  . hack/lib/image-refs.sh
  collect_image_refs packages | grep -q 'cozystack-packages@sha256:[0-9a-f]\{64\}'
}

@test "digests are never altered by the rewrite" {
  # Promotion retags by digest and must not rebuild; if the rewrite could touch
  # a digest, the stable image would stop being bit-for-bit the tested rc.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/packages/system/x/images"
  d=$(printf 'e%.0s' $(seq 64))
  echo "ghcr.io/cozystack/cozystack/x:v9.9.9-rc.9@sha256:$d" > "$tmp/packages/system/x/images/x.tag"

  hack/promote-rewrite-tags.sh 9.9.9-rc.9 9.9.9 "$tmp/packages" >/dev/null 2>&1 || {
    rm -rf "$tmp"; return 1
  }

  grep -q "@sha256:$d\$" "$tmp/packages/system/x/images/x.tag" || {
    echo "digest changed: $(cat "$tmp/packages/system/x/images/x.tag")" >&2
    rm -rf "$tmp"; return 1
  }
  rm -rf "$tmp"
}

@test "postcondition fails when a ref hides in an unenumerated location" {
  # The guard that makes a future blind spot loud instead of silent. A ref in a
  # location neither glob nor IMAGE_REF_EXTRA_FILES covers must abort the
  # promotion, not sail through as it did for v1.6.0.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/packages/system/surprise/deeply/nested"
  echo 'image: ghcr.io/cozystack/cozystack/surprise:v9.9.9-rc.9@sha256:f' \
    > "$tmp/packages/system/surprise/deeply/nested/manifest.yaml"

  rc=0
  hack/promote-rewrite-tags.sh 9.9.9-rc.9 9.9.9 "$tmp/packages" >"$tmp/log" 2>&1 || rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "expected a non-zero exit for an unenumerated ref, got 0" >&2
    rm -rf "$tmp"; return 1
  fi
  grep -q 'still carry the rc version' "$tmp/log" || {
    echo "expected the postcondition's message; got:" >&2; cat "$tmp/log" >&2
    rm -rf "$tmp"; return 1
  }
  rm -rf "$tmp"
}

@test "postcondition fires on a split tag/digest ref, not just an @sha256 one" {
  # Covers the postcondition's key-position branch specifically. Replacing the
  # whole pattern with the naive "${RC}@sha256:" — the narrowing that was
  # explicitly rejected because cilium keeps `tag:` and `digest:` under
  # separate keys — previously left every test green, so the branch that makes
  # the split shape visible was asserted by nothing.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/packages/system/surprise/files"
  {
    echo 'image:'
    echo '  repository: ghcr.io/cozystack/cozystack/surprise'
    echo '  tag: v9.9.9-rc.9'
    printf '  digest: "sha256:%s"\n' "$(printf 'a%.0s' $(seq 64))"
  } > "$tmp/packages/system/surprise/files/x.yaml"

  rc=0
  hack/promote-rewrite-tags.sh 9.9.9-rc.9 9.9.9 "$tmp/packages" >"$tmp/log" 2>&1 || rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "expected a non-zero exit for a split tag/digest ref at an unenumerated path, got 0" >&2
    cat "$tmp/log" >&2
    rm -rf "$tmp"; return 1
  fi
  grep -q 'still carry the rc version' "$tmp/log" || {
    echo "expected the postcondition diagnostic; got:" >&2; cat "$tmp/log" >&2
    rm -rf "$tmp"; return 1
  }
  rm -rf "$tmp"
}

@test "malformed versions are rejected before any file is touched" {
  # RC_VERSION becomes a sed pattern and STABLE_VERSION its replacement, so an
  # unvalidated argument is an injection surface as well as a correctness one.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/packages/system/x/images"
  echo 'ghcr.io/cozystack/cozystack/x:v1.0.0@sha256:a' > "$tmp/packages/system/x/images/x.tag"
  orig=$(cat "$tmp/packages/system/x/images/x.tag")

  rc=0
  hack/promote-rewrite-tags.sh 'not-a-version' 9.9.9 "$tmp/packages" >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ]

  rc=0
  hack/promote-rewrite-tags.sh 9.9.9-rc.9 'v9.9.9; rm -rf /' "$tmp/packages" >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ]

  # Nothing was modified by either rejected invocation.
  [ "$(cat "$tmp/packages/system/x/images/x.tag")" = "$orig" ]
  rm -rf "$tmp"
}

@test "image_ref_files enumerates all three storage shapes" {
  # Direct cover for the shared enumeration, so a regression there is
  # attributed to the library rather than surfacing only through its consumers.
  . hack/lib/image-refs.sh

  files=$(image_ref_files packages)
  echo "$files" | grep -q '^packages/system/objectstorage-controller/values.yaml$'
  echo "$files" | grep -q '^packages/system/monitoring/images/grafana.tag$'
  echo "$files" | grep -q '^packages/system/multus/templates/multus-daemonset-thick.yml$'

  # And it must not reach into vendored charts, whose values `make update`
  # regenerates and whose images the build neither pushes nor stamps.
  if echo "$files" | grep -q '/charts/'; then
    echo "enumeration leaked into a vendored charts/ subtree:" >&2
    echo "$files" | grep '/charts/' >&2
    return 1
  fi
}

@test "collect_image_refs finds refs the depth-2 values.yaml glob misses" {
  # The mirror and the retag both filter on ownership, so a ref the collector
  # never emits is silently skipped rather than failing. Assert the two shapes
  # that were missing are present in the real tree's collection.
  . hack/lib/image-refs.sh

  refs=$(collect_image_refs packages)
  echo "$refs" | grep -q 'cozystack/grafana@sha256:\|cozystack/grafana:[^ ]*@sha256:'
  echo "$refs" | grep -q 'multus-cni'
}

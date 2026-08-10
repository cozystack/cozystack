#!/usr/bin/env bats
# Contract checks for the CNI plugin staging that a helm-unittest case cannot
# make: they read files outside the chart, or render it more than one way.
#
# Run with: hack/cozytest.sh hack/cni-plugins-staging-contract.bats

# Keep the closing brace on the same line: hack/cozytest.sh rewrites any line
# that is exactly `}` into `return 0` plus `}`, without caring whether it closes
# a test, a helper or a plain brace group at file scope.
command -v yq >/dev/null || { echo "yq is required to read the vendored kube-ovn version" >&2; exit 1; }

# Is $2 one of the plugin names in the extract text $1? Separators differ by
# position in a continued shell line, so fold them to spaces and pad both ends
# -- `./bridge` must not be satisfied by `./bridgeport`.
extract_has() {
  case " $(printf '%s' "$1" | tr '\n;\\' '   ') " in
    *" ./$2 "*) return 0 ;;
  esac
  return 1
}

DOCKERFILE="$PWD/packages/system/multus/images/multus-cni/Dockerfile"
KUBEOVN_VALUES="$PWD/packages/system/kubeovn/values.yaml"
DOC="$PWD/docs/vm-external-vlan.md"
PLATFORM_VALUES="$PWD/packages/core/platform/values.yaml"
TEMPLATE="$PWD/packages/system/multus/templates/multus-daemonset-thick.yml"
PATCHFILE="$PWD/packages/system/multus/patches/customize-deployment.patch"

@test "membership in the extract list is decided the same way in every position" {
  middle=' \ ./bridge ./host-local ./vrf; \ '
  last=' \ ./bridge ./host-local ./loopback; \ '
  first=' \ ./loopback ./bridge ./host-local; \ '
  multiline=$(printf ' \\\n ./bridge ./host-local \\\n ./loopback; \\\n')

  extract_has "$middle" bridge      || { echo "FAIL: missed a name in the middle" >&2; return 1; }
  extract_has "$last" loopback      || { echo "FAIL: missed a name in last position" >&2; return 1; }
  extract_has "$first" loopback     || { echo "FAIL: missed a name in first position" >&2; return 1; }
  extract_has "$multiline" loopback || { echo "FAIL: missed a name across a line continuation" >&2; return 1; }

  if extract_has ' \ ./bridgeport ./host-local; \ ' bridge; then
    echo "FAIL: ./bridgeport satisfied a check for ./bridge" >&2
    return 1
  fi
  if extract_has ' \ ./host-local ./bridgeport; \ ' bridge; then
    echo "FAIL: ./bridgeport in last position satisfied a check for ./bridge" >&2
    return 1
  fi
  if extract_has "$middle" loopback; then
    echo "FAIL: reported a name that is not in the list" >&2
    return 1
  fi
}

@test "the Dockerfile stages the plugins and copies them into the final image" {
  grep -qE '^[[:space:]]*tar -xz -C /cni-plugins -f ' "$DOCKERFILE"

  # The COPY has to land in the LAST stage: one in an earlier stage satisfies
  # the same pattern while the image that ships carries no plugins at all, and
  # the init container then skips on every node.
  final_stage=$(awk '/^FROM/{buf=""} {buf = buf $0 ORS} END{printf "%s", buf}' "$DOCKERFILE")
  if [ -z "$final_stage" ]; then
    echo "no FROM in the Dockerfile" >&2
    return 1
  fi
  if ! printf '%s' "$final_stage" |
       grep -qE '^COPY[[:space:]]+--from=build[[:space:]]+/cni-plugins[[:space:]]+/cni-plugins[[:space:]]*$'; then
    echo "the final stage does not COPY /cni-plugins, so the plugins are built" >&2
    echo "and then left behind in the build stage." >&2
    grep -nE '^FROM|^COPY.*cni-plugins' "$DOCKERFILE" >&2
    return 1
  fi

  # And nothing after it may take them out again: the COPY being present says
  # nothing about the directory still existing when the build ends.
  after_copy=$(printf '%s' "$final_stage" |
    sed -n '/^COPY[[:space:]]\{1,\}--from=build[[:space:]]\{1,\}\/cni-plugins[[:space:]]/,$p')
  if printf '%s' "$after_copy" | grep -qE '^[^#]*\brm\b.*/cni-plugins'; then
    echo "something in the final stage removes /cni-plugins after it was" >&2
    echo "copied in, so the image ships without the plugins and the init" >&2
    echo "container skips on every node." >&2
    printf '%s' "$after_copy" | grep -nE '^[^#]*\brm\b.*/cni-plugins' >&2
    return 1
  fi

  # And nothing anywhere in the file may delete a staged plugin: the check above
  # only reads the tar arguments, so a later `rm -f /cni-plugins/bridge` in the
  # build stage ships an image with thirteen. The tarball itself is a different
  # path (/tmp/cni-plugins.tgz) and removing it is expected.
  if grep -qE '\brm\b[^#]*[^.[:alnum:]]/cni-plugins(/|[[:space:]]|;|$)' "$DOCKERFILE"; then
    echo "the Dockerfile removes something from /cni-plugins; whatever is" >&2
    echo "taken out there is missing from the image, and the init container" >&2
    echo "installs only what remains." >&2
    grep -nE '\brm\b[^#]*/cni-plugins' "$DOCKERFILE" >&2
    return 1
  fi

  # The pinned digest must still BE the pinned digest when the comparison runs.
  # Assigning ${sha256} from the downloaded file turns the verification into a
  # comparison of the download with itself, which any well-formed archive passes.
  assigns=$(grep -cE '^[[:space:]]*[^#]*\bsha256=' "$DOCKERFILE")
  pinned=$(grep -cE '^[[:space:]]+[a-z0-9]+\)[[:space:]]+sha256="[0-9a-f]{64}"' "$DOCKERFILE")
  if [ "$assigns" != "$pinned" ]; then
    echo "sha256 is assigned $assigns times but only $pinned of those are the" >&2
    echo "per-architecture pins. Anything else reassigns the digest before the" >&2
    echo "comparison, which then checks the download against itself." >&2
    grep -nE 'sha256=' "$DOCKERFILE" >&2
    return 1
  fi

  # The uid the init container runs as is decided in three places: the pod
  # securityContext, the container securityContext and the image's own USER.
  # The first two are asserted in packages/system/multus/tests/multus_test.yaml;
  # this is the third. A non-root USER in the final stage makes every write fail
  # while both Kubernetes levels still read as unset, and the script then
  # reports the failures, exits 0 and leaves the pod Ready with nothing staged.
  final_user=$(printf '%s' "$final_stage" | grep -E '^USER[[:space:]]' || true)
  case "$final_user" in
    "") ;;
    *"USER root"*|*"USER 0"*) ;;
    *)
      echo "the final stage sets a non-root USER:" >&2
      printf '%s\n' "$final_user" >&2
      echo "the init container writes into a hostPath and needs uid 0." >&2
      return 1 ;;
  esac

  # Only the plugins may land in /cni-plugins. The init container installs every
  # file it finds there and chmods it 0755, so anything else that arrives -- a
  # licence, a README, a stray COPY -- is published into the host CNI directory
  # as an executable. Measured: adding the licence to that directory makes the
  # script install 15 files with LICENSE among them.
  strays=$(grep -nE '^(COPY|ADD)[[:space:]].*[[:space:]]/cni-plugins(/|[[:space:]]*$)' "$DOCKERFILE" |
    grep -v 'COPY --from=build /cni-plugins /cni-plugins' || true)
  if [ -n "$strays" ]; then
    echo "something other than the plugin directory itself is copied into" >&2
    echo "/cni-plugins; the init container would install it on every node as" >&2
    echo "an executable CNI plugin." >&2
    printf '%s\n' "$strays" >&2
    return 1
  fi

  # Apache-2.0 binaries are redistributed here, so the licence ships with them.
  # Terminated on its own line for the same reason as the checksum: a command
  # chained after the `;` runs inside the same RUN and can undo it.
  grep -qE '^[[:space:]]*tar -xz -C /cni-plugins-licenses -f [^[:space:]]+ \./LICENSE;[[:space:]]*\\?[[:space:]]*$' "$DOCKERFILE"
  printf '%s' "$final_stage" |
    grep -qE '^COPY[[:space:]]+--from=build[[:space:]]+/cni-plugins-licenses[[:space:]]+/cni-plugins-licenses[[:space:]]*$'

  # Matched inside the extract's own continued line, so a mention elsewhere in
  # the file cannot satisfy it.
  extract=$(sed -n '/tar -xz -C \/cni-plugins -f /,/;/p' "$DOCKERFILE" | tr '\n' ' ')
  [ -n "$extract" ]
  for plugin in loopback dhcp dummy tap; do
    if extract_has "$extract" "$plugin"; then
        echo "packages/system/multus/images/multus-cni/Dockerfile now extracts" >&2
        echo "./$plugin, which this package deliberately does not stage, and" >&2
        echo "which docs/vm-external-vlan.md names as left alone." >&2
        if [ "$plugin" = loopback ]; then
          echo "The init container would then replace the node's loopback plugin" >&2
          echo "on every restart. The runtime calls loopback for every pod" >&2
          echo "sandbox, not just those using a NAD, so a bad copy costs the" >&2
          echo "whole node rather than one attachment -- and cilium and kube-ovn" >&2
          echo "already install it, so staging adds a writer for no gain." >&2
        elif [ "$plugin" = dhcp ]; then
          echo "dhcp is not usable from the binary alone: it needs a" >&2
          echo "long-running daemon and a socket on the host, which this" >&2
          echo "package does not run." >&2
        fi
        echo "Update the guide in the same commit if this is deliberate." >&2
        return 1
    fi
  done

  # Nothing may take the failure away: tar returns non-zero when a requested
  # member is absent from the archive, and `|| true` there ships an image
  # missing that plugin with a successful build. The range ends at the first
  # `;`, so a chain before it is inside what is checked.
  if printf '%s' "$extract" | grep -qE '\|\||&&|\|'; then
    echo "the plugin extraction is chained with another command, which takes" >&2
    echo "its failure away: a release archive missing one requested member" >&2
    echo "would then build a successful image without that plugin." >&2
    printf '%s\n' "$extract" >&2
    return 1
  fi

  # The staged set by NAME, not a count: a count is satisfied by one name
  # appearing twice while another is missing.
  staged=$(printf '%s' "$extract" | tr ' ' '\n' | sed -n 's|^\./||p' | tr -d ';' | sort)
  expected=$(printf '%s\n' bandwidth bridge firewall host-device host-local ipvlan \
    macvlan portmap ptp sbr static tuning vlan vrf | sort)
  if [ "$staged" != "$expected" ]; then
    echo "the set of plugins the Dockerfile stages has changed." >&2
    echo "staged now:" >&2
    printf '%s\n' "$staged" >&2
    echo "expected these 14:" >&2
    printf '%s\n' "$expected" >&2
    echo "docs/vm-external-vlan.md tells operators the package installs 14 of" >&2
    echo "the upstream reference plugins and names the four it leaves alone," >&2
    echo "so that sentence is now wrong. Update it in the same commit." >&2
    return 1
  fi

  for plugin in bridge macvlan ipvlan; do
    if ! extract_has "$extract" "$plugin"; then
        echo "packages/system/multus/images/multus-cni/Dockerfile no longer extracts" >&2
        echo "./$plugin into /cni-plugins, but docs/vm-external-vlan.md tells operators" >&2
        echo "the package stages it. The init container installs whatever is in that" >&2
        echo "directory and skips silently when it is absent, so a NAD naming" >&2
        echo "$plugin would go back to failing with" >&2
        echo "\"failed to find plugin \\\"$plugin\\\" in path [/opt/cni/bin]\"" >&2
        echo "on every node, with nothing else in this suite going red." >&2
        return 1
    fi
  done
}

@test "the tarball checksum is present, pinned, and precedes extraction" {
  # These are TEXT checks. PLATFORM defaults to empty in hack/common-envs.mk and
  # nothing in this repository sets it, so a plain build produces the runner's
  # arch as a single manifest; a caller passing PLATFORM gets an index, and both
  # per-arch digests below are then live.

  # One whole line, and it must TERMINATE. The `;` is what closes the family:
  # `|| true`, `&& :`, `| cat` (a pipeline's status is its last element and there
  # is no pipefail in POSIX sh), and `|| true` moved to the next physical line,
  # since an unterminated command must end in `\`. `${sha256}` in the comparison
  # is what stops the tarball being checked against itself.
  #
  # sha256sum accepts "HASH  FILE" and "HASH *FILE"; both are allowed here.
  # The file operand is taken from the curl line rather than left open: an
  # unpinned operand is satisfied by any path, so a decoy file with a pinned
  # digest passes the form while the downloaded tarball is extracted unchecked.
  tarball=$(grep -oE '[[:space:]]-o[[:space:]]+[^[:space:]]+' "$DOCKERFILE" | head -1 | awk '{print $2}')
  if [ -z "$tarball" ]; then
    echo "no 'curl ... -o <file>' in the Dockerfile, so there is no download" >&2
    echo "for the checksum to be about." >&2
    return 1
  fi
  tarball_re=$(printf '%s' "$tarball" | sed 's/[].[^$*\\/]/\\&/g')

  if ! grep -qE "^[[:space:]]*echo \"\\\$\\{sha256\\}([[:space:]]+|[[:space:]]\\*)${tarball_re}\" \\| sha256sum -c -;[[:space:]]*\\\\?[[:space:]]*$" "$DOCKERFILE"; then
    echo "the verify line is not the expected form:" >&2
    echo "  echo \"\${sha256}  ${tarball}\" | sha256sum -c -;" >&2
    echo "It must compare the pinned digest against the file curl downloaded," >&2
    echo "and must terminate on its own line -- anything chained after it, on" >&2
    echo "this line or the next, takes the failure away and an unverified" >&2
    echo "tarball is extracted." >&2
    grep -nE 'sha256sum' "$DOCKERFILE" >&2
    return 1
  fi

  # And the file that was verified must be the one unpacked.
  if ! grep -qE "^[[:space:]]*tar -xz -C /cni-plugins -f ${tarball_re}([[:space:]]|$)" "$DOCKERFILE"; then
    echo "the extraction does not unpack ${tarball}, the file the checksum" >&2
    echo "above verified, so the verification decides nothing." >&2
    grep -nE 'tar -xz' "$DOCKERFILE" >&2
    return 1
  fi

  # `\` joins the physical lines before the shell sees them, so `set +e` on any
  # continuation disables the checksum, curl --fail and tar together while
  # `RUN set -eux` stays intact.
  verify_run=$(awk '
    /^RUN /            { block = $0; inblock = 1; next }
    inblock            { block = block "\n" $0 }
    inblock && !/\\$/  { if (block ~ /sha256sum -c -/) { print block; exit }; inblock = 0 }
  ' "$DOCKERFILE")
  if [ -z "$verify_run" ]; then
    echo "could not find the RUN that verifies the tarball" >&2
    return 1
  fi
  # Errexit IS checked, on the logical line the shell actually runs. Four earlier
  # attempts to decide this by grepping physical lines failed, each in its own
  # way: `set -ux` (absent), `set +e` later (revoked), `# set -e` (a comment,
  # which under a continuation also silences the rest of the joined line), and
  # `echo 'set -e'` (an argument, not a command). Joining the continuations and
  # cutting every comment first removes all four: what remains is the text the
  # shell sees, and errexit has to be its FIRST command.
  #
  # Both spellings of revocation are rejected: `set +e` and `set +o errexit`.
  #
  # Not covered, and not claimed to be: errexit reached through a variable or a
  # conditional. Deciding that needs a shell, not a pattern.
  run_start=$(awk '/^RUN /{r=NR} /sha256sum -c -/{print r; exit}' "$DOCKERFILE")
  if [ -z "$run_start" ]; then
    echo "no RUN instruction contains the checksum comparison" >&2
    return 1
  fi
  joined=$(tail -n +"$run_start" "$DOCKERFILE" |
    awk '{print} !/\\[[:space:]]*$/{exit}' |
    sed 's/#.*//' | tr '\n' ' ' | tr -s ' ')
  # The same joined line, as the shell parses it. Deleting `case ... in` or its
  # `esac` leaves a syntax error the image build would catch -- in minutes, and
  # only on a pull request that touches this package. `sh -n` catches it here in
  # milliseconds. Continuations are stripped first: with the backslashes left in
  # place the shell sees them as literals and even a correct RUN fails to parse.
  if ! printf '%s' "$(tail -n +"$run_start" "$DOCKERFILE" |
        awk '{print} !/\\[[:space:]]*$/{exit}' |
        sed -e 's/^RUN[[:space:]]*//' -e 's/[[:space:]]*\\$//' | tr '\n' ' ')" | sh -n 2>/dev/null; then
    echo "the RUN that verifies and extracts the plugins does not parse as a" >&2
    echo "shell command once its continuations are joined." >&2
    return 1
  fi

  first=$(printf '%s' "$joined" | sed 's/^RUN[[:space:]]*//' | cut -d';' -f1)
  if ! printf '%s' "$first" | grep -qE '^[[:space:]]*set[[:space:]]+-[a-zA-Z]*e'; then
    echo "the RUN that verifies the tarball does not begin by enabling errexit." >&2
    echo "Its first command is: $first" >&2
    echo "Without it a failed checksum is reported and the build continues to" >&2
    echo "extract the archive it just rejected." >&2
    return 1
  fi
  if printf '%s' "$joined" | grep -qE 'set[[:space:]]+(\+[a-zA-Z]*e|\+o[[:space:]]+errexit)'; then
    echo "errexit is revoked later in the same RUN, which puts the checksum" >&2
    echo "back to being advisory." >&2
    printf '%s\n' "$joined" >&2
    return 1
  fi

  # Both per-arch pins, each separately: an alternation is satisfied by whichever
  # one survived.
  for arch in amd64 arm64; do
    if ! grep -qE "^[[:space:]]+$arch\)[[:space:]]+sha256=\"[0-9a-f]{64}\"" "$DOCKERFILE"; then
      echo "no pinned 64-hex SHA-256 for $arch in the Dockerfile." >&2
      echo "That arch would be extracted on the strength of its tag alone, and" >&2
      echo "for arm64 no build exercises the path that would notice." >&2
      grep -nE 'sha256=' "$DOCKERFILE" >&2
      return 1
    fi
  done

  # Verification must precede extraction. Located by the strict form above, not
  # by the substring: an inert `: "sha256sum -c -";` earlier in the file would
  # satisfy an ordering check anchored on a mention.
  v=$(grep -nE '^[[:space:]]*echo "\$\{sha256\}([[:space:]]+|[[:space:]]\*)[^"]+" \| sha256sum -c -;[[:space:]]*\\?[[:space:]]*$' "$DOCKERFILE" | head -1 | cut -d: -f1)
  t=$(grep -nE '^[[:space:]]*tar -xz -C /cni-plugins -f ' "$DOCKERFILE" | head -1 | cut -d: -f1)
  if [ -z "$v" ] || [ -z "$t" ] || [ "$v" -ge "$t" ]; then
    echo "the checksum no longer precedes the extraction (verify line $v, tar line $t)" >&2
    return 1
  fi
}

@test "the kube-ovn release the CNI plugin pin was checked against is the one vendored" {
  # Each side non-empty first: two missing keys compare equal.
  checked=$(sed -n 's|^# kube-ovn-cni-plugins-checked-against: *\([^ ]*\) *$|\1|p' "$DOCKERFILE")
  [ -n "$checked" ]

  vendored=$(yq -r '.global.images.kubeovn.tag' "$KUBEOVN_VALUES" | sed 's|@.*||')
  [ -n "$vendored" ]
  [ "$vendored" != "null" ]

  if [ "$checked" != "$vendored" ]; then
    echo "the multus CNI plugin pin was checked against kube-ovn $checked," >&2
    echo "but packages/system/kubeovn/values.yaml now vendors $vendored." >&2
    echo >&2
    echo "Read CNI_PLUGINS_VERSION in kube-ovn $vendored's base image" >&2
    echo "(dist/images/Dockerfile.base). If it still matches the" >&2
    echo "CNI_PLUGINS_VERSION in packages/system/multus/images/multus-cni/" >&2
    echo "Dockerfile, only move the marker there to $vendored. If it does not," >&2
    echo "the shared portmap and macvlan would flap between the two daemonsets," >&2
    echo "so bring the multus pin and its per-arch checksums across first." >&2
    return 1
  fi
}

@test "the rendered multus daemon config is valid JSON" {
  # Every assertion on this document elsewhere is a pattern over its text, and a
  # pattern is happy with trailing garbage: `"multusMasterCNI": "05-cilium.conflist" garbage,`
  # keeps them all green while multus gets a config it cannot parse and comes up
  # with no master CNI at all. Parsing is the only check that decides this, and
  # helm-unittest cannot do it.
  #
  # It sits here rather than beside the other daemon-config assertions because
  # those are helm-unittest cases in packages/system/multus/tests/multus_test.yaml
  # and this one needs a parser. This file is where the multus checks that need
  # something outside helm-unittest live, staging or not.
  cfg=$(helm template packages/system/multus |
    yq eval 'select(.kind == "ConfigMap") | .data["daemon-config.json"]' -)
  [ -n "$cfg" ]
  if ! printf '%s' "$cfg" | yq eval -p json '.' - >/dev/null 2>&1; then
    echo "the rendered daemon-config.json does not parse as JSON:" >&2
    printf '%s\n' "$cfg" >&2
    return 1
  fi
  # And the key that decides which conflist multus treats as primary has to
  # survive parsing, not merely appear in the text.
  master=$(printf '%s' "$cfg" | yq eval -p json '.multusMasterCNI' -)
  if [ "$master" != "05-cilium.conflist" ]; then
    echo "the parsed multusMasterCNI is '$master', not 05-cilium.conflist" >&2
    return 1
  fi
}

@test "the Talos release the CNI plugin pin was checked against is the one shipped" {
  # Each side non-empty first: two missing keys compare equal.
  checked=$(sed -n 's|^# talos-cni-plugins-checked-against: *\([^ ]*\) *$|\1|p' "$DOCKERFILE")
  [ -n "$checked" ]

  shipped=$(awk '/^version:/ {print $2; exit}' packages/core/talos/images/talos/profiles/installer.yaml)
  [ -n "$shipped" ]

  if [ "$checked" != "$shipped" ]; then
    echo "the multus CNI plugin pin was checked against Talos $checked," >&2
    echo "but packages/core/talos now ships $shipped." >&2
    echo >&2
    echo "Read cni_version in siderolabs/pkgs' Pkgfile on the matching release" >&2
    echo "branch. If it still equals CNI_PLUGINS_VERSION in this Dockerfile," >&2
    echo "only move the marker to $shipped. If it does not, staging would" >&2
    echo "replace the node image's bridge, firewall, host-local and portmap" >&2
    echo "with a different version of the same plugins on every Talos node," >&2
    echo "so decide which version wins before moving it." >&2
    return 1
  fi
}

@test "the VLAN guide's remediation quotes what the daemonset actually emits" {
  # The guide names a log line and a container; both spellings live in the
  # manifest.
  grep -qF -- 'no /cni-plugins in this image; skipping staging' "$TEMPLATE"
  grep -qF -- 'no /cni-plugins in this image; skipping staging' "$DOC"

  grep -qF -- 'name: install-cni-plugins' "$TEMPLATE"
  # Inside the command an operator is told to run, not anywhere in the file: a
  # mention left in prose ("older notes may say ...") satisfies a bare match
  # while the command itself names a different container.
  grep -qE -- 'kubectl [^`]*logs [^`]*--container install-cni-plugins($|[^-[:alnum:]])' "$DOC"

  # The count as a reader meets it. Only the staged count is quoted, never the
  # upstream total: that is a property of a release this suite cannot read.
  grep -qE -- '(^|[^0-9])14 of the upstream reference plugins' "$DOC"

  # The half of that sentence naming the four, pinned separately: of the four
  # only loopback arrives from anywhere else (cilium and kube-ovn install it).
  grep -qF -- 'and leaves `loopback`, `dhcp`, `dummy` and `tap` alone' "$DOC"
  if grep -qF -- '`tap` to the node image' "$DOC"; then
    echo "the guide again attributes the unstaged plugins to the node image;" >&2
    echo "staging exists because that image may not carry them" >&2
    return 1
  fi

  if grep -qF -- 'immediately above' "$DOC"; then
    echo "the guide again promises the cause sits immediately above the" >&2
    echo "'failed to install:' summary. It does not when more than one plugin" >&2
    echo "fails: the summary is printed once, after the loop." >&2
    return 1
  fi

  grep -qE -- '(^|[^0-9])14 of the upstream reference plugins' "$PLATFORM_VALUES"
  grep -qF -- 'loopback, dhcp, dummy and tap are left alone' "$PLATFORM_VALUES"

  # Presence only: a grep cannot decide whether prose still MEANS what it says,
  # and `do not set the opt-out before upgrading` contains this string. The
  # reversal is guarded by name, which closes the spellings seen so far and not
  # the general case -- that one stays open deliberately.
  grep -qE -- '(^|[^[:alnum:]])set the opt-out before upgrading' "$DOC"
  if grep -qiE -- '(do not|never|no need to|dont|don.t) set the opt-out' "$DOC"; then
    echo "the guide now tells operators NOT to set the opt-out before" >&2
    echo "upgrading. Declining afterwards does not restore a replaced plugin," >&2
    echo "so the instruction has to stay as it is." >&2
    return 1
  fi
}

@test "every accepted spelling of stageCniPlugins renders the direction it names" {
  # The chart holds the accepted spellings as TWO lists -- one deciding whether
  # the value is readable, one deciding whether it means on -- and nothing else
  # makes them agree, so each spelling is rendered rather than their union.
  #
  for v in true TRUE True yes YES on On 1; do
    n=$(helm template --set-string stageCniPlugins="$v" packages/system/multus |
          yq eval 'select(.kind == "DaemonSet") | .spec.template.spec.initContainers | length' - |
          tail -1)
    if [ "$n" != "2" ]; then
      echo "stageCniPlugins=$v is documented as ON but rendered $n init" >&2
      echo "containers (expected 2: install-multus-binary + install-cni-plugins)." >&2
      return 1
    fi
  done
  for v in false FALSE False no NO off Off 0; do
    n=$(helm template --set-string stageCniPlugins="$v" packages/system/multus |
          yq eval 'select(.kind == "DaemonSet") | .spec.template.spec.initContainers | length' - |
          tail -1)
    if [ "$n" != "1" ]; then
      echo "stageCniPlugins=$v is documented as OFF but rendered $n init" >&2
      echo "containers (expected 1: install-multus-binary alone)." >&2
      return 1
    fi
  done
  # And a spelling outside the documented set must stop the render rather than
  # resolving to either direction.
  if helm template --set-string stageCniPlugins=maybe packages/system/multus >/dev/null 2>&1; then
    echo "stageCniPlugins=maybe rendered instead of failing the template" >&2
    return 1
  fi
}

@test "every line the patch owns still matches the committed template" {
  # customize-deployment.patch is the SOURCE: `make update` does
  # `rm -rf templates && wget && patch`, so anything present only in the template
  # is discarded on the next run. Reverse-applying the whole patch proves every
  # line the PATCH OWNS still matches; it says nothing about the rest of the file.
  #
  # --fuzz=0 because `patch` defaults to a fuzz factor of 2 and will drop context
  # lines to make a hunk fit.
  d=$(mktemp -d)
  mkdir -p "$d/templates"
  sed 's|^\( *\)image: .*multus-cni.*|\1image: ghcr.io/k8snetworkplumbingwg/multus-cni:snapshot-thick|' \
    "$TEMPLATE" > "$d/templates/multus-daemonset-thick.yml"

  # Non-empty, or a moved template turns this into a patch against nothing.
  if [ ! -s "$d/templates/multus-daemonset-thick.yml" ]; then
    echo "the template is empty or missing; nothing to compare the patch to" >&2
    rm -rf "$d"
    return 1
  fi

  # --fuzz=0 for the same reason: a dropped context line is an edit absorbed
  # rather than reported.
  if ! (cd "$d" && patch -R --dry-run --fuzz=0 -p4 --input="$PATCHFILE" >"$d/out" 2>&1); then
    echo "customize-deployment.patch no longer describes the committed template." >&2
    echo "The patch is the source: anything present only in the template is" >&2
    echo "discarded by the next 'make update', and anything present only in the" >&2
    echo "patch appears on every node at that point without ever being tested." >&2
    cat "$d/out" >&2
    rm -rf "$d"
    return 1
  fi
  rm -rf "$d"
}

@test "every multus-cni reference in the manifest is the same string" {
  # Every container that must run the multus image, found BY NAME. Selecting the
  # image fields by what they already contain finds only those that still say
  # multus-cni, so a container repointed at another image leaves the comparison
  # instead of failing it.
  rendered=$(helm template --set stageCniPlugins=true packages/system/multus)
  for c in install-multus-binary install-cni-plugins kube-multus; do
    img=$(printf '%s' "$rendered" | yq eval "select(.kind == \"DaemonSet\") | [.spec.template.spec.initContainers[], .spec.template.spec.containers[]] | .[] | select(.name == \"$c\") | .image" -)
    case "$img" in
      ghcr.io/cozystack/cozystack/multus-cni:*) ;;
      *)
        echo "container $c runs '$img', not the cozystack multus image." >&2
        echo "All three run the same image by construction: the package's" >&2
        echo "image: target rewrites every multus-cni line together." >&2
        return 1 ;;
    esac
  done

  # `make image` sed's the built reference over every multus-cni line, so they
  # are one string by construction. Asserted as identity rather than per-index so
  # a fourth container is covered without touching this test.
  #
  # `image:[[:space:]]+`, not a literal space: YAML permits any run, and a
  # hard-coded one makes the pattern match nothing rather than match differently.
  refs=$(grep -oE 'image:[[:space:]]+[^[:space:]]*multus-cni[^[:space:]]*' "$TEMPLATE" |
           sed 's/^image:[[:space:]]*//' | sort -u)
  count=$(printf '%s\n' "$refs" | grep -c . || true)

  if [ "$count" = "0" ]; then
    echo "no multus-cni image reference found in the template at all; if the" >&2
    echo "image was renamed, this test and the package's image: target both" >&2
    echo "need updating together." >&2
    return 1
  fi
  if [ "$count" != "1" ]; then
    echo "the manifest carries $count different multus-cni references:" >&2
    printf '%s\n' "$refs" >&2
    echo "They must be one string. make image writes all of them together, so a" >&2
    echo "difference here means one was edited by hand." >&2
    return 1
  fi
}

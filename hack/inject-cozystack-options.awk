# SPDX-License-Identifier: Apache-2.0
#
# Re-inject the x-cozystack-options vendor extension into a CRD after
# controller-gen, which cannot emit arbitrary x- keys. The dashboard reads this
# extension to render a field as a dropdown populated from live cluster state.
#
# It tracks the YAML key stack by indentation and appends the extension after a
# target field's `type: string` line, preserving controller-gen's formatting
# byte-for-byte (no YAML reserialization, so it is independent of the host yq
# version).
#
# TARGETS is a space-separated list of "<parent>:<field>:<source>" triples,
# where <parent> is the key two levels above the field (the reference object,
# or "spec" for a direct spec property) and <source> is the Option provider
# name. Targets are per-CRD on purpose: the same structural field (e.g.
# planRef.name) is a dropdown in one CRD but free-text in another.
BEGIN {
  n = split(TARGETS, parts, " ")
  for (i = 1; i <= n; i++) {
    split(parts[i], t, ":")
    want[t[1] SUBSEP t[2]] = t[3]
  }
}
{
  line = $0
  if (match(line, /[^ ]/)) { indent = RSTART - 1; blank = 0 } else { indent = -1; blank = 1 }

  # Leave a block scalar (e.g. "description: |-") once we dedent to/below its key.
  if (inBlock && !blank && indent <= blockIndent) inBlock = 0

  if (!inBlock && !blank) {
    if (match(line, /^ *[^ #][^:]*:[ \t]*[|>][-+0-9]*[ \t]*$/)) { blockIndent = indent; inBlock = 1 }
    else if (match(line, /^ *[^ #][^:]*:[ \t]*$/)) {
      key = line; sub(/^ */, "", key); sub(/:[ \t]*$/, "", key)
      for (k in keyAt) if (k+0 >= indent) delete keyAt[k]
      keyAt[indent] = key
    }
  }

  print line

  if (!inBlock && match(line, /^ *type: string[ \t]*$/)) {
    field = keyAt[indent-2]; parent = keyAt[indent-6]
    src = want[parent SUBSEP field]
    if (src != "") {
      pad = substr("                                                  ", 1, indent)
      printf "%sx-cozystack-options:\n", pad
      printf "%s  source: %s\n", pad, src
    }
  }
}

#!/usr/bin/env bats
# A test that creates a tenant `Kubernetes` cluster must wait for that release
# to become Ready before the assertions it ends on. The mechanism, and how the
# budget is sized, are derived once in docs/agents/e2e-testing.md section 4 --
# not restated here, because two copies of a derivation drift and the one that
# drifts is the one nobody re-reads.
#
# Why the rule holds in every OIDC mode and why the budget's floor does not is
# derived with the rest of it, in the conventions named above. What follows from
# it here: the size of the budget is not checked, only its parse and its sign.
#
# What follows are the cases this cannot see, each with its mechanism and
# whether it fails loudly or quietly. A path that exits between the CR and the
# wait ends the run mid-install too, and the script writes many `exit 1` sites
# between the two. None carries an assertion name, so the rule reports ok on
# them -- deliberately for the failing ones: those runs are already red, and
# the rule is about a run that asserts, passes and ends.
#
# That argument covers a failure and nothing else. A path leaving successfully
# before the wait -- `exit 0`, or a `return` -- ends mid-install with a green
# run and the rule still answers ok, and this cannot see it: the exit carries
# no assertion name either. Silent, and the only entry here that is. What keeps
# it from being live is the subject: between the CR and the wait it writes one
# `exit 0`, inside a quoted `sh -c` that runs in a probe Pod, and no `return`
# at all.
#
# An indented assertion-helper definition is not exempted by the composition
# rule, which anchors at column 0, so its inner call counts as a call site and
# the ordering can red on a script that broke nothing. The diagnosis then names
# a line inside a definition body, which runs wherever the helper is called.
#
# A backslash at the end of a line inside single quotes is literal to the shell
# and a continuation to this fold, so the two lines are joined here and separate
# there, as /bin/sh reads it. The direction is loud: joining brings the
# next line's operators onto the wait's command, so a `|| true` that belongs to
# a different command reads as this one's and the case reds. Nothing in the
# subject writes it.
#
# The fold's EOF flush matters only for a file whose last line ends in a
# backslash, which no subject writes. The fixture `eof-continuation.sh` is that
# file, and the case reading it is what keeps the flush from being deleted as
# tolerance nothing needs.
#
# `set +e` in a CALLER, not in the body. Errexit being off is a property of the
# shell where the wait runs rather than of the line, and what can be read is the
# body around it: `_errexit_before` reds on a `set +e` above the wait in the same
# body. Above it in whatever called that body it does not, because the print
# stops at the definition. Narrowed, not closed, and silent -- as is the other
# silent one here, an assertion whose name the convention does not cover, which
# reads as no assertion at all. Every other entry costs a false red instead. What keeps it from being a live hole
# is the subject, which disables errexit nowhere, and suites that run under
# `set -eu`.
#
# What it does see, and refuses: a wait inside a compound command. `_wait_nesting`
# reads the level from bash's own printing of the parsed body, so a wait under
# `if`, `while`, `for` or `case` is reported as nested and the rule fails. It
# is a refusal rather than a verdict about the install -- whether the branch is
# taken is a runtime fact -- and it is refused because the alternative is the
# one shape that passes while the install runs unbounded.
#
# The cost is a false red on shapes that change nothing, and each answers with
# a different verdict, so the diagnosis names a different cause than the shape
# suggests. A wait inside a `{ ...; }` group, measured on all three spellings:
# written on one line it carries a `;`, which SEQUENCE_OK_RE refuses, so no
# wait is found; written across lines its closing brace stands alone, which the
# extraction refuses, so the body is `unextractable`; written `{ cmd` on the
# opening line the command no longer starts the line and the command matcher
# passes it by. `nested` is reachable for `if`, `while` and `case` and for no
# brace group. A wait wrapped in a subshell is a fourth answer: `( cmd` prints
# on the line that opens the subshell, so the printed body shows no wait and
# the verdict is `absent`. All of them are loud, and none is written in the
# body this guard reads.
#
# The opposite error is also possible and reds valid code: function bodies are
# delimited by a `}` at column 0, so a heredoc carrying one -- a JSON patch
# body, say -- ends the body early and the ordering rule answers `unscoped`.
# The message says so rather than blaming the ordering, which is why this is
# listed rather than guarded against.
#
# The conditional restore of xtrace is pinned as a property of this file's own
# text, not of a runner's behaviour. Turning xtrace on where the runner had it
# off makes bats' failure formatter trace its own output, and a later failure in
# that case then arrives as a hang rather than a red -- exit 124 against exit 1,
# measured on a two-case file written for that measurement. Watching it happen
# needs the real `bats` binary, which the lane enforcing this guard does not
# have -- `hack/cozytest.sh` never calls it -- and a case that skips where the
# guard runs is a case that passes there. So the behavioural check is not built,
# and the form is held instead: the case below reads this file and refuses an
# unconditional restore.
#
# What that text rule does not see is another spelling: a conditional restore
# written some other way reads as absent, and an unconditional trace-on written
# with anything but a bare `set -x` on its own line reads as fine. Direction:
# the first reds and names the line, the second passes -- the trade the matchers
# here make wherever a spelling has to be listed, and the reason the measurement
# above sits beside it rather than standing in for it.
#
# An assertion helper whose name starts with an underscore does not open an
# exempt body: the composition rule anchors on `cozy_assert_` and
# `cozy_switch_and_assert_`, while the subject writes its private helpers under
# a `_cozy_` prefix. A helper named `_cozy_assert_x` would therefore have its
# inner calls counted as call sites. Loud, like the indented definition above,
# and the same remedy -- name the helper so the rule sees it, or widen the
# anchor here.
#
# The creator sweep's root is not the whole of what it leaves out. The lane at
# `hack/e2e-apps` is outside it, and the file types the sweep reads (`.sh`,
# `.yaml`, `.yml`) leave it out a second time, since that lane holds `.bats`
# suites: widening the root alone would still read nothing there. Both halves
# fail in the same silent direction, and closing one of them closes half.
#
# The creator sweep pairs an apiVersion with a kind and cannot tell an `apply:`
# block from an `assert:` one, so a suite that only asserts on a tenant cluster
# is reported as creating one. Direction is loud -- the uniqueness case reds and
# names the file -- and the diagnosis says to check that before treating the
# file as a subject. Telling them apart needs the document structure, which is
# a parser; the sweep reads lines.
#
# The fixtures' marks are comments, and they stay invisible to the matchers that
# read those fixtures because the comment cut runs before each match. Mechanism:
# a mark is `# @name`, so a matcher that sees comments sees marks. Direction
# belongs to the pair of element and edit, not to the element: drop the cut from
# the call matcher and a comment counted as a call reds the shapes case; drop
# it from the fold and the wait vanishes under a `<<WORD` named inside a
# comment; coarsen it to a plain `sub(/[[:space:]]#.*$/)` and it eats the
# opener along with the tail.
# Three edits to one definition, three different reds: which matcher reds, and
# with which diagnosis, depends on the edit. Reachable: the cut is one shared
# definition, and every matcher here that reads shell lines reads through it --
# the fold's heredoc probe, the wait, the budget and the call sites -- so an
# edit to it reaches every marked fixture at once. The kind matchers read YAML
# rather than shell lines and do not go through it.
#
# What this rule costs the subject, which is a consequence rather than a
# limitation: splitting the body it reads is now a larger change than it looks.
# Source position is execution order only inside one function, so moving the
# assertions or the wait into a second one turns the ordering verdict to
# `outside` and reds the unit lane. The direction is loud and the diagnosis
# names the lines, but the split is not finished until this rule is pointed at
# wherever the two now live -- which the message for that verdict says at the
# moment it fires. Reachable rather than theoretical: the body is long enough
# that dividing it is a thing someone will want to do.
#
# Scope: the tenant cluster is created from a heredoc in
# hack/e2e-chainsaw/_lib/run-kubernetes.sh, so the script is the subject. There
# are no Chainsaw fixtures applying this kind -- a guard walking `apply.file`
# would match nothing and pass by having nothing to check.
#
# Run with: hack/cozytest.sh hack/run-kubernetes-install-wait_test.bats
#
# Both runners, and not for symmetry. `make unit-tests` runs this through
# hack/cozytest.sh, which is pure shell -- `sh` with `set -eu` -- so that is the
# one CI exercises; the `bats` binary runs it only when someone types it. They
# differ on failures they do not share, and the difference falls the awkward
# way: a command substitution that fails inside an assignment stops the run
# under the runner CI uses, while under bats the same helper carries on past it
# and answers normally, so the case passes. Nothing is hidden there -- no abort
# happens under bats at all -- which is why running this by hand can look green
# while CI reds, which is why both are kept green.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
TENANT_SCRIPT="${TENANT_SCRIPT:-$REPO_ROOT/hack/e2e-chainsaw/_lib/run-kubernetes.sh}"

# A `--for` that names Ready being true, in the spellings this tree writes.
# `kubectl wait` takes `condition=<name>[=<value>]` with the value defaulting
# to true and the name matched case-insensitively, so `Ready` and `ready=true`
# mean what `ready` means -- and run-kubernetes.sh itself writes the
# capitalised form for other resources thousands of lines from the wait this
# guard reads. Anchored at the end of the argument, so `ready=false`, which is
# the negation of what the rule requires, does not satisfy it.
#
# The flag itself is required, not just the condition word: `--for=` and not
# a bare `condition=ready`, which can sit in another argument. The separated
# `--for condition=ready` that kubectl's parser (spf13/pflag) also accepts is
# NOT taken, and the asymmetry with the budget matcher (which does take
# `--timeout 5m`) is not stylistic: no wait under hack/ writes the separated
# `--for` -- the only two occurrences are this file, naming it -- while the
# separated `--timeout` is written there, at hack/e2e-install-cozystack.bats.
#
# Not every spelling kubectl tolerates: `=TRUE` is rejected here. The accepted set
# is the ones written here plus the explicit `=true`, which is a set the table
# test below can check, unlike "whatever the parser accepts".
#
# The argument ends on whitespace or the end of the line, and on nothing else.
# A condition followed by `;` reaches SEQUENCE_OK_RE and is refused there for
# carrying a `;` at all, so it needs no arm here.
READY_CONDITION_RE='--for=condition=[Rr]eady(=[Tt]rue)?([[:space:]]|$)'

# A wait that does not block is not a wait. Rather than enumerate the ways a
# failure can be discarded -- `|| true`, `|| :`, `|| rc=$?`, a pipeline, and
# whatever anyone writes next, which is an open set -- these key on shell's
# control operators, which are a closed one.
#
# Rejected: a pipe of any kind, because `||` cannot be told from a masking
# `|| true` by reading and a pipeline hands the status to the last command;
# and any `&`, which covers backgrounding, `&&`, and a `2>&1` redirection
# added for diagnostics -- that last is a false red, loud, and named in the
# diagnosis rather than silently tolerated. docs/agents/e2e-testing.md, under
# "The install gate must have teeth", records a gate of the backgrounded shape
# carrying a permanently failing release through green CI.
#
# `&&` is rejected too, and the reason is not obvious. Under `set -e` a
# command that is not the last of an AND-OR list is exempt from errexit, so
# `kubectl wait ... && echo ok` carries on to the next line when the wait
# fails, in sh and bash alike. The list's exit status does
# carry the failure, but nothing in the script reads it.
#
# A HANDLED failure is refused too, so `;` is refused outright and the pattern
# below takes no `if`. The script writes `if ! kubectl wait ...; then
# <diagnostics>; exit 1; fi` elsewhere and that shape does end the run, but
# whether it ends the run lives in the branch body and this rule reads one
# line: a handler that logs and falls through satisfies every other clause.
# With `_printed_body` in this file this is a choice rather than a limit -- bash's
# own parse puts the branch body's top level in reach, so requiring it to end in
# `exit` is checkable -- and the choice is to keep the refusal narrow rather
# than grow a second parser of branch semantics here.
# Scanning the branch does not close it either -- an `exit 1` nested in a
# further `if` is in the text and conditional in fact, so the scan passes a
# swallowed failure SILENTLY, while refusing the shape reds every time. The
# cost is no inline diagnostics on THIS wait (`|| { ...; exit 1; }` goes to the
# `|` rule), leaving the failure to the errexit the suite runs under.
# Every filter below runs on the command with its trailing comment cut off.
# Each of them but SEQUENCE_OK_RE is unanchored, so without the cut a comment
# can supply whatever the command lacks -- a condition, a release name, a
# namespace, a missing operator. SEQUENCE_OK_RE needs the cut for the opposite
# reason: it is anchored on the whole line, so a `;` written inside a comment
# would disqualify a command that carries none. Only WAIT_COMMAND_RE is
# unaffected, and only because it runs before the cut.
#
# The matchers outside this pipeline need no cut for a different reason: they
# anchor on what must BEGIN the line, which a trailing comment cannot reach.
# A comment is not passed to kubectl, so anything found there is not an
# argument: without the cut, `--for=condition=ready=false # ready` reads as a
# Ready-true wait and a wait on another release can be rescued by naming the
# right one in a comment.
#
# The command in command position, and a bare one: no `if`, negated or not.
# A positive `if` makes the wait a condition and discards its status; a negated
# one hands the failure to a branch this rule cannot read, for which see
# TOOTHLESS_RE above.
#
# A line that merely mentions the command -- `echo kubectl wait hr ...`, or a
# comment -- runs no wait, and a substring search would report one. The anchor
# is also why THIS pattern needs no comment filter: a `#` cannot stand where
# `kubectl` must. It says nothing about the others -- the call matcher below
# takes a call anywhere in command position, so it has to cut comments first.
#
# Two spellings, and the second is the point: `kubectl_wait_retry`, which the
# subject script defines for transient apiserver failures and today calls on
# other resources but not on a release, hands its arguments to `kubectl wait`
# and returns the status, so it blocks and
# it carries teeth by the same standard as the direct form. Refusing it would
# leave the guard blocking the transient-failure hardening this tree writes
# elsewhere. The budget is read from it identically, because its retry
# list holds transient server-side signatures only and an expired `--timeout`
# is not among them: that failure returns on the first attempt.
#
# The optional `<digits>:` is grep -n's prefix. This pattern runs before that
# prefix exists, so it never meets one in the pipeline -- it is tolerated so
# the table below can feed the same row to this pattern and to SEQUENCE_OK_RE,
# which does run after grep -n and must handle it.
WAIT_COMMAND_RE='^[0-9]*:?[[:space:]]*kubectl(_wait_retry|[[:space:]]+wait)[[:space:]]+hr[[:space:]]'

# The tenant namespace, in the spellings a shell writes: either flag, with a
# space or an `=`, quoted or not. A namespace given through a variable is
# not resolvable by reading and is refused -- named in the diagnosis rather
# than silently treated as the wrong namespace.
TENANT_NAMESPACE_RE='(-n[[:space:]=]*|--namespace[[:space:]=]+)"?tenant-test"?([[:space:]]|$)'

TOOTHLESS_RE='\||&'
SEQUENCE_OK_RE='^[^;]*$'

# Whether $1 is a duration THIS GUARD can read and is non-zero -- a narrower
# set than the ones kubectl accepts, which include `500ms` and `1.5h`. Not
# whether it is the right length: that depends on where the wait sits and is
# held by review (docs/agents/e2e-testing.md section 4).
_usable_budget() {
  # Shape first, rather than a character set, because `m5s` and `h1m` are made
  # of the right characters and are rejected anyway, so a wait carrying one
  # does not happen. The digit bound is not part of the overflow verdict --
  # the ceiling below carries that -- it keeps awk's total inside the range
  # `[` can compare, since an unbounded component makes it print a number
  # `[` answers with "integer expression expected" on stderr instead. Then the
  # total, because kubectl parses the value as a Go
  # duration counting int64 nanoseconds: it overflows somewhat above 2562047h,
  # and bounding each component would not bound their sum. A day is far above
  # anything this suite would wait for and far below where the parser breaks,
  # so one comparison covers both ends without standing in for a size rule.
  printf '%s' "$1" | grep -Eq '^([0-9]{1,6}[hms])+$' || return 1
  _ub_seconds=$(printf '%s' "$1" | awk '{
    total = 0
    while (match($0, /^[0-9]+[hms]/)) {
      unit = substr($0, RSTART + RLENGTH - 1, 1)
      value = substr($0, RSTART, RLENGTH - 1) + 0
      if (unit == "h") { total += value * 3600 }
      else if (unit == "m") { total += value * 60 }
      else { total += value }
      $0 = substr($0, RSTART + RLENGTH)
    }
    print total
  }')
  # Explicit returns, not a bare test as the last command: the runner appends
  # `return 0` in front of a closing brace at column 0, so a helper whose
  # verdict is its last command's status always reports success.
  [ "$_ub_seconds" -gt 0 ] || return 1
  [ "$_ub_seconds" -le 86400 ] || return 1
  return 0
}

# The ways this repository's YAML can name the kind on one line, decided once
# and shared by both matchers below: two spellings of one rule drift apart, and
# a disagreement between the CR matcher and the sweep surfaces as a count
# nobody can place rather than as a failure naming its cause.
#
# Tolerated, each because the document is unchanged by it: any spacing after
# the colon including a tab, the value quoted with either quote, trailing
# whitespace, and a trailing comment.
#
# Refused, and this is a scope decision rather than an assumption that nobody
# writes them: a flow mapping (`{kind: Kubernetes}`), the JSON form
# (`{"kind":"Kubernetes"}`), anchors and aliases, tags, and block scalars.
# Recognising any of those means parsing YAML, and this guard matches lines by
# construction -- a parser is a different tool with different failure modes.
# The JSON form is not hypothetical: hack/kubernetes-md0-migration.bats spells
# this kind that way, in the canned answer of a mock kubectl. It sits outside
# the sweep root, which is the scope this file already declares, and a creator
# written that way inside the root would be missed -- named here rather than
# left to be discovered.
KIND_VALUE_RE='kind:[[:space:]]*["'"'"']?Kubernetes["'"'"']?[[:space:]]*(#.*)?$'

# Which element is taken is not distinguishable while the case below pins the
# count at one -- first and last are the same line -- so this is a declaration
# of intent rather than a rule with a case behind it.
_cr_apply_line() {
  _cr_apply_lines | head -n 1
}

# Every line applying the kind, not just the first. The rule below is written
# for one cluster per script and says so; this exists so that a second one is
# seen and refused rather than silently left unchecked.
#
# Keyed on the apiVersion/kind pair, adjacent and at column 0, not on `kind:`
# alone. The adjacency is where this matcher is deliberately stricter than the
# sweep below, which tolerates any key order because a YAML mapping is
# unordered: here the subject is one heredoc this repository writes, and
# pinning its shape makes a rewrite of it visible instead of silent.
# Keyed on the pair rather than on `kind:` alone because the script applies a
# KubernetesNodes CR too, and `kind:` alone takes whichever comes first. Both
# halves anchor at column 0, which is also what makes a commented-out block
# invisible here without a filter for it.
_cr_apply_lines() {
  awk -v kindre="^$KIND_VALUE_RE" '
       /^apiVersion: apps\.cozystack\.io\// { av = NR; next }
       # `av > 0` is load-bearing: awk starts it at 0, so on line 1 the test
       # `av == NR - 1` compares 0 with 0 and a kind line there would count as
       # a CR with no apiVersion anywhere in the file.
       $0 ~ kindre { if (av > 0 && av == NR - 1) { print NR } }' "$TENANT_SCRIPT"
}

# How many tenant `Kubernetes` CRs the subject applies, as a number and never
# as a failure.
#
# Counted from the kind line alone, at any indent, without the apiVersion
# pairing the matcher above uses. The indent matters: a second cluster applied
# from a `List` document has its keys indented under the item dash, and a count
# anchored at column 0 reports one cluster while two are created -- silent,
# where over-counting would red the case below and name the number. That pairing requires the two keys adjacent, which is right for
# finding the ONE CR whose shape this repository writes, and wrong for counting:
# a second CR with the keys in the other order, or with a field between them, is
# the same manifest creating the same cluster and was counted as none -- leaving
# the case below green while saying a second cluster ran unbounded.
#
# awk prints `0` for no matches and exits 0, so the count is a number on every
# path; a `grep -c` here would print 0 and exit 1, and the assignment would end
# the case before its own message.
_cr_count() {
  awk -v kindre="^[[:space:]]*$KIND_VALUE_RE" '$0 ~ kindre { n++ } END { print n + 0 }' "$TENANT_SCRIPT"
}


# Each line with its trailing comment removed, the way the shell removes one.
#
# `#` opens a comment where a word could start and only outside quotes. What
# this takes as "where a word could start" is narrower than the shell's rule:
# a space or a tab before the `#`, not the `;`, `&`, `|`, `(` or `)` that also
# open one (`sh -c 'echo hi;#c'` prints `hi`). Every line that divergence keeps
# is refused further on and loudly -- a `;` by SEQUENCE_OK_RE, an `&` or a `|`
# by TOOTHLESS_RE, and a kept `#<<WORD` opens a heredoc that hides the wait
# rather than inventing one. A backslash outside single quotes escapes whatever
# follows it, as /bin/sh reads them: `foo#bar`, `"a # b"`, `\'a # b\'` and
# `"a\" # b"` all keep the `#`, while `foo # note` does not. Cutting on any
# ` #` takes the quoted case with it, and a wait written
# `... "name #x" ... || true` then loses its
# `|| true` and reads as a wait with teeth. That is the silent direction, which
# is why this is not a `sed` one-liner.
# The shell's comment rule as an awk function, inlined into both programs that
# need it: this cut and the fold's heredoc-opener probe. A second copy would
# drift, and the probe needs the rule in this form specifically. Without any cut
# a `<<WORD` written inside a comment reads as an opener and every line below it
# becomes heredoc data, the wait among them. With a crude cut -- a plain
# `sub(/[[:space:]]#.*$/, "", probe)` -- a quoted ` #` earlier on a real opener
# line takes the `<<WORD` away with the tail. Both have a fixture.
AWK_CUT_COMMENTS='
    function cutcomments(s,   out, sq, dq, n, i, c, p) {
      out = ""; sq = 0; dq = 0; n = length(s)
      for (i = 1; i <= n; i++) {
        c = substr(s, i, 1)
        # A backslash escapes the next character everywhere but inside single
        # quotes, so `\"` does not close a double-quoted region. Without this
        # the region closes early, the rest of the line reads as unquoted, and a
        # `#` in it cuts away operators the shell still runs.
        if (c == "\\" && !sq) {
          out = out c
          i++
          if (i <= n) { out = out substr(s, i, 1) }
          continue
        }
        if (c == "\047" && !dq) { sq = !sq }
        else if (c == "\"" && !sq) { dq = !dq }
        else if (c == "#" && !sq && !dq) {
          p = (i == 1) ? " " : substr(s, i - 1, 1)
          if (p == " " || p == "\t") break
        }
        out = out c
      }
      sub(/[[:space:]]+$/, "", out)
      return out
    }
'

_cut_comments() {
  awk "$AWK_CUT_COMMENTS"'{ print cutcomments($0) }'
}

# The script's commands with their backslash continuations joined, each emitted
# as "<first physical line>:<command>".
#
# Every filter below is line-wise, and a `\` at the end of a line moves whatever
# follows out of their reach while the wait's own line still satisfies all of
# them. That turns the toothless shapes this file names -- `|| true`, `&`, an
# AND-list, a pipeline -- from a loud red into a silent pass, which is the one
# failure the rule exists to prevent. Folding here rather than teaching each
# filter about continuations keeps them reading one command at a time, which is
# what they were written for.
#
# The line number is the FIRST physical line, so a diagnosis points at the line
# a reader would look for. WAIT_COMMAND_RE tolerates that `<digits>:` prefix
# already, for the same reason it tolerated grep -n's.
_folded_commands() {
  awk "$AWK_CUT_COMMENTS"'
    # Inside a heredoc the lines are data, not commands. Without this a
    # `kubectl wait hr ...` written into a heredoc body satisfies every filter
    # below, so without this the rule reports ok on a script whose only such
    # line is data on its way to `kubectl apply`. The failure direction of the
    # tracking is the other way: mistaking a command for heredoc content hides
    # it and the wait is reported missing, which is loud.
    # One command can open several heredocs -- `cmd <<A <<B` -- and their bodies
    # arrive in the order they were opened. Tracking only the first ends the
    # tracking at the first terminator and hands the second body back as
    # commands, which is how a wait written into data reads as the wait that
    # runs. Silent, and the only shape here that would fail that way. No
    # apostrophe in this comment: the awk program is single-quoted, and one
    # ends it.
    inhd {
      if ($0 ~ hdq[hdi]) {
        hdi++
        if (hdi > hdn) { inhd = 0 }
      }
      next
    }
    {
      cur = $0
      if (buf != "") { sub(/^[[:space:]]+/, "", cur); cur = buf " " cur } else { start = NR }
      if (cur ~ /\\$/) { sub(/[[:space:]]*\\$/, "", cur); buf = cur; next }
      print start ":" cur
      buf = ""
      # The opener is itself a command and has been printed; what follows is not.
      # Searched with any trailing comment cut first, or a `<<WORD` written
      # inside a comment opens a heredoc that swallows the rest of the file.
      probe = cutcomments(cur)
      hdn = 0
      rest = probe
      pre = ""
      # The delimiter is the WORD after the operator, with quote removal
      # applied -- not a letter run with an optional quote on each end. The
      # shell ends that word at whitespace or at one of `;&|<>()`, and quoting
      # may sit anywhere inside it: `<<E"O"F` and `<<E\\OF` both name EOF.
      # Reading only the leading run takes `E` as the delimiter, the terminator
      # then never arrives, and every command after that heredoc is swallowed
      # as data -- silent, and measured that way before this read the word.
      while (match(rest, /<<-?[[:space:]]*[^[:space:];&|<>()]+/)) {
        d = substr(rest, RSTART, RLENGTH)
        # The character before the opener decides whether it is one. Taken from
        # the previous iteration when the match starts the remainder, since the
        # original line is no longer what is being scanned.
        if (RSTART > 1) { pre = substr(rest, RSTART - 1, 1) }
        prev = substr(d, length(d), 1)
        rest = substr(rest, RSTART + RLENGTH)
        # The third `<` is what that check refuses: a here-STRING `<<<WORD`
        # matches this pattern from its second `<` and would take WORD as a
        # delimiter that never arrives.
        if (pre != "<") {
          dash = (substr(d, 3, 1) == "-")
          sub(/^<<-?[[:space:]]*/, "", d)
          gsub(/[\\"\047]/, "", d)
          # A delimiter carrying an expansion cannot be resolved by reading:
          # what it names is decided at run time. Nothing under this root
          # writes one, and the register says what it would cost.
          if (d ~ /[$`]/) { d = "" }
          if (d == "") { continue }
          hdn++
          # Exactly what the shell accepts as a terminator: `<<WORD`
          # ends only on a line that is WORD alone at column 0, and `<<-WORD`
          # strips TABS only -- a space-indented terminator ends neither, and
          # neither does one with a trailing space. Accepting those closes the
          # heredoc early and hands the rest of its body back as commands, which
          # is how a wait planted in data reads as the wait.
          hdq[hdn] = dash ? "^\t*" d "$" : "^" d "$"
        }
        pre = prev
      }
      if (hdn > 0) { inhd = 1; hdi = 1 }
    }
    END { if (buf != "") print start ":" buf }
  ' "$TENANT_SCRIPT"
}

# Keyed on the release name the kind composes -- `kubernetes-<name>` -- so a
# wait on some other release, which says nothing about this install, does not
# satisfy the rule.
#
# Which lines count as a wait with teeth is TOOTHLESS_RE's and
# SEQUENCE_OK_RE's business, and the reasoning for both is stated there.
#
# The namespace is matched as well as the name, because a HelmRelease is
# identified by both: the CR is created in `tenant-test`, and a wait on the
# same name elsewhere leaves this install unbounded.
#
# The closing quote in that key is load-bearing, not incidental punctuation:
# the script also waits on child releases whose names extend the parent's
# (`kubernetes-<name>-csi`, `-ingress-nginx`, `-ouroboros`, `-<component>`),
# and without the quote the key is a prefix of every one of them. The cost is
# that the parent wait has to be written with the name quoted, as the script
# writes it; the same command with the name unquoted is invisible here, which
# is why the failure below says so rather than only asking for a wait.
#
# Which lines count as the command at all is WAIT_COMMAND_RE's business, and
# the reason for its shape is stated there.
#
# The first qualifying wait AFTER the CR is applied, not the first in the file:
# a wait on this release before it exists bounds nothing, and answering with it
# would report on the wrong line while the real wait sat untouched further
# down. Not the last, either -- what the ordering rule is about is whether the
# install was bounded before anything asserted on it, and a second wait bounds
# nothing the first has not.
#
# Split from reading the budget so the two can be reported apart: a wait that
# exists but whose timeout this guard cannot parse is a different fact from no
# wait at all, and saying the second about the first sends the reader to look
# for a line that is right where it should be.
# Which command this is, on stdin and on stdout, in the one place both matchers
# read it from. Two of them ask about the same wait -- one over the script's
# text, one over the body bash prints back -- and a clause held by one and not
# the other is two matchers answering about different commands.
_wait_identity() {
  grep -E -- "$READY_CONDITION_RE" \
    | grep -F 'kubernetes-${test_name}"' \
    | grep -E -- "$TENANT_NAMESPACE_RE" \
    | grep -vE "$TOOTHLESS_RE" \
    | grep -E "$SEQUENCE_OK_RE"
}

_ready_wait_line() {
  _folded_commands \
    | grep -E "$WAIT_COMMAND_RE" \
    | _cut_comments \
    | _wait_identity \
    | cut -d: -f1 \
    | awk -v cr="$(_cr_apply_line)" '$1 > cr + 0' \
    | head -n 1
}

# Nothing when the wait carries no `--timeout` THIS matcher can see, which is
# not the same as carrying none. Accepts the three spellings a shell writes --
# `=value`, `="value"` and a separating space -- because the value is what
# matters, and reporting a quoted one as unreadable would name a cause that is
# not there.
# A trailing comment is cut before the value is read: `... --for=condition=ready
# # --timeout=5m` carries no budget at all, and reading one out of the comment
# would report a bounded wait where kubectl falls back to its own default.
#
# A repeated scalar flag keeps the last occurrence: `Set` overwrites the value
# rather than accumulating and a duplicate raises no error, so reading the last
# `--timeout` on the command matches how the flag itself resolves.
#
# Read from the folded command, so a `--timeout` pushed onto a continuation is
# read rather than reported unreadable -- the fold is what makes the value's
# physical line irrelevant here.
_ready_wait_budget() {
  _folded_commands \
    | sed -n "s/^${1}://p" \
    | _cut_comments \
    | sed -n 's/.*--timeout[= ]"\{0,1\}\([^ "]*\)"\{0,1\}.*/\1/p'
}

# The one line a fixture marks with a trailing `# @<name>`, or `0` when that is
# not what it finds. None and more than one are refusals rather than
# resolutions: taking the first would answer about a line nobody meant, and
# taking none would skip a check while looking like it ran. `0` is never a line
# number, so even a caller that forgets to test it compares against something no
# matcher can return.
#
# A printed word rather than an exit status, because the repository runner
# appends `return 0` to a function's closing brace and a status contract reads
# as success under it.
_fixture_line() {
  _fl_hits="$(grep -nE "# @$1\$" "$TENANT_SCRIPT" | cut -d: -f1 | tr '\n' ' ')"
  case "$_fl_hits" in
  *' '*' ')
    echo "fixture marker @$1 is on more than one line: $_fl_hits" >&2
    echo 0
    ;;
  '')
    echo "fixture marker @$1 is on no line of $TENANT_SCRIPT" >&2
    echo 0
    ;;
  *)
    printf '%s\n' "${_fl_hits% }"
    ;;
  esac
}

# Every line a fixture marks with `# @<name>`, in file order and space-joined,
# which is the shape the matchers' own output is compared in.
#
# Where a mark comes from decides whether the comparison is a test at all. A
# mark states the author's reading of the shell's own rules -- this line is a
# call, this line opens a body -- checked against /bin/sh when in doubt. Marked
# instead from what the matcher answered, the expectation becomes a copy of the
# thing under test, every case passes by construction, and no mutation can red
# it. That is the same trade as reconciling against a generator rather than
# borrowing from it, one level up.
_fixture_lines() {
  grep -nE "# @$1\$" "$TENANT_SCRIPT" | cut -d: -f1 | tr '\n' ' '
}

# Definitions excluded, so the line that opens a helper is not counted as a
# call to it.
#
# Matched by name prefix rather than by an enumerated pair: the OIDC helpers
# run only under `enable_oidc`, so naming just those covers one of the two
# suites while the @test below reads as a statement about both.
#
# The leading forms are the ones the tree writes: a bare call, and a call in a
# condition (`if`, `elif`, `!`, `while`, `until`). `!` needs its space for the
# same reason the wait matcher requires one: `!cozy_assert_x` names a command
# that does not exist rather than negating a call, so counting it as a call
# site would order a line that never runs an assertion. This is a matcher over
# text, so the set it recognises is the set someone listed -- a shape nobody thought of is
# invisible to it, and `hack/testdata/` below pins the ones that are. No
# trailing space is required after the name: a call with no arguments, or one
# piped into `|| rc=$?`, is still a call. A comment needs no exclusion: `#` is
# not among the forms allowed to lead, so it fails the match outright.
#
# Digits and capitals belong to a name, and a call can end in `;` as well as
# whitespace or a pipe. A `)` is not among them: for a call to both start the
# line and end with one it would have to be a case-arm pattern or a subshell,
# and in either shape the line does not begin with the call. This script talks
# about md0 throughout, so `cozy_assert_md0_ready` is the next name someone
# writes rather than a hypothetical one; a name or terminator this misses reads
# as no assertion at all, which is the one failure here that passes silently.
#
# A definition is excluded by its trailing `() {`, not by the absence of a
# space before it: `name () {` defines a function exactly as `name() {` does,
# while satisfying the trailing-space class above.
#
# The prefix is a naming constraint as much as a matcher: the rule permits
# reads before the wait, but a read named `cozy_assert_...` is counted as one
# of the end-assertions and ordered accordingly. Name a mid-install read
# something else, or the rule reds a script that obeys it.
# Where this matcher and `_folded_commands` diverge, and deliberately: the fold
# tracks heredocs and this reads line by line, so a `cozy_assert_*` written
# inside a heredoc body counts as a call site here. Direction is loud -- a call
# that never runs is placed, and a script that broke nothing reds -- and no
# subject writes an assertion into a heredoc. Tracking heredocs in both would be
# the second copy of a rule this file keeps one of.
_assert_call_lines() {
  awk '
    # An assertion helper opens a body whose contents belong to that helper.
    # A `cozy_assert_*` call inside one is composition, not an end-assertion
    # call site: it runs wherever the helper is called from, which the call
    # site below already places. Counted as a call site it reds a script that
    # broke nothing, and the only remedy left to the author -- rename the
    # helper -- contradicts the naming convention this rule relies on.
    /^cozy_(assert_|switch_and_assert_)[A-Za-z0-9_]*[[:space:]]*\(\)[[:space:]]*\{/ {
      # Written on one line it also closes on that line, never reaching a `}` of
      # its own, so opening a body here swallows every call site below it and
      # the rule reports a script with no assertions at all.
      if ($0 ~ /\}[[:space:]]*$/) { next }
      inh = 1; next
    }
    /^\}/ { inh = 0; next }
    inh { next }
    # Any other definition line is a definition, not a call.
    /^[[:space:]]*[A-Za-z_][A-Za-z0-9_]*[[:space:]]*\(\)[[:space:]]*\{[[:space:]]*$/ { next }
    # Two properties of this pattern rest on a single assertion, the set
    # comparison in "_assert_call_lines recognises the call shapes it claims":
    # that the whitespace after an operator is optional, and that the keyword
    # arms below are taken. Either one alone would red that comparison, so
    # neither is unpinned -- but one carrier holds both, and weakening the
    # comparison drops both at once without naming either.
    #
    # Command position, which is not the same as the start of a line. The
    # whitespace after an operator is optional here because the shell makes it
    # optional -- `[ x ] &&cmd` runs, checked under `sh` -- and this subject
    # never writes it that way today, so the tolerance is for the next author
    # rather than for the current text. A call
    # after `&&`, `||`, `;`, `then`, `do` or `{` stands where it stands: it runs
    # there whenever its condition holds, and a conditional call before the wait
    # is still a call before the wait. Keying on the line start alone let both
    # end-assertions sit above the wait in that form with every case green,
    # which is the one outcome this file exists to prevent. Context, not the
    # reason: no line in the subject writes an assertion after `&&` today; the
    # `&&` it does write joins test brackets inside a condition. Comments are cut before this runs, since the widened opener no
    # longer keeps a `#` from standing where the call must.
    #
    # What it now takes that is not a call: the same text inside double quotes
    # after one of those operators, as in `echo "x && cozy_assert_y"`. That is
    # a false red, loud, and no line in the subject writes it.
    $0 ~ "(^|&&[[:space:]]*|\\|\\|[[:space:]]*|;[[:space:]]*|then[[:space:]]+|else[[:space:]]+|do[[:space:]]+|\\{[[:space:]]*|\\([[:space:]]*)[[:space:]]*(if[[:space:]]+|elif[[:space:]]+|![[:space:]]+|while[[:space:]]+|until[[:space:]]+)?cozy_(assert_[A-Za-z0-9_]+|switch_and_assert_[A-Za-z0-9_]+)([[:space:]]|$|\\||;|&)" { print NR }
  ' <<EOF
$(_cut_comments < "$TENANT_SCRIPT")
EOF
}

# "<start> <end>" of the column-0 function body containing line $1, or nothing
# if that line is at file scope.
#
# The ordering rule compares source positions, and source position is only
# execution order inside a single body: a call written above the wait but
# placed in a helper runs wherever that helper is invoked. Rather than pretend
# to resolve that, the rule below uses this to refuse the comparison when the
# two are not in the same body.
_enclosing_def_range() {
  awk -v n="$1" '
    /^[A-Za-z_][A-Za-z0-9_]*[[:space:]]*\(\)[[:space:]]*\{/ { start = NR; next }
    /^\}/ { if (start && n >= start && n <= NR) { print start, NR; exit } start = 0 }
  ' "$TENANT_SCRIPT"
}

# The ordering verdict for the wait at line $1. "ok" when the assertion call
# sites are ordered after it; otherwise a diagnosis -- "unscoped" when the
# wait is not inside a function body at all, "outside <lines>" for call sites
# in some other body, "before <lines>" for ones this guard can place and that
# run too early.
#
# "ok" rather than an empty string, because empty is what this prints when it
# could not answer at all -- no anchor, no input, an error on the way. Reading
# that as "no violations" is the same fault as a `case` with no default arm:
# the check that did not happen and the check that passed become one outcome,
# and the one that did not happen is the one worth hearing about.
#
# A function rather than inline in the rule below so that both answers can be
# pinned against a fixture: the live script has no call in another body, so
# the "outside" branch would otherwise never be exercised by anything.
_ordering_violation() {
  _ov_ln="$1"
  _ov_scope="$(_enclosing_def_range "$_ov_ln")"
  if [ -z "$_ov_scope" ]; then
    echo "unscoped"
    return 0
  fi
  _ov_start="${_ov_scope% *}"
  _ov_end="${_ov_scope#* }"
  _ov_bad=""
  _ov_outside=""
  for _ov_c in $(_assert_call_lines); do
    if [ "$_ov_c" -lt "$_ov_start" ] || [ "$_ov_c" -gt "$_ov_end" ]; then
      _ov_outside="${_ov_outside}${_ov_outside:+ }$_ov_c"
    # -le, not -lt, and the equal case is unreachable today: a call on the
    # wait's own line needs a `;` that SEQUENCE_OK_RE refuses. It stays -le
    # because line numbers cannot say whether a same-line call sits before or
    # after the wait, and an ambiguous case should red rather than pass.
    elif [ "$_ov_c" -le "$_ov_ln" ]; then
      _ov_bad="${_ov_bad}${_ov_bad:+ }$_ov_c"
    fi
  done
  if [ -n "$_ov_outside" ]; then
    echo "outside $_ov_outside"
  elif [ -n "$_ov_bad" ]; then
    echo "before $_ov_bad"
  else
    echo "ok"
  fi
}

# On stdout, so the caller places it.
#
# Split from the rule so every branch is reachable by a test. Left inside the
# rule, the branch for a wait at file scope could only be reached by moving
# the wait in the script it reads, which no test can do to its own subject --
# and a branch nothing reaches is a branch whose deletion nothing catches.
#
# The `""` arm is unreachable on purpose: `_ordering_violation` answers with a
# verdict in every path today. It is here so that an early return added later
# cannot read as `ok`, and the probe below is what keeps it from being deleted
# as dead.
_ordering_message() {
  case "$1" in
  unscoped)
    echo "no function body was found around the readiness wait at line $2."
    echo "Either it is at file scope, or the body around it ended early: a"
    echo "body is delimited here by a \`}\` at column 0, and one inside a"
    echo "heredoc -- a JSON patch written across lines, say -- closes it as"
    echo "far as this reads. Check for that before moving anything."
    echo "This guard orders assertion calls by source position, which is"
    echo "execution order only within one body: with no body around the wait"
    echo "there is nothing to compare within, and the ordering has to be"
    echo "established by review instead of asserted here."
    ;;
  outside*)
    echo "assertion call sites at line(s) ${1#outside } sit outside the function"
    echo "body that holds the readiness wait at line $2. This guard orders call"
    echo "sites by source position, and source position is execution order only"
    echo "within one body: a call in another function runs wherever that"
    echo "function is invoked, which this cannot see. Keep the assertions in the"
    echo "same body as the wait, or establish the ordering by review."
    echo "If the split is deliberate, this rule is part of it and there is no"
    echo "knob for that: the body comes from where the wait sits, so following"
    echo "the split means editing this file to read the body the assertions"
    echo "moved into. Leaving that for later leaves the unit lane red on a"
    echo "script that is correct."
    echo "This verdict outranks the one for a call running too early, so"
    echo "such a call may be present as well and is not listed here."
    ;;
  before*)
    echo "assertion call sites at line(s) ${1#before } run at or before the"
    echo "readiness wait at line $2. Asserting on a cluster whose release is"
    echo "still installing is what the wait exists to prevent, and the order is"
    echo "the load-bearing half -- a wait placed after the assertions bounds"
    echo "nothing."
    echo "Reading the cluster before the wait is allowed; being NAMED as an"
    echo "end-assertion is not. If one of those lines is a deliberate"
    echo "mid-install read, rename it off the \`cozy_assert_\` prefix and this"
    echo "rule stops counting it."
    ;;
  "")
    echo "no ordering verdict was produced for the wait at line $2. This is not"
    echo "a passing check: the matcher returned nothing at all, so the ordering"
    echo "is unknown rather than correct."
    ;;
  *)
    echo "unrecognised ordering verdict '$1' for the wait at line $2. A verdict"
    echo "this rule cannot read is treated as a failure, because the alternative"
    echo "is passing on an answer nobody interpreted."
    ;;
  esac
}

# Where the wait at line $1 sits in its own body: `top` when it runs whenever
# the body runs, `nested` when a compound command decides that, and a refusal
# otherwise -- `unscoped`, `unextractable`, `unparsed`, `absent`, `ambiguous`.
# `unparsed` covers both ways the definition can fail to come back readable:
# it did not source, or it printed without the brace line bash puts on a line
# of its own.
#
# The ordering rule already refuses to judge a call whose execution source
# position cannot establish, and a wait inside `if`, `while` or `case` is the
# same question about the wait itself. Accepting it would leave one shape that
# passes while the install runs unbounded, which is the failure this file
# exists to catch, so it is refused on the same grounds rather than allowed for
# being harder to see.
#
# The nesting is bash's own answer: `declare -f` re-prints a body it has
# parsed, one indent level per level of nesting. Counting keywords here would
# mean deciding which `if` opens a compound command and which is an argument --
# the parser's job, and a miscount downwards passes a nested wait silently.
#
# What is sourced is the extracted definition, never the script: the script's
# top level sources two more files, so sourcing it would run whatever THEIR top
# level grows later, on every run of this suite and with nothing to notice it.
# A definition has no side effect by construction, but only if it is one: the
# boundaries are checked before the text reaches `source` rather than taken from
# the range on trust. The first line must open a definition and the last must be
# the closing brace with nothing after it. A body closing `}; cmd` is a range
# the helper produces from ordinary shell, and that command would run at source
# time, so the closing check is the one that carries weight; the opening one is
# not reachable through the range helper as it reads today, and the stub in the
# case below is what keeps it from being deleted as dead.
#
# The body level is read off the printer's own shape -- the first line after
# the `{` it prints on a line of its own -- rather than fixed at four
# columns, so a change in how bash indents moves the floor and the comparison
# with it. The smallest indent in the body would be the obvious answer and is
# the wrong one: a heredoc prints verbatim, keeping the indentation it has in
# the script, and the subject's heredocs put dozens of lines below the level
# any statement is printed at.
# The body bash parsed for the definition holding line $1, printed back by
# `declare -f`. Status says why not, so a caller can name the reason rather
# than treat every silence alike: 1 the line is at file scope, 2 the range is
# not a definition, 3 it did not source or came back empty.
#
# Two rules read this -- where the wait sits, and whether errexit is switched
# off above it -- and one extraction serves both. A second copy of these steps
# would be a second thing to keep in step with `_enclosing_def_range`.
_printed_body() {
  _pb_scope="$(_enclosing_def_range "$1")"
  [ -n "$_pb_scope" ] || return 1
  _pb_dir="$(mktemp -d)"
  sed -n "${_pb_scope% *},${_pb_scope#* }p" "$TENANT_SCRIPT" >"$_pb_dir/def.sh"
  _pb_name="$(sed -n '1s/^\([A-Za-z_][A-Za-z0-9_]*\)[[:space:]]*()[[:space:]]*{.*$/\1/p' "$_pb_dir/def.sh")"
  # The last line has to BE the closing brace, not merely open with one.
  # `_enclosing_def_range` ends a body at any line starting `}`, so `}; cmd` is
  # a range it produces from a file a shell author writes, and the command after
  # the brace would run at source time. Trailing whitespace is all that may
  # stand after it.
  #
  # The direction of that narrowing, since narrowing a check is where a silent
  # pass gets introduced: everything refused here comes back as a refusal the
  # caller names, never as a pass. A brace carrying a trailing comment --
  # `} # end of foo`, which no body under hack/e2e-chainsaw writes today --
  # costs a false red on a script that is fine, and that is the price of never
  # sourcing a line whose tail nobody read.
  _pb_close="$(tail -n 1 "$_pb_dir/def.sh" | sed 's/[[:space:]]*$//')"
  [ "$_pb_close" = "}" ] || _pb_name=""
  # The closing brace of the definition need not be the one the range ends on.
  # A body can close early and leave a column-0 `}` further down, with commands
  # between them: both checks above pass on such a range -- the first line opens
  # a definition, the last is a bare brace -- and what stands between runs at
  # source time. Three spellings of that were measured, and all three wrote the
  # witness the case below reads when the refusal is removed: `  }` on its own
  # line, `  }; cmd`, and `  cmd1; }; cmd2` where the brace closes the body in
  # the middle of a line.
  #
  # So the test is the brace's ROLE, not its column: a brace that closes a body
  # is a command, and what the fold emits are commands with their heredocs and
  # comments already out of the way. A `}` standing in command position in any
  # emitted command but the last means the body closed before the range did.
  # Keying on the first character of a line is what let the third spelling
  # through, and keying on the character alone would take the `{}` that this
  # subject writes in YAML inside its heredocs.
  #
  # Why that is the whole class rather than three shapes: for a command to run
  # at source time it must stand outside the body; the body must therefore close
  # before the last command; and a body closes only on a `}` in command
  # position, which is what this reads.
  #
  # The cost is a false red on a body that closes a `{ ...; }` group of its own
  # -- loud, named by the caller, and the body this guard reads carries none.
  # The surface is the whole body, not the wait's neighbourhood.
  _pb_inner="$(
    TENANT_SCRIPT="$_pb_dir/def.sh" _folded_commands \
      | sed 's/^[0-9]*://' \
      | sed '$d' \
      | grep -cE '^[[:space:]]*\}|(;|&&|\|\||&|then[[:space:]]|do[[:space:]])[[:space:]]*\}([[:space:]]|;|$)' \
      || true
  )"
  [ "$_pb_inner" = 0 ] || _pb_name=""
  if [ -z "$_pb_name" ]; then
    rm -rf "$_pb_dir"
    return 2
  fi
  _pb_body="$(bash -c '. "$1" && declare -f "$2"' _ "$_pb_dir/def.sh" "$_pb_name" 2>/dev/null || true)"
  rm -rf "$_pb_dir"
  [ -n "$_pb_body" ] || return 3
  printf '%s\n' "$_pb_body"
}

# The line of the wait inside a printed body, as that body numbers its own
# lines. Nothing else can place it there: the print carries no line numbers
# from the script.
_printed_wait_line() {
  printf '%s\n' "$1" \
    | sed 's/;[[:space:]]*$//' \
    | grep -nE "$WAIT_COMMAND_RE" \
    | _wait_identity \
    | cut -d: -f1 \
    | head -n 1
}

# Whether errexit is still on where the wait at line $1 runs, as far as the body
# holding it can say: `enabled`, `disabled`, or `unreadable` when the body or
# the wait's place in it could not be had. The nesting rule reports on those
# too, and the case for this one reds on `unreadable` as well, so an unreadable
# body reds twice about one fact -- deliberately: this rule cannot establish
# what it exists to establish, and passing on that would be a rule that answers
# only when the answer is cheap.
#
# A wait whose failure is not fatal bounds nothing: the run carries on into the
# assertions with the install still going. `set +e` cannot appear on the command,
# so the closed-set argument the matchers rest on does not reach it; the body can.
#
# The spellings are the ones a shell writes to switch it off: an `e` among the
# letters of a combined short flag, and the long `+o errexit`. A `set +e` that a
# later `set -e` cancels before the wait reds as well -- loud, and the diagnosis
# says which line it saw.
_errexit_before() {
  _eb_body="$(_printed_body "$1")" && _eb_rc=0 || _eb_rc=$?
  if [ "$_eb_rc" != 0 ]; then
    echo unreadable
    return 0
  fi
  _eb_at="$(_printed_wait_line "$_eb_body")"
  if [ -z "$_eb_at" ]; then
    echo unreadable
    return 0
  fi
  printf '%s\n' "$_eb_body" \
    | sed 's/;[[:space:]]*$//' \
    | awk -v at="$_eb_at" '
        NR >= at + 0 { exit }
        /^[[:space:]]*set[[:space:]]+\+[a-zA-Z]*e[a-zA-Z]*([[:space:]]|$)/ { found = 1 }
        /^[[:space:]]*set[[:space:]]+\+o[[:space:]]+errexit([[:space:]]|$)/ { found = 1 }
        END { print found ? "disabled" : "enabled" }
      '
}

_wait_nesting() {
  # An assignment that is not the last member of an AND-OR list is exempt from
  # errexit, which is what keeps a refusal here from ending the run.
  _wn_body="$(_printed_body "$1")" && _wn_rc=0 || _wn_rc=$?
  case "$_wn_rc" in
  0) : ;;
  1) echo unscoped; return 0 ;;
  2) echo unextractable; return 0 ;;
  *) echo unparsed; return 0 ;;
  esac
  # The same identity the line was chosen by, so the two matchers answer about
  # one command. Comments cannot reach this one: the parser dropped them before
  # it printed. What it adds instead is a separator -- every command in the
  # printed body ends in `;`, which the condition anchor and the sequence rule
  # both refuse -- so the separator comes off before the source-side matchers
  # see the line. With the separator left on, every verdict here is `absent`.
  _wn_hits="$(
    printf '%s\n' "$_wn_body" \
      | sed 's/;[[:space:]]*$//' \
      | grep -E "$WAIT_COMMAND_RE" \
      | _wait_identity \
      | awk '{ match($0, /^ */); print RLENGTH }'
  )"
  if [ -z "$_wn_hits" ]; then
    echo absent
    return 0
  fi
  # The printed body carries no line numbers, so two waits on the same release
  # cannot be told apart here. Refused rather than answered about whichever
  # came first.
  if [ "$(printf '%s\n' "$_wn_hits" | awk 'END { print NR }')" != 1 ]; then
    echo ambiguous
    return 0
  fi
  _wn_base="$(printf '%s\n' "$_wn_body" | awk '
    seen { match($0, /^ */); print RLENGTH; exit }
    /^\{[[:space:]]*$/ { seen = 1 }
  ')"
  if [ -z "$_wn_base" ]; then
    echo unparsed
    return 0
  fi
  if [ "$_wn_hits" = "$_wn_base" ]; then
    echo top
  else
    echo nested
  fi
}

@test "_usable_budget accepts the durations this script can carry and refuses the rest" {
  # 24h and 86400s are the ceiling itself, which is a wait, and the two
  # values one second past it, which this refuses along with everything that
  # would overflow the parser.
  for good in 20m 5m 90s 1h 30s 1m30s 24h 86400s; do
    if ! _usable_budget "$good"; then
      echo "_usable_budget rejected '$good', which is a usable budget" >&2
      exit 1
    fi
  done
  # The digit bound's job is keeping awk's total inside the range `[` compares,
  # and that shows up on stderr rather than in the verdict -- so the verdict
  # alone would pass with the bound gone.
  # The capture has to run without xtrace or the trace lands in it -- and the
  # restore has to be conditional. Turning xtrace ON under a runner that never
  # had it makes bats' failure formatter trace its own output, so every later
  # failure in this case reports as a hang instead of a red -- the trace grows
  # without bound under plain bats, while the repository runner ends with a
  # readable failure. How large it gets depends on this file, which is why the
  # size is not written down here.
  # The capture shape is checked before it is trusted: a redirection that drops
  # stderr would make the silence asserted below unconditional. Run here rather
  # than inside the traced region, so this case keeps exactly one conditional
  # restore -- which the case at the end of this file counts.
  _ub_ctl="$(sh -c 'echo probe >&2' 2>&1 >/dev/null || true)"
  if [ "$_ub_ctl" != probe ]; then
    echo "the stderr capture answered '$_ub_ctl', not 'probe': the silence" >&2
    echo "asserted below would hold whatever the function wrote." >&2
    exit 1
  fi
  case "$-" in *x*) _ub_xtrace=1 ;; *) _ub_xtrace=0 ;; esac
  set +x
  _ub_noise="$(_usable_budget 999999999999999999999h 2>&1 >/dev/null || true)"
  [ "$_ub_xtrace" = 0 ] || set -x
  if [ -n "$_ub_noise" ]; then
    echo "_usable_budget wrote to stderr on an overlong component: $_ub_noise" >&2
    echo "the digit bound that keeps awk's total inside the range \`[\` can" >&2
    echo "compare is gone; the verdict alone does not show this" >&2
    exit 1
  fi
  # 0m and 00s are the ones worth naming: readable, positive-looking, useless.
  # The long ones are not pedantry: kubectl parses the value as a Go duration,
  # which overflows int64 nanoseconds, and a wait it refuses never happens.
  # `999999h999999h999999h` is the case a per-component bound lets through.
  for bad in "" 5 m 5x 500ms 0m 0s 00m -5m m5s h1m 5m5 ms 1m30 20mm \
             9999999h 999999999999999999999h 25h 86401s 1440m1s \
             999999h999999h999999h; do
    if _usable_budget "$bad"; then
      echo "_usable_budget accepted '$bad', which is not a usable budget" >&2
      exit 1
    fi
  done
}

@test "READY_CONDITION_RE accepts the ways Ready-is-true is written here" {
  # The negation case is the reason the pattern is anchored, and the accepted
  # cases are the reason the anchor cannot be a bare `ready`: this repository
  # writes the capitalised form too, and `=true` is the explicit spelling of
  # the default. A wait rejected here is reported as no wait at all.
  #
  # `; then` is among the rejected: the argument ends on whitespace or the end
  # of the line, and a condition followed by `;` belongs to a shape this rule
  # does not accept at all.
  for good in \
    "--for=condition=ready" \
    "--for=condition=Ready" \
    "--for=condition=ready=true" \
    "--for=condition=Ready=True" \
    "--for=condition=ready --timeout=5m"; do
    if ! printf '%s' "$good" | grep -Eq -- "$READY_CONDITION_RE"; then
      echo "READY_CONDITION_RE rejected '$good', which waits for Ready" >&2
      exit 1
    fi
  done
  for bad in \
    "condition=ready" \
    "--for condition=ready" \
    "--for=condition=ready; then" \
    "--for=condition=ready=false" \
    "--for=condition=Ready=False" \
    "--for=condition=reconciling" \
    "--for=condition=readyz" \
    "--for=condition=ready=TRUE" \
    "--for=condition=READY" \
    "--for=condition=notready"; do
    if printf '%s' "$bad" | grep -Eq -- "$READY_CONDITION_RE"; then
      echo "READY_CONDITION_RE accepted '$bad', which is not Ready-is-true" >&2
      exit 1
    fi
  done
}

@test "TENANT_NAMESPACE_RE takes the spellings of the tenant namespace" {
  # The namespace completes the release's identity, so a form it misses is
  # reported as no wait at all -- and `=` is the spelling this repo's own
  # convention pushes an author toward. A separator is not required after the
  # short flag, because `-ntenant-test` names the same namespace, but it is
  # required after the long one: `--namespacetenant-test` is a flag kubectl
  # does not have, and calling it correctly scoped would be scoping a line
  # that never runs. What ends the match is the name itself, which is what
  # keeps `tenant-test-2` out.
  for good in \
    "-n tenant-test" "--namespace tenant-test" "--namespace=tenant-test" \
    "-n \"tenant-test\"" "-n tenant-test --timeout=5m" "-ntenant-test" \
    "-n=tenant-test"; do
    if ! printf 'x %s y\n' "$good" | grep -qE -- "$TENANT_NAMESPACE_RE"; then
      echo "TENANT_NAMESPACE_RE rejected '$good', which names the tenant namespace" >&2
      exit 1
    fi
  done
  for bad in "-n other-ns" "-n tenant-test-2" "-n \${NS}" "--namespace kube-system" \
             "--namespacetenant-test"; do
    if printf 'x %s y\n' "$bad" | grep -qE -- "$TENANT_NAMESPACE_RE"; then
      echo "TENANT_NAMESPACE_RE accepted '$bad', which is not the tenant namespace" >&2
      exit 1
    fi
  done
}

@test "a wait with its failure discarded does not satisfy the rule" {
  # The accepted set is small and the rejected one is open, so this pins both
  # ends by shape rather than by naming the commands that mask a failure.
  #
  # Both spellings of the command itself are here, and so is the long resource
  # name the diagnosis promises is refused: `helmrelease` written out. Without
  # that row, widening the pattern to take it reds nothing.
  #
  # The negated `if !` is rejected with the rest: it hands the failure to a
  # branch, and whether that branch ends the run is not on this line. The first
  # rejected row carries no `if`, so it is the one that reaches SEQUENCE_OK_RE:
  # a wait ending in `; then` is the tail of a multi-line `if` condition, whose
  # status the `if` consumes rather than errexit. The one
  # accepted row carrying grep -n's `<digits>:` prefix is there because
  # SEQUENCE_OK_RE runs after grep -n and has to tolerate it.
  for good in \
    "  kubectl wait hr -n t \"k\" --timeout=5m --for=condition=ready" \
    "kubectl wait hr -n t \"k\" --timeout=5m --for=condition=ready" \
    "  kubectl wait hr -n t \"k\" --for=condition=ready --timeout=5m" \
    "  kubectl_wait_retry hr -n t \"k\" --timeout=5m --for=condition=ready" \
    "6013:  kubectl wait hr -n t \"k\" --timeout=5m --for=condition=ready"; do
    if ! printf '%s\n' "$good" | grep -E "$WAIT_COMMAND_RE" | grep -vE "$TOOTHLESS_RE" \
      | grep -qE "$SEQUENCE_OK_RE"; then
      echo "a wait that propagates its failure was rejected: $good" >&2
      exit 1
    fi
  done
  for bad in \
    "  kubectl wait hr -n t \"k\" --for=condition=ready &" \
    "  kubectl wait hr -n t \"k\" --for=condition=ready || true" \
    "  kubectl wait hr -n t \"k\" --for=condition=ready || rc=\$?" \
    "  kubectl wait hr -n t \"k\" --for=condition=ready | tee log" \
    "  kubectl wait hr -n t \"k\" --for=condition=ready; echo done" \
    "  kubectl wait hr -n t \"k\" --timeout=5m --for=condition=ready ; then" \
    "  kubectl wait hr/\"k\" -n t \"k\" --for=condition=ready" \
    "6013:  kubectl wait hr -n t \"k\" --for=condition=ready || true" \
    "6013:  kubectl wait hr -n t \"k\" --for=condition=ready &" \
    "6013:  if ! kubectl wait hr -n t \"k\" --for=condition=ready" \
    "  if ! kubectl wait hr -n t \"k\" --for=condition=ready; then" \
    "  if !kubectl wait hr -n t \"k\" --for=condition=ready; then" \
    "  if ! kubectl wait hr -n t \"k\" --for=condition=ready; echo x; then" \
    "  echo kubectl wait hr -n t \"k\" --for=condition=ready" \
    "  kubectl wait deployment -n t \"k\" --for=condition=ready" \
    "  kubectl get hr -n t \"k\" --for=condition=ready" \
    "  kubectl wait helmrelease -n t \"k\" --for=condition=ready" \
    "  kubectl wait --timeout=5m hr -n t \"k\" --for=condition=ready" \
    "  kubectl wait hr -n t \"k\" --for=condition=ready && echo ok" \
    "  if kubectl wait hr -n t \"k\" --for=condition=ready; then"; do
    if printf '%s\n' "$bad" | grep -E "$WAIT_COMMAND_RE" | grep -vE "$TOOTHLESS_RE" \
      | grep -qE "$SEQUENCE_OK_RE"; then
      echo "a wait whose failure goes nowhere was accepted: $bad" >&2
      exit 1
    fi
  done
}

@test "_assert_call_lines recognises the call shapes it claims, and only calls" {
  # The matcher whose gap is invisible: a shape it does not recognise reads as
  # "no assertion here", so the ordering rule passes on a script that breaks it.
  # Pinned against a fixture rather than the live script, because the live one
  # writes two shapes today and the point is the set, not those two.
  TENANT_SCRIPT="$REPO_ROOT/hack/testdata/install-wait/call-shapes.sh"
  got="$(_assert_call_lines | tr '\n' ' ')"
  # Each shape and each near-miss is labelled where it lives, in the fixture,
  # and the reason a near-miss is one sits on its own line there -- no address
  # is written down here, because an address in this file is the one thing that
  # does not move with the line it names. The near-miss next to the negated
  # call is the shape that reads closest to a call and is not one:
  # `!cozy_assert_...` with no space names a command rather than negating a
  # call. The last block covers command position that is not the line's start
  # -- after `&&`, `||`, `then`, `do` and `{` -- since a call in any of them
  # runs like one written first.
  want="$(_fixture_lines call)"
  if [ "$got" != "$want" ]; then
    echo "_assert_call_lines on the fixture = [$got], want [$want]" >&2
    echo "a missing line is a shape the ordering rule cannot see; an extra one" >&2
    echo "is a comment or a definition counted as a call" >&2
    exit 1
  fi
}

@test "_cr_apply_line finds the tenant CR and nothing that merely resembles it" {
  # Decoys stand ahead of the real CR in the order a looser match would take
  # them; each is labelled in the fixture.
  TENANT_SCRIPT="$REPO_ROOT/hack/testdata/install-wait/call-shapes.sh"
  got="$(_cr_apply_line)"
  # Marked on the apiVersion line: the kind line's own trailing space is a
  # fixture property, and a mark would take its place. The offset is the
  # adjacency the matcher itself requires.
  want="$(( $(_fixture_line cr-above) + 1 ))"
  if [ "$got" != "$want" ]; then
    echo "_cr_apply_line on the fixture = '$got', want $want (the tenant CR)" >&2
    exit 1
  fi
}

@test "_ready_wait_line takes the wait that runs, on the release that matters" {
  # A decoy per claim the matcher makes beyond `kubectl wait hr`, each labelled
  # in the fixture, plus a second qualifying wait after the real one.
  TENANT_SCRIPT="$REPO_ROOT/hack/testdata/install-wait/call-shapes.sh"
  got="$(_ready_wait_line)"
  # The wait's own line ends in a backslash, and a continuation cannot carry a
  # trailing comment, so the mark sits on the line above it.
  want="$(( $(_fixture_line live-wait-above) + 1 ))"
  if [ "$got" != "$want" ]; then
    echo "_ready_wait_line on the fixture = '$got', want $want" >&2
    echo "every earlier wait is disqualified by exactly one claim, and a later" >&2
    echo "qualifying wait follows, which bounds nothing the first has not" >&2
    exit 1
  fi
  # That wait is written across a continuation with the flag split from its
  # value, so reading its budget goes through the fold: the joined command has
  # to read `--timeout 5m`, not `--timeout` followed by the continuation's own
  # indent, or the value is invisible and the wait reads as unbudgeted.
  budget="$(_ready_wait_budget "$got")"
  if [ "$budget" != "5m" ]; then
    echo "_ready_wait_budget on the folded wait = '$budget', want 5m" >&2
    echo "the fold joined the continuation without dropping its indent" >&2
    exit 1
  fi
}

@test "_ready_wait_budget reads the spellings a shell writes" {
  # One value, three ways to attach it. A quoted budget reported as unreadable
  # would send the reader looking for a missing --timeout that is on the line.
  TENANT_SCRIPT="$REPO_ROOT/hack/testdata/install-wait/call-shapes.sh"
  # The three spellings the header names, in the order the fixture writes them:
  # `--timeout=5m`, `--timeout="5m"`, `--timeout 5m`. Named here as well as
  # marked there, so the set a reader checks against is the set the case runs.
  set -- '--timeout=5m' '--timeout="5m"' '--timeout 5m'
  for probe in $(_fixture_lines budget-probe); do
    spelling="$1"
    shift
    line="$(sed -n "${probe}p" "$TENANT_SCRIPT")"
    case "$line" in
    *"$spelling"*) : ;;
    *)
      echo "the fixture line $probe no longer writes $spelling, so this case" >&2
      echo "no longer covers the set its header claims" >&2
      exit 1
      ;;
    esac
    got="$(_ready_wait_budget "$probe")"
    if [ "$got" != "5m" ]; then
      echo "_ready_wait_budget $probe ($spelling) = '$got', want 5m" >&2
      exit 1
    fi
  done
  if [ "$#" != 0 ]; then
    echo "spellings named here and not marked in the fixture: $*" >&2
    exit 1
  fi
  # A --timeout only inside a trailing comment is no budget at all.
  in_comment="$(_fixture_line budget-in-comment)"
  if [ -n "$(_ready_wait_budget "$in_comment")" ]; then
    echo "_ready_wait_budget read a value out of a trailing comment" >&2
    exit 1
  fi
  # No line number, no budget: `sed -n "p"` would otherwise print the file and
  # a --timeout from some other line would be read as this wait's.
  if [ -n "$(_ready_wait_budget "")" ]; then
    echo "_ready_wait_budget answered without being given a line" >&2
    exit 1
  fi
}

@test "_enclosing_def_range places a line in the body that holds it" {
  TENANT_SCRIPT="$REPO_ROOT/hack/testdata/install-wait/call-shapes.sh"
  # Every line here comes from the fixture's own marks, expectations included:
  # a marked address moves with the line it names, while a number in this file
  # stays where it was written and starts answering about whatever moved into
  # its place.
  #
  # The body holding the wait. What must not be taken for a body -- an
  # indented definition, a brace inside a heredoc -- is labelled in the fixture.
  open="$(_fixture_line body-open)"
  close="$(_fixture_line body-close)"
  got="$(_enclosing_def_range "$((open + 1))")"
  if [ "$got" != "$open $close" ]; then
    echo "_enclosing_def_range $((open + 1)) = '$got', want '$open $close'" >&2
    exit 1
  fi
  sopen="$(_fixture_line spaced-open)"
  sclose="$(_fixture_line spaced-close)"
  got="$(_enclosing_def_range "$((sopen + 1))")"
  if [ "$got" != "$sopen $sclose" ]; then
    echo "_enclosing_def_range $((sopen + 1)) = '$got', want '$sopen $sclose'" >&2
    exit 1
  fi
  at_scope="$(_fixture_line file-scope)"
  got="$(_enclosing_def_range "$at_scope")"
  if [ -n "$got" ]; then
    echo "_enclosing_def_range $at_scope = '$got', want nothing: file scope" >&2
    exit 1
  fi
  # Without the reset that follows a close, a brace nothing opened would close
  # the body that closed already and swallow everything between.
  after="$(_fixture_line after-close)"
  got="$(_enclosing_def_range "$after")"
  if [ -n "$got" ]; then
    echo "_enclosing_def_range $after = '$got', want nothing: that line sits" >&2
    echo "between a closed body and a brace nothing opened" >&2
    exit 1
  fi
}

# Both keys within one document at the same indent, in either order, not
# necessarily adjacent, and with the kind quoted or bare -- `kind: "Kubernetes"`
# is the same scalar and the same cluster. The pre-filter has to accept the
# quotes too, since it decides which files the scan is given at all.
#
# A YAML mapping is unordered, which is why order is free here: `kind` before
# `apiVersion`, or `metadata` between them, are the same manifest and create
# the same cluster. Adjacency would see neither, and a sweep that misses a
# creator leaves the claim below passing while false -- which is the whole
# failure this case exists to prevent.
#
# No case here exercises the leading anchor: losing it widens the
# sweep, and a creator found where there is none reds the uniqueness case
# loudly rather than passing. Named because the CR matcher's comment credits
# its own anchor with the same job and that one has a case behind it.
#
# Two sibling objects at the SAME indent inside one document are not separated:
# the buckets reset on `---` and nothing else, so an `apps.cozystack.io`
# apiVersion in one `apply.resource:` block and a `kind: Kubernetes` in the next
# are paired and the file is reported as a creator though neither object is one.
# Reproduced -- a file whose two blocks are:
#
#     - apply: {resource: {apiVersion: apps.cozystack.io/v1alpha1, kind: Tenant}}
#     - apply: {resource: {apiVersion: v1, kind: Kubernetes}}
#
# written out as indented mappings, comes back from this sweep as a creator.
#
# Not cured, and the reason is the cure rather than the cost of the disease.
# Separating them does not need a parser -- a reset when the indent drops below
# the bucket would do it -- but that reset, wrong once, splits the apiVersion
# from the kind of ONE object and a real creator goes missing. The disease reds
# the uniqueness case and names the file; the cure can hide a creator silently,
# which is the failure this whole guard exists to prevent. A remedy that can
# fail quietly is worse than a fault that always fails loudly.
#
# Context, not the reason: no file under the sweep root pairs an indented
# `kind: Kubernetes` with a cozystack apiVersion today. One carries the
# indented kind alone -- `securitygroup/securitygroup.yaml`, under a `fromApp:`
# -- and it is named again below, where the reason it is not a creator is the
# apiVersion rather than the indent.
#
# The indent is what keeps the precision an anchor would give -- the case that
# reaches it is a `kind: Kubernetes` nested under another key with a cozystack
# apiVersion elsewhere in the same document, which `creators/near-misses.yaml`
# carries. The two near-misses under the sweep root today are excluded by the
# same fact: neither carries an `apps.cozystack.io` apiVersion at all, so no
# pair can form in either. The `application.kind` label is excluded twice over,
# on the field name as well. The `fromApp:` entry is not: its `- kind:` line
# registers in the bucket the dash makes, which is what the sequence fixture
# pins, so the apiVersion is the whole of why that file is not a creator.
# Pairing across documents is what the `---` split prevents.
# The sweep sorts under LC_ALL=C: `sort` otherwise orders by the collating
# sequence of whatever locale the suite runs in, and three cases compare its
# output against a list written in one order. No fixture name distinguishes the
# two orders today, so this pins a property nothing measures -- named here for
# that reason rather than left to look checked.
_kind_creators() {
  _kc_root="$1"
  # Each filename becomes one argument instead of a word of an unquoted
  # expansion: `$(grep -rl ...)` is re-split on whitespace and globbed, so a
  # checkout under a path carrying a space sweeps nothing and the uniqueness
  # case reds blaming a missing creator rather than the path. A filename
  # holding a newline still splits, since the list is newline-delimited, and
  # that one reports a file that does not exist rather than passing.
  #
  # One awk over the whole list, not one per file: awk dies on the file it
  # cannot handle, and with the run split per file the survivors come back
  # looking like a complete answer: a mutation that makes awk fatal
  # stops reddening anything the moment each file gets its own run.
  #
  # What a creator can be written in, decided here rather than left to whatever
  # the root happens to contain. The rule is pre-emptive: a manifest quoted in
  # prose creates nothing, and a grep over every file reds on an edit to it.
  # The shape exists in this repository -- docs/oidc-tenant.md carries the kind
  # in a fenced example -- outside this root rather than in it, which is why
  # the root is a choice about place and this is a choice about form. A parked
  # `.disabled` suite creates nothing either; re-enabling one restores the
  # `.yaml` name the sweep reads, so that exclusion cannot outlive the parking.
  #
  # The two spellings of the YAML suffix are both taken, and the direction is
  # why: a suffix left out is a creator the sweep walks past, and the case below
  # then reports one creator while two clusters run. Every other gap here costs
  # a false red; this one costs a silent pass, so the list errs wide.
  # `set --` here is the function's own parameter list, not the caller's: a
  # function's positional parameters are restored on return, measured under
  # sh, dash and bash. The list is built this way so each filename reaches awk
  # as one argument regardless of the spaces in it.
  set --
  while IFS= read -r _kc_file; do
    case "$_kc_file" in
    *.sh | *.yaml | *.yml) set -- "$@" "$_kc_file" ;;
    esac
  done <<EOF
$(grep -rlE 'kind:[[:space:]]*["'"'"']?Kubernetes' "$_kc_root" 2>/dev/null)
EOF
  # No files is not no answer: with an empty argument list awk falls back to
  # standard input and the sweep wedges on whatever the caller was reading.
  [ "$#" -gt 0 ] || return 0
  awk -v kindre="^$KIND_VALUE_RE" '
    function endoc(f,   i) {
      for (i in kinds) if (i in avs) { print f; break }
      delete kinds; delete avs
    }
    FNR == 1 && NR > 1 { endoc(prev) }
    {
      prev = FILENAME
      ind = $0; sub(/[^ \t].*$/, "", ind)
      body = $0; sub(/^[ \t]*/, "", body)
      # A sequence dash introduces an item whose remaining keys are indented
      # past it, so the bucket takes the width of the dash and the whitespace
      # after it. Its own width, not a fixed two: `-   apiVersion:` puts the
      # siblings four columns in, and a fixed two would file the keys of one item
      # in different buckets and report the creator as none at all -- silently.
      # No apostrophe in this comment: the awk program is single-quoted, and one
      # ends it. The break is loud -- the file stops parsing and nothing runs.
      if (match(body, /^-[ \t]+/)) {
        ind = ind sprintf("%*s", RLENGTH, "")
        body = substr(body, RLENGTH + 1)
      }
    }
    /^---/ { endoc(FILENAME); next }
    body ~ /^apiVersion: apps\.cozystack\.io\// { avs[ind] = 1 }
    body ~ kindre { kinds[ind] = 1 }
    END { endoc(prev) }
  ' "$@" | LC_ALL=C sort -u
}

@test "KIND_VALUE_RE decides every way this tree can name the kind on one line" {
  # The axis list, decided once here rather than discovered one at a time.
  # Accepted forms leave the document unchanged; refused ones
  # need a YAML parser, which is a different tool than a line matcher.
  for good in \
    "kind: Kubernetes" \
    "kind:  Kubernetes" \
    "kind:	Kubernetes" \
    "kind: \"Kubernetes\"" \
    "kind: 'Kubernetes'" \
    "kind: Kubernetes # a note" \
    "kind: Kubernetes "; do
    if ! printf '%s\n' "$good" | grep -qE "^$KIND_VALUE_RE"; then
      echo "KIND_VALUE_RE rejected '$good', which names this kind" >&2
      exit 1
    fi
  done
  # Refused on purpose. A parser would take these; this guard reads lines, and
  # the header says so beside the pattern.
  for bad in \
    "{kind: Kubernetes}" \
    "{\"kind\":\"Kubernetes\"}" \
    "kind: &anchor Kubernetes" \
    "kind: !!str Kubernetes" \
    "kind: KubernetesNodes" \
    "kind: \"KubernetesNodes\"" \
    "kind: Kubernetes extra"; do
    if printf '%s\n' "$bad" | grep -qE "^$KIND_VALUE_RE"; then
      echo "KIND_VALUE_RE accepted '$bad'" >&2
      echo "either it names another kind, or reading it needs a parser" >&2
      exit 1
    fi
  done
}

@test "one script under the e2e-chainsaw root creates the tenant Kubernetes CR" {
  # Everything here reads TENANT_SCRIPT, which names one file. A second e2e
  # script that began creating this kind would be governed by no rule in this
  # file and nothing would red -- the guard would keep passing about a script
  # nobody asked it to read. True today, and this is what keeps it true.
  #
  # The sweep is hack/e2e-chainsaw only, not the repository. Widening it would
  # need an exclusion for hack/testdata/, whose fixtures carry the kind on
  # purpose, and for docs/, where docs/oidc-tenant.md carries it inside a
  # fenced example; neither creates a cluster. What the narrow root leaves
  # outside is not only those: `hack/e2e-apps/` is a live lane of its own, and a
  # creator appearing there would pass this case in silence -- nothing there
  # creates this kind today, so it is a risk the root carries rather than a
  # hole it has. That direction costs the most, which is why it is named here
  # rather than discovered when a second cluster runs unguarded. The root is
  # not a filter by itself either: what a creator can be
  # written in is decided beside the sweep, so that prose acquiring a fenced
  # manifest reds nothing here.
  found="$(_kind_creators "$REPO_ROOT/hack/e2e-chainsaw" \
    | sed "s|^$REPO_ROOT/||" | tr '\n' ' ')"
  want="hack/e2e-chainsaw/_lib/run-kubernetes.sh "
  if [ "$found" != "$want" ]; then
    echo "scripts under hack/e2e-chainsaw creating a Kubernetes CR: [$found]" >&2
    echo "Check first whether the new file APPLIES the kind or only asserts on" >&2
    echo "it: this sweep pairs an apiVersion with a kind and cannot tell an" >&2
    echo "\`apply:\` block from an \`assert:\` one, so a suite that merely" >&2
    echo "asserts lands here too and needs excluding rather than governing." >&2
    echo "want [$want]. Another one means this guard reads a subject that is no" >&2
    echo "longer the only one, so point it at each of them or widen the rule." >&2
    exit 1
  fi
}

@test "the fixtures still carry the trailing spaces the end anchors depend on" {
  # Both anchors are pinned by a line ending in a space, which is invisible and
  # which an editor or a formatter removes without asking. Losing it would not
  # red anything -- it would quietly unpin the anchors -- so it is checked here.
  #
  # Those lines carry two properties each: they are what the matcher must find,
  # and they end in the space that measures the end anchor. Appending anything
  # to one of them -- a note, a fixture mark -- takes the space's place and
  # removes the second property while the first still reads as intact, which is
  # why the marks near them sit on neighbouring lines.
  for f in "$REPO_ROOT/hack/testdata/install-wait/creators/trailing-space.yaml" \
           "$REPO_ROOT/hack/testdata/install-wait/creators/near-misses.yaml" \
           "$REPO_ROOT/hack/testdata/install-wait/call-shapes.sh"; do
    if ! grep -qE 'kind: [A-Za-z]+ $' "$f"; then
      echo "$f no longer has a \`kind:\` line ending in a space." >&2
      echo "That space is what makes the end anchors measurable; restore it or" >&2
      echo "the two cases that depend on it pass without checking anything." >&2
      exit 1
    fi
  done
  # The same hazard on a different property: a heredoc terminator carrying a
  # trailing space is not a terminator to the shell, and that is the whole
  # subject of the fixture below. Stripped, the fixture stops testing the
  # thing it is named for and still passes.
  hd="$REPO_ROOT/hack/testdata/install-wait/heredoc-terminators.sh"
  if ! grep -qE '^[A-Z_]+ $' "$hd"; then
    echo "$hd no longer has a terminator line ending in a space." >&2
    echo "That space is why the shell reads that line as heredoc content" >&2
    echo "rather than as the end of the body; without it the case below" >&2
    echo "measures nothing." >&2
    exit 1
  fi
}

@test "the creator sweep emits the last document of the last file" {
  # The scan holds a document open until a separator or the end of the input,
  # and the END flush is what emits the final one. On the other roots the last
  # file readdir returns happens not to be a creator, so dropping that flush
  # changes nothing there -- and readdir order differs between greps and file
  # systems, which makes that coverage an accident rather than a pin. This root
  # holds exactly one file and it is a creator.
  found="$(_kind_creators "$REPO_ROOT/hack/testdata/install-wait/creators-single" \
    | sed "s|^$REPO_ROOT/||" | tr '\n' ' ')"
  want="hack/testdata/install-wait/creators-single/only.yaml "
  if [ "$found" != "$want" ]; then
    echo "sweep over a single-creator root = [$found], want [$want]" >&2
    echo "the last document of the last file was not emitted" >&2
    exit 1
  fi
}

@test "the creator sweep pairs across sequence items, loudly and on purpose" {
  # The cost written above `_kind_creators`, measured rather than asserted: two
  # items at one indent share a bucket, so an apiVersion from one pairs with a
  # kind from the other and the file is reported. Nothing under the sweep root
  # is written this way today.
  #
  # The cure is what keeps it uncured. Resetting the bucket when the indent
  # drops separates the items, and that reset -- wrong once -- separates the
  # apiVersion from the kind of ONE object, and a real creator goes missing
  # with nothing to say so. This fault names a file; that one names nothing.
  found="$(_kind_creators "$REPO_ROOT/hack/testdata/install-wait/creators-cross-pair" \
    | sed "s|^$REPO_ROOT/||" | tr '\n' ' ')"
  want="hack/testdata/install-wait/creators-cross-pair/list.yaml "
  if [ "$found" != "$want" ]; then
    echo "sweep over the cross-pairing fixture = [$found], want [$want]" >&2
    echo "If this now answers nothing, the pairing was narrowed: check that" >&2
    echo "the narrowing cannot also separate the two keys of a single object," >&2
    echo "because that failure is silent and this one is not." >&2
    exit 1
  fi
}

@test "the creator sweep reads scripts and manifests, not prose or parked suites" {
  # Neither a manifest quoted in prose nor one in a parked `.disabled` file
  # creates a cluster. No file under the sweep root carries either shape today,
  # so the fixtures here are what makes the rule measurable at all -- they put
  # the kind in both, which is the difference between a rule that is checked
  # and one that is only stated.
  #
  # Both YAML suffixes are here, and they are the half that must be FOUND. A
  # suffix the list forgets is a creator walked past in silence, while a file
  # type taken by mistake costs a red somebody reads.
  found="$(_kind_creators "$REPO_ROOT/hack/testdata/install-wait/creators-filetypes" \
    | sed "s|^$REPO_ROOT/||" | tr '\n' ' ')"
  want="hack/testdata/install-wait/creators-filetypes/real.yaml hack/testdata/install-wait/creators-filetypes/real.yml "
  if [ "$found" != "$want" ]; then
    echo "sweep over the file-type fixtures = [$found], want [$want]" >&2
    echo "both manifests must be found and the fenced example and the parked" >&2
    echo "suite must not. A missing manifest is the worse direction: the" >&2
    echo "uniqueness case then reports one creator while two clusters run." >&2
    exit 1
  fi
}

@test "the creator sweep reads a file whose path carries a space" {
  # The path rather than the content: a file list expanded unquoted is re-split
  # on the space, the sweep answers nothing, and the uniqueness case reds with
  # a diagnosis about a missing creator -- true about a subject that is not the
  # one at fault. Checkout paths are the caller's, not this repository's.
  tmp="$(mktemp -d)"
  d="$tmp/sweep root"
  mkdir -p "$d"
  printf 'apiVersion: apps.cozystack.io/v1alpha1\nkind: Kubernetes\n' >"$d/creator.yaml"
  found="$(_kind_creators "$d")"
  if [ "$found" != "$d/creator.yaml" ]; then
    echo "sweep of a root whose path carries a space = [$found]," >&2
    echo "want [$d/creator.yaml]" >&2
    rm -rf "$tmp"
    exit 1
  fi
  rm -rf "$tmp"
}

@test "the creator sweep answers on an empty sweep rather than reading stdin" {
  # With no files in its argument list awk falls back to standard input, so the
  # sweep wedges on a terminal instead of failing -- in the one case it should
  # be shouting loudest, its root gone. Checked by what is left of stdin, not
  # by waiting for a hang, so this case cannot itself hang.
  rest="$(printf 'a\nb\n' | { _kind_creators "$REPO_ROOT/no-such-sweep-root" >/dev/null; cat; })"
  if [ "$rest" != "a
b" ]; then
    echo "the creator sweep consumed its standard input on an empty sweep;" >&2
    echo "with no files to read awk reads stdin, and a run whose sweep root" >&2
    echo "moved hangs instead of reporting that it moved" >&2
    exit 1
  fi
}

@test "the creator sweep sees a CR in any valid layout and refuses shapes that only name the kind" {
  # The sweep's own teeth. Its subject today writes the CR at column 0, so
  # nothing in the tree would notice the indent tolerance going away -- this
  # fixture is what notices.
  found="$(_kind_creators "$REPO_ROOT/hack/testdata/install-wait/creators" \
    | sed "s|^$REPO_ROOT/||" | tr '\n' ' ')"
  want="hack/testdata/install-wait/creators/indented-apply.yaml hack/testdata/install-wait/creators/kind-first.yaml hack/testdata/install-wait/creators/quoted-kind.yaml hack/testdata/install-wait/creators/sequence-item.yaml hack/testdata/install-wait/creators/trailing-space.yaml hack/testdata/install-wait/creators/wide-dash.yaml "
  if [ "$found" != "$want" ]; then
    echo "creator sweep over the fixtures = [$found], want [$want]" >&2
    echo "the indented apply.resource shape must be found, and the selector" >&2
    echo "entry and the application.kind label must not be" >&2
    exit 1
  fi
}

@test "a terminator the shell would not accept does not end a heredoc here" {
  # `<<WORD` ends only on a line that is WORD alone at column 0; `<<-WORD`
  # strips tabs and nothing else, in all four forms /bin/sh accepts.
  # Every wait in this fixture sits in a heredoc body behind one of those
  # near-terminators, so none of them runs -- and a matcher that ends the
  # heredoc early answers with a line of YAML data instead of reporting that
  # the script has no wait.
  # An assertion that nothing was found is satisfied by a matcher that finds
  # nothing anywhere, so the control comes first: the same matcher, on a file
  # of the same shape whose wait is a command, has to answer.
  _ctl_dir="$(mktemp -d)"
  printf '%s\n' "f() {" \
    '  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready' \
    "}" >"$_ctl_dir/live.sh"
  TENANT_SCRIPT="$_ctl_dir/live.sh"
  if [ -z "$(_ready_wait_line)" ]; then
    echo "the control answered nothing: _ready_wait_line finds no wait even" >&2
    echo "where one runs, so the emptiness asserted below says nothing about" >&2
    echo "the fixture." >&2
    rm -rf "$_ctl_dir"
    exit 1
  fi
  rm -rf "$_ctl_dir"
  TENANT_SCRIPT="$REPO_ROOT/hack/testdata/install-wait/heredoc-terminators.sh"
  got="$(_ready_wait_line)"
  if [ -n "$got" ]; then
    echo "_ready_wait_line on the false-terminator fixture = '$got'," >&2
    echo "want nothing: every wait there is heredoc data. A terminator was" >&2
    echo "accepted that the shell would not accept -- space-indented, trailing" >&2
    echo "space, or space-indented under <<- which strips tabs only." >&2
    exit 1
  fi
}

@test "the three kind matchers diverge only where they are meant to" {
  # They answer different questions, so they answer differently: the CR matcher
  # pins the one heredoc this repository writes, the counter has to see a second
  # cluster in any spelling, and the sweep hunts creators in files nobody here
  # wrote. Left as prose this becomes a fourth copy of the rules and drifts from
  # them; derived here, a matcher that changes reds this case and the author
  # decides whether the new divergence was intended.
  #
  # Columns: _cr_apply_lines / _cr_count / _kind_creators / _ready_wait_line.
  # `-` is no answer.
  #
  # The rule the rows follow from: any form in which YAML can write the same
  # object must be seen by the counter and by the sweep, because a cluster does
  # not stop being one when it is written differently. Divergence between
  # matchers is allowed only where they ask different questions -- as with a
  # heredoc, which is a CR to the matcher that reads what `kubectl apply`
  # receives and is not a command to the matcher that reads what runs.
  #
  # Each row records what was observed AND why that answer is right, because a
  # recorded value on its own reads as "checked" when it only means "observed":
  # a wrong answer pinned as an expectation is indistinguishable from a right
  # one until someone states the reason beside it.
  #
  # bare-adjacent / quoted: the CR matcher answers the kind's line, since that
  # is what it reports; the count is 1 and the sweep finds it -- one cluster,
  # named three ways.
  # kind-first / field-between: the CR matcher declines, because it pins the one
  # heredoc shape this repository writes and that shape is adjacent; the count
  # and the sweep must still see a cluster, because a cluster is what it is.
  # indented-pair / sequence-item: same reasoning, and the count is 1 because a
  # second cluster written either way is still a second cluster.
  #
  # The `in-heredoc` row is the one that must NOT agree, and it is here so that
  # nobody harmonises it away: the CR matcher reads inside a heredoc because
  # that heredoc is piped to `kubectl apply` and its body IS the CR, while the
  # wait matcher must not, because nothing there executes. Same syntax,
  # opposite requirements, recorded as an expectation rather than left looking
  # like an inconsistency between two neighbouring functions.
  d="$(mktemp -d)"
  for probe in \
    "bare-adjacent|apiVersion: apps.cozystack.io/v1alpha1;kind: Kubernetes|2|1|found|-" \
    "kind-first|kind: Kubernetes;apiVersion: apps.cozystack.io/v1alpha1|-|1|found|-" \
    "field-between|apiVersion: apps.cozystack.io/v1alpha1;metadata: {};kind: Kubernetes|-|1|found|-" \
    "quoted|apiVersion: apps.cozystack.io/v1alpha1;kind: \"Kubernetes\"|2|1|found|-" \
    "indented-pair|  apiVersion: apps.cozystack.io/v1alpha1;  kind: Kubernetes|-|1|found|-" \
    "sequence-item|items:;- apiVersion: apps.cozystack.io/v1alpha1;  kind: Kubernetes|-|1|found|-" \
    "in-heredoc|cat <<EOF;apiVersion: apps.cozystack.io/v1alpha1;kind: Kubernetes;  kubectl wait hr -n tenant-test \"kubernetes-\${test_name}\" --timeout=5m --for=condition=ready;EOF|3|1|found|-"; do
    name="${probe%%|*}"; rest="${probe#*|}"
    body="${rest%%|*}"; rest="${rest#*|}"
    want_cr="${rest%%|*}"; rest="${rest#*|}"
    want_n="${rest%%|*}"; rest="${rest#*|}"
    want_sweep="${rest%%|*}"; want_wait="${rest#*|}"
    mkdir -p "$d/$name"
    printf '%s\n' "$body" | tr ';' '\n' > "$d/$name/x.yaml"
    TENANT_SCRIPT="$d/$name/x.yaml"
    got_cr="$(_cr_apply_lines | tr '\n' ' ')"; got_cr="${got_cr% }"
    [ -n "$got_cr" ] || got_cr="-"
    got_n="$(_cr_count)"
    if [ -n "$(_kind_creators "$d/$name")" ]; then got_sweep="found"; else got_sweep="-"; fi
    got_wait="$(_ready_wait_line)"; [ -n "$got_wait" ] || got_wait="-"
    if [ "$got_cr" != "$want_cr" ] || [ "$got_n" != "$want_n" ] \
       || [ "$got_sweep" != "$want_sweep" ] || [ "$got_wait" != "$want_wait" ]; then
      echo "$name: cr=$got_cr count=$got_n sweep=$got_sweep wait=$got_wait" >&2
      echo "want:  cr=$want_cr count=$want_n sweep=$want_sweep wait=$want_wait" >&2
      echo "a matcher changed how it answers this shape. The three are allowed" >&2
      echo "to differ -- they answer different questions -- but each difference" >&2
      echo "is deliberate and this row is the record of it." >&2
      rm -rf "$d"
      exit 1
    fi
  done
  rm -rf "$d"
}

@test "a kind line on the first line is not a CR without an apiVersion" {
  # awk starts `av` at 0, so on line 1 the adjacency test compares 0 with 0.
  # Pointed at a fixture whose first line names the kind and which carries no
  # apiVersion at all: without the `av > 0` guard this answers 1.
  # Control first, for the same reason: a matcher that answers nothing
  # everywhere would satisfy the emptiness below without reading anything.
  TENANT_SCRIPT="$REPO_ROOT/hack/testdata/install-wait/call-shapes.sh"
  if [ -z "$(_cr_apply_lines)" ]; then
    echo "the control answered nothing: _cr_apply_lines finds no CR even in" >&2
    echo "the fixture that carries one, so the emptiness below is about the" >&2
    echo "matcher rather than about the file." >&2
    exit 1
  fi
  TENANT_SCRIPT="$REPO_ROOT/hack/testdata/install-wait/creators/first-line-kind.yaml"
  got="$(_cr_apply_lines | tr '\n' ' ')"
  if [ -n "$got" ]; then
    echo "_cr_apply_lines on a file with no apiVersion = [$got], want nothing" >&2
    echo "a kind line on line 1 satisfied the adjacency test against av = 0" >&2
    exit 1
  fi
}

@test "counting the CRs of a subject that has none answers zero rather than failing" {
  # The zero path of the count. Pointed at the ordering fixture, which carries a
  # wait and no CR at all: what makes the answer a number rather than a failure
  # is _cr_count's `print n + 0`, since awk prints 0 for no matches and exits 0.
  TENANT_SCRIPT="$REPO_ROOT/hack/testdata/install-wait/order-violation.sh"
  n="$(_cr_count)"
  if [ "$n" != "0" ]; then
    echo "_cr_count on a subject with no CR = '$n', want 0" >&2
    exit 1
  fi
}

@test "the script creates exactly one tenant Kubernetes cluster" {
  # The ordering rule pairs one CR with one wait. A second CR in the same
  # script would be checked by nothing: the matchers take the first of each,
  # so the existing wait would keep the guard green while the second cluster
  # ran to its assertions unbounded. Refusing here is the honest answer --
  # extending the rule to many clusters is a different rule, and this makes
  # someone write it rather than inherit a guard that covers half.
  n="$(_cr_count)"
  if [ "$n" != "1" ]; then
    echo "$n tenant Kubernetes CRs applied in $TENANT_SCRIPT; this rule pairs" >&2
    echo "one CR with one readiness wait and cannot say anything about the" >&2
    echo "others. Extend it to match waits to CRs before adding another." >&2
    exit 1
  fi
}

@test "the script creates a tenant Kubernetes cluster" {
  # Not a claim about how many: only that the subject this file reasons about
  # is still there. Zero means the creation moved or changed shape, and every
  # rule below would then pass by having nothing to check.
  cr="$(_cr_apply_line)"
  if [ -z "$cr" ]; then
    echo "no tenant Kubernetes CR application found in $TENANT_SCRIPT;" >&2
    echo "either it moved, or this guard's match is stale -- refusing to" >&2
    echo "report on a script it could not read" >&2
    exit 1
  fi
  echo "tenant CR applied at line $cr" >&2
}

@test "the tenant release is waited for Ready, with a budget this guard can read" {
  cr="$(_cr_apply_line)"
  [ -n "$cr" ] || { echo "no tenant CR found; see the case above" >&2; exit 1; }
  ln="$(_ready_wait_line)"
  if [ -z "$ln" ]; then
    echo "no readiness wait on the parent tenant HelmRelease after the CR is applied." >&2
    echo "A release whose install action is still running cannot be uninstalled," >&2
    echo "so whatever is left of the install is charged to teardown and the run" >&2
    echo "reds on a step that asserts nothing. Add a" >&2
    echo "\`kubectl wait hr ... --for=condition=ready --timeout=<budget>\` on the" >&2
    echo "parent release before the assertions." >&2
    echo "If such a wait is already there, every way this match can reject a" >&2
    echo "line ends here, so check all of them rather than the first that comes" >&2
    echo "to mind:" >&2
    echo "  - the command spelled \`kubectl wait hr\`, or \`kubectl_wait_retry hr\`" >&2
    echo "    through the wrapper the subject defines: not helmrelease, not" >&2
    echo "    hr/<name>, not mentioned inside another command or a comment, and" >&2
    echo "    the resource directly after \`wait\` -- a flag written ahead of it," >&2
    echo "    as in \`kubectl wait --timeout=5m hr ...\`, is invisible here" >&2
    echo "  - the call in command position and bare: no \`if\`, negated or not," >&2
    echo "    since a handler this rule cannot read may log and fall through." >&2
    echo "    If you are here because you wrapped the wait to add diagnostics," >&2
    echo "    only the inline handler is refused: a Chainsaw \`catch:\` block" >&2
    echo "    attaches them without touching this line, which is what the" >&2
    echo "    conventions prescribe. To keep them inline, widen this rule to" >&2
    echo "    read the branch's own exit rather than reading the red as a ban" >&2
    echo '  - the release written as `kubernetes-${test_name}"`, closing quote and' >&2
    echo "    variable name included, which is what stops the child releases" >&2
    echo "    whose names extend the parent's from satisfying the rule. The" >&2
    echo "    name has to stand on the line as text: hoisted into a variable" >&2
    echo "    and waited on through it, there is nothing here to match and" >&2
    echo "    the wait reads as absent" >&2
    echo "  - the wait placed after the CR is applied: one before it waits on a" >&2
    echo "    release that does not exist yet and is not counted. A wait moved" >&2
    echo "    into a helper whose DEFINITION sits above the CR falls here too," >&2
    echo "    though it runs wherever that helper is called: position in the" >&2
    echo "    file is all this reads, and it is dropped rather than refused" >&2
    echo "  - the namespace \`tenant-test\`, in a spelling TENANT_NAMESPACE_RE" >&2
    echo "    accepts: a release is identified by namespace and name both, and a" >&2
    echo "    namespace passed through a variable cannot be read from the text" >&2
    echo "  - a \`--for\` naming Ready as true, in a spelling READY_CONDITION_RE" >&2
    echo "    accepts" >&2
    echo "  - no \`|\` and no \`&\` in the command, \`||\` and \`&&\` included: a" >&2
    echo "    wait whose failure goes nowhere is not a wait. A \`2>&1\` added" >&2
    echo "    for diagnostics carries an \`&\` and is refused with them --" >&2
    echo "    a false red, and the redirection is the cause rather than a" >&2
    echo "    discarded failure: move it off this line." >&2
    echo "  - no \`;\` in the command at all: every use of one here puts the" >&2
    echo "    wait in a list or a condition whose status is somebody else's" >&2
    echo "  A trailing comment is cut before every check but the first, so" >&2
    echo "  nothing inside one is ever the cause of any of them." >&2
    exit 1
  fi
  budget="$(_ready_wait_budget "$ln")"
  if [ -z "$budget" ]; then
    echo "the readiness wait is at line $ln but this guard cannot see a --timeout on it." >&2
    echo "The wait is there; what is missing is a budget this guard can read." >&2
    echo "Either the command carries no --timeout at all, or it carries one in a" >&2
    echo "spelling this cannot read: the value may be bare or double-quoted, and" >&2
    echo "single quotes are not among the forms it takes." >&2
    exit 1
  fi
  if ! _usable_budget "$budget"; then
    echo "the readiness wait at line $ln carries '$budget', which is not a duration" >&2
    echo "this guard can read as non-zero. The readable set is narrower than the" >&2
    echo "one kubectl accepts -- '500ms' and '1.5h' are refused here by choice --" >&2
    echo "so check it against _usable_budget rather than against kubectl." >&2
    echo "A zero timeout is not a wait either." >&2
    exit 1
  fi
  echo "readiness wait at line $ln, budget $budget" >&2
}

@test "the assertion helpers this rule can name run after the readiness wait" {
  ln="$(_ready_wait_line)"
  [ -n "$ln" ] || { echo "no readiness wait; see the case above" >&2; exit 1; }
  calls="$(_assert_call_lines)"
  if [ -z "$calls" ]; then
    echo "no tenant-cluster assertion call sites found; either they moved or" >&2
    echo "this guard's match is stale" >&2
    exit 1
  fi
  v="$(_ordering_violation "$ln")"
  if [ "$v" != "ok" ]; then
    _ordering_message "$v" "$ln" >&2
    exit 1
  fi
}

@test "_ordering_violation refuses to place a call in another function body" {
  # Not a violation of the ordering rule but a shape it cannot decide; the two
  # must not be reported as the same thing.
  TENANT_SCRIPT="$REPO_ROOT/hack/testdata/install-wait/call-shapes.sh"
  open="$(_fixture_line body-open)"
  close="$(_fixture_line body-close)"
  outside=""
  for c in $(_fixture_lines call); do
    if [ "$c" -lt "$open" ] || [ "$c" -gt "$close" ]; then
      outside="${outside}${outside:+ }$c"
    fi
  done
  got="$(_ordering_violation "$((open + 1))")"
  if [ "$got" != "outside $outside" ]; then
    echo "_ordering_violation $((open + 1)) = '$got', want 'outside $outside'" >&2
    echo "those calls sit outside the body holding the wait, so source position" >&2
    echo "cannot place them" >&2
    exit 1
  fi
  # This probe, not the one above, is what exercises the precedence. The wait
  # in the fixture stands before every in-body call, so the first probe has no
  # placeable call to weigh against the unplaceable ones and any rule answering
  # `outside` would satisfy it. Moving the wait to the last in-body call makes
  # both sets non-empty, and only then is `outside` a choice rather than the
  # single available answer. Collapsing this case to one probe turns it from a
  # test of precedence into a test of detection.
  last=""
  for c in $(_fixture_lines call); do
    if [ "$c" -gt "$open" ] && [ "$c" -lt "$close" ]; then last="$c"; fi
  done
  got="$(_ordering_violation "$last")"
  if [ "$got" != "outside $outside" ]; then
    echo "_ordering_violation $last = '$got', want 'outside $outside'" >&2
    exit 1
  fi
  # A line at file scope has no body to order within, so the answer is a
  # refusal rather than a verdict about any call site.
  got="$(_ordering_violation "$(_fixture_line file-scope)")"
  if [ "$got" != "unscoped" ]; then
    echo "_ordering_violation at file scope = '$got', want 'unscoped'" >&2
    exit 1
  fi
}

@test "_ordering_violation reports a call that runs before the wait" {
  # The verdict that names the violation this guard exists to catch. It needs
  # its own fixture, for the reason that fixture's header gives.
  TENANT_SCRIPT="$REPO_ROOT/hack/testdata/install-wait/order-violation.sh"
  # The equal case the `-le` carries: a call whose line number is the one handed
  # in as the wait's. Source position cannot say which of the two ran first, so
  # it reds rather than passing.
  early="$(_fixture_line early-call)"
  got="$(_ordering_violation "$(_fixture_line wait)")"
  if [ "$got" != "before $early" ]; then
    echo "_ordering_violation at the wait = '$got', want 'before $early': a call on the" >&2
    echo "line handed in as the wait's is ambiguous and must red, not pass" >&2
    exit 1
  fi
  got="$(_ordering_violation "$early")"
  if [ "$got" != "before $early" ]; then
    echo "_ordering_violation at the early call = '$got'," >&2
    echo "want 'before $early': that line calls an assertion ahead of the wait," >&2
    echo "and line 11 calls one after it, which is not a violation" >&2
    exit 1
  fi
}

@test "_ordering_violation says ok rather than nothing when the order holds" {
  # Against the real script, where the order does hold. The point is the word:
  # silence is what the matcher produces when it could not answer, so success
  # has to be something a reader and the rule can both name.
  ln="$(_ready_wait_line)"
  [ -n "$ln" ] || { echo "no readiness wait; see the case above" >&2; exit 1; }
  got="$(_ordering_violation "$ln")"
  if [ "$got" != "ok" ]; then
    echo "_ordering_violation $ln = '$got', want 'ok'" >&2
    exit 1
  fi
}

@test "_ordering_message answers every verdict, including ones it cannot read" {
  # The branch for a wait at file scope is unreachable from the live script,
  # so this is the only thing that exercises it -- and the empty and unknown
  # verdicts are the two that must not come out as silence.
  # Each verdict is checked against a phrase only its own arm produces, not
  # merely against non-emptiness: an arm deleted from the case falls through
  # to the closing one, which still prints something and still names the line.
  # Non-emptiness calls that a pass, and a real violation is then reported as
  # an unreadable verdict instead of as the violation.
  for probe in \
    "unscoped|no function body was found around" \
    "outside 42|sit outside the function" \
    "before 42|run at or before the" \
    "|no ordering verdict was produced" \
    "something nobody wrote|unrecognised ordering verdict"; do
    verdict="${probe%%|*}"
    phrase="${probe#*|}"
    got="$(_ordering_message "$verdict" 99)"
    case "$got" in
    *"$phrase"*) : ;;
    *)
      echo "_ordering_message '$verdict' did not say '$phrase'" >&2
      echo "it said: $got" >&2
      exit 1
      ;;
    esac
    case "$got" in
    *99*) : ;;
    *) echo "_ordering_message '$verdict' did not name the line" >&2; exit 1 ;;
    esac
  done
}

@test "_wait_nesting tells a wait that always runs from one a branch decides" {
  TENANT_SCRIPT="$REPO_ROOT/hack/testdata/install-wait/wait-nesting.sh"
  # The line is found from the function name rather than written down, so
  # editing the fixture cannot silently move a probe onto another wait.
  for row in \
    "cozy_wait_at_top top" \
    "cozy_wait_in_branch nested" \
    "cozy_wait_in_loop nested" \
    "cozy_wait_in_case nested" \
    "cozy_wait_in_until nested" \
    "cozy_wait_in_subshell absent" \
    "cozy_wait_twice ambiguous" \
    "cozy_wait_tail unextractable" \
    "cozy_wait_broken unparsed"; do
    fn="${row% *}"
    want="${row#* }"
    ln="$(awk -v fn="$fn" '
      $0 ~ "^" fn "\\(\\) \\{" { inside = 1; next }
      inside && /kubectl wait hr/ { print NR; exit }
    ' "$TENANT_SCRIPT")"
    if [ -z "$ln" ]; then
      echo "no wait found in $fn: the fixture moved out from under this case" >&2
      exit 1
    fi
    got="$(_wait_nesting "$ln")"
    if [ "$got" != "$want" ]; then
      echo "_wait_nesting in $fn (line $ln) = '$got', want '$want'" >&2
      exit 1
    fi
  done
  # A wait at file scope has no body to be nested in, and the ordering rule
  # reports that same verdict about the same line.
  got="$(_wait_nesting 1)"
  if [ "$got" != unscoped ]; then
    echo "_wait_nesting at file scope = '$got', want 'unscoped'" >&2
    exit 1
  fi
}

@test "_wait_nesting refuses rather than sources what is not a definition" {
  TENANT_SCRIPT="$REPO_ROOT/hack/testdata/install-wait/wait-nesting.sh"
  # Stubbed in a subshell: this refusal guards what reaches `source`, and the
  # range helper cannot produce a range whose first line opens no definition.
  # Without the stub it would be a branch nothing reaches, and a branch nothing
  # reaches is one whose deletion nothing catches. The other refusal -- a body
  # closing `}; cmd` -- the helper does produce, so the case below reaches it
  # through the fixture rather than through a stub.
  (
    _enclosing_def_range() { echo "1 4"; }
    got="$(_wait_nesting 2)"
    if [ "$got" != unextractable ]; then
      echo "_wait_nesting on a range that is not a definition = '$got'," >&2
      echo "want 'unextractable'" >&2
      exit 1
    fi
  )
  # A body this could read, holding no wait on the release asked about.
  (
    _enclosing_def_range() { awk '/^cozy_wait_none\(\) \{/ { s = NR } s && /^\}/ { print s, NR; exit }' "$TENANT_SCRIPT"; }
    got="$(_wait_nesting 2)"
    if [ "$got" != absent ]; then
      echo "_wait_nesting on a body without the wait = '$got', want 'absent'" >&2
      exit 1
    fi
  )
  # The reachable refusals that close early, both spellings of one shape: the
  # definition closes on an indented brace and the column-0 one is left to a
  # later line, so the range passes both boundary checks while carrying what
  # stands between them. The second fixture puts a command on the brace line
  # itself, which is the spelling a refusal keyed on the brace standing alone
  # would let through.
  # The control for the three cases below: each fixture is supposed to carry a
  # command that runs when its range is sourced, and an empty witness proves
  # the refusal only if the tail would otherwise write one. Sourced here
  # deliberately, by the same extraction, so a fixture edited into a no-op reds
  # instead of leaving the refusals vacuously green.
  for fx in inner-brace inner-brace-command midline-close; do
    ctl="$(mktemp)"
    (
      TENANT_SCRIPT="$REPO_ROOT/hack/testdata/install-wait/${fx}.sh"
      ln="$(awk '/kubectl wait hr/ { print NR; exit }' "$TENANT_SCRIPT")"
      range="$(_enclosing_def_range "$ln")"
      dir="$(mktemp -d)"
      sed -n "${range% *},${range#* }p" "$TENANT_SCRIPT" >"$dir/def.sh"
      COZY_TAIL_WITNESS="$ctl" bash -c '. "$1"' _ "$dir/def.sh" >/dev/null 2>&1 || true
      rm -rf "$dir"
    )
    if [ ! -s "$ctl" ]; then
      echo "$fx carries no command that runs when its range is sourced, so" >&2
      echo "the empty witness below proves nothing: the fixture, not the" >&2
      echo "refusal, is what keeps it empty." >&2
      rm -f "$ctl"
      exit 1
    fi
    rm -f "$ctl"
  done
  for fx in inner-brace inner-brace-command midline-close; do
    witness2="$(mktemp)"
    (
      TENANT_SCRIPT="$REPO_ROOT/hack/testdata/install-wait/${fx}.sh"
      ln2="$(awk '/kubectl wait hr/ { print NR; exit }' "$TENANT_SCRIPT")"
      COZY_TAIL_WITNESS="$witness2"
      export COZY_TAIL_WITNESS
      got2="$(_wait_nesting "$ln2")"
      if [ "$got2" != unextractable ] || [ -s "$witness2" ]; then
        echo "$fx answered '$got2'; the witness is" >&2
        echo "$([ -s "$witness2" ] && echo written || echo empty). Want" >&2
        echo "'unextractable' and an empty witness: the range ends on a bare" >&2
        echo "brace that closes nothing, and what stands above it would run." >&2
        exit 1
      fi
    ) || { rm -f "$witness2"; exit 1; }
    rm -f "$witness2"
  done
  # The refusal that is reachable, and the only one whose cost is a side effect
  # rather than a wrong answer. The verdict alone cannot show it: a tail that
  # ran and then handed back a readable definition answers `top`, and one
  # refused answers `unextractable`, so the witness is what separates them.
  witness="$(mktemp)"
  ln="$(awk '
    /^cozy_wait_tail\(\) \{/ { inside = 1; next }
    inside && /kubectl wait hr/ { print NR; exit }
  ' "$TENANT_SCRIPT")"
  if [ -z "$ln" ]; then
    echo "no wait found in cozy_wait_tail: the fixture moved out from under" >&2
    echo "this case" >&2
    exit 1
  fi
  got="$(
    COZY_TAIL_WITNESS="$witness"
    export COZY_TAIL_WITNESS
    _wait_nesting "$ln"
  )"
  if [ "$got" != unextractable ] || [ -s "$witness" ]; then
    echo "a body closing \`}; cmd\` answered '$got' and left the witness" >&2
    echo "$([ -s "$witness" ] && echo written || echo empty). Want" >&2
    echo "'unextractable' and an empty witness: what stands after the brace" >&2
    echo "runs at source time, and this helper's whole claim is that only a" >&2
    echo "definition ever reaches \`source\`." >&2
    rm -f "$witness"
    exit 1
  fi
  rm -f "$witness"
}

@test "the readiness wait runs whenever the body holding it runs" {
  ln="$(_ready_wait_line)"
  if [ -z "$ln" ]; then
    echo "no readiness wait; see the case above" >&2
    exit 1
  fi
  got="$(_wait_nesting "$ln")"
  # One text per verdict. A single message written for the nested case tells a
  # reader whose wait is at top level that it is not, and leaves the cause --
  # two waits, a body that would not parse -- named nowhere.
  case "$got" in
  top) ;;
  nested)
    echo "the readiness wait at line $ln sits inside a compound command." >&2
    echo "How far down the body it sits does not matter, only what encloses" >&2
    echo "it: a wait under an \`if\`, a loop or a \`case\` runs when that branch" >&2
    echo "is taken, and whether it is taken is a runtime fact no rule over" >&2
    echo "source text has. The ordering below would then be about a command" >&2
    echo "that need not run at all." >&2
    exit 1
    ;;
  ambiguous)
    echo "more than one readiness wait on this release stands in the body at" >&2
    echo "line $ln. The printed body carries no line numbers, so this cannot" >&2
    echo "say which of them the rule above found, and answering about the" >&2
    echo "wrong one is worse than refusing. Leave one wait on the parent" >&2
    echo "release in that body." >&2
    exit 1
    ;;
  absent)
    echo "the wait at line $ln is not in the body this read back, though the" >&2
    echo "rule above found it in the file. A wait inside a subshell reads" >&2
    echo "this way -- the printer puts it on the line that opens the" >&2
    echo "subshell, where no matcher looks for it -- and so does a" >&2
    echo "disagreement between the two readings, which would be a defect" >&2
    echo "here rather than in the script. Check for the subshell first." >&2
    exit 1
    ;;
  unscoped)
    echo "the readiness wait at line $ln is at file scope, with no body to" >&2
    echo "be nested in. The ordering rule reports the same about the same" >&2
    echo "line and says what to do about it." >&2
    exit 1
    ;;
  *)
    echo "the body holding the readiness wait at line $ln could not be read" >&2
    echo "('$got'). That is a refusal to judge and not a pass: the placement" >&2
    echo "this rule exists to establish was never established." >&2
    echo "Look first for a closing brace standing as a command inside that" >&2
    echo "body -- at the start of a line or after a \`;\` -- which a" >&2
    echo "\`{ ...; }\` group written across lines does. Such a brace closes" >&2
    echo "the definition as far as this reads, so the extraction refuses the" >&2
    echo "range rather than sourcing what follows it. A heredoc carrying a" >&2
    echo "\`}\` is a different failure with a different verdict: that one" >&2
    echo "ends the body at column 0 and reports the wait as unscoped. A" >&2
    echo "definition that does not parse on its own lands here too." >&2
    exit 1
    ;;
  esac
}

@test "_errexit_before reads the body around the wait, above it and not below" {
  TENANT_SCRIPT="$REPO_ROOT/hack/testdata/install-wait/wait-nesting.sh"
  for row in \
    "cozy_wait_errexit_off disabled" \
    "cozy_wait_errexit_combined disabled" \
    "cozy_wait_errexit_after enabled" \
    "cozy_wait_at_top enabled"; do
    fn="${row% *}"
    want="${row#* }"
    ln="$(awk -v fn="$fn" '
      $0 ~ "^" fn "\\(\\) \\{" { inside = 1; next }
      inside && /kubectl wait hr/ { print NR; exit }
    ' "$TENANT_SCRIPT")"
    if [ -z "$ln" ]; then
      echo "no wait found in $fn: the fixture moved out from under this case" >&2
      exit 1
    fi
    got="$(_errexit_before "$ln")"
    if [ "$got" != "$want" ]; then
      echo "_errexit_before in $fn (line $ln) = '$got', want '$want'" >&2
      exit 1
    fi
  done
}

@test "the composition rule sees a one-line assertion definition close" {
  TENANT_SCRIPT="$REPO_ROOT/hack/testdata/install-wait/one-line-def.sh"
  # A definition written on one line opens nothing: the call below it is a call
  # site, and a rule that opens a body there reports a script with no
  # assertions at all -- which reads as "nothing asserts after the wait".
  want="$(grep -n 'cozy_assert_tenant_reachable "' "$TENANT_SCRIPT" | cut -d: -f1)"
  got="$(_assert_call_lines | tr '\n' ' ')"
  if [ "$got" != "$want " ]; then
    echo "_assert_call_lines on a one-line definition = [$got], want [$want ]" >&2
    exit 1
  fi
}

@test "_errexit_before takes the spellings that switch errexit off and no others" {
  # The set is decided here, the way the other matchers in this file decide
  # theirs, rather than left to whichever spelling a fixture happens to carry.
  # `set -euo pipefail` is what this tree writes, so the combined forms are the
  # ones an author reaches for, and a spelling missed here is a silent pass:
  # the guard answers `enabled` over a body that switched errexit off.
  d="$(mktemp -d)"
  for row in \
    "set +e|disabled" \
    "set +ex|disabled" \
    "set +eu|disabled" \
    "set +euo pipefail|disabled" \
    "set +xe|disabled" \
    "set +o errexit|disabled" \
    "set +u|enabled" \
    "set +E|enabled" \
    "set +o pipefail|enabled" \
    "set -e|enabled"; do
    line="${row%|*}"
    want="${row#*|}"
    printf '%s\n' "cozy_probe() {" "  $line" \
      "  kubectl wait hr -n tenant-test \"kubernetes-\${test_name}\" --timeout=5m --for=condition=ready" \
      "}" >"$d/probe.sh"
    TENANT_SCRIPT="$d/probe.sh"
    got="$(_errexit_before 3)"
    if [ "$got" != "$want" ]; then
      echo "_errexit_before over a body carrying \`$line\` = '$got'," >&2
      echo "want '$want'. A spelling that switches errexit off and is not" >&2
      echo "taken here passes silently, which is the direction this rule" >&2
      echo "cannot afford." >&2
      rm -rf "$d"
      exit 1
    fi
  done
  rm -rf "$d"
}

@test "errexit is not switched off above the readiness wait" {
  ln="$(_ready_wait_line)"
  if [ -z "$ln" ]; then
    echo "no readiness wait; see the case above" >&2
    exit 1
  fi
  got="$(_errexit_before "$ln")"
  if [ "$got" != enabled ]; then
    echo "errexit is '$got' where the readiness wait at line $ln runs. A wait" >&2
    echo "whose failure is not fatal bounds nothing: the run carries on into" >&2
    echo "the assertions with the install still going, and every other check" >&2
    echo "here passes while it does. 'unreadable' means the body could not be" >&2
    echo "read at all, and this case reds on that too: the nesting case above" >&2
    echo "names the cause, and both refuse rather than pass on a body neither" >&2
    echo "of them could establish anything about." >&2
    exit 1
  fi
}

@test "a wait written through the retry wrapper is placed and budgeted like any other" {
  TENANT_SCRIPT="$REPO_ROOT/hack/testdata/install-wait/retry-wrapper.sh"
  ln="$(_ready_wait_line)"
  if [ -z "$ln" ]; then
    echo "the retry wrapper form was not taken as a wait at all, so hardening" >&2
    echo "the wait against a transient failure would read as removing it" >&2
    exit 1
  fi
  v="$(_ordering_violation "$ln")"
  if [ "$v" != ok ]; then
    echo "_ordering_violation on the wrapper subject = '$v', want 'ok'" >&2
    exit 1
  fi
  # Why an expiry is bounded by the number on the line is derived once, beside
  # WAIT_COMMAND_RE. What the retry can multiply is the transient path, which
  # ends the run non-zero anyway.
  budget="$(_ready_wait_budget "$ln")"
  if [ "$budget" != 5m ]; then
    echo "_ready_wait_budget on the wrapper form = '$budget', want 5m" >&2
    exit 1
  fi
}

@test "a wrapper wait with no budget is refused, not exempted" {
  TENANT_SCRIPT="$REPO_ROOT/hack/testdata/install-wait/retry-wrapper-unbounded.sh"
  ln="$(_ready_wait_line)"
  if [ -z "$ln" ]; then
    echo "the unbounded wrapper wait was not found, so this case measures" >&2
    echo "nothing about the budget" >&2
    exit 1
  fi
  budget="$(_ready_wait_budget "$ln")"
  if [ -n "$budget" ]; then
    echo "_ready_wait_budget invented '$budget' for a wait carrying no" >&2
    echo "--timeout; kubectl would fall back to its own default and the rule" >&2
    echo "would report a bound nobody wrote" >&2
    exit 1
  fi
}

@test "_cut_comments cuts what a shell cuts and keeps what it keeps" {
  # The claims in this helper's header, as cases rather than as prose. The last
  # row is the one that matters most: an escaped quote does not close the
  # double-quoted region, so the `||` after it survives the cut and the wait
  # that carries it is refused for having no teeth.
  for kept in \
    'foo#bar' \
    '"a # b"' \
    "'a # b'" \
    '"a\" # b" || true'; do
    got="$(printf '%s\n' "$kept" | _cut_comments)"
    if [ "$got" != "$kept" ]; then
      echo "_cut_comments changed [$kept] into [$got]" >&2
      exit 1
    fi
  done
  got="$(printf '%s\n' 'cmd --flag # note' | _cut_comments)"
  if [ "$got" != 'cmd --flag' ]; then
    echo "_cut_comments left [$got], want [cmd --flag]" >&2
    exit 1
  fi
}

@test "a command still folding when the input ends is emitted, not dropped" {
  # The flush at EOF, which nothing else reaches: every other subject ends its
  # last command before the file ends. A file ending inside a continuation
  # leaves that command in the buffer, and without the flush the wait it holds
  # is never seen -- the guard then reports a script with no readiness wait,
  # which is the loud direction but about the wrong cause.
  TENANT_SCRIPT="$REPO_ROOT/hack/testdata/install-wait/eof-continuation.sh"
  want="$(grep -n 'kubectl wait hr' "$TENANT_SCRIPT" | cut -d: -f1)"
  got="$(_ready_wait_line)"
  if [ "$got" != "$want" ]; then
    echo "_ready_wait_line on a file ending mid-continuation = '$got'," >&2
    echo "want $want. The command was still in the fold when the input ran" >&2
    echo "out, so only the flush at the end of the scan can emit it." >&2
    exit 1
  fi
}

@test "the heredoc opener takes the spellings a shell takes, and their delimiters" {
  # Derived from the shell\'s rule rather than from what this tree happens to
  # write: the delimiter is the word after the operator with quote removal
  # applied, and quoting may sit anywhere inside that word. Both directions are
  # checked per spelling, because each fails differently -- a body read as
  # commands reports a wait that never runs, and a delimiter read short
  # swallows every command after the heredoc, which is the silent one.
  #
  # `<<<WORD` is a here-string and opens nothing; a delimiter carrying an
  # expansion cannot be resolved by reading, so it is not taken as an opener
  # and its body reads as commands -- a false red, loud, and written nowhere
  # under this root.
  d="$(mktemp -d)"
  for spec in \
    "EOF|EOF|hides" \
    "-EOF|\tEOF|hides" \
    " EOF|EOF|hides" \
    "\"EOF\"|EOF|hides" \
    "'EOF'|EOF|hides" \
    "\\\\EOF|EOF|hides" \
    "E\"O\"F|EOF|hides" \
    "E\\\\OF|EOF|hides" \
    "-\"EOF\"|\tEOF|hides" \
    "<EOF|EOF|reads" \
    "\$DELIM|EOF|reads"; do
    opener="${spec%%|*}"; rest="${spec#*|}"
    term="${rest%%|*}"; want="${rest#*|}"
    wait_line='  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready'
    printf '%s\n' "f() {" "  cat <<$opener" "$wait_line" "$(printf '%b' "$term")" "}" >"$d/in.sh"
    printf '%s\n' "f() {" "  cat <<$opener" "data" "$(printf '%b' "$term")" "$wait_line" "}" >"$d/after.sh"
    TENANT_SCRIPT="$d/in.sh"
    inside="$(_ready_wait_line)"
    TENANT_SCRIPT="$d/after.sh"
    following="$(_ready_wait_line)"
    case "$want" in
    hides)
      if [ -n "$inside" ] || [ -z "$following" ]; then
        echo "opener \`<<$opener\`: a wait in the body answered '$inside'" >&2
        echo "(want nothing) and a wait after the terminator answered" >&2
        echo "'$following' (want a line). The shell takes this spelling and" >&2
        echo "resolves the delimiter to the quoted word; reading it short" >&2
        echo "swallows everything after the heredoc without a sound." >&2
        rm -rf "$d"
        exit 1
      fi
      ;;
    reads)
      if [ -z "$inside" ]; then
        echo "opener \`<<$opener\` hid the body, and the shell opens no" >&2
        echo "heredoc on it -- a here-string, or a delimiter this cannot" >&2
        echo "resolve. Hiding it is the silent direction." >&2
        rm -rf "$d"
        exit 1
      fi
      ;;
    esac
  done
  rm -rf "$d"
}

@test "a backslash-quoted heredoc delimiter hides its body like a quoted one" {
  # `<<\\EOF` quotes the delimiter the way `<<'EOF'` does, and an opener
  # matcher that takes the quotes and not the backslash reads the body as
  # commands. The wait inside this fixture is data, so the answer is nothing --
  # and the failure that follows from getting it wrong is a wait reported on a
  # script that runs none.
  # An assertion that nothing was found is satisfied by a matcher that finds
  # nothing anywhere, so the control comes first: the same matcher, on a file
  # of the same shape whose wait is a command, has to answer.
  _ctl_dir="$(mktemp -d)"
  printf '%s\n' "f() {" \
    '  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready' \
    "}" >"$_ctl_dir/live.sh"
  TENANT_SCRIPT="$_ctl_dir/live.sh"
  if [ -z "$(_ready_wait_line)" ]; then
    echo "the control answered nothing: _ready_wait_line finds no wait even" >&2
    echo "where one runs, so the emptiness asserted below says nothing about" >&2
    echo "the fixture." >&2
    rm -rf "$_ctl_dir"
    exit 1
  fi
  rm -rf "$_ctl_dir"
  TENANT_SCRIPT="$REPO_ROOT/hack/testdata/install-wait/heredoc-backslash.sh"
  got="$(_ready_wait_line)"
  if [ -n "$got" ]; then
    echo "_ready_wait_line on the backslash-quoted fixture = '$got'," >&2
    echo "want nothing: that wait is heredoc data. The opener matcher must" >&2
    echo "take the backslash as a quote, or the body below it reads as" >&2
    echo "commands." >&2
    exit 1
  fi
}

@test "a heredoc opener carrying a quoted hash still hides its body" {
  # An assertion that nothing was found is satisfied by a matcher that finds
  # nothing anywhere, so the control comes first: the same matcher, on a file
  # of the same shape whose wait is a command, has to answer.
  _ctl_dir="$(mktemp -d)"
  printf '%s\n' "f() {" \
    '  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready' \
    "}" >"$_ctl_dir/live.sh"
  TENANT_SCRIPT="$_ctl_dir/live.sh"
  if [ -z "$(_ready_wait_line)" ]; then
    echo "the control answered nothing: _ready_wait_line finds no wait even" >&2
    echo "where one runs, so the emptiness asserted below says nothing about" >&2
    echo "the fixture." >&2
    rm -rf "$_ctl_dir"
    exit 1
  fi
  rm -rf "$_ctl_dir"
  TENANT_SCRIPT="$REPO_ROOT/hack/testdata/install-wait/heredoc-hash-opener.sh"
  # The fold decides where a heredoc starts, and it has to read the opener the
  # way the shell does. Cut comments crudely and the ` #` inside quotes takes
  # the `<<YAML` with it: the body then reads as commands and its planted line
  # answers as the wait, which is a pass on a script that never waits.
  ln="$(_ready_wait_line)"
  if [ -n "$ln" ]; then
    echo "_ready_wait_line answered line $ln on a subject whose only such line" >&2
    echo "is heredoc data. The opener was lost, so the body was read as" >&2
    echo "commands -- the failure the comment rule exists to prevent." >&2
    exit 1
  fi
}

@test "a heredoc named inside a comment does not swallow the wait below it" {
  TENANT_SCRIPT="$REPO_ROOT/hack/testdata/install-wait/comment-names-heredoc.sh"
  # The fold has to cut comments before it looks for a heredoc opener. Left in,
  # the `<<PATCHDOC` inside a comment opens a heredoc that never ends, every
  # line below it becomes data, and the wait disappears with them. This is the
  # input that tells the cut apart from its absence: the fixture that names a
  # heredoc in a comment elsewhere answers the same either way.
  want="$(_fixture_line wait)"
  got="$(_ready_wait_line)"
  if [ "$got" != "$want" ]; then
    echo "_ready_wait_line = '$got', want '$want': the wait sits below a" >&2
    echo "comment that names a heredoc, and reading that comment as an opener" >&2
    echo "turns the rest of the file into heredoc data" >&2
    exit 1
  fi
}

# Lines where an apostrophe sits inside a comment in an awk program, in $1.
#
# Every awk program in this tree lives inside a single-quoted shell string, so
# one apostrophe in one of its comments ends the string: what follows is read as
# shell, and the failure lands anywhere between a mangled program and a file
# that will not parse. Which of the two depends on what happens to follow, so
# the loud end of that range cannot be relied on.
#
# Comments rather than every line: the quote that ends a program is an
# apostrophe too, so the two cannot be told apart by counting. Where it sits
# can -- inside a comment it can only be prose. A program written entirely on
# one line carries two quotes, which is why an opener is a line whose count is
# odd.
_awk_comment_apostrophes() {
  awk '
    { q = gsub(/\047/, "&") }
    !inprog && /awk .*\047$/ && q % 2 == 1 { inprog = 1; next }
    !inprog && /^[A-Z_]+=\047$/ { inprog = 1; next }
    inprog && /^[[:space:]]*#/ { if (q > 0) { print FNR ": " $0 } next }
    inprog && q > 0 { inprog = 0; next }
  ' "$1"
}

@test "no comment inside an awk program here carries an apostrophe" {
  # The rule is written beside two of those programs and gets typed through
  # anyway, which is the point at which a rule in the author's head is replaced
  # by a premise in the tool.
  #
  # The fixture is the positive control, and it is what makes this case a
  # measurement rather than an assertion that nothing was found: the reader can
  # see the check answer on a file that carries the fault. Without it, a
  # matcher that silently answers nothing everywhere reads exactly like a clean
  # tree.
  planted="$(_awk_comment_apostrophes \
    "$REPO_ROOT/hack/testdata/install-wait/awk-apostrophe.sh")"
  case "$planted" in
  *"heredoc's body"*) : ;;
  *)
    echo "the control fixture did not answer: [$planted]" >&2
    echo "it carries an apostrophe inside an awk comment on purpose, so a" >&2
    echo "matcher that finds nothing there finds nothing anywhere." >&2
    exit 1
    ;;
  esac
  guard="$REPO_ROOT/hack/run-kubernetes-install-wait_test.bats"
  if [ ! -f "$guard" ]; then
    echo "this rule reads its own file by name, and $guard is not there." >&2
    exit 1
  fi
  bad="$(_awk_comment_apostrophes "$guard")"
  if [ -n "$bad" ]; then
    echo "a comment inside an awk program in this file carries an apostrophe:" >&2
    printf '%s\n' "$bad" >&2
    echo "That quote ends the shell string holding the program. Rewrite the" >&2
    echo "comment without it." >&2
    exit 1
  fi
}

@test "this file turns xtrace on nowhere unconditionally" {
  # The rule is the one the second check states, and it is wider than the
  # restore: no bare `set -x` anywhere in this file. Any unconditional
  # trace-on turns a later failure in its case into a hang, so a diagnostic
  # `set -x` added in good faith reds here -- deliberately, and the message
  # says so rather than blaming the restore.
  #
  # Read as text, the way every other rule here reads its subject. The
  # behavioural form of this check -- run a case with an unconditional restore
  # and watch it hang -- needs the bats binary, which the lane that runs this
  # guard does not have, and a case that skips there passes there.
  # `grep -c` exits 1 when it counts none, and under the runner CI uses a
  # failing substitution stops the case at the assignment -- before the
  # diagnosis below, which is the one thing this case exists to print. Both
  # counts carry the same guard for that reason, and `|| true` rather than
  # `|| echo 0`, which would add a second line to a count.
  # Named rather than derived: under the runner CI uses, `$0` is the runner and
  # `BATS_TEST_FILENAME` is unset, so a file cannot ask what it is called. The
  # cost is a name that a rename leaves behind, and a grep over a missing file
  # counts zero, which is the answer that passes -- so the name is checked
  # first, and a rename reds here instead of quietly retiring the rule.
  guard="$REPO_ROOT/hack/run-kubernetes-install-wait_test.bats"
  if [ ! -f "$guard" ]; then
    echo "this rule reads its own file by name, and $guard is not there." >&2
    echo "Point it at the new name; it cannot derive one -- the runner that" >&2
    echo "enforces this guard passes its own path as \$0." >&2
    exit 1
  fi
  cond="$(grep -cE '^[[:space:]]*\[ "\$_ub_xtrace" = 0 \] \|\| set -x$' "$guard" || true)"
  bare="$(grep -cE '^[[:space:]]*set -x$' "$guard" || true)"
  if [ "$cond" != 1 ] || [ "$bare" != 0 ]; then
    echo "xtrace restores in this file: conditional=$cond bare=$bare," >&2
    echo "want conditional=1 bare=0. Restoring xtrace where the runner had it" >&2
    echo "off makes bats trace its own failure output, and the case that" >&2
    echo "follows reports as a hang instead of a red." >&2
    exit 1
  fi
}

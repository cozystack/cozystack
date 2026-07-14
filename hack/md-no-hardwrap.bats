#!/usr/bin/env bats
# Unit test: the md-no-hardwrap agent hook.
#
# The hook is a PreToolUse guard for AI coding agents. It refuses two classes
# of write:
#
#   1. Write/Edit/MultiEdit whose resulting markdown contains a prose
#      paragraph, list item or blockquote spanning more than one line.
#   2. Bash invocations of `gh` that publish such a body to GitHub.
#
# Markdown renderers collapse a soft line break into a space, so a paragraph
# hardwrapped at ~80 columns gains nothing and loses on narrow viewports and
# in diffs, where a one-word edit reflows the whole block. Everything that is
# not prose -- code blocks, tables, headings, front matter, link reference
# definitions, explicit hard breaks -- keeps its line breaks, because there a
# break carries meaning.
#
# Two properties matter more than coverage of exotic markdown, and most of
# the cases below defend them:
#
#   * A false positive is worse than a miss. A hook that blocks legitimate
#     work gets switched off, and then it guards nothing.
#   * The single most common wrap -- a bullet the agent broke across two
#     lines -- must be caught. It is the whole reason the hook exists.
#
# The hook speaks the agent hook protocol: a JSON payload on stdin, exit 0 to
# allow, exit 2 to block with an explanation on stderr.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
HOOK="$REPO_ROOT/.claude/hooks/md-no-hardwrap.py"

# The hook is written in python3, and so is the payload builder below. Other
# hack/*.bats deliberately avoid a python3 dependency; a test for a python3
# hook cannot. Fail loudly when the interpreter is missing rather than passing
# vacuously -- a green suite that ran nothing is worse than a red one.
have_python3() {
  command -v python3 >/dev/null 2>&1 && return 0
  echo "python3 is required to exercise .claude/hooks/md-no-hardwrap.py" >&2
  return 1
}

# Feed a JSON payload to the hook and echo its exit code. Never fails the
# calling test on a non-zero exit -- the exit code IS the assertion subject.
hook_rc() {
  local rc=0
  printf '%s' "$1" | python3 "$HOOK" >/dev/null 2>&1 || rc=$?
  printf '%s' "$rc"
}

# Payload builders. python3 does the JSON encoding so that newlines, quotes
# and backslashes survive the trip intact.
write_payload() {
  python3 -c 'import json,sys; print(json.dumps({"tool_name":"Write","tool_input":{"file_path":sys.argv[1],"content":sys.argv[2]}}))' "$1" "$2"
}

edit_payload() {
  python3 -c 'import json,sys; print(json.dumps({"tool_name":"Edit","tool_input":{"file_path":sys.argv[1],"old_string":sys.argv[2],"new_string":sys.argv[3]}}))' "$1" "$2" "$3"
}

bash_payload() {
  python3 -c 'import json,sys; print(json.dumps({"tool_name":"Bash","tool_input":{"command":sys.argv[1]}}))' "$1"
}

@test "hook is executable" {
  [ -x "$HOOK" ]
}

###############################################################################
# Markdown: what must be blocked                                              #
###############################################################################

@test "markdown: multi-line prose paragraph is blocked" {
  have_python3
  payload="$(write_payload /tmp/doc.md 'A paragraph that the agent
hardwrapped at eighty columns because of a reflex.')"
  [ "$(hook_rc "$payload")" = "2" ]
}

@test "markdown: bullet wrapped onto a second line is blocked" {
  have_python3
  # The commonest wrap of all, and the one the hook exists for. A lazy
  # continuation line belongs to the bullet above it, not to a new paragraph.
  payload="$(write_payload /tmp/doc.md '- a bullet the agent wrapped
  at eighty columns
- a bullet that stays on one line')"
  [ "$(hook_rc "$payload")" = "2" ]
}

@test "markdown: ordered list item wrapped onto a second line is blocked" {
  have_python3
  payload="$(write_payload /tmp/doc.md '1. an ordered item the agent wrapped
   onto a second line')"
  [ "$(hook_rc "$payload")" = "2" ]
}

@test "markdown: wrapped blockquote is blocked" {
  have_python3
  payload="$(write_payload /tmp/doc.md '> a quoted paragraph the agent
> wrapped across two lines')"
  [ "$(hook_rc "$payload")" = "2" ]
}

###############################################################################
# Markdown: what must NOT be blocked                                          #
###############################################################################

@test "markdown: single-line paragraphs pass" {
  have_python3
  payload="$(write_payload /tmp/doc.md '# Title

One continuous line per paragraph, however long it happens to run.

Another paragraph, also a single line.')"
  [ "$(hook_rc "$payload")" = "0" ]
}

@test "markdown: fenced code block keeps its line breaks" {
  have_python3
  payload="$(write_payload /tmp/doc.md 'Intro line.

```bash
echo one
echo two
```')"
  [ "$(hook_rc "$payload")" = "0" ]
}

@test "markdown: indented code block keeps its line breaks" {
  have_python3
  payload="$(write_payload /tmp/doc.md 'Intro line.

    apiVersion: v1
    kind: ConfigMap
    metadata:
      name: demo')"
  [ "$(hook_rc "$payload")" = "0" ]
}

@test "markdown: explicit hard break with two trailing spaces is preserved" {
  have_python3
  # Two trailing spaces render as <br>. Reflowing that onto one line would
  # change the rendered output, so it is a deliberate break, not a wrap.
  payload="$(printf '%s' '{"tool_name":"Write","tool_input":{"file_path":"/tmp/doc.md","content":"Line one, ending in a hard break.  \nLine two, its own rendered line.\n"}}')"
  [ "$(hook_rc "$payload")" = "0" ]
}

@test "markdown: explicit hard break with a trailing backslash is preserved" {
  have_python3
  payload="$(printf '%s' '{"tool_name":"Write","tool_input":{"file_path":"/tmp/doc.md","content":"Line one, ending in a backslash break.\\\\\nLine two, its own rendered line.\n"}}')"
  [ "$(hook_rc "$payload")" = "0" ]
}

@test "markdown: adjacent link reference definitions keep one per line" {
  have_python3
  payload="$(write_payload /tmp/doc.md '[upstream]: https://example.com/upstream
[downstream]: https://example.com/downstream')"
  [ "$(hook_rc "$payload")" = "0" ]
}

@test "markdown: footnote definitions keep one per line" {
  have_python3
  payload="$(write_payload /tmp/doc.md '[^one]: The first footnote.
[^two]: The second footnote.')"
  [ "$(hook_rc "$payload")" = "0" ]
}

@test "markdown: lists and tables keep their line breaks" {
  have_python3
  payload="$(write_payload /tmp/doc.md '- first bullet
- second bullet

| a | b |
| --- | --- |
| 1 | 2 |')"
  [ "$(hook_rc "$payload")" = "0" ]
}

@test "markdown: pipe-less table keeps its line breaks" {
  have_python3
  payload="$(write_payload /tmp/doc.md 'a | b
--- | ---
1 | 2')"
  [ "$(hook_rc "$payload")" = "0" ]
}

@test "markdown: badge stack keeps one shield per line" {
  have_python3
  payload="$(write_payload /tmp/README.md '[![Build](https://example.com/b.svg)](https://example.com/build)
[![License](https://example.com/l.svg)](https://example.com/license)')"
  [ "$(hook_rc "$payload")" = "0" ]
}

@test "markdown: the repo's own README passes the hook" {
  have_python3
  # README.md uses a two-space hard break. A hook that cannot write the repo's
  # front page is a hook that gets switched off.
  payload="$(python3 -c 'import json,sys; p=sys.argv[1]; print(json.dumps({"tool_name":"Write","tool_input":{"file_path":p,"content":open(p,encoding="utf-8").read()}}))' "$REPO_ROOT/README.md")"
  [ "$(hook_rc "$payload")" = "0" ]
}

@test "markdown: AGENTS.md passes the hook it documents" {
  have_python3
  payload="$(python3 -c 'import json,sys; p=sys.argv[1]; print(json.dumps({"tool_name":"Write","tool_input":{"file_path":p,"content":open(p,encoding="utf-8").read()}}))' "$REPO_ROOT/AGENTS.md")"
  [ "$(hook_rc "$payload")" = "0" ]
}

###############################################################################
# Markdown: Edit hunks are judged against the file, not in isolation          #
###############################################################################

@test "markdown: Edit hunk introducing a wrap is blocked" {
  have_python3
  tmp="$(mktemp -d)"
  printf 'Existing single line.\n\nold\n' > "$tmp/doc.md"
  payload="$(edit_payload "$tmp/doc.md" 'old' 'A fresh paragraph that got
wrapped across two lines.')"
  rc="$(hook_rc "$payload")"
  rm -rf "$tmp"
  [ "$rc" = "2" ]
}

@test "markdown: Edit hunk landing inside a code fence is allowed" {
  have_python3
  # The hunk alone looks like a wrapped paragraph. Only the surrounding file
  # shows it is YAML inside a fence -- so the file is what must be judged.
  tmp="$(mktemp -d)"
  printf 'Intro line.\n\n```yaml\nPLACEHOLDER\n```\n' > "$tmp/doc.md"
  payload="$(edit_payload "$tmp/doc.md" 'PLACEHOLDER' 'apiVersion: v1
kind: ConfigMap
metadata:
  name: demo')"
  rc="$(hook_rc "$payload")"
  rm -rf "$tmp"
  [ "$rc" = "0" ]
}

@test "markdown: fixing a typo inside a legacy wrapped paragraph is allowed" {
  have_python3
  # The paragraph stays as wrapped as it was -- the edit adds no wrap. Keying
  # violations by their text would see the changed wording as a brand-new
  # violation and refuse a one-word fix.
  tmp="$(mktemp -d)"
  printf 'A legacy paragraph with teh typo\nwrapped long ago.\n' > "$tmp/doc.md"
  payload="$(edit_payload "$tmp/doc.md" 'teh' 'the')"
  rc="$(hook_rc "$payload")"
  rm -rf "$tmp"
  [ "$rc" = "0" ]
}

@test "markdown: adding a wrap to an already-wrapped file is still blocked" {
  have_python3
  tmp="$(mktemp -d)"
  printf 'A legacy paragraph that was\nwrapped long ago.\n\nTARGET\n' > "$tmp/doc.md"
  payload="$(edit_payload "$tmp/doc.md" 'TARGET' 'A brand new paragraph that the
agent wrapped as well.')"
  rc="$(hook_rc "$payload")"
  rm -rf "$tmp"
  [ "$rc" = "2" ]
}

@test "markdown: Edit preserving a pre-existing wrap elsewhere is allowed" {
  have_python3
  # Legacy files are full of wrapped paragraphs. Editing one word in such a
  # file must not require reflowing the rest of it.
  tmp="$(mktemp -d)"
  printf 'A legacy paragraph that was\nwrapped long ago.\n\nTARGET\n' > "$tmp/doc.md"
  payload="$(edit_payload "$tmp/doc.md" 'TARGET' 'A replacement on one line.')"
  rc="$(hook_rc "$payload")"
  rm -rf "$tmp"
  [ "$rc" = "0" ]
}

###############################################################################
# Markdown: paths the repo does not own                                       #
###############################################################################

@test "markdown: vendored upstream chart docs are out of scope" {
  have_python3
  # AGENTS.md forbids editing vendored charts; `make update` regenerates them
  # verbatim from upstream. Flagging their wraps would demand a forbidden fix.
  payload="$(write_payload "$REPO_ROOT/packages/system/cert-manager/charts/cert-manager/README.md" 'A vendored paragraph
wrapped by its upstream author.')"
  [ "$(hook_rc "$payload")" = "0" ]
}

@test "markdown: published changelogs are out of scope" {
  have_python3
  payload="$(write_payload "$REPO_ROOT/docs/changelogs/v1.5.0.md" 'A released changelog entry
wrapped when it was published.')"
  [ "$(hook_rc "$payload")" = "0" ]
}

@test "non-markdown files are none of the hook's business" {
  have_python3
  payload="$(write_payload /tmp/main.go 'package main

// A Go comment wrapped at eighty columns, which is correct style
// for Go and must not be blocked by a markdown rule.
func main() {}')"
  [ "$(hook_rc "$payload")" = "0" ]
}

###############################################################################
# gh: bodies that must be blocked                                             #
###############################################################################

@test "gh: PR body with a wrapped paragraph is blocked" {
  have_python3
  payload="$(bash_payload 'gh pr create --title "fix(x): y" --body "This body was
hardwrapped by the agent."')"
  [ "$(hook_rc "$payload")" = "2" ]
}

@test "gh: --body= equals form is blocked" {
  have_python3
  payload="$(bash_payload 'gh issue comment 42 --body="A body that was
wrapped across two lines."')"
  [ "$(hook_rc "$payload")" = "2" ]
}

@test "gh: global flag before the subcommand does not hide the body" {
  have_python3
  # `gh -R owner/repo pr comment` puts the repo between `gh` and the
  # subcommand. Reading the subcommand as "the first non-flag argument" picks
  # up the repo instead, and the body sails through unchecked.
  payload="$(bash_payload 'gh -R cozystack/cozystack pr comment 123 --body "First line of the body
and its wrapped continuation."')"
  [ "$(hook_rc "$payload")" = "2" ]
}

@test "gh: environment prefix does not hide the body" {
  have_python3
  payload="$(bash_payload 'GH_TOKEN=secret gh issue comment 1 --body "First line of the body
and its wrapped continuation."')"
  [ "$(hook_rc "$payload")" = "2" ]
}

@test "gh: subshell does not hide the body" {
  have_python3
  payload="$(bash_payload '(gh issue comment 1 --body "First line of the body
and its wrapped continuation.")')"
  [ "$(hook_rc "$payload")" = "2" ]
}

@test "gh: chained command after && is still inspected" {
  have_python3
  payload="$(bash_payload 'git push && gh pr create --title "t" --body "First line of the body
and its wrapped continuation."')"
  [ "$(hook_rc "$payload")" = "2" ]
}

@test "gh: chained command after a semicolon with no space is still inspected" {
  have_python3
  # `push;` is one shlex token unless the lexer treats `;` as punctuation, and
  # then the gh segment that follows is never recognised as one.
  payload="$(bash_payload 'git push; gh pr create --title "t" --body "First line of the body
and its wrapped continuation."')"
  [ "$(hook_rc "$payload")" = "2" ]
}

@test "gh: body survives a redirect on the same command" {
  have_python3
  payload="$(bash_payload 'gh pr create --title "t" --body "First line of the body
and its wrapped continuation." > /dev/null')"
  [ "$(hook_rc "$payload")" = "2" ]
}

@test "gh: heredoc body with a wrapped paragraph is blocked" {
  have_python3
  payload="$(bash_payload 'gh pr create --body-file - <<EOF
A heredoc body that the agent
wrapped across two lines.
EOF')"
  [ "$(hook_rc "$payload")" = "2" ]
}

@test "gh: --body-file pointing at a hardwrapped file is blocked" {
  have_python3
  tmp="$(mktemp -d)"
  printf 'A body file paragraph split\nacross two lines.\n' > "$tmp/body.md"
  payload="$(bash_payload "gh pr create --body-file $tmp/body.md")"
  rc="$(hook_rc "$payload")"
  rm -rf "$tmp"
  [ "$rc" = "2" ]
}

@test "gh: api field body with a wrapped paragraph is blocked" {
  have_python3
  payload="$(bash_payload 'gh api --method POST /repos/o/r/issues/1/comments -f body="A review reply that was
wrapped across two lines."')"
  [ "$(hook_rc "$payload")" = "2" ]
}

@test "gh: api raw field body with a wrapped paragraph is blocked" {
  have_python3
  # For `gh api`, -F means --raw-field, not --body-file. Reading it as a file
  # name drops the body on the floor and the wrap ships.
  payload="$(bash_payload 'gh api --method POST /repos/o/r/issues/1/comments -F body="A review reply that was
wrapped across two lines."')"
  [ "$(hook_rc "$payload")" = "2" ]
}

###############################################################################
# gh: bodies and commands that must NOT be blocked                            #
###############################################################################

@test "gh: single-line PR body passes" {
  have_python3
  payload="$(bash_payload 'gh pr create --title "fix(x): y" --body "One continuous line, as the renderer expects."')"
  [ "$(hook_rc "$payload")" = "0" ]
}

@test "gh: read-only subcommands are never blocked" {
  have_python3
  payload="$(bash_payload 'gh pr view 42 --json body')"
  [ "$(hook_rc "$payload")" = "0" ]
}

@test "gh: release notes from a published changelog are not blocked" {
  have_python3
  # hack/upload-releasenotes.sh publishes docs/changelogs/<version>.md, which
  # is historical and wrapped. Blocking it would break the release process.
  payload="$(bash_payload "gh release edit v1.5.0 --notes-file $REPO_ROOT/docs/changelogs/v1.5.0.md")"
  [ "$(hook_rc "$payload")" = "0" ]
}

@test "gh: api JSON heredoc is not read as markdown" {
  have_python3
  # `gh api --input -` takes JSON, not markdown. Its braces and quoted keys
  # are not a hardwrapped paragraph.
  #
  # The JSON is indented so that no line of it starts with `}`: cozytest.sh
  # converts each @test into a shell function with awk, and a bare `}` in the
  # first column ends that function early, silently truncating the test.
  payload="$(bash_payload 'gh api --method POST /repos/o/r/pulls/1/reviews --input - <<EOF
  {
    "event": "COMMENT",
    "body": "One continuous line."
  }
EOF')"
  [ "$(hook_rc "$payload")" = "0" ]
}

@test "gh: api JSON heredoc with a wrapped body field is blocked" {
  have_python3
  # The wrap lives inside the JSON string as a \n escape. Only the decoded
  # body is prose, and that is what must be checked.
  payload="$(bash_payload 'gh api --method POST /repos/o/r/pulls/1/reviews --input - <<EOF
  {
    "event": "COMMENT",
    "body": "A review body that was\nwrapped across two lines."
  }
EOF')"
  [ "$(hook_rc "$payload")" = "2" ]
}

@test "gh: a heredoc belonging to another command is not adopted" {
  have_python3
  # A commit message body is wrapped at ~72 columns on purpose -- git and
  # AGENTS.md both require it. The presence of a `gh` call further down the
  # line must not drag that heredoc into the markdown rule.
  payload="$(bash_payload 'git commit --file - <<EOF
fix(hook): correct the thing

The body of a commit message is wrapped at seventy-two columns,
exactly as git and AGENTS.md require.
EOF
gh pr edit 1 --add-label area/ai')"
  [ "$(hook_rc "$payload")" = "0" ]
}

@test "gh: a YAML heredoc for kubectl is not adopted" {
  have_python3
  payload="$(bash_payload 'kubectl apply -f - <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
EOF
gh issue comment 1 --body "One continuous line."')"
  [ "$(hook_rc "$payload")" = "0" ]
}

@test "non-gh shell commands are never blocked" {
  have_python3
  payload="$(bash_payload 'printf "line one\nline two\n" > /tmp/notes.txt')"
  [ "$(hook_rc "$payload")" = "0" ]
}

###############################################################################
# Robustness: the hook must never hang, crash, or block what it cannot read   #
###############################################################################

@test "unparseable payload fails open" {
  have_python3
  rc=0
  printf 'not json at all' | python3 "$HOOK" >/dev/null 2>&1 || rc=$?
  [ "$rc" = "0" ]
}

@test "gh: --body-file on a fifo does not hang" {
  have_python3
  # An unopened fifo blocks forever on read. The hook must refuse to read
  # anything that is not a regular file rather than stall the agent.
  tmp="$(mktemp -d)"
  mkfifo "$tmp/fifo"
  payload="$(bash_payload "gh pr create --body-file $tmp/fifo")"
  rc=0
  printf '%s' "$payload" | python3 "$HOOK" >/dev/null 2>&1 || rc=$?
  rm -rf "$tmp"
  [ "$rc" = "0" ]
}

@test "gh: unbalanced quotes fail open" {
  have_python3
  payload="$(bash_payload 'gh pr create --body "unterminated')"
  [ "$(hook_rc "$payload")" = "0" ]
}

@test "block message names the rule and the offending lines" {
  have_python3
  payload="$(write_payload /tmp/doc.md 'A paragraph that the agent
hardwrapped at eighty columns.')"
  msg="$(printf '%s' "$payload" | python3 "$HOOK" 2>&1 >/dev/null || true)"
  printf '%s' "$msg" | grep -q 'one continuous line per paragraph'
  printf '%s' "$msg" | grep -q '/tmp/doc.md'
}

###############################################################################
# The question is always "did this edit ADD the line break", never "is there  #
# a wrap somewhere in the file". These cases pin that distinction down.       #
###############################################################################

@test "markdown: rewriting a file as entirely wrapped prose is blocked" {
  have_python3
  # Ties the violation count of the legacy file, so any count-based test waves
  # it through -- while every wrap in it is brand new.
  tmp="$(mktemp -d)"
  printf 'A legacy paragraph that was\nwrapped long ago.\n' > "$tmp/doc.md"
  payload="$(write_payload "$tmp/doc.md" 'A completely new paragraph that the
agent has just hardwrapped.')"
  rc="$(hook_rc "$payload")"
  rm -rf "$tmp"
  [ "$rc" = "2" ]
}

@test "markdown: reflowing one paragraph while wrapping another is blocked" {
  have_python3
  tmp="$(mktemp -d)"
  printf 'One wrapped paragraph\nfrom long ago.\n\nSECOND\n' > "$tmp/doc.md"
  payload="$(edit_payload "$tmp/doc.md" 'SECOND' 'A second paragraph the agent
has now wrapped as well.')"
  rc="$(hook_rc "$payload")"
  rm -rf "$tmp"
  [ "$rc" = "2" ]
}

@test "markdown: rewriting a legacy file verbatim is allowed" {
  have_python3
  tmp="$(mktemp -d)"
  printf 'A legacy paragraph that was\nwrapped long ago.\n' > "$tmp/doc.md"
  payload="$(write_payload "$tmp/doc.md" 'A legacy paragraph that was
wrapped long ago.')"
  rc="$(hook_rc "$payload")"
  rm -rf "$tmp"
  [ "$rc" = "0" ]
}

@test "gh: a publishing command on the second line of a script is inspected" {
  have_python3
  # A newline is whitespace to shlex, so without explicit handling the gh call
  # folds into the previous command's segment and is never seen.
  payload="$(bash_payload 'git status
gh pr create --title "t" --body "A body paragraph the agent
hardwrapped across two lines."')"
  [ "$(hook_rc "$payload")" = "2" ]
}

@test "gh: backslash-continued command with a heredoc body is inspected" {
  have_python3
  payload="$(bash_payload 'gh pr create \
  --title "t" \
  --body-file - <<EOF
A body paragraph the agent
hardwrapped across two lines.
EOF')"
  [ "$(hook_rc "$payload")" = "2" ]
}

@test "gh: a well-formed cat-heredoc body is allowed" {
  have_python3
  # The argument is shell syntax, not markdown. Reading `$(cat <<EOF` as a
  # wrapped paragraph refuses a body the agent cannot possibly reflow.
  payload="$(bash_payload 'gh pr create --title "t" --body "$(cat <<EOF
One continuous line per paragraph, exactly as the rule requires.
EOF
)"')"
  [ "$(hook_rc "$payload")" = "0" ]
}

@test "gh: a hardwrapped cat-heredoc body is blocked" {
  have_python3
  payload="$(bash_payload 'gh pr create --title "t" --body "$(cat <<EOF
A body paragraph the agent
hardwrapped across two lines.
EOF
)"')"
  [ "$(hook_rc "$payload")" = "2" ]
}

@test "gh: an absolute path to gh is still inspected" {
  have_python3
  payload="$(bash_payload '/usr/bin/gh pr create --title "t" --body "A body paragraph the agent
hardwrapped across two lines."')"
  [ "$(hook_rc "$payload")" = "2" ]
}

@test "markdown: a details block keeps its line breaks" {
  have_python3
  payload="$(write_payload /tmp/doc.md '<details>
<summary>Test output</summary>
<pre>all green</pre>
</details>')"
  [ "$(hook_rc "$payload")" = "0" ]
}

@test "gh: a details block in a PR body is not blocked" {
  have_python3
  payload="$(bash_payload 'gh pr create --title "t" --body "<details>
<summary>Test output</summary>
<pre>all green</pre>
</details>"')"
  [ "$(hook_rc "$payload")" = "0" ]
}

@test "markdown: a leading horizontal rule does not swallow the document" {
  have_python3
  # `---` on line 1 opens front matter only if a closing delimiter follows.
  payload="$(write_payload /tmp/doc.md '---

A paragraph the agent
hardwrapped after a horizontal rule.')"
  [ "$(hook_rc "$payload")" = "2" ]
}

@test "gh: body redirected onto stdin from a file is inspected" {
  have_python3
  # `--body-file -` says the body arrives on stdin, and `< body.md` says where
  # stdin comes from. Recording the first without following the second lets the
  # body publish unread.
  tmp="$(mktemp -d)"
  printf 'A body paragraph the agent\nhardwrapped across two lines.\n' > "$tmp/body.md"
  payload="$(bash_payload "gh pr create --title t --body-file - < $tmp/body.md")"
  rc="$(hook_rc "$payload")"
  rm -rf "$tmp"
  [ "$rc" = "2" ]
}

@test "gh: api input redirected onto stdin from a file is inspected" {
  have_python3
  tmp="$(mktemp -d)"
  printf '{"body": "A review body that was\\nwrapped across two lines."}\n' > "$tmp/review.json"
  payload="$(bash_payload "gh api --method POST /repos/o/r/pulls/1/reviews --input - < $tmp/review.json")"
  rc="$(hook_rc "$payload")"
  rm -rf "$tmp"
  [ "$rc" = "2" ]
}

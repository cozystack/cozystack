#!/usr/bin/env python3
"""PreToolUse hook: keep hardwrapped prose out of markdown and out of GitHub.

A markdown renderer collapses a soft line break into a space, so a paragraph
wrapped at ~80 columns renders identically to the same paragraph on one line.
The wrap is not free, though: it breaks narrow viewports, it mangles list and
table rendering, and it turns a one-word edit into a diff that reflows the
whole block. Prose therefore stays on a single line per paragraph, and the
renderer decides where it wraps.

The rule is easy to state and easy to forget, because an ~80-column wrap is
correct in the places a coding agent spends most of its time (Go comments,
commit message bodies), and the reflex leaks from there into markdown files
and GitHub bodies. This hook is the mechanical check that the reflex slips
past. It guards two surfaces:

    Write / Edit / MultiEdit   markdown content headed for a file
    Bash                       a `gh` command publishing a body to GitHub

Two design rules keep it from becoming a nuisance, which matters more than
catching every last wrap -- a guard that blocks legitimate work gets switched
off, and then it guards nothing:

  * Only NEW wraps are refused. The hook compares the file as it stands
    against the file as it would land, and reports only what the edit adds.
    Editing one word of a legacy document that is wrapped throughout, or
    rewriting a vendored chart README verbatim, therefore stays possible.

  * Anything ambiguous is allowed. A body assembled at runtime by the shell,
    an unsplittable command line, a path that is not a regular file, a
    document the hook cannot read -- all pass. The hook only ever blocks what
    it is certain about.

Line breaks are rejected only inside prose: a paragraph, a list item, or a
blockquote spanning more than one line. Everywhere else a break carries
meaning and is left alone -- code blocks (fenced and indented), tables,
headings, front matter, HTML, link reference definitions, footnotes, badge
stacks, and explicit hard breaks (two trailing spaces, or a trailing
backslash), which render as <br> and cannot be reflowed without changing the
output.

Agent hook protocol: a JSON payload on stdin, exit 0 to allow the tool call,
exit 2 to block it with an explanation on stderr.
"""

from __future__ import annotations

import difflib
import json
import os
import re
import shlex
import stat
import sys
from typing import NamedTuple

MARKDOWN_SUFFIXES = (".md", ".markdown", ".mdx")

# Paths whose prose the repo does not own. Vendored upstream charts are
# regenerated verbatim by `make update` and AGENTS.md forbids editing them;
# published changelogs are historical records; *.md.gotmpl is a Go template,
# not a document. Reflowing any of them is the wrong instruction to give.
OUT_OF_SCOPE = (
    re.compile(r"packages/[^/]+/[^/]+/charts/"),
    re.compile(r"/?docs/changelogs/"),
    re.compile(r"/vendor/"),
)

# Refuse to read a body file larger than this. A body is prose; anything of
# this size is not, and a hook must not stall the agent chewing through it.
MAX_BODY_FILE_BYTES = 1 << 20  # 1 MiB

# Above this, diffing before against after costs more than the check is worth.
# The hook then cannot tell an added wrap from an inherited one, and allows.
MAX_DIFF_CHARS = 400_000

# `gh` subcommands that publish a body. `gh pr view`, `gh pr list` and friends
# are absent by design: they read, they do not publish.
GH_PUBLISHING_SUBCOMMANDS = {
    ("pr", "create"),
    ("pr", "edit"),
    ("pr", "comment"),
    ("pr", "review"),
    ("issue", "create"),
    ("issue", "edit"),
    ("issue", "comment"),
    ("release", "create"),
    ("release", "edit"),
    ("api",),
}
GH_NOUNS = {"pr", "issue", "release"}
GH_ACTIONS = {"create", "edit", "comment", "review"}

# Flag meanings are subcommand-dependent: -F is --body-file for `gh pr`, but
# --raw-field for `gh api`. Getting this backwards silently drops the body.
BODY_FLAGS = ("--body", "-b", "--notes", "-n")
BODY_FILE_FLAGS = ("--body-file", "-F", "--notes-file")
API_FIELD_FLAGS = ("-f", "--field", "-F", "--raw-field")


class Violation(NamedTuple):
    start_line: int  # 1-based, first line of the offending block
    end_line: int    # 1-based, last line of the offending block
    text: str        # the block, joined with " | " for the message


_RE_HEADING = re.compile(r"^#{1,6}\s")
_RE_LIST_MARKER = re.compile(r"^\s*([-*+]|\d+[.)])\s")
_RE_BLOCKQUOTE = re.compile(r"^\s*>")
_RE_TABLE_ROW = re.compile(r"^\s*\|")
# A table delimiter row is dashes, colons, pipes and spaces, with at least one
# pipe. The pipe is what tells it apart from a `---` horizontal rule or setext
# underline, and it is what makes a pipe-less GFM table (`a | b`) detectable.
_RE_TABLE_DELIMITER = re.compile(r"^\s*:?-{1,}:?(\s*\|\s*:?-{1,}:?)+\s*$")
_RE_SETEXT_UNDERLINE = re.compile(r"^\s*(=+|-+)\s*$")
_RE_ADMONITION_OPENER = re.compile(r"^\s*(\?\?\?|!!!)")
# Any line opening with a tag is HTML, whether or not it also carries content:
# `<summary>Test output</summary>` and `<pre>all green</pre>` are two lines of
# a <details> block, not a wrapped paragraph, and joining them would be wrong.
_RE_HTML_BLOCK = re.compile(r"^\s*</?[a-zA-Z!]")
_RE_HTML_COMMENT_OPEN = re.compile(r"^\s*<!--")
_RE_HTML_COMMENT_CLOSE = re.compile(r"-->\s*$")
_RE_FENCE = re.compile(r"^\s*(```|~~~)")
_RE_HR = re.compile(r"^\s*([-*_])(\s*\1){2,}\s*$")
_RE_FRONT_MATTER = re.compile(r"^\s*(---|\+\+\+)\s*$")
# `[label]: https://…` and `[^note]: text` -- one definition per line is the
# only way to write these, so a run of them is not a wrapped paragraph.
_RE_LINK_REF_DEF = re.compile(r"^\s*\[[^\]]+\]:\s")
# `: definition` under a term.
_RE_DEFINITION = re.compile(r"^\s*:\s")
# Indented code: four spaces or a tab, outside a list.
_RE_INDENTED_CODE = re.compile(r"^(    |\t)")
# A README shield stack is one badge per line by convention; collapsing it
# would be wrong. A paragraph mixing badges with prose is still wrapped prose.
_RE_BADGE_ROW = re.compile(r"^\s*\[!\[")
_RE_IMAGE_ROW = re.compile(r"^\s*!\[")
# Heredoc opener: `<<EOF`, `<<-EOF`, `<<'EOF'`, `<<"EOF"`.
_RE_HEREDOC = re.compile(r"<<-?\s*(['\"]?)([A-Za-z_][A-Za-z0-9_]*)\1")


def is_badge_or_image_row(line: str) -> bool:
    return bool(_RE_BADGE_ROW.match(line) or _RE_IMAGE_ROW.match(line))


def ends_with_hard_break(line: str) -> bool:
    """True iff the line ends in an explicit <br>: two spaces, or a backslash.

    Such a break renders as a line break. Reflowing it onto one line would
    change the output, so it is a deliberate break and not a wrap.
    """
    return line.endswith("  ") or line.rstrip(" ").endswith("\\")


def is_structure(line: str) -> bool:
    """True iff the line is markdown structure rather than prose."""
    return any(
        rx.match(line)
        for rx in (
            _RE_HEADING,
            _RE_BLOCKQUOTE,
            _RE_TABLE_ROW,
            _RE_TABLE_DELIMITER,
            _RE_SETEXT_UNDERLINE,
            _RE_ADMONITION_OPENER,
            _RE_HTML_BLOCK,
            _RE_HR,
            _RE_LINK_REF_DEF,
            _RE_DEFINITION,
        )
    )


def wrapped_groups(block: list[tuple[int, str]]) -> list[list[tuple[int, str]]]:
    """Split a block into logical lines, and return those spanning >1 physical line.

    An explicit hard break ends a logical line. Everything up to the next hard
    break (or the end of the block) is one logical line, and if it occupies
    more than one physical line, the author wrapped it.
    """
    groups: list[list[tuple[int, str]]] = []
    current: list[tuple[int, str]] = []

    for numbered in block:
        current.append(numbered)
        if ends_with_hard_break(numbered[1]):
            groups.append(current)
            current = []
    if current:
        groups.append(current)

    return [g for g in groups if len(g) > 1]


def find_violations(content: str) -> list[Violation]:
    """Return every prose block in content that spans more than one line."""
    violations: list[Violation] = []
    lines = content.split("\n")

    def flush(block: list[tuple[int, str]], strip_quote: bool = False) -> None:
        if len(block) < 2:
            return
        # A stack of badges or images is one per line by convention.
        if all(is_badge_or_image_row(text) for _, text in block):
            return
        for group in wrapped_groups(block):
            violations.append(
                Violation(
                    start_line=group[0][0],
                    end_line=group[-1][0],
                    text=" | ".join(
                        (t.lstrip("> ").strip() if strip_quote else t.strip())
                        for _, t in group
                    ),
                )
            )

    in_fence = False
    fence_marker = ""
    in_html_comment = False
    # A leading `---` opens front matter only if a matching delimiter closes it.
    # Otherwise it is a horizontal rule, and treating it as an unterminated
    # front-matter block would swallow the whole document unchecked.
    front_matter_marker = (
        lines[0].strip() if lines and _RE_FRONT_MATTER.match(lines[0]) else ""
    )
    in_front_matter = bool(front_matter_marker) and any(
        line.strip() == front_matter_marker for line in lines[1:]
    )
    in_table = False

    block: list[tuple[int, str]] = []
    block_kind: str | None = None  # "para" | "list" | "quote"

    for idx, raw in enumerate(lines, start=1):
        if in_front_matter:
            if idx > 1 and raw.strip() == front_matter_marker:
                in_front_matter = False
            continue

        if in_html_comment:
            if _RE_HTML_COMMENT_CLOSE.search(raw):
                in_html_comment = False
            continue
        if _RE_HTML_COMMENT_OPEN.match(raw):
            flush(block, block_kind == "quote")
            block, block_kind = [], None
            if not _RE_HTML_COMMENT_CLOSE.search(raw):
                in_html_comment = True
            continue

        fence = _RE_FENCE.match(raw)
        if fence:
            flush(block, block_kind == "quote")
            block, block_kind = [], None
            if not in_fence:
                in_fence, fence_marker = True, fence.group(1)
            elif fence_marker in raw:
                in_fence, fence_marker = False, ""
            continue
        if in_fence:
            continue

        if not raw.strip():
            flush(block, block_kind == "quote")
            block, block_kind, in_table = [], None, False
            continue

        # A delimiter row proves the line above it was a table header, not the
        # first line of a paragraph. This is what makes a pipe-less GFM table
        # (`a | b` over `--- | ---`) readable as a table.
        if _RE_TABLE_DELIMITER.match(raw):
            if block_kind == "para" and block:
                block.pop()
            flush(block, False)
            block, block_kind, in_table = [], None, True
            continue
        if in_table:
            if "|" in raw:
                continue
            in_table = False

        if _RE_BLOCKQUOTE.match(raw):
            if block_kind != "quote":
                flush(block, block_kind == "quote")
                block, block_kind = [], "quote"
            # `>` on its own separates paragraphs inside the quote.
            if not raw.lstrip("> ").strip():
                flush(block, True)
                block = []
            else:
                block.append((idx, raw))
            continue

        if _RE_LIST_MARKER.match(raw):
            flush(block, block_kind == "quote")
            block, block_kind = [(idx, raw)], "list"
            continue

        if is_structure(raw):
            flush(block, block_kind == "quote")
            block, block_kind = [], None
            continue

        # An indented line directly under a list item is that item's lazy
        # continuation -- the wrapped-bullet case. Outside a list, and with no
        # paragraph open, it is an indented code block.
        if _RE_INDENTED_CODE.match(raw) and block_kind != "list" and not block:
            continue

        if block_kind == "list":
            block.append((idx, raw))  # lazy continuation of the item
            continue

        if block_kind != "para":
            flush(block, block_kind == "quote")
            block, block_kind = [], "para"
        block.append((idx, raw))

    flush(block, block_kind == "quote")
    return violations


def out_of_scope(path: str) -> bool:
    """True iff the repo does not own this file's prose."""
    normalised = path.replace(os.sep, "/")
    if normalised.endswith(".gotmpl"):
        return True
    return any(rx.search(normalised) for rx in OUT_OF_SCOPE)


def is_markdown(path: str) -> bool:
    return path.lower().endswith(MARKDOWN_SUFFIXES)


def read_text_file(path: str) -> str | None:
    """Read a regular file, or None if it is missing, huge, or not a file.

    A fifo or a character device would block forever, and the agent with it.
    """
    try:
        expanded = os.path.expanduser(path)
        info = os.stat(expanded)
        if not stat.S_ISREG(info.st_mode) or info.st_size > MAX_BODY_FILE_BYTES:
            return None
        with open(expanded, encoding="utf-8") as fh:
            return fh.read()
    except (OSError, UnicodeDecodeError):
        return None


def added_violations(before: str | None, after: str) -> list[Violation]:
    """Return the violations whose line breaks THIS edit introduced.

    Legacy documents are wrapped throughout, and demanding that an agent reflow
    a file it only meant to touch one word of is how a hook gets switched off.
    But "is this violation new" survives neither of the obvious tests: comparing
    the violations' text refuses a typo fix inside a paragraph that was already
    wrapped, and counting them lets a file be rewritten as entirely fresh
    hardwrapped prose so long as the total does not rise.

    The question that does hold up is about the break, not the block: a soft
    break is this edit's doing only when BOTH lines it joins are lines the edit
    wrote. Touching one line of a pair that was already wrapped leaves the break
    where it was; writing both lines is what creates it.
    """
    found = find_violations(after)
    if before is None or not found:
        return found

    before_lines = before.split("\n")
    after_lines = after.split("\n")

    # Lines of `after` that came through the edit untouched, 1-based.
    untouched: set[int] = set()
    matcher = difflib.SequenceMatcher(None, before_lines, after_lines, autojunk=False)
    for tag, _, _, start_line, end_line in matcher.get_opcodes():
        if tag == "equal":
            untouched.update(range(start_line + 1, end_line + 1))

    def edit_wrote_this_break(violation: Violation) -> bool:
        return any(
            number not in untouched and number + 1 not in untouched
            for number in range(violation.start_line, violation.end_line)
        )

    return [v for v in found if edit_wrote_this_break(v)]


def apply_edits(original: str, tool_input: dict) -> str | None:
    """Apply an Edit/MultiEdit payload to the file's current text."""
    edits: list[dict] = []
    if isinstance(tool_input.get("edits"), list):
        edits = [e for e in tool_input["edits"] if isinstance(e, dict)]
    elif isinstance(tool_input.get("new_string"), str):
        edits = [tool_input]

    result = original
    for edit in edits:
        old = edit.get("old_string")
        new = edit.get("new_string")
        if not isinstance(old, str) or not isinstance(new, str) or not old:
            return None  # an empty old_string would prepend, not replace
        if old not in result:
            return None  # cannot reproduce the edit; do not guess
        if edit.get("replace_all"):
            result = result.replace(old, new)
        else:
            result = result.replace(old, new, 1)
    return result


def gh_subcommand(args: list[str]) -> tuple[str, ...] | None:
    """Return the (noun, action) a `gh` invocation names, or None.

    Position alone cannot find the subcommand: a global flag may precede it,
    and `gh -R owner/repo pr comment` would be read as the subcommand
    `owner/repo`, letting the body through. Match on the token's value.
    """
    noun: str | None = None
    for token in args:
        if noun is None:
            if token == "api":
                return ("api",)
            if token in GH_NOUNS:
                noun = token
        elif token in GH_ACTIONS:
            return (noun, token)
    return None


HEREDOC_MARKER = "__COZY_HEREDOC_{}__"
_RE_HEREDOC_MARKER = re.compile(r"__COZY_HEREDOC_(\d+)__")


def split_off_heredocs(command: str) -> tuple[str, list[str]]:
    """Split a command line into (command with heredocs marked, their bodies).

    A heredoc body is data, and shlex would tokenise it into nonsense. Lift the
    bodies out and leave a marker where each opener stood, so that the body can
    later be traced back to the command that opened it -- a heredoc feeding
    `kubectl apply -f -` is not a GitHub body just because a `gh` call shares
    the line.

    Every line is scanned for an opener, not only the first: a long `gh` call is
    routinely spread over backslash-continued lines, the heredoc opening on the
    last of them.
    """
    kept: list[str] = []
    bodies: list[str] = []
    body: list[str] = []
    pending: list[str] = []

    for line in command.split("\n"):
        if pending:
            if line.strip() == pending[0]:
                bodies.append("\n".join(body))
                body, pending = [], pending[1:]
            else:
                body.append(line)
            continue

        openers = list(_RE_HEREDOC.finditer(line))
        if openers:
            marked = line
            for offset, opener in enumerate(openers):
                index = len(bodies) + len(pending) + offset
                marked = marked.replace(
                    opener.group(0), HEREDOC_MARKER.format(index), 1
                )
            kept.append(marked)
            pending.extend(m.group(2) for m in openers)
        else:
            kept.append(line)

    if body:  # unterminated heredoc: take what we have
        bodies.append("\n".join(body))

    return "\n".join(kept), bodies


def stdin_redirect(args: list[str]) -> str | None:
    """Return the file a `< path` redirect feeds this segment's stdin from."""
    for index, arg in enumerate(args):
        if arg == "<" and index + 1 < len(args):
            return args[index + 1]
        if arg.startswith("<") and len(arg) > 1 and not arg.startswith("<<"):
            return arg[1:]
    return None


def owned_heredocs(args: list[str], bodies: list[str]) -> list[str]:
    """Return the heredoc bodies opened by this segment, in order."""
    owned: list[str] = []
    for arg in args:
        for match in _RE_HEREDOC_MARKER.finditer(arg):
            index = int(match.group(1))
            if index < len(bodies):
                owned.append(bodies[index])
    return owned


def newlines_to_separators(command: str) -> str:
    """Turn command-separating newlines into `;`, leaving quoted ones alone.

    shlex treats a newline as plain whitespace, so a script whose second line
    is the `gh` call lexes into one segment beginning with the FIRST line's
    command -- and the `gh` invocation is never recognised as one. A newline
    inside a quoted string belongs to the body and must survive; a
    backslash-continued newline joins one command, so it is not a separator.
    """
    out: list[str] = []
    quote: str | None = None
    index = 0

    while index < len(command):
        char = command[index]

        if quote:
            if char == "\\" and quote == '"' and index + 1 < len(command):
                out.append(char)
                out.append(command[index + 1])
                index += 2
                continue
            if char == quote:
                quote = None
            out.append(char)
            index += 1
            continue

        if char in ("'", '"'):
            quote = char
            out.append(char)
            index += 1
            continue

        if char == "\\" and index + 1 < len(command) and command[index + 1] == "\n":
            out.append(" ")  # line continuation: one command, not two
            index += 2
            continue

        if char == "\n":
            out.append(" ; ")
            index += 1
            continue

        out.append(char)
        index += 1

    return "".join(out)


def shell_assembled(value: str) -> bool:
    """True iff the shell, not the agent, decides what this argument says.

    `--body "$(cat <<EOF …)"` is the standard way to pass a multi-line body in
    a single call. What the hook sees is shell syntax, and reading it as
    markdown would refuse a body that has nothing to reflow -- so leave the
    argument alone and judge the heredoc it draws from instead.
    """
    return bool(re.search(r"\$\(|\$\{|`", value))


def json_bodies(text: str) -> list[str]:
    """Return the prose fields of a JSON document (`gh api --input` payload).

    A JSON heredoc is not markdown: its braces and quoted keys are not a
    wrapped paragraph. Only the human-facing values inside it are prose.
    """
    try:
        data = json.loads(text)
    except (json.JSONDecodeError, ValueError):
        return []

    found: list[str] = []

    def walk(node: object) -> None:
        if isinstance(node, dict):
            for key, value in node.items():
                if key in ("body", "text") and isinstance(value, str):
                    found.append(value)
                else:
                    walk(value)
        elif isinstance(node, list):
            for item in node:
                walk(item)

    walk(data)
    return found


def segment_bodies(args: list[str], is_api: bool) -> tuple[list[str], bool]:
    """Return (bodies, wants_stdin) for one `gh` segment's arguments."""
    bodies: list[str] = []
    wants_stdin = False

    idx = 0
    while idx < len(args):
        arg = args[idx]
        nxt = args[idx + 1] if idx + 1 < len(args) else None

        if is_api:
            # For `gh api`, -F is --raw-field, not --body-file.
            if arg in API_FIELD_FLAGS and nxt is not None:
                if nxt.startswith("body=") or nxt.startswith("text="):
                    value = nxt.split("=", 1)[1]
                    if value.startswith("@"):
                        content = read_text_file(value[1:])
                        if content is not None:
                            bodies.append(content)
                    else:
                        bodies.append(value)
                idx += 2
                continue
            if arg == "--input" and nxt is not None:
                if nxt == "-":
                    wants_stdin = True
                else:
                    content = read_text_file(nxt)
                    if content is not None:
                        bodies.extend(json_bodies(content))
                idx += 2
                continue
            idx += 1
            continue

        if arg in BODY_FLAGS and nxt is not None:
            if not shell_assembled(nxt):
                bodies.append(nxt)
            idx += 2
            continue
        if arg.startswith(("--body=", "--notes=")):
            value = arg.split("=", 1)[1]
            if not shell_assembled(value):
                bodies.append(value)
            idx += 1
            continue
        if arg in BODY_FILE_FLAGS and nxt is not None:
            if nxt == "-":
                wants_stdin = True
            elif not out_of_scope(nxt):
                content = read_text_file(nxt)
                if content is not None:
                    bodies.append(content)
            idx += 2
            continue
        if arg.startswith(("--body-file=", "--notes-file=")):
            value = arg.split("=", 1)[1]
            if value == "-":
                wants_stdin = True
            elif not out_of_scope(value):
                content = read_text_file(value)
                if content is not None:
                    bodies.append(content)
            idx += 1
            continue
        idx += 1

    return bodies, wants_stdin


def gh_bodies(command: str) -> list[str]:
    """Return every body a `gh` command line would publish to GitHub."""
    if "gh" not in command:
        return []

    stripped, heredocs = split_off_heredocs(command)
    stripped = newlines_to_separators(stripped)

    # punctuation_chars makes the lexer return `;`, `&&`, `|`, `(` and friends
    # as tokens of their own. Without it `git push; gh pr create …` lexes as
    # `push;` plus a `gh` that never starts a segment, and the body it
    # publishes is never seen.
    try:
        lexer = shlex.shlex(stripped, posix=True, punctuation_chars=True)
        lexer.whitespace_split = True
        tokens = list(lexer)
    except ValueError:  # unbalanced quotes: not ours to judge
        return []

    # A command line may chain several commands; only `gh` segments matter.
    separators = (";", "&", "&&", "||", "|", "(", ")", "{", "}", "\n")
    segments: list[list[str]] = [[]]
    for token in tokens:
        if token in separators:
            segments.append([])
        else:
            segments[-1].append(token)

    bodies: list[str] = []
    for segment in segments:
        # An environment prefix (`GH_TOKEN=… gh …`) would otherwise hide the
        # `gh` behind a token that is not a command name.
        while segment and re.match(r"^[A-Za-z_][A-Za-z0-9_]*=", segment[0]):
            segment = segment[1:]
        if not segment or os.path.basename(segment[0]) != "gh":
            continue

        subcommand = gh_subcommand(segment[1:])
        if subcommand not in GH_PUBLISHING_SUBCOMMANDS:
            continue

        is_api = subcommand == ("api",)
        found, wants_stdin = segment_bodies(segment[1:], is_api)
        bodies.extend(found)

        # A heredoc THIS segment opened is the body it publishes -- as JSON for
        # `gh api`, as markdown otherwise. A heredoc opened by another command
        # on the same line (a commit message, a YAML manifest) belongs to that
        # command, and blocking on it would be a false positive of exactly the
        # kind that gets a hook switched off.
        owned = owned_heredocs(segment[1:], heredocs)
        for heredoc in owned:
            bodies.extend(json_bodies(heredoc) if is_api else [heredoc])

        # `--body-file -` / `--input -` take the body from stdin; a `< file`
        # redirect says which file that is. Without following it the body
        # publishes unread.
        if wants_stdin and not owned:
            source = stdin_redirect(segment[1:])
            if source and not out_of_scope(source):
                content = read_text_file(source)
                if content is not None:
                    bodies.extend(json_bodies(content) if is_api else [content])

    return bodies


def truncate(text: str, limit: int = 160) -> str:
    return text if len(text) <= limit else text[: limit - 1] + "…"


def report(subject: str, violations: list[Violation]) -> None:
    lines = [
        "BLOCKED: hardwrapped prose.",
        "AGENTS.md rule: one continuous line per paragraph — the renderer wraps to viewer width.",
        "Reflow each block below onto a single line, then retry.",
        "",
        f"Subject: {subject}",
    ]
    for v in violations[:5]:
        lines.append(f"  - lines {v.start_line}-{v.end_line}: {truncate(v.text)}")
    if len(violations) > 5:
        lines.append(f"  - … and {len(violations) - 5} more")
    print("\n".join(lines), file=sys.stderr)


def main() -> int:
    try:
        payload = json.load(sys.stdin)
    except (json.JSONDecodeError, UnicodeDecodeError):
        return 0
    if not isinstance(payload, dict):
        return 0

    tool_name = payload.get("tool_name", "")
    tool_input = payload.get("tool_input")
    if not isinstance(tool_input, dict):
        return 0

    violations: list[Violation] = []

    if tool_name in ("Write", "Edit", "MultiEdit"):
        path = tool_input.get("file_path")
        if not isinstance(path, str) or not is_markdown(path) or out_of_scope(path):
            return 0

        subject = path
        before = read_text_file(path)

        if tool_name == "Write":
            after = tool_input.get("content")
            if not isinstance(after, str):
                return 0
        else:
            if before is None:
                return 0  # cannot see the file, cannot judge the hunk
            after = apply_edits(before, tool_input)
            if after is None:
                return 0

        violations = added_violations(before, after)

    elif tool_name == "Bash":
        command = tool_input.get("command")
        if not isinstance(command, str):
            return 0
        subject = "gh command body"
        for body in gh_bodies(command):
            violations.extend(find_violations(body))

    else:
        return 0

    if not violations:
        return 0

    report(str(subject), violations)
    return 2


if __name__ == "__main__":
    sys.exit(main())

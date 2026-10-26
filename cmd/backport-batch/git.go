/*
Copyright 2026 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var shaRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// commandEnv pins the locale, because telling a conflict from an empty pick
// reads git's messages, and keeps git from opening an editor.
var commandEnv = []string{"LC_ALL=C", "GIT_EDITOR=true"}

func command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), commandEnv...)
	return cmd
}

func capture(name string, args ...string) (string, error) {
	out, err := command(name, args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return string(out), nil
}

// git runs a git command whose failure means the batch cannot be trusted.
func git(args ...string) (string, error) { return capture("git", args...) }

func gitOK(args ...string) bool {
	return command("git", args...).Run() == nil
}

// gitStatus runs git for a command whose non-zero exit is an answer rather
// than a failure: a conflicting pick, a grep that matched nothing.
func gitStatus(args ...string) (stdout, stderr string, code int, err error) {
	var out, errOut bytes.Buffer
	cmd := command("git", args...)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.String(), errOut.String(), ee.ExitCode(), nil
	}
	if err != nil {
		return "", "", -1, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out.String(), errOut.String(), 0, nil
}

func checkSHA(s string) error {
	if !shaRE.MatchString(s) {
		return fmt.Errorf("%q is not a full commit SHA", s)
	}
	return nil
}

// checkBranch refuses a name git could read as an option or would rewrite.
// check-ref-format --branch expands @{-N} into whatever branch that was, so
// its answer has to be the name itself.
func checkBranch(name string) error {
	if name == "" || strings.HasPrefix(name, "-") || strings.ContainsFunc(name, unicode.IsControl) {
		return fmt.Errorf("%q is not a branch name", name)
	}
	out, err := git("check-ref-format", "--branch", name)
	if err != nil || strings.TrimSuffix(out, "\n") != name {
		return fmt.Errorf("%q is not a branch name", name)
	}
	return nil
}

// resolve pins a ref to the commit it names now, so every later read sees the
// same commit however the ref moves.
func resolve(ref string) (string, error) {
	out, err := git("rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("cannot resolve %s: %w", ref, err)
	}
	return strings.TrimSpace(out), nil
}

func isAncestor(a, b string) bool {
	return gitOK("merge-base", "--is-ancestor", "--end-of-options", a, b)
}

// splitNUL splits -z output, dropping the empty field after the last NUL.
func splitNUL(out string) []string {
	var fields []string
	for f := range strings.SplitSeq(out, "\x00") {
		if f != "" {
			fields = append(fields, f)
		}
	}
	return fields
}

// revList lists commits with rev-list, whose output is object names and
// nothing a commit message could add to; each one is checked to be one.
func revList(args ...string) ([]string, error) {
	out, err := git(append([]string{"rev-list"}, args...)...)
	if err != nil {
		return nil, err
	}
	shas := strings.Fields(out)
	for _, sha := range shas {
		if err := checkSHA(sha); err != nil {
			return nil, fmt.Errorf("rev-list: %w", err)
		}
	}
	return shas, nil
}

// commitObject is what the batch reads of a commit: its message, and when it
// was committed, which for a merge GitHub made is when it merged.
type commitObject struct {
	message   string
	committed time.Time
}

// commitMessages reads commits through one cat-file --batch, whose output
// gives each object's length before its content: nothing inside a message, a
// NUL or a line shaped like a header included, can pass for the start of the
// next commit. A message ends at its first NUL, where git log ends it, so
// nothing hidden from a reader of the log counts here.
func commitMessages(shas []string) (map[string]commitObject, error) {
	msgs := map[string]commitObject{}
	if len(shas) == 0 {
		return msgs, nil
	}
	cmd := command("git", "cat-file", "--batch")
	cmd.Stdin = strings.NewReader(strings.Join(shas, "\n") + "\n")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git cat-file --batch: %w", err)
	}
	r := bufio.NewReader(bytes.NewReader(out))
	for _, want := range shas {
		header, err := r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("git cat-file --batch: no answer for %s", want)
		}
		f := strings.Fields(header)
		if len(f) != 3 || f[0] != want || f[1] != "commit" {
			return nil, fmt.Errorf("git cat-file --batch: %q is not commit %s", strings.TrimSpace(header), want)
		}
		size, err := strconv.Atoi(f[2])
		if err != nil || size < 0 {
			return nil, fmt.Errorf("git cat-file --batch: bad size in %q", strings.TrimSpace(header))
		}
		body := make([]byte, size+1)
		if _, err := io.ReadFull(r, body); err != nil || body[size] != '\n' {
			return nil, fmt.Errorf("git cat-file --batch: short object %s", want)
		}
		headers, msg, _ := strings.Cut(string(body[:size]), "\n\n")
		msg, _, _ = strings.Cut(msg, "\x00")
		committed, err := committerTime(headers)
		if err != nil {
			return nil, fmt.Errorf("commit %s: %w", want, err)
		}
		msgs[want] = commitObject{message: msg, committed: committed}
	}
	return msgs, nil
}

// committerTime reads the committer line of a commit's headers:
// "committer Name <email> <unix seconds> <zone>".
func committerTime(headers string) (time.Time, error) {
	for ln := range strings.Lines(headers) {
		rest, ok := strings.CutPrefix(strings.TrimRight(ln, "\n"), "committer ")
		if !ok {
			continue
		}
		f := strings.Fields(rest[strings.LastIndexByte(rest, '>')+1:])
		if len(f) != 2 {
			break
		}
		secs, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			break
		}
		return time.Unix(secs, 0).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("no readable committer line")
}

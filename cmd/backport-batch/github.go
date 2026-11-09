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
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// openPullsCap is the page size of the open pull request listing. The REST
// listing stops at it in silence, so a full page is an error, not an answer.
const openPullsCap = 100

// searchResultCap is the most results GitHub search returns for one query,
// however far it is paginated. gh pr list answers a --label filter through
// search, so a listing that long has to be taken as cut short.
const searchResultCap = 1000

// The labels count on a pull request merged into main, the original, and on
// no batch pull request.
const (
	labelInclude = "backport/include"
	labelSkip    = "backport/skip"
)

var (
	batchBranchRE = regexp.MustCompile(`^backport-batch/(release-\d+\.\d+)-(\d{8})$`)
	loginRE       = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?$`)
	botLoginRE    = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?\[bot\]$`)
	pullURLRE     = regexp.MustCompile(`^https://[^/\s]+/[^/\s]+/[^/\s]+/pull/[1-9][0-9]*$`)
)

// batchBotEnv names the GitHub App bot the scheduled batch opens its pull
// requests and comments as, <app slug>[bot]. The workflow sets it from the
// token it mints. Unset, it is defaultBatchBot, so that a maintainer's run by
// hand continues the scheduled batch instead of opening a second one; set and
// empty, no bot is trusted.
const (
	batchBotEnv     = "BACKPORT_BATCH_BOT"
	defaultBatchBot = "cozystack-ci[bot]"
)

// batchBotLogin is the bot to trust given lookup, os.LookupEnv outside tests.
func batchBotLogin(lookup func(string) (string, bool)) string {
	if v, ok := lookup(batchBotEnv); ok {
		return v
	}
	return defaultBatchBot
}

// pullRequest is the REST view of a pull request: unlike gh pr list, it
// carries the author's association with the repository.
type pullRequest struct {
	Number            int    `json:"number"`
	HTMLURL           string `json:"html_url"`
	Body              string `json:"body"`
	AuthorAssociation string `json:"author_association"`
	User              struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
	Head struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref  string `json:"ref"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
}

// issueComment is the REST view of a comment on a pull request.
type issueComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	User struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
}

// github is the part of GitHub the tool talks to: gh outside tests.
type github interface {
	openPulls(base string) ([]pullRequest, error)
	// permission is login's permission on the repository as GitHub's
	// legacy field gives it: admin, write, read or none.
	permission(login string) (string, error)
	// labelled lists the pull requests merged into main that carry label.
	labelled(label string) ([]int, error)
	// createPull opens the draft and returns its URL.
	createPull(base, head, title, bodyFile string) (string, error)
	editPull(number int, title, bodyFile string) error
	comments(number int) ([]issueComment, error)
	createComment(number int, bodyFile string) error
	editComment(id int64, bodyFile string) error
	// batchBot is the bot the batch writes as, from batchBotEnv, or "".
	batchBot() string
}

type ghCLI struct{ bot string }

func (g ghCLI) batchBot() string { return g.bot }

func (ghCLI) openPulls(base string) ([]pullRequest, error) {
	out, err := capture("gh", "api", "-X", "GET", "repos/{owner}/{repo}/pulls",
		"-f", "state=open", "-f", "base="+base, "-f", "per_page="+strconv.Itoa(openPullsCap))
	if err != nil {
		return nil, err
	}
	var prs []pullRequest
	if err := json.Unmarshal([]byte(out), &prs); err != nil {
		return nil, fmt.Errorf("reading the open pull requests on %s: %w", base, err)
	}
	if len(prs) >= openPullsCap {
		return nil, fmt.Errorf("the open pull requests on %s fill a page of %d, so the list may be truncated", base, openPullsCap)
	}
	return prs, nil
}

func (ghCLI) permission(login string) (string, error) {
	if !loginRE.MatchString(login) {
		return "", fmt.Errorf("%q is not a GitHub login", login)
	}
	out, err := capture("gh", "api", "repos/{owner}/{repo}/collaborators/"+login+"/permission")
	if err != nil {
		return "", err
	}
	var answer struct {
		Permission string `json:"permission"`
	}
	if err := json.Unmarshal([]byte(out), &answer); err != nil {
		return "", fmt.Errorf("reading %s's permission: %w", login, err)
	}
	return answer.Permission, nil
}

func (ghCLI) labelled(label string) ([]int, error) {
	out, err := capture("gh", "pr", "list", "--base", "main", "--state", "merged", "--label", label,
		"--limit", strconv.Itoa(searchResultCap), "--json", "number")
	if err != nil {
		return nil, err
	}
	var prs []struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal([]byte(out), &prs); err != nil {
		return nil, fmt.Errorf("reading the pull requests labelled %s: %w", label, err)
	}
	if len(prs) >= searchResultCap {
		return nil, fmt.Errorf("%d pull requests carry %s, as many as GitHub search returns, so the list may be cut short", len(prs), label)
	}
	numbers := make([]int, 0, len(prs))
	for _, pr := range prs {
		numbers = append(numbers, pr.Number)
	}
	return numbers, nil
}

func (ghCLI) createPull(base, head, title, bodyFile string) (string, error) {
	out, err := capture("gh", "pr", "create", "--draft", "--base", base, "--head", head, "--title", title, "--body-file", bodyFile)
	if err != nil {
		return "", err
	}
	url := strings.TrimSpace(out)
	if !pullURLRE.MatchString(url) {
		return "", fmt.Errorf("gh pr create printed %q, not the URL of a pull request", url)
	}
	return url, nil
}

func (ghCLI) editPull(number int, title, bodyFile string) error {
	_, err := capture("gh", "pr", "edit", strconv.Itoa(number), "--title", title, "--body-file", bodyFile)
	return err
}

func (ghCLI) comments(number int) ([]issueComment, error) {
	out, err := capture("gh", "api", "--paginate", "--slurp", "repos/{owner}/{repo}/issues/"+strconv.Itoa(number)+"/comments")
	if err != nil {
		return nil, err
	}
	var pages [][]issueComment
	if err := json.Unmarshal([]byte(out), &pages); err != nil {
		return nil, fmt.Errorf("reading the comments on #%d: %w", number, err)
	}
	var all []issueComment
	for _, page := range pages {
		all = append(all, page...)
	}
	return all, nil
}

func (ghCLI) createComment(number int, bodyFile string) error {
	_, err := capture("gh", "api", "--method", "POST", "repos/{owner}/{repo}/issues/"+strconv.Itoa(number)+"/comments", "-F", "body=@"+bodyFile)
	return err
}

func (ghCLI) editComment(id int64, bodyFile string) error {
	_, err := capture("gh", "api", "--method", "PATCH", "repos/{owner}/{repo}/issues/comments/"+strconv.FormatInt(id, 10), "-F", "body=@"+bodyFile)
	return err
}

// trusted says why a pull request's author may not speak for the batch, or ""
// when they may.
func trusted(gh github, pr pullRequest) (string, error) {
	why, err := trustedAuthor(gh, pr.User.Login, pr.User.Type)
	if why != "" {
		why = "opened by " + why
	}
	return why, err
}

// trustedAuthor says why an author may not speak for the batch, or "" when
// they may: the bot the batch runs as, by its exact login, or a person whose
// permission on the repository is write or admin. Any other bot is refused,
// GitHub Actions' own included: anything with a workflow's token can write as
// it. The author association is not asked: an organization member or an
// invited collaborator can hold read access only, and the legacy permission
// folds maintain into write and triage into read. A permission GitHub does not
// answer is an error, not a refusal: taken for one, it would hide the open
// batch and its comment, and the run would open a second of each.
func trustedAuthor(gh github, login, kind string) (string, error) {
	switch {
	case kind == "Bot" && gh.batchBot() != "" && login == gh.batchBot():
		return "", nil
	case kind != "User" || !loginRE.MatchString(login):
		return fmt.Sprintf("%q, which is neither a person nor the batch's bot (%s)", login, batchBotEnv), nil
	}
	perm, err := gh.permission(login)
	switch {
	case err != nil:
		return "", err
	case perm != "write" && perm != "admin":
		return fmt.Sprintf("%s, whose permission is %q", login, perm), nil
	}
	return "", nil
}

// batchPull picks the open batch pull request for l out of prs, or nil.
//
// A pull request counts only when its head is a batch branch for l in this
// repository, not a fork, and a trusted author opened it: anyone can open a
// pull request from a branch named like one, and its -x lines and its list of
// deferred conflicts are what check believes. Look-alikes come back as
// warnings.
func batchPull(gh github, prs []pullRequest, l line) (*pullRequest, []string, error) {
	var found []*pullRequest
	var warnings []string
	for i := range prs {
		pr := &prs[i]
		m := batchBranchRE.FindStringSubmatch(pr.Head.Ref)
		if m == nil || m[1] != l.branch() || pr.Base.Ref != l.branch() {
			continue
		}
		switch {
		case pr.Head.Repo == nil || pr.Head.Repo.FullName != pr.Base.Repo.FullName:
			warnings = append(warnings, fmt.Sprintf("ignoring #%d: its batch branch is in a fork", pr.Number))
		case checkSHA(pr.Head.SHA) != nil:
			warnings = append(warnings, fmt.Sprintf("ignoring #%d: GitHub reports no head commit for it", pr.Number))
		default:
			why, err := trusted(gh, *pr)
			if err != nil {
				return nil, warnings, fmt.Errorf("batch pull request #%d: %w", pr.Number, err)
			}
			if why != "" {
				warnings = append(warnings, fmt.Sprintf("ignoring #%d: %s", pr.Number, why))
				continue
			}
			found = append(found, pr)
		}
	}
	switch len(found) {
	case 0:
		return nil, warnings, nil
	case 1:
		return found[0], warnings, nil
	}
	return nil, warnings, fmt.Errorf("more than one open batch pull request on %s (#%d, #%d); close all but one", l.branch(), found[0].Number, found[1].Number)
}

// batchHead is the head of the batch pull request as fetched here, refused
// unless it is the commit GitHub reports for the pull request.
func batchHead(pr *pullRequest) (string, error) {
	if err := checkBranch(pr.Head.Ref); err != nil {
		return "", err
	}
	ref := "refs/remotes/" + remote + "/" + pr.Head.Ref
	got, err := lookup(ref)
	if err != nil || got == pr.Head.SHA {
		return got, err
	}
	if _, err := git("fetch", "--quiet", "--end-of-options", remote, "+refs/heads/"+pr.Head.Ref+":"+ref); err != nil {
		return "", err
	}
	if got, err = resolve(ref); err != nil {
		return "", err
	}
	if got != pr.Head.SHA {
		return "", fmt.Errorf("batch pull request #%d: %s is at %.12s, not at %.12s as GitHub reports; run again", pr.Number, pr.Head.Ref, got, pr.Head.SHA)
	}
	return got, nil
}

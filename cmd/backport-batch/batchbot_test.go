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
	"slices"
	"strings"
	"testing"
)

// A maintainer's run by hand continues the batch pull request the scheduled
// run opened as its bot, with nothing set: it edits that pull request rather
// than opening a second one for the line. A record another bot wrote is no
// record the batch trusts, and the run stops rather than reach past it.
func TestHandRunContinuesTheBotsBatch(t *testing.T) {
	for _, tc := range []struct {
		name, author string
		continued    bool
	}{
		{"the scheduled run's bot", "cozystack-ci[bot]", true},
		{"another bot", "renovate[bot]", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.lines()
			f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
			gh := &fakeGH{bot: batchBotLogin(func(string) (string, bool) { return "", false })}
			openBatch(t, f, gh)
			gh.prs[0].User.Login, gh.prs[0].User.Type = tc.author, "Bot"
			f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
			f.now = f.now.AddDate(0, 0, 3)

			code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--continue", "--push")
			if tc.continued && code != 0 || !tc.continued && (code != 2 || !strings.Contains(stderr, "batch pull request #90 (open) carries a ```backport-batch block but was opened by \"renovate[bot]\"")) {
				t.Fatalf("run --continue = %d\n%s", code, stderr)
			}
			continued := slices.Equal(gh.edited, []string{"#90 fix(backport): batch for release-1.1, 2026-10-26"}) && len(gh.created) == 1
			if continued != tc.continued || len(gh.created) != 1 {
				t.Errorf("edited %q, created %q; want the batch of %s continued=%v and no second one\n%s", gh.edited, gh.created, tc.author, tc.continued, stderr)
			}
			if !tc.continued && !strings.Contains(stderr, "ignoring #90") {
				t.Errorf("the run does not say it ignores #90:\n%s", stderr)
			}
		})
	}
}

// The bot comes from BACKPORT_BATCH_BOT when it is set, empty included, and
// is the scheduled run's own bot when it is not.
func TestBatchBotLogin(t *testing.T) {
	env := func(v string, ok bool) func(string) (string, bool) {
		return func(key string) (string, bool) {
			if key != batchBotEnv {
				t.Errorf("looked up %s", key)
			}
			return v, ok
		}
	}
	for _, tc := range []struct {
		name, v string
		ok      bool
		want    string
	}{
		{"unset", "", false, "cozystack-ci[bot]"},
		{"set empty", "", true, ""},
		{"set", "other-app[bot]", true, "other-app[bot]"},
	} {
		if got := batchBotLogin(env(tc.v, tc.ok)); got != tc.want {
			t.Errorf("%s: batchBotLogin = %q, want %q", tc.name, got, tc.want)
		}
	}
}

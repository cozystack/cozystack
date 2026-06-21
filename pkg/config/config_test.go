package config

import (
	"strings"
	"testing"
	"time"
)

// Cover the annotation parser used by cozystack-api at startup. The parser
// is consumed by pkg/cmd/server/start.go on every ApplicationDefinition; a
// typo here silently drops back to flux defaults and the Kubernetes tenant
// race described in cozystack#2412 reappears, so the table must exercise:
// - the unset path (empty string treated as "no override"),
// - every unit Flux accepts (ms, s, m, h),
// - compound forms (the CRD pattern accepts repeats),
// - units time.ParseDuration accepts but Flux rejects (ns, us, µs),
// - outright garbage.
func TestParseHelmInstallTimeoutAnnotation(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		want     time.Duration
		wantErr  bool
		errMatch string
	}{
		{
			name:  "empty string leaves flux defaults in place",
			input: "",
			want:  0,
		},
		{
			name:  "minutes",
			input: "15m",
			want:  15 * time.Minute,
		},
		{
			name:  "hours",
			input: "1h",
			want:  time.Hour,
		},
		{
			name:  "seconds",
			input: "45s",
			want:  45 * time.Second,
		},
		{
			name:  "milliseconds",
			input: "500ms",
			want:  500 * time.Millisecond,
		},
		{
			name:  "compound hour and minutes",
			input: "2h30m",
			want:  2*time.Hour + 30*time.Minute,
		},
		{
			name:  "decimal minutes",
			input: "1.5m",
			want:  90 * time.Second,
		},
		{
			name:     "nanoseconds rejected - Flux CRD pattern excludes ns",
			input:    "500ns",
			wantErr:  true,
			errMatch: "Flux accepts ms/s/m/h units only",
		},
		{
			name:     "microseconds rejected - Flux CRD pattern excludes us",
			input:    "500us",
			wantErr:  true,
			errMatch: "Flux accepts ms/s/m/h units only",
		},
		{
			name:     "microseconds unicode rejected",
			input:    "500µs",
			wantErr:  true,
			errMatch: "Flux accepts ms/s/m/h units only",
		},
		{
			name:     "bare digits rejected",
			input:    "15",
			wantErr:  true,
			errMatch: "Flux accepts ms/s/m/h units only",
		},
		{
			name:     "garbage rejected",
			input:    "abc",
			wantErr:  true,
			errMatch: "Flux accepts ms/s/m/h units only",
		},
		{
			name:     "negative rejected",
			input:    "-15m",
			wantErr:  true,
			errMatch: "Flux accepts ms/s/m/h units only",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseHelmInstallTimeoutAnnotation(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got duration=%v", got)
				}
				if tc.errMatch != "" && !strings.Contains(err.Error(), tc.errMatch) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.errMatch)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// Cover the disable-wait annotation parser. cozystack-api consumes it on
// every ApplicationDefinition at startup (see pkg/cmd/server/start.go);
// a typo in the parsed value silently drops back to false and the
// chicken-and-egg deadlock between the kubernetes chart and its emitted
// in-tenant addon HelmReleases reappears (parent chart waits for child
// HRs Ready before running post-install hooks, child HRs cannot be Ready
// without the TalosConfigTemplate the parent's hook produces, deadlock).
// Mirrors the sibling timeout parser table.
func TestParseHelmInstallDisableWaitAnnotation(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		want     bool
		wantErr  bool
		errMatch string
	}{
		{
			name:  "empty string leaves flux defaults in place",
			input: "",
			want:  false,
		},
		{
			name:  "lower-case true",
			input: "true",
			want:  true,
		},
		{
			name:  "title-case True",
			input: "True",
			want:  true,
		},
		{
			name:  "upper-case TRUE",
			input: "TRUE",
			want:  true,
		},
		{
			name:  "lower-case false",
			input: "false",
			want:  false,
		},
		{
			name:  "title-case False",
			input: "False",
			want:  false,
		},
		{
			name:  "upper-case FALSE",
			input: "FALSE",
			want:  false,
		},
		{
			name:     "mixed-case rejected (Helm-style scrubbing not applied here)",
			input:    "tRue",
			wantErr:  true,
			errMatch: `must be "true" or "false"`,
		},
		{
			name:     "integer rejected",
			input:    "1",
			wantErr:  true,
			errMatch: `must be "true" or "false"`,
		},
		{
			name:     "yes/no idiom rejected",
			input:    "yes",
			wantErr:  true,
			errMatch: `must be "true" or "false"`,
		},
		{
			name:     "garbage rejected",
			input:    "abc",
			wantErr:  true,
			errMatch: `must be "true" or "false"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseHelmInstallDisableWaitAnnotation(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				if tc.errMatch != "" && !strings.Contains(err.Error(), tc.errMatch) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.errMatch)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

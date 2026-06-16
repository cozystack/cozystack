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

// ParsePositiveDuration is the startup validator shared by cozystack-operator
// and cozystack-api. Both binaries reject zero/negative/malformed values so a
// misconfigured flag fails fast instead of propagating into every generated
// HelmRelease and being rejected by helm-controller's CRD validation later.
func TestParsePositiveDuration(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want time.Duration
		// errMatch pins which of the two error branches fired: a
		// time.ParseDuration failure ("invalid duration") versus a
		// non-positive value ("must be > 0"). Keeps the two diagnostics
		// from drifting and operators seeing the wrong message.
		errMatch string
		wantErr  bool
	}{
		{name: "valid seconds", raw: "30s", want: 30 * time.Second},
		{name: "valid minutes", raw: "5m", want: 5 * time.Minute},
		{name: "valid compound", raw: "1h30m", want: 90 * time.Minute},
		{name: "zero rejected", raw: "0s", wantErr: true, errMatch: "must be > 0"},
		{name: "negative rejected", raw: "-5m", wantErr: true, errMatch: "must be > 0"},
		{name: "malformed rejected", raw: "5x", wantErr: true, errMatch: "invalid duration"},
		{name: "empty rejected", raw: "", wantErr: true, errMatch: "invalid duration"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePositiveDuration("--test-flag", tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got nil", tt.raw)
				}
				if tt.errMatch != "" && !strings.Contains(err.Error(), tt.errMatch) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errMatch)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

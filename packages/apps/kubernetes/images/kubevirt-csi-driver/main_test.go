package main

import (
	"context"
	"testing"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
)

// client-go applies these limits to a rest.Config that leaves QPS/Burst unset.
// They are what the driver ran with before this fix.
const (
	clientGoDefaultQPS   float32 = 5
	clientGoDefaultBurst int     = 10
)

// applyKubeAPIRateLimits must raise the client both above the client-go default,
// otherwise the ControllerPublishVolume PVC-bound poll keeps starving under a
// burst of concurrent attaches.
func TestApplyKubeAPIRateLimitsRaisesAboveClientGoDefault(t *testing.T) {
	cfg := &rest.Config{}
	applyKubeAPIRateLimits(cfg, defaultKubeAPIQPS, defaultKubeAPIBurst)

	if cfg.QPS <= clientGoDefaultQPS {
		t.Errorf("QPS = %v, want > %v (client-go default)", cfg.QPS, clientGoDefaultQPS)
	}
	if cfg.Burst <= clientGoDefaultBurst {
		t.Errorf("Burst = %d, want > %d (client-go default)", cfg.Burst, clientGoDefaultBurst)
	}
}

// A non-default qps/burst must actually reach the config, so --kube-api-qps and
// --kube-api-burst are real tuning knobs and not just decoration over the constants.
func TestApplyKubeAPIRateLimitsPropagatesValues(t *testing.T) {
	cfg := &rest.Config{}
	applyKubeAPIRateLimits(cfg, 42, 84)

	if cfg.QPS != 42 {
		t.Errorf("QPS = %v, want 42", cfg.QPS)
	}
	if cfg.Burst != 84 {
		t.Errorf("Burst = %d, want 84", cfg.Burst)
	}
}

// The default flag values must pass validation, and a non-positive value — which
// client-go would silently turn into the starving default (0) or unlimited
// (<0) — must be rejected.
func TestValidateRateLimitFlags(t *testing.T) {
	if err := validateRateLimitFlags(defaultKubeAPIQPS, defaultKubeAPIBurst); err != nil {
		t.Errorf("defaults must be valid, got %v", err)
	}

	cases := []struct {
		name  string
		qps   float64
		burst int
	}{
		{"zero qps falls back to the starving default", 0, defaultKubeAPIBurst},
		{"negative qps disables rate limiting", -1, defaultKubeAPIBurst},
		{"zero burst", defaultKubeAPIQPS, 0},
		{"negative burst", defaultKubeAPIQPS, -5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateRateLimitFlags(tc.qps, tc.burst); err == nil {
				t.Errorf("validateRateLimitFlags(%v, %d) = nil, want error", tc.qps, tc.burst)
			}
		})
	}
}

// Behavioural regression for the FailedAttachVolume flake. The NFS-volume
// ControllerPublishVolume path polls the infra PVC once per second for up to two
// minutes; when several volumes attach at once the default 5 QPS / 10 burst limiter starves and Wait returns
// "context deadline exceeded" — the exact error the tenant kubelet reports as
// FailedAttachVolume. The limits applyKubeAPIRateLimits sets must admit that burst.
func TestRateLimitedConfigAdmitsBurstThatDefaultStarves(t *testing.T) {
	// A burst larger than any refill the deadline can supply: 5 QPS over 500ms
	// tops up ~2.5 tokens on top of a 10-token bucket, so the default limiter
	// cannot admit 200 waiters before the deadline, while a bucket sized for the
	// burst admits them immediately. Both outcomes are deterministic.
	const burst = 200
	const deadline = 500 * time.Millisecond

	drain := func(qps float32, b int) error {
		lim := flowcontrol.NewTokenBucketRateLimiter(qps, b)
		ctx, cancel := context.WithTimeout(context.Background(), deadline)
		defer cancel()
		for range burst {
			if err := lim.Wait(ctx); err != nil {
				return err
			}
		}
		return nil
	}

	if err := drain(clientGoDefaultQPS, clientGoDefaultBurst); err == nil {
		t.Fatal("default 5 QPS / 10 burst limiter admitted the burst; the test can no longer prove the fix")
	}

	cfg := &rest.Config{}
	applyKubeAPIRateLimits(cfg, defaultKubeAPIQPS, defaultKubeAPIBurst)
	if err := drain(cfg.QPS, cfg.Burst); err != nil {
		t.Errorf("configured limiter (%v QPS / %d burst) still starved on the attach burst: %v", cfg.QPS, cfg.Burst, err)
	}
}

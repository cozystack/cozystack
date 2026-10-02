package backupcontroller

import (
	"bytes"
	"context"
	"encoding/json"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

// LegacyPasswordScrubber is a one-shot Runnable that removes plaintext
// users[].password values from CNPG Backup snapshots in
// status.underlyingResources.
//
// Snapshots taken while the Postgres app still accepted users[].password
// copied it verbatim, where anyone with `get backups` in the namespace can
// read it. New snapshots no longer carry the field, and restore ignores it,
// so dropping it changes nothing but the exposure.
//
// It runs on every leader start rather than once per cluster, so a Backup
// written by a pre-upgrade replica during the rollout, or brought back from
// an object-level backup, is still cleaned. On a clean cluster it is a
// single cached List.
//
// TODO(#4179): remove once upgrading from a release that accepted
// users[].password is no longer supported.
type LegacyPasswordScrubber struct {
	Client client.Client
}

var (
	_ manager.Runnable               = (*LegacyPasswordScrubber)(nil)
	_ manager.LeaderElectionRunnable = (*LegacyPasswordScrubber)(nil)
)

func (s *LegacyPasswordScrubber) NeedLeaderElection() bool { return true }

// Start never returns an error: a returned error stops the manager, and a
// Backup that cannot be cleaned must not take backups down with it.
func (s *LegacyPasswordScrubber) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("legacy-password-scrubber")

	var backups backupsv1alpha1.BackupList
	if err := s.Client.List(ctx, &backups); err != nil {
		logger.Error(err, "listing Backups; legacy snapshot passwords were not scrubbed")
		return nil
	}

	scrubbed := 0
	for i := range backups.Items {
		b := &backups.Items[i]
		if b.Status.UnderlyingResources == nil {
			continue
		}
		raw, changed, err := scrubCNPGSnapshotPasswords(b.Status.UnderlyingResources.Raw)
		if err != nil {
			logger.Error(err, "decoding status.underlyingResources", "namespace", b.Namespace, "backup", b.Name)
			continue
		}
		if !changed {
			continue
		}
		// A merge patch computed from the diff carries only
		// `password: null` per user, so it cannot overwrite status fields
		// another writer changed since the List.
		orig := b.DeepCopy()
		b.Status.UnderlyingResources = &runtime.RawExtension{Raw: raw}
		if err := s.Client.Patch(ctx, b, client.MergeFrom(orig)); client.IgnoreNotFound(err) != nil {
			logger.Error(err, "patching Backup", "namespace", b.Namespace, "backup", b.Name)
			continue
		}
		scrubbed++
		logger.Info("removed legacy plaintext passwords from Backup snapshot", "namespace", b.Namespace, "backup", b.Name)
	}
	logger.Info("legacy snapshot password scrub finished", "scanned", len(backups.Items), "scrubbed", scrubbed)
	return nil
}

// scrubCNPGSnapshotPasswords returns raw with users.*.password removed when
// raw is a CNPG snapshot carrying one, and changed=false otherwise. It
// decodes into generic maps so fields the current snapshot type does not
// know survive the round trip.
func scrubCNPGSnapshotPasswords(raw []byte) ([]byte, bool, error) {
	if len(raw) == 0 {
		return raw, false, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var snap map[string]any
	if err := dec.Decode(&snap); err != nil {
		return nil, false, err
	}
	if snap["kind"] != cnpgBackupSnapshotKind || snap["apiVersion"] != cnpgBackupSnapshotAPIVersion {
		return raw, false, nil
	}
	users, _ := snap["users"].(map[string]any)
	changed := false
	for _, u := range users {
		user, ok := u.(map[string]any)
		if !ok {
			continue
		}
		if _, ok := user["password"]; ok {
			delete(user, "password")
			changed = true
		}
	}
	if !changed {
		return raw, false, nil
	}
	out, err := json.Marshal(snap)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

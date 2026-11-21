// SPDX-License-Identifier: Apache-2.0

// Command credentials-migrate converts the accounts of an application kind
// to platform-minted credentials: "seed" from the pre-upgrade hook, "finish"
// from a Job once the charts have switched. Both take the kind's format and
// accounts map from flags, because the hook runs before the converted chart
// renders the CredentialTemplates. See design-proposals/user-secrets-api in
// cozystack/community.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	fluxmeta "github.com/fluxcd/pkg/apis/meta"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	"github.com/cozystack/cozystack/pkg/credentials/account"
	"github.com/cozystack/cozystack/pkg/credentials/format"
)

// errUnresolvable marks a release whose accounts cannot be resolved from its
// values: the one cause the hook leaves a release out for. Every other error
// fails the hook, so the upgrade retries instead of switching a release whose
// Secrets were not written.
var errUnresolvable = errors.New("accounts cannot be resolved")

const (
	reasonSkipped = "CredentialsMigrationSkipped"
	defaultActor  = "system:serviceaccount:cozy-system:cozystack-migration-hook"
	// incompleteLabel on the state ConfigMap is the durable signal behind the
	// CredentialsMigrationIncomplete alert: the ConfigMap outlives the finish
	// Job, so an alert keyed on this label fires until the conversion is
	// finished, not only for the Job's TTL. "true" while any release is left
	// out, "false" once none is.
	incompleteLabel = "credentials.cozystack.io/incomplete"
)

const (
	// RotatedAnnotation on the source Secret records when finish rotated its
	// platform passwords.
	RotatedAnnotation = "credentials.cozystack.io/rotated"
	// ForcedAnnotation on the source Secret holds the RotatedAnnotation
	// value of the rotation a forced upgrade was requested for.
	ForcedAnnotation = "credentials.cozystack.io/forced"
	// SuspendedByAnnotation on a HelmRelease names the migration that
	// suspended it for a rotation, so only that suspension is lifted.
	SuspendedByAnnotation = "credentials.cozystack.io/suspended-by-migration"
)

// options is the kind's credentials block and the migration's identity, as
// the hook and the Job pass them.
type options struct {
	migration      int
	kind           string
	formatName     string
	accountsPath   string
	refuseFlags    stringFlags
	reservedUsers  stringFlags
	skipAccounts   stringFlags
	labels         labelFlags
	valuesPath     string
	sourceSuffix   string
	platformKeys   stringFlags
	rotateKeys     stringFlags
	stateNamespace string
	actor          string
	timeout        time.Duration
	holdRollouts   stringFlags
	holdTimeout    time.Duration
}

type labelFlags map[string]string

func (l labelFlags) String() string { return fmt.Sprint(map[string]string(l)) }
func (l labelFlags) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok || k == "" {
		return fmt.Errorf("label %q is not key=value", v)
	}
	l[k] = val
	return nil
}

type stringFlags []string

func (s *stringFlags) String() string     { return strings.Join(*s, ",") }
func (s *stringFlags) Set(v string) error { *s = append(*s, v); return nil }

func (o *options) stateName() string {
	return fmt.Sprintf("credentials-migration-%d", o.migration)
}

// state is what the migration keeps between the hook and the Job, in a
// ConfigMap in cozy-system named for the migration.
type state struct {
	// Releases maps namespace/name to the record of the release.
	Releases map[string]*releaseState `json:"releases"`
	// Skipped maps namespace/name to the reason and time it was left out on
	// the last run. A release leaves the map once a later run finishes it.
	Skipped map[string]string `json:"skipped,omitempty"`
	// Rollouts maps namespace/name of each Deployment the held releases wait
	// for to the images it ran when the hook recorded it.
	Rollouts map[string]string `json:"rollouts,omitempty"`
}

type releaseState struct {
	// ChartVersion the release rendered when the hook recorded it; the Job
	// acts once the release is Ready on another one.
	ChartVersion string `json:"chartVersion"`
	Done         bool   `json:"done,omitempty"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "credentials-migrate:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: credentials-migrate seed|finish [flags]")
	}
	cmd := args[0]
	o := &options{labels: labelFlags{}}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.IntVar(&o.migration, "migration", 0, "number of the platform migration that converts the kind")
	fs.StringVar(&o.kind, "kind", "", "application kind, as the HelmRelease label names it")
	fs.StringVar(&o.formatName, "format", "", "password format of the kind's credentials block")
	fs.StringVar(&o.accountsPath, "accounts-path", "users", "values map whose keys are the tenant accounts")
	fs.Var(&o.refuseFlags, "refuse-flag", "user field that leaves a release out when it is true, because the converted chart refuses it; repeatable")
	fs.Var(&o.reservedUsers, "reserved-user", "user name that leaves a release out, because the converted chart refuses it as a platform or engine account; repeatable")
	fs.Var(&o.skipAccounts, "skip-account", "key of the accounts map that the converted chart renders no CredentialTemplate for, such as a platform account the chart keeps out of the tenant's map; it gets no account Secret; repeatable")
	fs.Var(o.labels, "label", "label every account Secret carries, key=value; repeatable")
	fs.StringVar(&o.valuesPath, "values-path", "", "the kind's CredentialTemplate valuesPath: the hook adds the valuesFrom entry of every account to the release, so the first render of the converted chart already carries the stored values")
	fs.StringVar(&o.sourceSuffix, "source-suffix", "-credentials", "suffix of the chart-generated Secret holding the plaintext passwords, after the release name")
	fs.Var(&o.platformKeys, "platform-key", "key of the source Secret that belongs to a platform account and stays; repeatable")
	fs.Var(&o.rotateKeys, "rotate-key", "finish: platform key of the source Secret to replace with a new random password once, followed by a forced upgrade of the release; repeatable")
	fs.StringVar(&o.stateNamespace, "state-namespace", "cozy-system", "namespace of the state ConfigMap")
	fs.StringVar(&o.actor, "actor", envOr("CREDENTIALS_MIGRATE_ACTOR", defaultActor), "who the record names for the migrated password")
	fs.DurationVar(&o.timeout, "timeout", 10*time.Minute, "finish: how long to wait per release for the switched chart to be Ready")
	fs.Var(&o.holdRollouts, "hold-until-rollout", "seed: Deployment, namespace/name, whose new revision the converted chart needs; the hook suspends every release of the kind and the finish Job resumes them once each such Deployment runs images other than the ones recorded now; repeatable")
	fs.DurationVar(&o.holdTimeout, "hold-timeout", time.Hour, "finish: how long to wait for the --hold-until-rollout Deployments before reporting the held releases left out")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if o.migration == 0 || o.kind == "" || !format.Known(o.formatName) {
		return errors.New("--migration, --kind and a built-in --format are required")
	}
	for _, k := range o.rotateKeys {
		if !slices.Contains(o.platformKeys, k) {
			return fmt.Errorf("--rotate-key %q is not a --platform-key, and finish would delete it from the source Secret", k)
		}
	}
	for _, d := range o.holdRollouts {
		if _, err := parseObjectKey(d); err != nil {
			return fmt.Errorf("--hold-until-rollout: %w", err)
		}
	}

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return err
	}
	if err := helmv2.AddToScheme(scheme); err != nil {
		return err
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		return err
	}
	c, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("build client: %w", err)
	}
	m := &migrator{c: c, o: o, now: time.Now}
	ctx := context.Background()
	switch cmd {
	case "seed":
		return m.seed(ctx)
	case "finish":
		return m.finish(ctx)
	}
	return fmt.Errorf("unknown command %q: want seed or finish", cmd)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type migrator struct {
	c   client.Client
	o   *options
	now func() time.Time
}

// seed records every release of the kind and writes the account Secrets it
// still lacks from the chart-generated passwords. A release whose accounts
// cannot be resolved is left out with an Event; one whose source Secret is
// gone is recorded, since the Job still has to switch it, and its accounts
// get their Secrets from the first mint.
func (m *migrator) seed(ctx context.Context) error {
	st, err := m.loadState(ctx)
	if err != nil {
		return err
	}
	releases, err := m.releases(ctx)
	if err != nil {
		return err
	}
	if len(releases) > 0 {
		// Saved before any release is held, so the finish Job always knows
		// what it waits for before it resumes them.
		if err := m.recordRollouts(ctx, st); err != nil {
			return err
		}
		if err := m.saveState(ctx, st); err != nil {
			return err
		}
	}
	for i := range releases {
		hr := &releases[i]
		key := hr.Namespace + "/" + hr.Name
		rec, known := st.Releases[key]
		if !known {
			rec = &releaseState{ChartVersion: chartVersion(hr)}
			st.Releases[key] = rec
		}
		if err := m.seedRelease(ctx, hr); err != nil {
			if !errors.Is(err, errUnresolvable) {
				if serr := m.saveState(ctx, st); serr != nil {
					return errors.Join(fmt.Errorf("%s: %w", key, err), serr)
				}
				return fmt.Errorf("%s: %w", key, err)
			}
			m.skip(ctx, st, hr, err)
		} else {
			delete(st.Skipped, key)
			fmt.Printf("%s: recorded chart version %q\n", key, rec.ChartVersion)
		}
		if err := m.hold(ctx, hr); err != nil {
			return errors.Join(fmt.Errorf("%s: %w", key, err), m.saveState(ctx, st))
		}
	}
	return m.saveState(ctx, st)
}

// finish converts every recorded release once its chart has switched, and
// exits non-zero while any is left out, so one place shows what remains.
func (m *migrator) finish(ctx context.Context) error {
	st, err := m.loadState(ctx)
	if err != nil {
		return err
	}
	if err := m.releaseHolds(ctx, st); err != nil {
		return err
	}
	keys := make([]string, 0, len(st.Releases))
	for k := range st.Releases {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		rec := st.Releases[key]
		if rec.Done {
			continue
		}
		ns, name, _ := strings.Cut(key, "/")
		hr := &helmv2.HelmRelease{}
		if err := m.c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, hr); err != nil {
			if apierrors.IsNotFound(err) {
				// Deleted since the hook: nothing left to convert.
				rec.Done = true
				delete(st.Skipped, key)
				continue
			}
			return err
		}
		if err := m.finishReleaseRetrying(ctx, hr, rec); err != nil {
			m.leaveOut(ctx, st, hr, err)
			if err := m.saveState(ctx, st); err != nil {
				return err
			}
			continue
		}
		rec.Done = true
		delete(st.Skipped, key)
		fmt.Printf("%s: converted\n", key)
		if err := m.saveState(ctx, st); err != nil {
			return err
		}
	}
	if err := m.saveState(ctx, st); err != nil {
		return err
	}
	if len(st.Skipped) > 0 {
		var left []string
		for k, why := range st.Skipped {
			left = append(left, k+": "+why)
		}
		sort.Strings(left)
		return fmt.Errorf("%d release(s) left out:\n  %s", len(left), strings.Join(left, "\n  "))
	}
	return nil
}

func (m *migrator) finishRelease(ctx context.Context, hr *helmv2.HelmRelease, rec *releaseState) error {
	// Seed again first: an account whose Secret is still missing, as after an
	// interrupted hook, gets it here while the plaintext still exists.
	if err := m.seedRelease(ctx, hr); err != nil {
		return err
	}
	// Without a recorded version nothing tells the old chart from the new
	// one, and retiring the source under the old chart takes its passwords.
	if rec.ChartVersion == "" {
		return errors.New("no chart version was recorded before the upgrade, so the switch cannot be confirmed; check that the release renders the converted chart and finish it by hand")
	}
	deadline := m.now().Add(m.o.timeout)
	for {
		if apimeta.IsStatusConditionTrue(hr.Status.Conditions, "Ready") && chartVersion(hr) != "" && chartVersion(hr) != rec.ChartVersion {
			break
		}
		if m.now().After(deadline) {
			return fmt.Errorf("not Ready on a chart version other than %q after %s (current %q)", rec.ChartVersion, m.o.timeout, chartVersion(hr))
		}
		time.Sleep(10 * time.Second)
		if err := m.c.Get(ctx, client.ObjectKeyFromObject(hr), hr); err != nil {
			return err
		}
	}
	if err := m.stripPasswords(ctx, hr); err != nil {
		return fmt.Errorf("strip passwords from values: %w", err)
	}
	if err := m.retireSource(ctx, hr); err != nil {
		return err
	}
	return m.rotatePlatformKeys(ctx, hr)
}

// seedRelease writes the account Secrets the release lacks, unowned: the
// CredentialTemplate controller adopts them once the converted chart renders
// the templates. Once every declared account has its Secret, it marks the
// release for seeding, so accounts added later get theirs from the
// controller. A release where an account is left without a Secret, its
// plaintext missing or one that would not carry over, stays unmarked: a seeded
// random password would lock out the clients that use the old one, so that
// account waits for a mint. A release that declares no account has no
// plaintext to lose, so it is marked whether or not its source exists: the
// chart renders the source only once there is a user, and without the mark
// the first user added later would never get a Secret. Every failure other
// than unresolvable accounts is an error.
func (m *migrator) seedRelease(ctx context.Context, hr *helmv2.HelmRelease) error {
	accounts, err := m.o.resolveAccounts(hr)
	if err != nil {
		return fmt.Errorf("%w: %v", errUnresolvable, err)
	}
	source := &corev1.Secret{}
	if err := m.c.Get(ctx, types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name + m.o.sourceSuffix}, source); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		if len(accounts) > 0 {
			fmt.Printf("%s/%s: %s%s is gone; its accounts get a Secret at their first mint\n", hr.Namespace, hr.Name, hr.Name, m.o.sourceSuffix)
			return nil
		}
	}
	now := metav1.NewTime(m.now())
	complete := true
	for _, acc := range accounts {
		name := account.SecretName(hr.Name, acc.Mapped)
		cur := &corev1.Secret{}
		err := m.c.Get(ctx, types.NamespacedName{Namespace: hr.Namespace, Name: name}, cur)
		switch {
		case err == nil && account.IsAccountSecret(cur) && string(cur.Data[account.UsernameKey]) == acc.Username:
			continue
		case err == nil && metav1.GetControllerOf(cur) == nil:
			// Left from a deleted application of the same name, or not an
			// account Secret at all: the operator would apply whatever it
			// holds, so this one replaces it.
			if err := m.c.Delete(ctx, cur, client.Preconditions{UID: &cur.UID}); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("replace stale %s: %w", name, err)
			}
		case err == nil:
			fmt.Printf("%s/%s: %s exists and is not an account Secret this conversion wrote; leaving it and the account %q alone\n", hr.Namespace, hr.Name, name, acc.Username)
			complete = false
			continue
		case !apierrors.IsNotFound(err):
			return err
		}
		plaintext, ok := source.Data[acc.Username]
		if !ok {
			fmt.Printf("%s/%s: no password for %q in %s%s; the account gets a Secret at its first mint\n", hr.Namespace, hr.Name, acc.Username, hr.Name, m.o.sourceSuffix)
			complete = false
			continue
		}
		if !format.CarriesOver(m.o.formatName, string(plaintext)) {
			// A verifier built from this password would not authenticate it
			// after the engine's login-time normalization, so the account is
			// re-minted instead of carried over with a Secret nobody can use.
			fmt.Printf("%s/%s: the password for %q in %s%s does not carry into a %s verifier unchanged; the account gets a Secret at its first mint\n", hr.Namespace, hr.Name, acc.Username, hr.Name, m.o.sourceSuffix, m.o.formatName)
			complete = false
			continue
		}
		stored, err := format.Encode(m.o.formatName, string(plaintext))
		if err != nil {
			return err
		}
		sec := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Namespace:   hr.Namespace,
				Labels:      account.Labels(m.o.labels),
				Annotations: map[string]string{account.FormatAnnotation: m.o.formatName},
			},
			Type: corev1.SecretTypeBasicAuth,
			Data: map[string][]byte{
				account.UsernameKey: []byte(acc.Username),
				account.PasswordKey: []byte(stored),
			},
		}
		account.WriteRecord(sec, account.Record{}.Apply(corev1alpha1.CredentialOperationMigrate, now, m.o.actor))
		if err := m.c.Create(ctx, sec); err != nil {
			return fmt.Errorf("create %s: %w", name, err)
		}
		fmt.Printf("%s/%s: migrated account %q into %s\n", hr.Namespace, hr.Name, acc.Username, name)
	}
	if err := m.addValuesFrom(ctx, hr, accounts); err != nil {
		return fmt.Errorf("add valuesFrom entries: %w", err)
	}
	if !complete || hr.Annotations[account.SeedAnnotation] == "true" {
		return nil
	}
	patch := client.MergeFrom(hr.DeepCopy())
	if hr.Annotations == nil {
		hr.Annotations = map[string]string{}
	}
	hr.Annotations[account.SeedAnnotation] = "true"
	if err := m.c.Patch(ctx, hr, patch); err != nil {
		return fmt.Errorf("mark for seeding: %w", err)
	}
	return nil
}

// addValuesFrom gives the release the valuesFrom entry of every account,
// as the CredentialTemplate controller builds it, for a kind whose templates
// name a valuesPath. The controller adds them only once the converted chart
// has rendered the templates, so without them that first render would carry
// no stored value, and the chart would leave every account out of the engine
// until the next upgrade. The entries are optional and the old chart ignores
// the values they fill, so adding them ahead of the switch changes nothing for
// it. Non-account entries keep their place, and account entries are replaced
// in template-name order, which is the controller's, so it finds nothing to
// change.
func (m *migrator) addValuesFrom(ctx context.Context, hr *helmv2.HelmRelease, accounts []account.Account) error {
	if m.o.valuesPath == "" {
		return nil
	}
	var want []helmv2.ValuesReference
	for _, ref := range hr.Spec.ValuesFrom {
		if !account.IsAccountValuesFrom(ref) {
			want = append(want, ref)
		}
	}
	sorted := slices.Clone(accounts)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Mapped < sorted[j].Mapped })
	for _, acc := range sorted {
		want = append(want, account.ValuesFrom(account.SecretName(hr.Name, acc.Mapped), m.o.valuesPath, acc.Username))
	}
	if equality.Semantic.DeepEqual(hr.Spec.ValuesFrom, want) {
		return nil
	}
	patch := client.MergeFrom(hr.DeepCopy())
	hr.Spec.ValuesFrom = want
	return m.c.Patch(ctx, hr, patch)
}

// resolveAccounts returns the accounts the release declares: the keys of its
// accounts map, in sorted order. Two users mapping to one name, a user whose
// name maps to an invalid one, or a user with a refused field set to true,
// which the converted chart would not render, or a reserved user name leave
// the release unresolvable. A skipped key is not a tenant account: the
// converted chart renders no template for it, so it is left out of the result.
func (o *options) resolveAccounts(hr *helmv2.HelmRelease) ([]account.Account, error) {
	var values map[string]any
	if hr.Spec.Values != nil && len(hr.Spec.Values.Raw) > 0 {
		if err := json.Unmarshal(hr.Spec.Values.Raw, &values); err != nil {
			return nil, err
		}
	}
	users, _ := lookup(values, o.accountsPath).(map[string]any)
	names := make([]string, 0, len(users))
	for name := range users {
		if slices.Contains(o.skipAccounts, name) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var accounts []account.Account
	byMapped := map[string]string{}
	for _, name := range names {
		if slices.Contains(o.reservedUsers, name) {
			return nil, fmt.Errorf("%s.%s is a name the converted chart reserves for a platform or engine account; rename the user", o.accountsPath, name)
		}
		if u, ok := users[name].(map[string]any); ok {
			for _, field := range o.refuseFlags {
				if on, _ := u[field].(bool); on {
					return nil, fmt.Errorf("%s.%s.%s is true, which the converted chart refuses; set it to false", o.accountsPath, name, field)
				}
			}
		}
		mapped, err := account.MapName(name)
		if err != nil {
			return nil, err
		}
		if other, dup := byMapped[mapped]; dup {
			return nil, fmt.Errorf("users %q and %q both map to the name %q", other, name, mapped)
		}
		byMapped[mapped] = name
		accounts = append(accounts, account.Account{Username: name, Mapped: mapped})
	}
	return accounts, nil
}

// lookup walks a dotted path through nested maps.
func lookup(values map[string]any, path string) any {
	var cur any = values
	for _, key := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = m[key]
		if !ok {
			return nil
		}
	}
	return cur
}

// stripPasswords removes a password left under any key of the accounts map in
// the release's values.
func (m *migrator) stripPasswords(ctx context.Context, hr *helmv2.HelmRelease) error {
	if hr.Spec.Values == nil || len(hr.Spec.Values.Raw) == 0 {
		return nil
	}
	var values map[string]any
	if err := json.Unmarshal(hr.Spec.Values.Raw, &values); err != nil {
		return err
	}
	users, _ := lookup(values, m.o.accountsPath).(map[string]any)
	changed := false
	for name, raw := range users {
		u, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if _, has := u["password"]; has {
			delete(u, "password")
			changed = true
			fmt.Printf("%s/%s: removed %s.%s.password from the values\n", hr.Namespace, hr.Name, m.o.accountsPath, name)
		}
	}
	if !changed {
		return nil
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return err
	}
	patch := client.MergeFrom(hr.DeepCopy())
	hr.Spec.Values.Raw = raw
	return m.c.Patch(ctx, hr, patch)
}

// retireSource removes the tenant keys from the chart-generated Secret, which
// stay in its data otherwise, and the Secret itself when no platform key
// remains.
func (m *migrator) retireSource(ctx context.Context, hr *helmv2.HelmRelease) error {
	source := &corev1.Secret{}
	if err := m.c.Get(ctx, types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name + m.o.sourceSuffix}, source); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	keep := map[string]bool{}
	for _, k := range m.o.platformKeys {
		keep[k] = true
	}
	remaining := map[string][]byte{}
	for k, v := range source.Data {
		if keep[k] {
			remaining[k] = v
		}
	}
	if len(remaining) == 0 {
		if err := m.c.Delete(ctx, source, client.Preconditions{UID: &source.UID}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		fmt.Printf("%s/%s: deleted %s\n", hr.Namespace, hr.Name, source.Name)
		return nil
	}
	if len(remaining) == len(source.Data) {
		return nil
	}
	patch := client.MergeFrom(source.DeepCopy())
	source.Data = remaining
	if err := m.c.Patch(ctx, source, patch); err != nil {
		return err
	}
	fmt.Printf("%s/%s: removed the tenant keys from %s\n", hr.Namespace, hr.Name, source.Name)
	return nil
}

// rotatePlatformKeys replaces, once, the platform passwords tenants could read
// in the source Secret before the conversion, and forces an upgrade of the
// release so the chart, which reads them back with lookup, applies the new
// ones. The release is suspended while the Secret changes, so no render that
// started from the old value writes it back, and only a suspension this
// migration set is lifted. A release someone else suspended is left out
// until they resume it: helm-controller marks a reconcile request handled
// even while the release is suspended, and a force request only counts
// together with an unhandled reconcile request (fluxcd/pkg apis/meta
// HandleAnnotationRequest), so a force requested now would be spent before
// the resume and the new password would never reach the engine. Annotations
// on the Secret record the rotation, so a rerun does not rotate twice, and
// the chart may key a restart of the consumers that read the password at
// start on it, and the forced upgrade, so a rerun does not force twice.
func (m *migrator) rotatePlatformKeys(ctx context.Context, hr *helmv2.HelmRelease) error {
	if len(m.o.rotateKeys) == 0 {
		return nil
	}
	source := &corev1.Secret{}
	if err := m.c.Get(ctx, types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name + m.o.sourceSuffix}, source); err != nil {
		if apierrors.IsNotFound(err) {
			// The chart generates a value no tenant has read at its next render.
			return nil
		}
		return err
	}
	mark := strconv.Itoa(m.o.migration)
	if source.Annotations[RotatedAnnotation] == "" {
		if !hr.Spec.Suspend {
			patch := client.MergeFrom(hr.DeepCopy())
			hr.Spec.Suspend = true
			if hr.Annotations == nil {
				hr.Annotations = map[string]string{}
			}
			hr.Annotations[SuspendedByAnnotation] = mark
			if err := m.c.Patch(ctx, hr, patch); err != nil {
				return fmt.Errorf("suspend for rotation: %w", err)
			}
		}
		patch := client.MergeFrom(source.DeepCopy())
		rotated := 0
		for _, k := range m.o.rotateKeys {
			if _, ok := source.Data[k]; !ok {
				continue
			}
			password, err := format.GeneratePassword()
			if err != nil {
				return err
			}
			source.Data[k] = []byte(password)
			rotated++
		}
		if source.Annotations == nil {
			source.Annotations = map[string]string{}
		}
		source.Annotations[RotatedAnnotation] = m.now().UTC().Format(time.RFC3339)
		if err := m.c.Patch(ctx, source, patch); err != nil {
			return fmt.Errorf("rotate %s: %w", source.Name, err)
		}
		fmt.Printf("%s/%s: rotated %d platform password(s) in %s\n", hr.Namespace, hr.Name, rotated, source.Name)
	}
	if source.Annotations[ForcedAnnotation] == source.Annotations[RotatedAnnotation] {
		return nil
	}
	if hr.Spec.Suspend && hr.Annotations[SuspendedByAnnotation] != mark {
		return fmt.Errorf("the platform passwords in %s are rotated, but the release is suspended by someone else, and a forced upgrade requested now would be spent while it stays suspended; resume it and run the conversion again", source.Name)
	}
	// Request a forced upgrade, and lift the suspension this migration set,
	// including one left by an interrupted earlier run.
	now := m.now().UTC().Format(time.RFC3339Nano)
	patch := client.MergeFrom(hr.DeepCopy())
	if hr.Annotations == nil {
		hr.Annotations = map[string]string{}
	}
	hr.Annotations[fluxmeta.ReconcileRequestAnnotation] = now
	hr.Annotations[fluxmeta.ForceRequestAnnotation] = now
	if hr.Annotations[SuspendedByAnnotation] == mark {
		hr.Spec.Suspend = false
		delete(hr.Annotations, SuspendedByAnnotation)
	}
	if err := m.c.Patch(ctx, hr, patch); err != nil {
		return fmt.Errorf("force an upgrade after rotation: %w", err)
	}
	forced := client.MergeFrom(source.DeepCopy())
	source.Annotations[ForcedAnnotation] = source.Annotations[RotatedAnnotation]
	if err := m.c.Patch(ctx, source, forced); err != nil {
		return fmt.Errorf("record the forced upgrade on %s: %w", source.Name, err)
	}
	return nil
}

func (m *migrator) releases(ctx context.Context) ([]helmv2.HelmRelease, error) {
	list := &helmv2.HelmReleaseList{}
	if err := m.c.List(ctx, list, client.MatchingLabels{
		appsv1alpha1.ApplicationGroupLabel: appsv1alpha1.GroupName,
		appsv1alpha1.ApplicationKindLabel:  m.o.kind,
	}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// skip leaves a release out: logged, recorded in the state, and announced
// with an Event on the HelmRelease, so the cause is visible where the
// release is.
func (m *migrator) skip(ctx context.Context, st *state, hr *helmv2.HelmRelease, cause error) {
	key := hr.Namespace + "/" + hr.Name
	why := fmt.Sprintf("%s at %s", cause.Error(), m.now().UTC().Format(time.RFC3339))
	st.Skipped[key] = why
	fmt.Fprintf(os.Stderr, "%s: left out: %v\n", key, cause)
	ev := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{GenerateName: hr.Name + ".", Namespace: hr.Namespace},
		InvolvedObject: corev1.ObjectReference{
			APIVersion: helmv2.GroupVersion.String(), Kind: helmv2.HelmReleaseKind,
			Name: hr.Name, Namespace: hr.Namespace, UID: hr.UID,
		},
		Reason:         reasonSkipped,
		Message:        fmt.Sprintf("credentials migration %d left this release out: %v", m.o.migration, cause),
		Type:           corev1.EventTypeWarning,
		Source:         corev1.EventSource{Component: "credentials-migrate"},
		FirstTimestamp: metav1.NewTime(m.now()),
		LastTimestamp:  metav1.NewTime(m.now()),
		Count:          1,
	}
	if err := m.c.Create(ctx, ev); err != nil {
		fmt.Fprintf(os.Stderr, "%s: failed to record event: %v\n", key, err)
	}
}

func (m *migrator) loadState(ctx context.Context) (*state, error) {
	st := &state{Releases: map[string]*releaseState{}, Skipped: map[string]string{}}
	cm := &corev1.ConfigMap{}
	err := m.c.Get(ctx, types.NamespacedName{Namespace: m.o.stateNamespace, Name: m.o.stateName()}, cm)
	if apierrors.IsNotFound(err) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if raw := cm.Data["state"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), st); err != nil {
			return nil, fmt.Errorf("decode state ConfigMap %s: %w", cm.Name, err)
		}
	}
	if st.Releases == nil {
		st.Releases = map[string]*releaseState{}
	}
	if st.Skipped == nil {
		st.Skipped = map[string]string{}
	}
	return st, nil
}

func (m *migrator) saveState(ctx context.Context, st *state) error {
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: m.o.stateNamespace, Name: m.o.stateName()}}
	_, err = ctrl.CreateOrUpdate(ctx, m.c, cm, func() error {
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data["state"] = string(raw)
		if cm.Labels == nil {
			cm.Labels = map[string]string{}
		}
		if len(st.Skipped) > 0 {
			cm.Labels[incompleteLabel] = "true"
		} else {
			cm.Labels[incompleteLabel] = "false"
		}
		return nil
	})
	return err
}

func chartVersion(hr *helmv2.HelmRelease) string {
	if s := hr.Status.History.Latest(); s != nil {
		return s.ChartVersion
	}
	return ""
}

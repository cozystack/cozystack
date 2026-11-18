package fluxplunger

import (
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
)

// The chart's pre-delete hook restates two of this package's names in shell and
// go-template, where nothing ties them to the Go constants: the field manager
// whose Apply entry owns a suspension, and the fence finalizer. A rename on
// either side alone would leave the hook removing fences while keeping
// suspensions, or looking for a fence nobody sets. These tests guard the
// spellings only; what the hook's predicates mean is held by
// hack/flux-plunger-fence-hook_test.bats.
const (
	hookTemplate       = "../../../packages/system/flux-plunger/templates/uninstall-fence-cleanup.yaml"
	deploymentTemplate = "../../../packages/system/flux-plunger/templates/deployment.yaml"
)

// The binary must run the manager through RunManager, so the drain runs on stop,
// and size the Lease with ApplyShutdownTimings, so the next flux-plunger cannot
// lead during it; dropping either from main.go leaves every other test green.
func TestMain_RunsTheDrainUnderTheLease(t *testing.T) {
	raw, err := os.ReadFile("../../../cmd/flux-plunger/main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	for _, want := range []string{"fluxplunger.RunManager(", "fluxplunger.ApplyShutdownTimings(", "mgr.GetAPIReader()"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("main.go does not call %q", want)
		}
	}
	if strings.Contains(string(raw), "mgr.Start(") {
		t.Error("main.go starts the manager directly, skipping the drain")
	}
}

// The chart must turn leader election on: without it nothing keeps the next
// flux-plunger from writing while the old one drains.
func TestDeploymentTemplate_EnablesLeaderElection(t *testing.T) {
	raw, err := os.ReadFile(deploymentTemplate)
	if err != nil {
		t.Fatalf("read %s: %v", deploymentTemplate, err)
	}
	if !strings.Contains(string(raw), "- --leader-elect\n") && !strings.Contains(string(raw), "- --leader-elect=true") {
		t.Error("deployment does not pass --leader-elect")
	}
}

// The chart sizes the drain as the grace period less what the manager's stop and
// the margin before SIGKILL take; both live here, so the subtraction must match.
func TestDeploymentTemplate_SubtractsTheStopAndTheMarginTheBinaryUses(t *testing.T) {
	raw, err := os.ReadFile(deploymentTemplate)
	if err != nil {
		t.Fatalf("read %s: %v", deploymentTemplate, err)
	}
	m := regexp.MustCompile(`--` + ShutdownDrainTimeoutFlag + `=\{\{ sub \.Values\.shutdownGracePeriodSeconds (\d+) \}\}s`).FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("deployment does not derive the drain timeout from shutdownGracePeriodSeconds")
	}
	want := int((managerStopTimeout + killMargin) / time.Second)
	if m[1] != strconv.Itoa(want) {
		t.Fatalf("deployment subtracts %s, the binary needs %d (manager stop %v + margin %v)", m[1], want, managerStopTimeout, killMargin)
	}
}

// The binary must refuse a drain deadline it could not drain under, whatever
// the chart or a hand-written Deployment passes, and before it reaches for a
// cluster: the run has no kubeconfig, so a binary that skipped the check fails
// on that instead, with another message.
func TestMain_RefusesADrainTimeoutThatLeavesNoTime(t *testing.T) {
	bin := t.TempDir() + "/flux-plunger"
	if out, err := exec.Command("go", "build", "-o", bin, "../../../cmd/flux-plunger").CombinedOutput(); err != nil {
		t.Fatalf("build flux-plunger: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "--"+ShutdownDrainTimeoutFlag+"=0s")
	cmd.Env = append(os.Environ(), "KUBECONFIG=/dev/null", "KUBERNETES_SERVICE_HOST=")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("flux-plunger started with a drain deadline of 0s:\n%s", out)
	}
	if want := "--" + ShutdownDrainTimeoutFlag + " must be positive"; !strings.Contains(string(out), want) {
		t.Fatalf("flux-plunger did not refuse the deadline itself, want %q in:\n%s", want, out)
	}
}

// The chart's lowest accepted grace period has to leave the drain time after the
// manager's stop and the margin before SIGKILL, or raising either constant makes
// the chart render a drain deadline of zero or less at its own minimum.
func TestDeploymentTemplate_TheLowestGracePeriodLeavesTheDrainTime(t *testing.T) {
	raw, err := os.ReadFile(deploymentTemplate)
	if err != nil {
		t.Fatalf("read %s: %v", deploymentTemplate, err)
	}
	m := regexp.MustCompile(`\(lt \$grace (\d+)\)`).FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("deployment has no lower bound on shutdownGracePeriodSeconds")
	}
	floor, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("lower bound %q: %v", m[1], err)
	}
	if drain := time.Duration(floor)*time.Second - managerStopTimeout - killMargin; drain <= 0 {
		t.Fatalf("at the lowest grace period %ds the drain gets %v (manager stop %v, margin %v)", floor, drain, managerStopTimeout, killMargin)
	}
	if want := strconv.Itoa(int(managerStopTimeout/time.Second)) + "s stop"; !strings.Contains(string(raw), want) {
		t.Errorf("the range message does not name the manager's %q", want)
	}
}

// The README and the values comment restate the drain's timings for operators;
// they must name the numbers the binary and the chart use, or a change to either
// leaves the prose stating the old ones.
func TestDocs_StateTheTimingsTheBinaryAndTheChartUse(t *testing.T) {
	raw, err := os.ReadFile(deploymentTemplate)
	if err != nil {
		t.Fatalf("read %s: %v", deploymentTemplate, err)
	}
	m := regexp.MustCompile(`\(lt \$grace (\d+)\) \(gt \$grace (\d+)\)`).FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("deployment has no range on shutdownGracePeriodSeconds")
	}
	want := []string{
		"between " + m[1] + " and " + m[2],
		strconv.Itoa(int(managerStopTimeout/time.Second)) + " seconds",
		strconv.Itoa(int((managerStopTimeout+killMargin)/time.Second)) + " seconds",
	}
	for _, doc := range []string{"../../../packages/system/flux-plunger/README.md", "../../../packages/system/flux-plunger/values.yaml"} {
		text, err := os.ReadFile(doc)
		if err != nil {
			t.Fatalf("read %s: %v", doc, err)
		}
		for _, w := range want {
			if !strings.Contains(string(text), w) {
				t.Errorf("%s does not state %q", doc, w)
			}
		}
	}
}

// The chart passes the drain deadline as a flag; a name the binary does not
// define makes flux-plunger exit on start.
func TestDeploymentTemplate_PassesTheDrainFlagTheBinaryDefines(t *testing.T) {
	raw, err := os.ReadFile(deploymentTemplate)
	if err != nil {
		t.Fatalf("read %s: %v", deploymentTemplate, err)
	}
	if want := "- --" + ShutdownDrainTimeoutFlag + "="; !strings.Contains(string(raw), want) {
		t.Errorf("deployment does not pass %q", want)
	}
}

func TestHookTemplate_UsesTheControllersNames(t *testing.T) {
	raw, err := os.ReadFile(hookTemplate)
	if err != nil {
		t.Fatalf("read %s: %v", hookTemplate, err)
	}
	hook := string(raw)

	for _, want := range []string{
		`(eq .manager "` + suspendFieldManager + `") (eq .operation "Apply")`,
		`--field-manager ` + suspendFieldManager,
		`fence=` + fenceFinalizer,
	} {
		if !strings.Contains(hook, want) {
			t.Errorf("hook does not contain %q", want)
		}
	}

	// Every flux-plunger finalizer literal in the templates is the controller's
	// fence, whole: matched by the domain, so a misspelt name is caught too.
	literals := regexp.MustCompile(`"[^"\s]*cozystack\.io/[^"\s]*"`).FindAllString(hook, -1)
	if len(literals) == 0 {
		t.Fatal("hook names no fence finalizer in a template")
	}
	for _, l := range literals {
		if l != `"`+fenceFinalizer+`"` {
			t.Errorf("hook template names fence %s, controller sets %q", l, fenceFinalizer)
		}
	}
}

// The hook tells a release helm-controller has stopped holding by
// helm-controller's own finalizer. A rename on the flux side that the hook did
// not follow would read every release being deleted as handled, and the hook
// would unfence it without waiting for the uninstall.
func TestHookTemplate_UsesHelmControllersFinalizer(t *testing.T) {
	raw, err := os.ReadFile(hookTemplate)
	if err != nil {
		t.Fatalf("read %s: %v", hookTemplate, err)
	}
	hook := string(raw)
	if want := `"` + helmv2.HelmReleaseFinalizer + `"`; !strings.Contains(hook, want) {
		t.Fatalf("hook does not name helm-controller's finalizer %s", want)
	}
	// Every other fluxcd.io literal in quotes, the RBAC API group aside, is that
	// finalizer, whole.
	for _, l := range regexp.MustCompile(`"[^"\s]*fluxcd\.io[^"\s]*"`).FindAllString(hook, -1) {
		if l == `"`+helmv2.GroupVersion.Group+`"` {
			continue
		}
		if l != `"`+helmv2.HelmReleaseFinalizer+`"` {
			t.Errorf("hook template names %s, helm-controller's finalizer is %q", l, helmv2.HelmReleaseFinalizer)
		}
	}
}

package fluxplunger

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
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

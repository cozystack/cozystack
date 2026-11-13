//go:build linux

package main

import (
	"errors"
	"slices"
	"testing"

	"k8s.io/mount-utils"
	"k8s.io/utils/exec"
	testingexec "k8s.io/utils/exec/testing"
)

type cmdResult struct {
	out string
	err error
}

type execCall struct {
	cmd  string
	args []string
}

// scriptedExec answers each command with the next output and error, and
// records what was run.
func scriptedExec(calls *[]execCall, results ...cmdResult) *testingexec.FakeExec {
	fe := &testingexec.FakeExec{}
	for _, r := range results {
		fe.CommandScript = append(fe.CommandScript, func(cmd string, args ...string) exec.Cmd {
			*calls = append(*calls, execCall{cmd, args})
			return &testingexec.FakeCmd{CombinedOutputScript: []testingexec.FakeAction{
				func() ([]byte, []byte, error) { return []byte(r.out), nil, r.err },
			}}
		})
	}
	return fe
}

var blkidArgs = []string{"-p", "-s", "TYPE", "-s", "PTTYPE", "-o", "export", "/dev/vdb"}

func TestMountUtilsSafeFormatAndMountCommands(t *testing.T) {
	unformatted := &testingexec.FakeExitError{Status: 2}
	for _, tc := range []struct {
		name      string
		fstype    string
		options   []string
		results   []cmdResult
		want      []execCall
		wantErr   bool
		wantMount bool
	}{
		{
			name:      "unformatted ext4",
			fstype:    "ext4",
			results:   []cmdResult{{"", unformatted}, {"", nil}},
			want:      []execCall{{"blkid", blkidArgs}, {"mkfs.ext4", []string{"-F", "-m0", "/dev/vdb"}}},
			wantMount: true,
		},
		{
			name:      "unformatted defaults to ext4",
			results:   []cmdResult{{"", unformatted}, {"", nil}},
			want:      []execCall{{"blkid", blkidArgs}, {"mkfs.ext4", []string{"-F", "-m0", "/dev/vdb"}}},
			wantMount: true,
		},
		{
			name:      "unformatted xfs",
			fstype:    "xfs",
			results:   []cmdResult{{"", unformatted}, {"", nil}},
			want:      []execCall{{"blkid", blkidArgs}, {"mkfs.xfs", []string{"-f", "/dev/vdb"}}},
			wantMount: true,
		},
		{
			name:      "formatted ext4 is checked, not formatted",
			fstype:    "ext4",
			results:   []cmdResult{{"DEVNAME=/dev/vdb\nTYPE=ext4\n", nil}, {"", nil}},
			want:      []execCall{{"blkid", blkidArgs}, {"fsck", []string{"-a", "/dev/vdb"}}},
			wantMount: true,
		},
		{
			name:    "unformatted read-only is refused",
			fstype:  "ext4",
			options: []string{"ro"},
			results: []cmdResult{{"", unformatted}},
			want:    []execCall{{"blkid", blkidArgs}},
			wantErr: true,
		},
		{
			name:    "mkfs failure is reported",
			fstype:  "ext4",
			results: []cmdResult{{"", unformatted}, {"boom", errors.New("exit 1")}},
			want:    []execCall{{"blkid", blkidArgs}, {"mkfs.ext4", []string{"-F", "-m0", "/dev/vdb"}}},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []execCall
			fm := mount.NewFakeMounter(nil)
			m := &mount.SafeFormatAndMount{Interface: fm, Exec: scriptedExec(&calls, tc.results...)}

			err := m.FormatAndMount("/dev/vdb", "/target", tc.fstype, tc.options)
			if (err != nil) != tc.wantErr {
				t.Fatalf("FormatAndMount error = %v, wantErr %v", err, tc.wantErr)
			}
			if !slices.EqualFunc(calls, tc.want, func(a, b execCall) bool { return a.cmd == b.cmd && slices.Equal(a.args, b.args) }) {
				t.Errorf("commands = %v, want %v", calls, tc.want)
			}
			mps, _ := fm.List()
			if !tc.wantMount {
				if len(mps) != 0 {
					t.Errorf("mounted %v, want nothing", mps)
				}
				return
			}
			wantType := tc.fstype
			if wantType == "" {
				wantType = "ext4"
			}
			if len(mps) != 1 || mps[0].Device != "/dev/vdb" || mps[0].Path != "/target" || mps[0].Type != wantType || !slices.Contains(mps[0].Opts, "defaults") {
				t.Errorf("mount points = %+v, want /dev/vdb on /target type %s with defaults", mps, wantType)
			}
		})
	}
}

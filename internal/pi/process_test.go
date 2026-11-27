package pi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunPiLifecycle(t *testing.T) {
	const state = `{"type":"response","id":"get_state","command":"get_state","success":true,"data":{"model":{"provider":"test","id":"fixture"}}}`
	const accepted = `{"type":"response","id":"prompt","command":"prompt","success":true,"data":{"disposition":"started"}}`
	const settled = `{"type":"agent_settled","aborted":false}`
	const ended = `{"type":"agent_end"}`
	for _, tc := range []struct {
		name, state  string
		promptEvents []string
		want         string
	}{
		{"success", state, []string{accepted, ended, settled}, ""},
		{"early settlement", state, []string{ended, settled, accepted}, ""},
		{"early exit", state, []string{accepted, ended}, "Pi exited before the step ended"},
		{"provider failure", state, []string{accepted, `{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"private provider error"}}`, settled}, "Pi reported a model error"},
		{"abort", state, []string{accepted, `{"type":"agent_settled","aborted":true}`}, "Pi aborted the step"},
		{"wrong model", strings.Replace(state, `"fixture"`, `"other"`, 1), nil, "Pi did not select the configured provider and model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			executable := filepath.Join(dir, "fake-pi")
			lines := append([]string{tc.state, `{"type":"response","id":"set_auto_retry","command":"set_auto_retry","success":true}`}, tc.promptEvents...)
			script := `#!/bin/sh
read -r line && printf '%s\n' '` + lines[0] + `'
read -r line && printf '%s\n' '` + lines[1] + `'
read -r line || exit 0
`
			for _, line := range lines[2:] {
				script += "printf '%s\\n' '" + line + "'\n"
			}
			if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err := runPi(ctx, executable, dir, "extension.ts", "msg.sock", stepRequest{Provider: "test", Model: "fixture", Prompt: "test"})
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || err.Error() != tc.want) {
				t.Errorf("got %v, want %q", err, tc.want)
			}
		})
	}
}

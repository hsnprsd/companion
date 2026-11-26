package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func fileArgs(a Action) json.RawMessage {
	data, _ := json.Marshal(map[string]string{"path": a.Path, "content": a.Content})
	return data
}

func TestWriteApprovalAcrossRestart(t *testing.T) {
	action := Action{Tool: "write_file", Path: "draft 📝.txt", Content: "first line\n exact spaces  \n<last>\r\n"}
	var checks, writes, runs int
	f := fakeFiles{
		check: func(a Action, _ map[string]bool) (bool, error) {
			checks++
			if a != action {
				t.Errorf("checked %+v, want %+v", a, action)
			}
			return false, nil // The engine must still require approval.
		},
		execute: func(a Action) (string, error) {
			writes++
			if a != action {
				t.Errorf("executed %+v, want %+v", a, action)
			}
			return "Exact write finished.", nil
		},
	}
	r := runnerFunc(func(ctx context.Context, prompt string, call callback) (string, error) {
		runs++
		if runs == 1 {
			msg, err := call(ctx, action.Tool, fileArgs(action))
			if err != nil || !strings.Contains(msg, actionJSON(action)) {
				t.Fatalf("proposal response = %q, %v", msg, err)
			}
			if _, err := call(ctx, action.Tool, fileArgs(action)); err == nil {
				t.Fatal("runner could propose two writes in the same step")
			}
			return "Wait for the draft decision.", nil
		}
		var input struct {
			Actions  []approval `json:"actions_and_decisions"`
			Progress string     `json:"previous_step"`
		}
		decodePrompt(t, prompt, &input)
		if len(input.Actions) != 1 || input.Actions[0].Action != action || input.Actions[0].Decision != approved || input.Actions[0].Outcome != applied || input.Actions[0].Detail != "Exact write finished." || input.Progress != "Wait for the draft decision." {
			t.Fatalf("resume input lost the exact action, decision, or progress: %+v", input)
		}
		return "Draft saved.", nil
	})
	dir := t.TempDir()
	e := openEngine(t, dir, r, f)
	acceptUpdate(t, e, 1, "/work Draft it")
	runJob(t, e)
	if s := stored(t, e); s.Tasks[1].Status != waiting || s.Approvals[1].Action != action || writes != 0 || checks != 1 {
		t.Fatalf("proposal was not durably paused: %+v", s)
	}
	closeEngine(t, e)
	e = openEngine(t, dir, r, f)
	acceptUpdate(t, e, 2, "/task 1")
	acceptUpdate(t, e, 3, "/approve 1 please")
	s := stored(t, e)
	preview := proposalText(s.Approvals[1])
	if jobFor(t, e) != nil || strings.Count(outboxText(s), preview) != 2 || messagePart(preview) != preview || !strings.Contains(preview, actionJSON(action)) {
		t.Fatalf("bad approval command or preview: %s", outboxText(s))
	}
	acceptUpdate(t, e, 4, "/approve 1")
	acceptUpdate(t, e, 4, "/deny 1") // duplicate update
	closeEngine(t, e)
	e = openEngine(t, dir, r, f)
	job := jobFor(t, e)
	if s := stored(t, e); job == nil || s.Tasks[1].Status != applying || s.Approvals[1].Decision != approved || writes != 0 || checks != 2 {
		t.Fatalf("execution did not save intent and recheck access: %+v", s)
	}
	if err := job(); err != nil {
		t.Fatal(err)
	}
	closeEngine(t, e)
	e = openEngine(t, dir, r, f)
	runJob(t, e)
	acceptUpdate(t, e, 5, "/approve 1")
	if s := stored(t, e); s.Tasks[1].Status != complete || writes != 1 || !strings.Contains(outboxText(s), "Draft saved.") || jobFor(t, e) != nil {
		t.Fatalf("unexpected final task state: %+v, writes %d", s, writes)
	}
}

func TestChangeInvalidatesApprovalAndDenialResumes(t *testing.T) {
	first := Action{Tool: "write_file", Path: "old.txt", Content: "old"}
	second := Action{Tool: "write_file", Path: "new.txt", Content: "new\n"}
	runs := 0
	r := runnerFunc(func(ctx context.Context, prompt string, call callback) (string, error) {
		runs++
		switch runs {
		case 1:
			return call(ctx, first.Tool, fileArgs(first))
		case 2:
			big := Action{Tool: "write_file", Path: "big.txt", Content: strings.Repeat("📝", 2048)}
			if _, err := call(ctx, big.Tool, fileArgs(big)); err == nil {
				t.Error("accepted a proposal that cannot fit in one Telegram message")
			}
			return call(ctx, second.Tool, fileArgs(second))
		case 3:
			var input struct {
				Request string     `json:"user_request"`
				Actions []approval `json:"actions_and_decisions"`
			}
			decodePrompt(t, prompt, &input)
			if input.Request != "new instructions" || len(input.Actions) != 2 || input.Actions[0].Decision != void || input.Actions[0].Action != first || input.Actions[1].Decision != denied || input.Actions[1].Action != second {
				t.Fatalf("unexpected next-step input: %+v", input)
			}
		}
		return "No files changed.", nil
	})
	e := openEngine(t, t.TempDir(), r, fakeFiles{execute: func(a Action) (string, error) {
		t.Errorf("executed an unapproved action: %+v", a)
		return "", nil
	}})
	acceptUpdate(t, e, 1, "/work old instructions")
	runJob(t, e)
	acceptUpdate(t, e, 2, "/change 1 new instructions")
	acceptUpdate(t, e, 3, "/approve 1")
	runJob(t, e)
	acceptUpdate(t, e, 4, "/deny 2")
	runJob(t, e)
	if s := stored(t, e); s.Tasks[1].Status != complete || len(s.Approvals) != 2 || s.Approvals[2].Outcome != notRun {
		t.Fatalf("unexpected final state: %+v", s)
	}
}

func TestGrantRevokeDuringRead(t *testing.T) {
	started := make(chan struct{})
	unblock := make(chan struct{})
	results := make(chan error, 1)
	finished := make(chan string, 1)
	f := fakeFiles{
		aliases: []string{"archive"},
		check: func(a Action, grants map[string]bool) (bool, error) {
			if a.Path == "@archive/document" && !grants["archive"] {
				t.Error("broker did not receive the grant")
			}
			grants["bogus"] = true // Broker cannot mutate the persisted grants.
			return false, nil
		},
		execute: func(a Action) (string, error) {
			if a.Path == "@archive/document" {
				close(started)
				<-unblock
			}
			return a.Path, nil
		},
	}
	r := runnerFunc(func(ctx context.Context, _ string, call callback) (string, error) {
		for _, a := range []Action{
			{Tool: "read_file", Path: "workspace.txt"},
			{Tool: "list_files", Path: "."},
			{Tool: "read_file", Path: "@archive/document"},
		} {
			if result, err := call(ctx, a.Tool, fileArgs(a)); err != nil || result != a.Path {
				results <- fmt.Errorf("execution of %+v: %q, %v", a, result, err)
				return "", err
			}
		}
		for _, a := range []Action{
			{Tool: "read_file", Path: "@archive/document"},
			{Tool: "read_file", Path: "@bogus/document"},
			{Tool: "write_file", Path: "@archive/document", Content: "never"},
			{Tool: "shell", Path: "."},
		} {
			if _, err := call(ctx, a.Tool, fileArgs(a)); err == nil {
				results <- fmt.Errorf("executed forbidden action %+v", a)
				return "", nil
			}
		}
		results <- nil
		return "Read finished.", nil
	})
	e := openEngine(t, t.TempDir(), r, f)
	acceptUpdate(t, e, 1, "/grant bogus")
	acceptUpdate(t, e, 2, "/grant archive")
	acceptUpdate(t, e, 3, "/work Read files")
	stopWork := startLoop(t, e.Work)
	defer func() {
		select {
		case <-unblock:
		default:
			close(unblock)
		}
	}()
	startLoop(t, func(ctx context.Context) error {
		return e.Deliver(ctx, func(_ context.Context, msg string) error {
			if strings.Contains(msg, "Read finished.") || strings.Contains(msg, "Revoked") {
				finished <- msg
			}
			return nil
		})
	})
	receive(t, started)
	acceptUpdate(t, e, 4, "/revoke archive")
	if got := receive(t, finished); !strings.Contains(got, "Revoked access") {
		t.Fatalf("revoke was not reported while read was in flight: %q", got)
	}
	close(unblock)
	if err := receive(t, results); err != nil {
		t.Fatal(err)
	}
	if got := receive(t, finished); !strings.Contains(got, "Read finished.") {
		t.Fatalf("unexpected result: %q", got)
	}
	stopWork()
	if s := stored(t, e); len(s.Grants) != 0 || len(s.Approvals) != 0 {
		t.Fatalf("unexpected access state: %+v", s)
	}
}

func TestRecoveryChecksWriteWithoutReplaying(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		applied    bool
		known      bool
		err        error
	}{
		{name: "applied", want: applied, applied: true, known: true},
		{name: "not applied", want: absent, known: true},
		{name: "unknown", want: uncertain},
		{name: "check failed", want: uncertain, err: errors.New("cannot read file")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			action := Action{Tool: "write_file", Path: "result.txt", Content: "exact\n"}
			calls, checks := 0, 0
			r := runnerFunc(func(ctx context.Context, prompt string, call callback) (string, error) {
				calls++
				if calls == 1 {
					return call(ctx, action.Tool, fileArgs(action))
				}
				var input struct {
					Actions []approval `json:"actions_and_decisions"`
				}
				decodePrompt(t, prompt, &input)
				if len(input.Actions) != 1 || input.Actions[0].Action != action || input.Actions[0].Outcome != tc.want {
					t.Fatalf("recovery input: %+v", input)
				}
				return "Result checked.", nil
			})
			f := fakeFiles{
				execute: func(Action) (string, error) {
					t.Fatal("interrupted write was replayed")
					return "", nil
				},
				outcome: func(a Action) (bool, bool, error) {
					checks++
					if a != action {
						t.Fatalf("checked a different action: %+v", a)
					}
					return tc.applied, tc.known, tc.err
				},
			}
			dir := t.TempDir()
			e := openEngine(t, dir, r, f)
			acceptUpdate(t, e, 1, "/work write result")
			runJob(t, e)
			acceptUpdate(t, e, 2, "/approve 1")
			if jobFor(t, e) == nil || stored(t, e).Tasks[1].Status != applying {
				t.Fatal("interrupted write intent was not persisted")
			}
			closeEngine(t, e) // Leave the intended write incomplete in state.
			e = openEngine(t, dir, r, f)
			runJob(t, e)
			s := stored(t, e)
			if s.Approvals[1].Outcome != tc.want || checks != 1 {
				t.Fatalf("write recovery: %+v, %d checks", s.Approvals[1], checks)
			}
			if tc.want == uncertain {
				acceptUpdate(t, e, 3, "/retry 1")
				acceptUpdate(t, e, 4, "/change 1 try again")
				if s := stored(t, e); s.Tasks[1].Status != blocked || !strings.Contains(outboxText(s), "will not repeat it") || jobFor(t, e) != nil {
					t.Fatal("uncertain write can resume or was not reported")
				}
			} else {
				runJob(t, e)
				if jobFor(t, e) != nil || checks != 1 {
					t.Fatal("resolved write was scheduled again")
				}
			}
		})
	}
}

func TestRecheckAtExecutionAndReportLiveWriteError(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(fmt.Sprint("check rejected: ", reject), func(t *testing.T) {
			checks, executions, outcomes := 0, 0, 0
			action := Action{Tool: "write_file", Path: "file", Content: "new"}
			f := fakeFiles{
				check: func(Action, map[string]bool) (bool, error) {
					checks++
					if checks == 2 && reject {
						return false, errors.New("path no longer permitted")
					}
					return true, nil
				},
				execute: func(Action) (string, error) {
					executions++
					return "", errors.New("write failed")
				},
				outcome: func(Action) (bool, bool, error) {
					outcomes++
					return false, false, nil
				},
			}
			r := runnerFunc(func(ctx context.Context, _ string, call callback) (string, error) {
				return call(ctx, action.Tool, fileArgs(action))
			})
			e := openEngine(t, t.TempDir(), r, f)
			acceptUpdate(t, e, 1, "/work write it")
			runJob(t, e)
			acceptUpdate(t, e, 2, "/approve 1")
			if reject {
				if jobFor(t, e) != nil || stored(t, e).Approvals[1].Outcome != notRun || executions != 0 || outcomes != 0 {
					t.Fatal("approved write bypassed the access recheck")
				}
			} else {
				runJob(t, e)
				if s := stored(t, e); s.Approvals[1].Outcome != uncertain || s.Tasks[1].Status != blocked || executions != 1 || outcomes != 1 {
					t.Fatal("write error was not checked and recorded as uncertain")
				}
			}
		})
	}
}

func TestWorkShutdownRecoveryAndModelTimeout(t *testing.T) {
	t.Run("shutdown", func(t *testing.T) {
		started := make(chan struct{})
		r := runnerFunc(func(ctx context.Context, _ string, _ callback) (string, error) {
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		})
		dir := t.TempDir()
		e := openEngine(t, dir, r, fakeFiles{})
		acceptUpdate(t, e, 1, "/work run")
		stop := startLoop(t, e.Work)
		receive(t, started)
		if e.Close() == nil {
			t.Fatal("released the lock during Work")
		}
		stop()
		if s := stored(t, e); s.Tasks[1].Status != ready {
			t.Fatalf("shutdown task status = %q, want ready", s.Tasks[1].Status)
		}
		closeEngine(t, e)
		e = openEngine(t, dir, echoRunner, fakeFiles{})
		runJob(t, e)
	})
	t.Run("interrupted snapshot", func(t *testing.T) {
		dir := t.TempDir()
		e := openEngine(t, dir, echoRunner, fakeFiles{})
		acceptUpdate(t, e, 1, "/work run")
		if jobFor(t, e) == nil || stored(t, e).Tasks[1].Status != running {
			t.Fatal("model start was not persisted")
		}
		e.active.cancel()
		closeEngine(t, e)
		e = openEngine(t, dir, echoRunner, fakeFiles{})
		runJob(t, e)
	})
	t.Run("step timeout and retry", func(t *testing.T) {
		r := runnerFunc(func(ctx context.Context, _ string, _ callback) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		})
		dir := t.TempDir()
		e := openEngine(t, dir, r, fakeFiles{})
		e.stepTimeout = time.Millisecond
		acceptUpdate(t, e, 1, "/work run")
		runJob(t, e)
		if s := stored(t, e); s.Tasks[1].Status != blocked || !strings.Contains(s.Tasks[1].Problem, "time limit") || jobFor(t, e) != nil {
			t.Fatalf("timed out step did not block: %+v", s.Tasks[1])
		}
		closeEngine(t, e)
		e = openEngine(t, dir, echoRunner, fakeFiles{})
		acceptUpdate(t, e, 2, "/retry 1")
		runJob(t, e)
		if s := stored(t, e); s.Tasks[1].Status != complete {
			t.Fatalf("retry did not finish: %+v", s.Tasks[1])
		}
	})
}

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

func decodeAction(tool string, args json.RawMessage) (Action, error) {
	switch tool {
	case "read_file", "list_files", "write_file":
	default:
		return Action{}, fmt.Errorf("unknown dynamic tool %q", tool)
	}
	if !utf8.Valid(args) {
		return Action{}, errors.New("tool arguments must use UTF-8")
	}
	var input struct {
		Path    *string `json:"path"`
		Content *string `json:"content"`
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		return Action{}, fmt.Errorf("invalid tool arguments: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return Action{}, errors.New("provide exactly one tool arguments object")
	}
	if input.Path == nil || *input.Path == "" {
		return Action{}, errors.New("provide a path")
	}
	a := Action{Tool: tool, Path: *input.Path}
	if input.Content != nil {
		a.Content = *input.Content
	}
	if tool == "write_file" && input.Content == nil {
		return Action{}, errors.New("provide the exact content to write, or an empty string for an empty file")
	}
	if tool != "write_file" && a.Content != "" {
		return Action{}, errors.New("content is only used by write_file")
	}
	return a, nil
}

// check is called under the state lock immediately before starting a tool.
// A revoke after this point does not interrupt an in-flight read.
func (e *Engine) check(a Action) (bool, error) {
	grants := make(map[string]bool)
	for alias := range e.aliases {
		if e.state.Grants[alias] {
			grants[alias] = true
		}
	}
	if strings.HasPrefix(a.Path, "@") {
		alias, _, _ := strings.Cut(a.Path[1:], "/")
		if !validAlias(alias) {
			return false, errors.New("use @alias/path for external files")
		}
		if a.Tool == "write_file" {
			return false, errors.New("external aliases are read-only")
		}
		if !grants[alias] {
			return false, fmt.Errorf("access to @%s/ is not granted", alias)
		}
	}
	needsApproval, err := e.files.Check(a, grants)
	return a.Tool == "write_file" || needsApproval, err
}

func (e *Engine) tool(ctx, stepCtx context.Context, step *activeStep, tool string, args json.RawMessage) (string, error) {
	a, err := decodeAction(tool, args)
	if err != nil {
		return "", err
	}
	step.toolMu.Lock()
	defer step.toolMu.Unlock()
	start, output, err := e.startTool(ctx, stepCtx, step, a)
	if start {
		return e.files.Execute(a)
	}
	return output, err
}

func (e *Engine) startTool(ctx, stepCtx context.Context, step *activeStep, a Action) (start bool, output string, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failed != nil {
		return false, "", e.failed
	}
	if err := stepCtx.Err(); err != nil {
		return false, "", err
	}
	if err := ctx.Err(); err != nil {
		return false, "", err
	}
	t := e.state.Tasks[step.id]
	if e.active != step || t.Revision != step.revision || t.Status != running {
		return false, "", errors.New("this step ended or is paused for approval")
	}
	needsApproval, err := e.check(a)
	if err != nil {
		return false, "", err
	}
	if !needsApproval {
		return true, "", nil
	}
	proposal := &approval{ID: e.state.ActionSeq + 1, TaskID: t.ID, Action: a, Decision: pending}
	message := proposalText(proposal)
	if units(message) > telegramLimit {
		return false, "", errors.New("the exact action is too large for a Telegram approval message; propose a smaller action")
	}
	if err := e.commit(func(s *state) {
		s.ActionSeq = proposal.ID
		s.Approvals[proposal.ID] = proposal
		t.ActionIDs = append(t.ActionIDs, proposal.ID)
		t.LastActionID = proposal.ID
		t.Status = waiting
		s.say(message)
	}); err != nil {
		return false, "", err
	}
	return false, fmt.Sprintf("Action %d was proposed, not executed. End this step and wait for the user's decision. Exact action: %s", proposal.ID, actionJSON(a)), nil
}

func actionJSON(a Action) string {
	data, _ := json.Marshal(a)
	return string(data)
}

func proposalText(a *approval) string {
	return fmt.Sprintf("Task %d requests approval for action %d.\nExact action (JSON):\n%s\n\nTo approve this action only, send /approve %d\nTo deny it, send /deny %d", a.TaskID, a.ID, actionJSON(a.Action), a.ID, a.ID)
}

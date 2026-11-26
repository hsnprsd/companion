package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"companion/internal/engine"
)

func TestFilePolicyAndRecovery(t *testing.T) {
	root := t.TempDir()
	workspace, mounts := filepath.Join(root, "workspace"), filepath.Join(root, "mounts")
	for _, dir := range []string{workspace, filepath.Join(mounts, "allowed"), filepath.Join(mounts, "other")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"secret": "private value", "mounts/allowed/data": "granted data", "mounts/other/data": "not granted",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		"workspace/escape": "../secret", "workspace/elsewhere": "../mounts/allowed", "mounts/allowed/escape": "../other/data",
		"mounts/fake": "other",
	} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	files, err := Open(workspace, mounts)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	if got := files.Aliases(); !reflect.DeepEqual(got, []string{"allowed", "other"}) {
		t.Fatalf("aliases: %v", got)
	}
	for _, name := range []string{"../secret", "/etc/passwd", "a/../b", `a\b`, ".companion-write-manual", "@other/data"} {
		if _, err := files.Check(engine.Action{Tool: "read_file", Path: name}, nil); err == nil {
			t.Errorf("unexpected permission for %q", name)
		}
	}
	for _, action := range []engine.Action{
		{Tool: "read_file", Path: "escape"}, {Tool: "list_files", Path: "elsewhere"},
		{Tool: "write_file", Path: "elsewhere/future", Content: "out"}, {Tool: "read_file", Path: "@allowed/escape"},
		{Tool: "read_file", Path: "@fake/data"}, {Tool: "write_file", Path: "@allowed/data", Content: "outside write"},
	} {
		if _, err := files.Execute(action); err == nil {
			t.Errorf("unexpected successful action: %+v", action)
		}
	}
	readGranted := engine.Action{Tool: "read_file", Path: "@allowed/data"}
	if needs, err := files.Check(readGranted, map[string]bool{"allowed": true}); err != nil || needs {
		t.Fatalf("granted read: %t, %v", needs, err)
	}
	if body, err := files.Execute(readGranted); err != nil || body != "granted data" {
		t.Fatalf("granted file: %q, %v", body, err)
	}
	if _, err := files.Check(readGranted, map[string]bool{"allowed": false}); err == nil {
		t.Fatal("revoked read permitted")
	}
	write := engine.Action{Tool: "write_file", Path: "result.txt", Content: "approved content"}
	if needs, err := files.Check(write, nil); !needs || err != nil {
		t.Fatalf("write permission: %t, %v", needs, err)
	}
	if _, err := os.Stat(filepath.Join(workspace, write.Path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("permission check changed the workspace")
	}
	if applied, known, err := files.CheckOutcome(write); applied || known || err != nil {
		t.Fatalf("missing outcome: %t %t %v", applied, known, err)
	}
	if _, err := files.Execute(write); err != nil {
		t.Fatal(err)
	}
	if applied, known, err := files.CheckOutcome(write); !applied || !known || err != nil {
		t.Fatalf("verified outcome: %t %t %v", applied, known, err)
	}
	write.Content = "different value!"
	if applied, known, err := files.CheckOutcome(write); applied || known || err != nil {
		t.Fatalf("different outcome: %t %t %v", applied, known, err)
	}
	contents, err := files.Execute(engine.Action{Tool: "list_files", Path: "."})
	if err != nil || !strings.Contains(contents, `"result.txt"`) || strings.Contains(contents, stagePrefix) {
		t.Fatalf("list: %q %v", contents, err)
	}
}

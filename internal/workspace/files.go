// Package workspace applies file permissions independently of Codex.
package workspace

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"companion/internal/engine"
)

const (
	maxRead     = 32 << 10
	maxWrite    = 1600
	maxPath     = 300
	maxEntries  = 100
	stagePrefix = ".companion-write-"
)

var aliasPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

type Files struct {
	workspace *os.Root
	external  *os.Root
}

func Open(workspace, external string) (*Files, error) {
	if err := os.MkdirAll(workspace, 0700); err != nil {
		return nil, fmt.Errorf("create workspace: %w", err)
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, fmt.Errorf("open workspace: %w", err)
	}
	f := &Files{workspace: root}
	if external != "" {
		f.external, err = os.OpenRoot(external)
		if err != nil {
			root.Close()
			return nil, fmt.Errorf("open external mount directory: %w", err)
		}
	}
	return f, nil
}

func (f *Files) Close() error {
	if f.external != nil {
		return errors.Join(f.external.Close(), f.workspace.Close())
	}
	return f.workspace.Close()
}

func (f *Files) Aliases() []string {
	if f.external == nil {
		return nil
	}
	entries, err := fs.ReadDir(f.external.FS(), ".")
	if err != nil {
		return nil
	}
	var aliases []string
	for _, entry := range entries {
		if entry.IsDir() && aliasPattern.MatchString(entry.Name()) {
			aliases = append(aliases, entry.Name())
		}
	}
	return aliases
}

type location struct {
	alias string
	path  string
}

func parse(action engine.Action) (location, error) {
	p := action.Path
	if !utf8.ValidString(p) || len(p) == 0 || len(p) > maxPath ||
		strings.ContainsRune(p, '\\') || strings.ContainsFunc(p, unicode.IsControl) {
		return location{}, errors.New("invalid file path")
	}
	var loc location
	if strings.HasPrefix(p, "@") {
		var hasPath bool
		loc.alias, p, hasPath = strings.Cut(p[1:], "/")
		if !aliasPattern.MatchString(loc.alias) {
			return location{}, errors.New("invalid external mount name")
		}
		if !hasPath {
			p = "."
		}
	}
	if path.IsAbs(p) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") {
		return location{}, errors.New("use a clean relative path without traversal")
	}
	for part := range strings.SplitSeq(p, "/") {
		if strings.HasPrefix(part, stagePrefix) {
			return location{}, errors.New("reserved file path")
		}
	}
	loc.path = p

	switch action.Tool {
	case "read_file", "list_files":
		if action.Content != "" {
			return location{}, errors.New("read and list do not accept file content")
		}
	case "write_file":
		if loc.alias != "" {
			return location{}, errors.New("external mounts are read-only")
		}
		if p == "." || len(action.Content) > maxWrite || !utf8.ValidString(action.Content) || strings.ContainsRune(action.Content, 0) {
			return location{}, errors.New("write requires a file path and at most 1600 bytes of UTF-8 text without NUL characters")
		}
	default:
		return location{}, errors.New("file tool is not permitted")
	}
	return loc, nil
}

func (f *Files) open(loc location) (*os.Root, func(), error) {
	if loc.alias == "" {
		return f.workspace, func() {}, nil
	}
	if f.external == nil {
		return nil, nil, errors.New("no external mounts are configured")
	}
	info, err := f.external.Lstat(loc.alias)
	if err != nil || !info.IsDir() {
		return nil, nil, errors.New("external mount does not exist or is not a directory")
	}
	root, err := f.external.OpenRoot(loc.alias)
	if err != nil {
		return nil, nil, err
	}
	return root, func() { root.Close() }, nil
}

// Check verifies the current grant and identifies actions that need approval.
func (f *Files) Check(action engine.Action, grants map[string]bool) (bool, error) {
	loc, err := parse(action)
	if err != nil {
		return false, err
	}
	if loc.alias != "" && !grants[loc.alias] {
		return false, fmt.Errorf("grant for external mount %q is required", loc.alias)
	}
	root, closeRoot, err := f.open(loc)
	if err != nil {
		return false, err
	}
	defer closeRoot()
	if action.Tool == "write_file" {
		dir, err := root.OpenRoot(path.Dir(loc.path))
		if err != nil {
			return false, err
		}
		defer dir.Close()
		return true, checkTarget(dir, path.Base(loc.path))
	}
	return false, nil
}

// Execute runs an action after Engine has checked grants and, for a write,
// recorded the user's decision. It repeats the path and tool validation.
func (f *Files) Execute(action engine.Action) (string, error) {
	loc, err := parse(action)
	if err != nil {
		return "", err
	}
	root, closeRoot, err := f.open(loc)
	if err != nil {
		return "", err
	}
	defer closeRoot()
	switch action.Tool {
	case "read_file":
		data, err := readText(root, loc.path, maxRead)
		return string(data), err
	case "list_files":
		return list(root, loc.path)
	case "write_file":
		if err := writeAtomic(root, loc.path, action.Content); err != nil {
			return "", err
		}
		return fmt.Sprintf("Saved %d bytes to %s.", len(action.Content), action.Path), nil
	}
	panic("validated file tool has no implementation")
}

// CheckOutcome verifies the desired effect rather than replaying the write.
// A missing or changed file cannot establish that the write did not occur.
func (f *Files) CheckOutcome(action engine.Action) (applied, known bool, err error) {
	loc, err := parse(action)
	if err != nil {
		return false, false, err
	}
	if action.Tool != "write_file" {
		return false, false, errors.New("only write outcomes can be checked")
	}
	info, err := f.workspace.Lstat(loc.path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if !info.Mode().IsRegular() || info.Size() != int64(len(action.Content)) {
		return false, false, nil
	}
	data, err := readText(f.workspace, loc.path, maxWrite)
	if err != nil {
		return false, false, err
	}
	matches := string(data) == action.Content
	return matches, matches, nil
}

func readText(root *os.Root, name string, limit int64) ([]byte, error) {
	info, err := root.Stat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("path is not a regular file")
	}
	// Do not wait on a FIFO if an administrator changes an external mount
	// between the metadata check and open.
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("path is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit || !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return nil, errors.New("file exceeds the size limit or is not UTF-8 text")
	}
	return data, nil
}

func list(root *os.Root, name string) (string, error) {
	dir, err := root.OpenRoot(name)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	file, err := dir.Open(".")
	if err != nil {
		return "", err
	}
	defer file.Close()
	entries, err := file.ReadDir(maxEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	names := make([]string, 0, min(maxEntries, len(entries)))
	for _, entry := range entries[:min(maxEntries, len(entries))] {
		if !strings.HasPrefix(entry.Name(), stagePrefix) {
			name := entry.Name()
			if entry.IsDir() {
				name += "/"
			}
			names = append(names, name)
		}
	}
	sort.Strings(names)
	data, err := json.Marshal(struct {
		Names     []string `json:"names"`
		Truncated bool     `json:"truncated"`
	}{names, len(entries) > maxEntries})
	return string(data), err
}

func checkTarget(dir *os.Root, name string) error {
	info, err := dir.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("will not replace a directory, symlink, or special file")
	}
	return nil
}

func writeAtomic(root *os.Root, name, content string) error {
	dir, err := root.OpenRoot(path.Dir(name))
	if err != nil {
		return err
	}
	defer dir.Close()
	name = path.Base(name)
	if err := checkTarget(dir, name); err != nil {
		return err
	}
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	temp := stagePrefix + hex.EncodeToString(suffix[:])
	file, err := dir.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer dir.Remove(temp)
	if _, err = file.WriteString(content); err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		return err
	}
	if err := dir.Rename(temp, name); err != nil {
		return err
	}
	parent, err := dir.Open(".")
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}

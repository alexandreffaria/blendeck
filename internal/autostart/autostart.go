// Package autostart turns a per-user "start when I sign in" entry on and off:
// on Windows a value under HKCU\Software\Microsoft\Windows\CurrentVersion\Run.
// It needs no admin rights, and the program starts in the user's session,
// where it can open MIDI devices (a service couldn't).
package autostart

import (
	"errors"
	"fmt"
	"strings"
)

// ErrUnsupported is returned by Set where there is no entry to set: other
// systems, or a binary that `go run` will delete.
var ErrUnsupported = errors.New("starting with the system isn't supported here")

// ErrTaken is returned when the sign-in entry's name already starts another
// program (another app of that name): it is left as it is.
var ErrTaken = errors.New("another program's sign-in entry already has this name")

// RunKey is where Windows looks for programs to start at sign-in.
const RunKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// Entry is one program's sign-in entry.
type Entry struct {
	name    string
	exe     string
	args    string // the command after the exe
	command string
	key     string
}

// New makes the entry name that starts exe with args. An empty exe makes an
// entry that is not Supported.
func New(name, exe string, args ...string) *Entry {
	e := &Entry{name: name, exe: exe, key: RunKey}
	if exe != "" {
		quoted := make([]string, len(args))
		for i, a := range args {
			quoted[i] = quote(a)
		}
		e.args = strings.Join(quoted, " ")
		e.command = strings.TrimSpace(quote(exe) + " " + e.args)
	}
	return e
}

// Command is what the entry runs.
func (e *Entry) Command() string { return e.command }

// Supported says whether Set can work.
func (e *Entry) Supported() bool { return supported && e.command != "" }

// Enabled says whether the entry exists and starts this program. An entry
// under the same name that starts another copy (say, one since moved) counts
// as off; turning it on points it here.
func (e *Entry) Enabled() (bool, error) {
	if !e.Supported() {
		return false, nil
	}
	v, err := e.readValue(e.name)
	if err != nil || v == "" {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(v), e.command), nil
}

// Set adds the entry (on) or removes it (off). Removing an entry that isn't
// there is fine. A value under the entry's name that starts another program
// is never changed: turning on returns ErrTaken, turning off leaves it.
func (e *Entry) Set(on bool) error {
	if !e.Supported() {
		return ErrUnsupported
	}
	v, err := e.readValue(e.name)
	if err != nil {
		return err
	}
	if v != "" && !e.owns(v) {
		if on {
			return fmt.Errorf("%w: %q starts %s", ErrTaken, e.name, v)
		}
		return nil
	}
	if on {
		return e.write()
	}
	return e.deleteValue(e.name)
}

// owns says whether a Run value is this program's: it starts this exe, or
// another copy of it (same file name, same arguments; say, one since moved).
func (e *Entry) owns(v string) bool {
	exe, args := commandExe(v), commandArgs(v)
	if strings.EqualFold(exe, e.exe) {
		return true
	}
	return strings.EqualFold(baseName(exe), baseName(e.exe)) && args == e.args
}

// Migrate moves an entry the program left under an older name (before a
// rename) to this one, so it keeps starting at sign-in under one name, not
// two or none. The old entry moves only when it starts this exe or one named
// like oldExes (say, the old program's file, maybe in another folder); an
// entry of that name for some other program is left alone, and so is
// everything when Set can't work. It says whether it moved one.
func (e *Entry) Migrate(oldName string, oldExes ...string) (bool, error) {
	if !e.Supported() || oldName == "" || strings.EqualFold(oldName, e.name) {
		return false, nil
	}
	v, err := e.readValue(oldName)
	if err != nil || v == "" {
		return false, err
	}
	exe := commandExe(v)
	ours := strings.EqualFold(exe, e.exe)
	for _, o := range oldExes {
		ours = ours || strings.EqualFold(baseName(exe), o)
	}
	if !ours {
		return false, nil
	}
	// The new name may belong to another program already: then neither
	// entry changes, and the caller says why.
	cur, err := e.readValue(e.name)
	if err != nil {
		return false, err
	}
	if cur != "" && !e.owns(cur) {
		return false, fmt.Errorf("%w: %q starts %s", ErrTaken, e.name, cur)
	}
	if err := e.write(); err != nil {
		return false, err
	}
	return true, e.deleteValue(oldName)
}

// commandExe is the program a Run value starts: the quoted first part, or
// everything up to the first space.
func commandExe(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if rest, ok := strings.CutPrefix(cmd, `"`); ok {
		exe, _, _ := strings.Cut(rest, `"`)
		return exe
	}
	exe, _, _ := strings.Cut(cmd, " ")
	return exe
}

// commandArgs is what a Run value passes after the program.
func commandArgs(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if rest, ok := strings.CutPrefix(cmd, `"`); ok {
		_, args, _ := strings.Cut(rest, `"`)
		return strings.TrimSpace(args)
	}
	_, args, _ := strings.Cut(cmd, " ")
	return strings.TrimSpace(args)
}

// baseName is a path's last part, for Windows paths on any system.
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// quote wraps s in double quotes when it has spaces, the way Windows command
// lines expect.
func quote(s string) string {
	if s == "" || strings.ContainsAny(s, " \t") {
		return `"` + s + `"`
	}
	return s
}

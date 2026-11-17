// Package autostart turns a per-user "start when I sign in" entry on and off:
// on Windows a value under HKCU\Software\Microsoft\Windows\CurrentVersion\Run.
// It needs no admin rights, and the program starts in the user's session,
// where it can open MIDI devices (a service couldn't).
package autostart

import (
	"errors"
	"strings"
)

// ErrUnsupported is returned by Set where there is no entry to set: other
// systems, or a binary that `go run` will delete.
var ErrUnsupported = errors.New("starting with the system isn't supported here")

// RunKey is where Windows looks for programs to start at sign-in.
const RunKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// Entry is one program's sign-in entry.
type Entry struct {
	name    string
	exe     string
	command string
	key     string
}

// New makes the entry name that starts exe with args. An empty exe makes an
// entry that is not Supported.
func New(name, exe string, args ...string) *Entry {
	e := &Entry{name: name, exe: exe, key: RunKey}
	if exe != "" {
		e.command = quote(exe)
		for _, a := range args {
			e.command += " " + quote(a)
		}
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
// there is fine.
func (e *Entry) Set(on bool) error {
	if !e.Supported() {
		return ErrUnsupported
	}
	if on {
		return e.write()
	}
	return e.deleteValue(e.name)
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
	ours := strings.EqualFold(exe, e.exe) || strings.EqualFold(baseName(exe), baseName(e.exe))
	for _, o := range oldExes {
		ours = ours || strings.EqualFold(baseName(exe), o)
	}
	if !ours {
		return false, nil
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

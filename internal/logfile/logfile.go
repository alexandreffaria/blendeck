// Package logfile is a log file with a size cap: when it would pass the cap
// it moves to <name>.1 (replacing the older copy) and starts over, so a
// bridge that runs for weeks keeps at most twice the cap on disk (three
// times while rotation fails: see Write).
package logfile

import (
	"os"
	"sync"
	"time"
)

// File is a capped, append-only log file. It is safe for concurrent use.
type File struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64

	now     func() time.Time
	retryAt time.Time // after a failed rotation, don't try again before this
}

// Open opens path for appending, rotating it first if it is already over max.
func Open(path string, max int64) (*File, error) {
	l := &File{path: path, max: max, now: time.Now}
	if err := l.open(); err != nil {
		return nil, err
	}
	if l.size >= max {
		if err := l.rotate(); err != nil && l.f == nil {
			return nil, err
		}
	}
	return l, nil
}

// Path is the file's name.
func (l *File) Path() string { return l.path }

func (l *File) open() error {
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, st.Size()
	return nil
}

// Swapped in tests to make rotation fail.
var (
	rename   = os.Rename
	truncate = os.Truncate
)

// retryEvery is how long a failed rotation waits before trying again.
const retryEvery = time.Minute

// rotate moves the file to .1 and starts a new one. If it can't move it
// (another program has one of them open?) it starts the file over instead;
// if it can't do that either it reopens the file as it was and returns the
// error. l.f is nil afterwards only when the file can't be opened at all.
func (l *File) rotate() error {
	l.f.Close()
	l.f = nil
	_ = os.Remove(l.path + ".1")
	var err error
	if rename(l.path, l.path+".1") != nil {
		err = truncate(l.path, 0)
	}
	if oerr := l.open(); oerr != nil {
		return oerr
	}
	return err
}

// Write appends p, rotating first if p would take the file past the cap.
// While rotation fails it retries once a minute, keeps logging up to twice
// the cap, and drops lines past that.
func (l *File) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return 0, os.ErrClosed
	}
	grow := int64(len(p))
	if l.size > 0 && l.size+grow > l.max {
		if now := l.now(); !now.Before(l.retryAt) {
			if err := l.rotate(); err != nil {
				if l.f == nil {
					return 0, err
				}
				l.retryAt = now.Add(retryEvery)
			}
		}
		if l.size > 0 && l.size+grow > 2*l.max {
			return len(p), nil // dropped: the hard cap
		}
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

// Close closes the file.
func (l *File) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

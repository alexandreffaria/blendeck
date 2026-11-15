// Package logfile is a log file with a size cap: when it would pass the cap
// it moves to <name>.1 (replacing the older copy) and starts over, so a
// bridge that runs for weeks keeps at most twice the cap on disk.
package logfile

import (
	"os"
	"sync"
)

// File is a capped, append-only log file. It is safe for concurrent use.
type File struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

// Open opens path for appending, rotating it first if it is already over max.
func Open(path string, max int64) (*File, error) {
	l := &File{path: path, max: max}
	if err := l.open(); err != nil {
		return nil, err
	}
	if l.size >= max {
		if err := l.rotate(); err != nil {
			l.f.Close()
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

func (l *File) rotate() error {
	l.f.Close()
	_ = os.Remove(l.path + ".1")
	if err := os.Rename(l.path, l.path+".1"); err != nil {
		// Can't move it (another program has it open?): start it over instead.
		if err := os.Truncate(l.path, 0); err != nil {
			return err
		}
	}
	return l.open()
}

// Write appends p, rotating first if p would take the file past the cap.
func (l *File) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return 0, os.ErrClosed
	}
	if l.size > 0 && l.size+int64(len(p)) > l.max {
		if err := l.rotate(); err != nil {
			l.f = nil
			return 0, err
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

//go:build !windows

package autostart

const supported = false

func (e *Entry) read() (string, error) { return "", nil }
func (e *Entry) write() error          { return ErrUnsupported }
func (e *Entry) remove() error         { return nil }

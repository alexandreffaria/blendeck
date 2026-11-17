//go:build !windows

package autostart

const supported = false

func (e *Entry) readValue(string) (string, error) { return "", nil }
func (e *Entry) write() error                     { return ErrUnsupported }
func (e *Entry) deleteValue(string) error         { return nil }

//go:build !windows

package tray

import (
	"fmt"
	"os"
)

type state struct{}

// Run returns ErrUnsupported: there is no tray outside Windows.
func (t *Tray) Run() error { return ErrUnsupported }

func postClose(uintptr) {}

// Alert prints text to stderr.
func Alert(text string) { fmt.Fprintln(os.Stderr, text) }

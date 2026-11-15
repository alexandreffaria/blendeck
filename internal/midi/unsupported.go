//go:build !windows

package midi

import "errors"

var errUnsupported = errors.New("MIDI input is only implemented on Windows so far")

// Inputs lists the names of the MIDI input devices.
func Inputs() ([]string, error) { return nil, errUnsupported }

func openInput(index int, onMsg func(Message)) (input, error) { return nil, errUnsupported }

package main

import (
	"testing"

	"github.com/alexandreffaria/blendeck/internal/app"
	"github.com/alexandreffaria/blendeck/internal/link"
	"github.com/alexandreffaria/blendeck/internal/midi"
)

func TestTooltip(t *testing.T) {
	cases := []struct {
		s    app.Snapshot
		want string
	}{
		{app.Snapshot{MIDI: midi.Status{State: "connected", Device: "Loupedeck+"}, Blender: link.State{Connected: true}},
			"Blendeck\nController: Loupedeck+\nBlender: connected"},
		{app.Snapshot{MIDI: midi.Status{State: "searching", Device: "Loupedeck+"}},
			"Blendeck\nController: looking for Loupedeck+\nBlender: not connected"},
		{app.Snapshot{MIDI: midi.Status{State: "searching"}},
			"Blendeck\nController: none picked\nBlender: not connected"},
		{app.Snapshot{MIDI: midi.Status{State: "error", Device: "X", Error: "busy"}},
			"Blendeck\nController: error\nBlender: not connected"},
	}
	for _, c := range cases {
		if got := tooltip(c.s); got != c.want {
			t.Errorf("tooltip = %q, want %q", got, c.want)
		}
	}
}

func TestLoopback(t *testing.T) {
	for addr, want := range map[string]bool{"127.0.0.1:7870": true, "localhost:1": true, "[::1]:2": true, "0.0.0.0:7870": false, "nope": false} {
		if isLoopback(addr) != want {
			t.Errorf("isLoopback(%q) != %v", addr, want)
		}
	}
}

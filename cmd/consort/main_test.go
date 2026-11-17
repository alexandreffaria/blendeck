package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alexandreffaria/consort/internal/app"
	"github.com/alexandreffaria/consort/internal/link"
	"github.com/alexandreffaria/consort/internal/midi"
)

func TestTooltip(t *testing.T) {
	cases := []struct {
		s    app.Snapshot
		want string
	}{
		{app.Snapshot{MIDI: midi.Status{State: "connected", Device: "Loupedeck+"}, Blender: link.State{Connected: true}},
			"Consort\nController: Loupedeck+\nBlender: connected"},
		{app.Snapshot{MIDI: midi.Status{State: "searching", Device: "Loupedeck+"}},
			"Consort\nController: looking for Loupedeck+\nBlender: not connected"},
		{app.Snapshot{MIDI: midi.Status{State: "searching"}},
			"Consort\nController: none picked\nBlender: not connected"},
		{app.Snapshot{MIDI: midi.Status{State: "error", Device: "X", Error: "busy"}},
			"Consort\nController: error\nBlender: not connected"},
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

func TestUserConfigPathKeepsTheOldConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "consort", "config.json")
	old := filepath.Join(dir, "blendeck", "config.json")
	if got := userConfigPath(dir); got != path {
		t.Fatalf("nothing yet: %s, want %s", got, path)
	}
	write := func(p string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(old)
	if got := userConfigPath(dir); got != old {
		t.Fatalf("only the old one: %s, want %s", got, old)
	}
	write(path)
	if got := userConfigPath(dir); got != path {
		t.Fatalf("both: %s, want %s", got, path)
	}
}

package app

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/alexandreffaria/blendeck/internal/config"
	"github.com/alexandreffaria/blendeck/internal/engine"
	"github.com/alexandreffaria/blendeck/internal/link"
	"github.com/alexandreffaria/blendeck/internal/midi"
)

func newTestApp(t *testing.T) *App {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := config.Open(filepath.Join(t.TempDir(), "c.json"))
	if err != nil {
		t.Fatal(err)
	}
	return New(log, store, midi.NewConn(log, "Loupedeck+"), link.New(log), func() ([]string, error) { return []string{"Loupedeck+"}, nil })
}

func TestLearnThenBind(t *testing.T) {
	a := newTestApp(t)
	a.Learn(engine.LearnBinding, nil)
	a.Handle(midi.Message{Kind: midi.ControlChange, Channel: 1, Number: 4, Value: 1})
	s := a.Snapshot()
	if s.Learning != "" || s.Learned.Seq != 1 || s.Learned.Control.Number != 4 {
		t.Fatalf("snapshot = %+v", s)
	}
	cfg := a.Config()
	cfg.Profiles[0].Bindings[0].Control = s.Learned.Control
	if err := a.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	a.Handle(midi.Message{Kind: midi.ControlChange, Channel: 1, Number: 4, Value: 1})
	evs := a.EventsSince(1)
	if len(evs) != 1 || evs[0].Hit.Command == nil || evs[0].Hit.Command.Frames != 10 {
		t.Fatalf("events = %+v", evs)
	}
	if evs[0].Delivered {
		t.Fatal("delivered with no Blender connected")
	}
}

func TestEventRing(t *testing.T) {
	a := newTestApp(t)
	for i := 0; i < EventLogSize+10; i++ {
		a.Handle(midi.Message{Kind: midi.NoteOn, Channel: 1, Number: 1, Value: 1})
	}
	all := a.EventsSince(0)
	if len(all) != EventLogSize || all[0].Seq != 11 || all[len(all)-1].Seq != EventLogSize+10 {
		t.Fatalf("len %d first %d last %d", len(all), all[0].Seq, all[len(all)-1].Seq)
	}
}

func TestSetConfigRejectsInvalid(t *testing.T) {
	a := newTestApp(t)
	cfg := a.Config()
	cfg.ActiveProfile = "missing"
	if err := a.SetConfig(cfg); err == nil {
		t.Fatal("expected error")
	}
}

func TestEventsSinceFutureSeqRestarts(t *testing.T) {
	a := newTestApp(t)
	a.Handle(midi.Message{Kind: midi.NoteOn, Channel: 1, Number: 1, Value: 1})
	if got := a.EventsSince(500); len(got) != 1 {
		t.Fatalf("got %d events for a seq from an earlier run", len(got))
	}
}

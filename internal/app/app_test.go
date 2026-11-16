package app

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

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

// hintApp is an app whose profile has Fn on note 110 and a knob on cc 42.
func hintApp(t *testing.T) *App {
	t.Helper()
	a := newTestApp(t)
	cfg := a.Config()
	p := &cfg.Profiles[0]
	p.Modifiers[0].Control.Number = 110
	p.Bindings[0].Control.Number = 42
	if err := a.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	return a
}

var (
	fnDown   = midi.Message{Kind: midi.NoteOn, Channel: 1, Number: 110, Value: 127}
	fnUp     = midi.Message{Kind: midi.NoteOff, Channel: 1, Number: 110}
	knob     = midi.Message{Kind: midi.ControlChange, Channel: 1, Number: 42, Value: 1}
	unbound  = midi.Message{Kind: midi.NoteOn, Channel: 1, Number: 7, Value: 127}
	pressure = midi.Message{Kind: midi.Aftertouch, Channel: 1, Value: 5}
)

// Holding Fn on its own for HintDelay shows the hints; letting go hides
// them (decision 202611167kkw).
func TestHintsShowWhileModifierHeldAlone(t *testing.T) {
	a := hintApp(t)
	a.Handle(fnDown)
	now := time.Now()
	a.hintTick(now.Add(HintDelay / 2))
	if a.link.HintsShown() {
		t.Fatal("shown before the delay")
	}
	a.Handle(pressure) // not a control: doesn't count as touching one
	a.hintTick(now.Add(HintDelay + time.Second))
	if !a.link.HintsShown() {
		t.Fatal("not shown after the delay")
	}
	a.Handle(fnUp)
	if a.link.HintsShown() {
		t.Fatal("still shown after the release")
	}
	a.hintTick(now.Add(time.Hour))
	if a.link.HintsShown() {
		t.Fatal("shown again with nothing held")
	}
}

// Fn on the way to a fine turn shows nothing, and using any control while
// the hints are up hides them until Fn is pressed again.
func TestHintsHideOnAnyOtherControl(t *testing.T) {
	for _, other := range []midi.Message{knob, unbound} {
		a := hintApp(t)
		a.Handle(fnDown)
		a.Handle(other)
		a.hintTick(time.Now().Add(time.Hour))
		if a.link.HintsShown() {
			t.Fatalf("shown after %+v was used", other)
		}
		a.Handle(fnUp)
		a.Handle(fnDown)
		a.hintTick(time.Now().Add(time.Hour))
		if !a.link.HintsShown() {
			t.Fatal("not shown on the next hold")
		}
		a.Handle(other)
		if a.link.HintsShown() {
			t.Fatalf("still shown after %+v was used", other)
		}
		a.hintTick(time.Now().Add(time.Hour))
		if a.link.HintsShown() {
			t.Fatal("shown again while the same hold goes on")
		}
	}
}

// The device going away hides them: the release will never come. And a
// modifier pressed while the page is learning a control shows nothing.
func TestHintsHideWhenDeviceGoesOrLearning(t *testing.T) {
	a := hintApp(t)
	a.Handle(fnDown)
	a.hintTick(time.Now().Add(time.Hour))
	a.hintOff()
	if a.link.HintsShown() {
		t.Fatal("still shown with the device gone")
	}
	a.hintTick(time.Now().Add(time.Hour))
	if a.link.HintsShown() {
		t.Fatal("shown again with the device gone")
	}

	a.Handle(fnUp)
	a.Learn(engine.LearnBinding, nil)
	a.Handle(fnDown)
	a.hintTick(time.Now().Add(time.Hour))
	if a.link.HintsShown() {
		t.Fatal("shown while learning")
	}

	// A learn that begins while Fn is already held, before the delay
	b := hintApp(t)
	b.Handle(fnDown)
	b.Learn(engine.LearnBinding, nil)
	b.hintTick(time.Now().Add(time.Hour))
	if b.link.HintsShown() {
		t.Fatal("shown for a learn that began during the wait")
	}
	b.Learn(engine.LearnOff, nil)
	b.hintTick(time.Now().Add(time.Hour))
	if b.link.HintsShown() {
		t.Fatal("shown once the learn ended, for the same hold")
	}
}

// "On its own": with two modifiers down nothing shows, whichever came
// first, until all are released and one is pressed again.
func TestHintsNotWithTwoModifiers(t *testing.T) {
	a := hintApp(t)
	cfg := a.Config()
	p := &cfg.Profiles[0]
	p.Modifiers = append(p.Modifiers, config.Modifier{Name: "Shift", Control: config.Control{Type: "note", Channel: 1, Number: 111}})
	if err := a.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	shiftDown := midi.Message{Kind: midi.NoteOn, Channel: 1, Number: 111, Value: 127}
	shiftUp := midi.Message{Kind: midi.NoteOff, Channel: 1, Number: 111}
	later := func() { a.hintTick(time.Now().Add(time.Hour)) }

	a.Handle(fnDown)
	later()
	if !a.link.HintsShown() {
		t.Fatal("not shown with Fn alone")
	}
	a.Handle(shiftDown)
	later()
	if a.link.HintsShown() {
		t.Fatal("shown with two modifiers held")
	}
	a.Handle(shiftUp)
	later()
	if a.link.HintsShown() {
		t.Fatal("shown again after one of the two was released")
	}
	a.Handle(fnUp)
	a.Handle(shiftDown)
	later()
	if !a.link.HintsShown() {
		t.Fatal("not shown with the other modifier alone")
	}
}

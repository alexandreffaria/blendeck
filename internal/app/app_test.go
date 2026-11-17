package app

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexandreffaria/consort/internal/config"
	"github.com/alexandreffaria/consort/internal/engine"
	"github.com/alexandreffaria/consort/internal/link"
	"github.com/alexandreffaria/consort/internal/midi"
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

// switchApp is hintApp with Switch app on note 52 and cut on note 41, its
// link serving on a port of its own, and Blender then nomnom connected.
// It returns the app and the two connections.
func switchApp(t *testing.T) (*App, net.Conn, net.Conn) {
	t.Helper()
	a := hintApp(t)
	cfg := a.Config()
	for i := range cfg.Profiles[0].Bindings {
		b := &cfg.Profiles[0].Bindings[i]
		switch b.ID {
		case "switch-app":
			b.Control.Number = 52
		case "cut":
			b.Control.Number = 41
		}
	}
	if err := a.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go a.link.Serve(ctx, ln)
	connect := func(hello string, apps int) net.Conn {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		fmt.Fprintln(c, hello)
		deadline := time.Now().Add(3 * time.Second)
		for len(a.link.State().Apps) != apps {
			if time.Now().After(deadline) {
				t.Fatalf("%s: not connected", hello)
			}
			time.Sleep(2 * time.Millisecond)
		}
		return c
	}
	blender := connect(`{"type":"hello","app":"blender"}`, 1)
	nomnom := connect(`{"type":"hello","app":"nomnom"}`, 2)
	return a, blender, nomnom
}

// cuts counts the cut commands c was sent within a moment.
func cuts(t *testing.T, c net.Conn) int {
	t.Helper()
	n := 0
	r := bufio.NewReader(c)
	for {
		c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		line, err := r.ReadString('\n')
		if err != nil {
			return n
		}
		if strings.HasPrefix(line, `{"type":"cut"`) {
			n++
		}
	}
}

var (
	switchDown = midi.Message{Kind: midi.NoteOn, Channel: 1, Number: 52, Value: 127}
	switchUp   = midi.Message{Kind: midi.NoteOff, Channel: 1, Number: 52}
	cutDown    = midi.Message{Kind: midi.NoteOn, Channel: 1, Number: 41, Value: 127}
)

// The Switch app button moves the controls to the next app: the cut that
// follows goes to it only, the log says where it went, and the hints, which
// name it, show for SwitchShow, through the button's own release.
func TestSwitchAppButton(t *testing.T) {
	a, blender, nomnom := switchApp(t)
	a.Handle(cutDown)
	if b, n := cuts(t, blender), cuts(t, nomnom); b != 1 || n != 0 {
		t.Fatalf("before the switch: Blender %d cuts, nomnom %d", b, n)
	}

	now := time.Now()
	a.Handle(switchDown)
	a.Handle(switchUp)
	if id, _ := a.link.Target(); id != "nomnom" || a.Snapshot().Blender.Target != "nomnom" {
		t.Fatalf("target %q after the switch", id)
	}
	evs := a.EventsSince(0)
	if ev := evs[len(evs)-2]; ev.Switched != "nomnom" || !ev.Delivered {
		t.Fatalf("switch event = %+v", ev)
	}
	if !a.link.HintsShown() {
		t.Fatal("hints not shown for the switch")
	}
	a.hintTick(now.Add(SwitchShow / 2))
	if !a.link.HintsShown() {
		t.Fatal("hints gone before SwitchShow")
	}
	a.hintTick(now.Add(SwitchShow + time.Second))
	if a.link.HintsShown() {
		t.Fatal("hints still shown after SwitchShow")
	}

	a.Handle(cutDown)
	if b, n := cuts(t, blender), cuts(t, nomnom); b != 0 || n != 1 {
		t.Fatalf("after the switch: Blender %d cuts, nomnom %d", b, n)
	}
	a.Handle(switchDown)
	if id, _ := a.link.Target(); id != link.Blender {
		t.Fatalf("second switch went to %q", id)
	}
}

// With no app connected the button does nothing and shows nothing.
func TestSwitchAppWithNothingConnected(t *testing.T) {
	a := hintApp(t)
	cfg := a.Config()
	for i := range cfg.Profiles[0].Bindings {
		if b := &cfg.Profiles[0].Bindings[i]; b.ID == "switch-app" {
			b.Control.Number = 52
		}
	}
	if err := a.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	a.Handle(switchDown)
	evs := a.EventsSince(0)
	if ev := evs[len(evs)-1]; ev.Delivered || ev.Switched != "" || ev.Hit.Binding != "switch-app" {
		t.Fatalf("event = %+v", ev)
	}
	if a.link.HintsShown() {
		t.Fatal("hints shown with no app to name")
	}
}

// Fn's hints and a switch's share the add-ons' one switch: letting go of Fn
// doesn't hide a fresh switch's, and a switch's time running out doesn't
// hide Fn's.
func TestSwitchAndFnHints(t *testing.T) {
	a, _, _ := switchApp(t)
	now := time.Now()
	a.Handle(fnDown)
	a.hintTick(now.Add(HintDelay + time.Millisecond))
	if !a.link.HintsShown() {
		t.Fatal("Fn's hints not shown")
	}
	a.Handle(switchDown) // Fn + switch: switches, and shows the new target
	if id, _ := a.link.Target(); id != "nomnom" {
		t.Fatalf("target %q", id)
	}
	a.Handle(fnUp)
	if !a.link.HintsShown() {
		t.Fatal("Fn's release hid the switch's hints")
	}
	a.hintTick(time.Now().Add(SwitchShow + time.Second))
	if a.link.HintsShown() {
		t.Fatal("still shown with nothing held and the switch's time up")
	}
}

// The page's test buttons are Blender's: they reach Blender whichever app
// the controls drive.
func TestTestGoesToBlender(t *testing.T) {
	a, blender, nomnom := switchApp(t)
	a.link.Switch()
	if !a.Test(engine.Command{Type: "cut"}) {
		t.Fatal("not delivered")
	}
	if b, n := cuts(t, blender), cuts(t, nomnom); b != 1 || n != 0 {
		t.Fatalf("Blender %d cuts, nomnom %d", b, n)
	}
}

package engine

import (
	"testing"

	"github.com/alexandreffaria/blendeck/internal/config"
	"github.com/alexandreffaria/blendeck/internal/midi"
)

func knob(v int) midi.Message {
	return midi.Message{Kind: midi.ControlChange, Channel: 1, Number: 5, Value: v}
}

var fnDown = midi.Message{Kind: midi.NoteOn, Channel: 1, Number: 40, Value: 127}
var fnUp = midi.Message{Kind: midi.NoteOff, Channel: 1, Number: 40}

func scrubProfile() config.Profile {
	p := config.Default().Profiles[0]
	p.Bindings[0].Control.Number = 5
	p.Bindings[1].Control.Number = 5
	p.Modifiers = []config.Modifier{{Name: "Fn", Control: config.Control{Type: "note", Channel: 1, Number: 40}}}
	return p
}

func frames(t *testing.T, e *Engine, m midi.Message) int {
	t.Helper()
	hit, _ := e.Handle(m)
	if hit.Command == nil {
		return 0
	}
	return hit.Command.Frames
}

func TestScrubTenAndFnOne(t *testing.T) {
	e := New(scrubProfile())
	if got := frames(t, e, knob(1)); got != 10 {
		t.Fatalf("one click right = %d frames, want 10", got)
	}
	if got := frames(t, e, knob(127)); got != -10 {
		t.Fatalf("one click left = %d, want -10", got)
	}
	if got := frames(t, e, knob(3)); got != 30 {
		t.Fatalf("three clicks at once = %d, want 30", got)
	}
	e.Handle(fnDown)
	if h := e.Held(); len(h) != 1 || h[0] != "Fn" {
		t.Fatalf("held = %v", h)
	}
	if got := frames(t, e, knob(1)); got != 1 {
		t.Fatalf("fn + right = %d, want 1", got)
	}
	if got := frames(t, e, knob(127)); got != -1 {
		t.Fatalf("fn + left = %d, want -1", got)
	}
	e.Handle(fnUp)
	if got := frames(t, e, knob(1)); got != 10 {
		t.Fatalf("after fn release = %d, want 10", got)
	}
}

func TestFnWithoutFineBindingFallsBack(t *testing.T) {
	p := scrubProfile()
	p.Bindings = p.Bindings[:1]
	e := New(p)
	e.Handle(fnDown)
	if got := frames(t, e, knob(1)); got != 10 {
		t.Fatalf("got %d, want 10", got)
	}
}

func TestUndefinedModifierBindingNeverFires(t *testing.T) {
	p := scrubProfile()
	p.Modifiers = nil // Fn not learned yet
	e := New(p)
	if got := frames(t, e, knob(1)); got != 10 {
		t.Fatalf("got %d, want 10 from the plain binding", got)
	}
}

func TestUnlearnedControlIgnored(t *testing.T) {
	e := New(config.Default().Profiles[0])
	hit, _ := e.Handle(midi.Message{Kind: midi.ControlChange, Channel: 1, Number: 0, Value: 1})
	if hit.Binding != "" {
		t.Fatalf("unlearned binding matched: %+v", hit)
	}
}

func TestZeroDeltaSendsNothing(t *testing.T) {
	e := New(scrubProfile())
	hit, _ := e.Handle(knob(0))
	if hit.Command != nil || hit.Binding != "scrub" {
		t.Fatalf("hit = %+v", hit)
	}
}

func TestLearn(t *testing.T) {
	e := New(scrubProfile())
	e.Learn(true)
	if _, l := e.Handle(fnUp); l != nil {
		t.Fatal("a release was learned")
	}
	_, l := e.Handle(midi.Message{Kind: midi.ControlChange, Channel: 2, Number: 9, Value: 1})
	if l == nil || *l != (config.Control{Type: "cc", Channel: 2, Number: 9}) {
		t.Fatalf("learned %+v", l)
	}
	if e.Learning() {
		t.Fatal("still learning")
	}
	// Learn mode doesn't act on the captured message
	e.Learn(true)
	hit, _ := e.Handle(knob(1))
	if hit.Command != nil {
		t.Fatal("learn mode sent a command")
	}
}

func TestSetProfileKeepsHeldModifier(t *testing.T) {
	e := New(scrubProfile())
	e.Handle(fnDown)
	e.SetProfile(scrubProfile())
	if len(e.Held()) != 1 {
		t.Fatal("Fn released by profile switch")
	}
	p := scrubProfile()
	p.Modifiers = nil
	e.SetProfile(p)
	if len(e.Held()) != 0 {
		t.Fatal("removed modifier still held")
	}
}

func TestCCModifier(t *testing.T) {
	p := scrubProfile()
	p.Modifiers[0].Control = config.Control{Type: "cc", Channel: 1, Number: 64}
	e := New(p)
	e.Handle(midi.Message{Kind: midi.ControlChange, Channel: 1, Number: 64, Value: 127})
	if got := frames(t, e, knob(1)); got != 1 {
		t.Fatalf("got %d", got)
	}
	e.Handle(midi.Message{Kind: midi.ControlChange, Channel: 1, Number: 64, Value: 0})
	if got := frames(t, e, knob(1)); got != 10 {
		t.Fatalf("got %d", got)
	}
}

func TestDelta(t *testing.T) {
	cases := []struct {
		v    int
		enc  string
		want int
	}{
		{1, config.EncoderTwos, 1}, {127, config.EncoderTwos, -1}, {63, config.EncoderTwos, 63}, {64, config.EncoderTwos, -64},
		{65, config.EncoderOffset, 1}, {63, config.EncoderOffset, -1}, {64, config.EncoderOffset, 0},
		{1, config.EncoderSignBit, 1}, {65, config.EncoderSignBit, -1}, {64, config.EncoderSignBit, 0},
	}
	for _, c := range cases {
		if got := Delta(c.v, c.enc); got != c.want {
			t.Errorf("Delta(%d, %s) = %d, want %d", c.v, c.enc, got, c.want)
		}
	}
}

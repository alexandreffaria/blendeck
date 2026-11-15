package engine

import (
	"encoding/json"
	"testing"
	"time"

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
	e.Learn(LearnBinding, nil)
	if _, l := e.Handle(midi.Message{Kind: midi.NoteOff, Channel: 3, Number: 1}); l != nil {
		t.Fatal("a release was learned")
	}
	_, l := e.Handle(midi.Message{Kind: midi.ControlChange, Channel: 2, Number: 9, Value: 1})
	if l == nil || l.Control != (config.Control{Type: "cc", Channel: 2, Number: 9}) || len(l.Held) != 0 {
		t.Fatalf("learned %+v", l)
	}
	if e.Learning() != LearnOff {
		t.Fatal("still learning")
	}
	// Learn mode doesn't act on the captured message
	e.Learn(LearnBinding, nil)
	hit, _ := e.Handle(knob(1))
	if hit.Command != nil {
		t.Fatal("learn mode sent a command")
	}
}

// Learning the fine binding: hold Fn, turn the knob. Fn must not be the
// thing learned, and the result says Fn was held.
func TestLearnBindingWithFnHeld(t *testing.T) {
	e := New(scrubProfile())
	e.Learn(LearnBinding, nil)
	if hit, l := e.Handle(fnDown); l != nil || hit.Modifier != "Fn" {
		t.Fatalf("Fn press during binding learn: hit %+v learned %+v", hit, l)
	}
	_, l := e.Handle(knob(1))
	if l == nil || l.Control.Number != 5 || len(l.Held) != 1 || l.Held[0] != "Fn" {
		t.Fatalf("learned %+v", l)
	}
}

func TestLearnModifierCapturesKnownModifier(t *testing.T) {
	e := New(scrubProfile())
	e.Learn(LearnModifier, nil)
	_, l := e.Handle(fnDown)
	if l == nil || l.Control.Type != "note" || l.Control.Number != 40 {
		t.Fatalf("learned %+v", l)
	}
	e.Handle(fnUp)
	if len(e.Held()) != 0 {
		t.Fatalf("held = %v after the learned button was released", e.Held())
	}
}

// Fn learned on the page but not saved, pressed before Learn is clicked:
// it must still be reported as held when the knob is captured.
func TestUnsavedModifierPressedBeforeLearn(t *testing.T) {
	p := scrubProfile()
	p.Modifiers = nil
	e := New(p)
	e.Handle(fnDown)
	e.Learn(LearnBinding, []config.Modifier{{Name: "Fn", Control: config.Control{Type: "note", Channel: 1, Number: 40}}})
	_, l := e.Handle(knob(1))
	if l == nil || len(l.Held) != 1 || l.Held[0] != "Fn" {
		t.Fatalf("learned %+v", l)
	}
}

func TestLearnTimesOut(t *testing.T) {
	e := New(scrubProfile())
	now := time.Now()
	e.now = func() time.Time { return now }
	e.Learn(LearnBinding, nil)
	now = now.Add(LearnTimeout + time.Second)
	if e.Learning() != LearnOff {
		t.Fatal("learn mode outlived its timeout")
	}
	if got := frames(t, e, knob(1)); got != 10 {
		t.Fatalf("knob after timeout = %d, want 10", got)
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

// Fn held when a modifier learn starts: its release must still count, or Fn
// stays held and the knob scrubs 1 frame for good.
func TestModifierReleaseDuringLearn(t *testing.T) {
	e := New(scrubProfile())
	e.Handle(fnDown)
	e.Learn(LearnModifier, nil)
	if _, l := e.Handle(fnUp); l != nil {
		t.Fatal("release was learned")
	}
	e.Learn(LearnOff, nil)
	if h := e.Held(); len(h) != 0 {
		t.Fatalf("held = %v after Fn released", h)
	}
	if got := frames(t, e, knob(1)); got != 10 {
		t.Fatalf("knob = %d, want 10", got)
	}
}

// The page learned Fn but hasn't saved: it sends its modifiers with the
// binding learn, so holding Fn passes through and is reported.
func TestBindingLearnWithUnsavedModifier(t *testing.T) {
	p := scrubProfile()
	p.Modifiers = nil // saved profile has no Fn yet
	e := New(p)
	draft := []config.Modifier{{Name: "Shift", Control: config.Control{Type: "note", Channel: 1, Number: 40}}}
	e.Learn(LearnBinding, draft)
	if hit, l := e.Handle(fnDown); l != nil || hit.Modifier != "Shift" {
		t.Fatalf("hit %+v learned %+v", hit, l)
	}
	_, l := e.Handle(knob(1))
	if l == nil || l.Control.Number != 5 || len(l.Held) != 1 || l.Held[0] != "Shift" {
		t.Fatalf("learned %+v", l)
	}
	if len(e.Held()) != 0 {
		t.Fatal("unsaved modifier counted as held in the profile")
	}
	// Released after the learn ended, then saved: must not come back held
	e.Handle(fnUp)
	p.Modifiers = draft
	e.SetProfile(p)
	if len(e.Held()) != 0 {
		t.Fatalf("held = %v after release and save", e.Held())
	}
}

func TestRenamedModifierStaysHeld(t *testing.T) {
	e := New(scrubProfile())
	e.Handle(fnDown)
	p := scrubProfile()
	p.Modifiers[0].Name = "Shift"
	p.Bindings[1].Modifier = "Shift"
	e.SetProfile(p)
	if got := frames(t, e, knob(1)); got != 1 {
		t.Fatalf("knob = %d, want 1 with renamed modifier held", got)
	}
}

func ccShiftProfile() config.Profile {
	p := scrubProfile()
	p.Modifiers = []config.Modifier{{Name: "Shift", Control: config.Control{Type: "cc", Channel: 1, Number: 64}}}
	p.Bindings[1].Modifier = "Shift"
	return p
}

var shiftDown = midi.Message{Kind: midi.ControlChange, Channel: 1, Number: 64, Value: 127}
var shiftUp = midi.Message{Kind: midi.ControlChange, Channel: 1, Number: 64, Value: 0}

func TestCCModifierReleasedUnderOtherProfile(t *testing.T) {
	e := New(ccShiftProfile())
	e.Handle(shiftDown)
	other := scrubProfile()
	other.Modifiers = nil
	e.SetProfile(other)
	e.Handle(shiftUp)
	e.SetProfile(ccShiftProfile())
	if h := e.Held(); len(h) != 0 {
		t.Fatalf("held = %v", h)
	}
	if got := frames(t, e, knob(1)); got != 10 {
		t.Fatalf("knob = %d, want 10", got)
	}
}

func TestUnsavedCCModifierReleasedAfterLearn(t *testing.T) {
	p := scrubProfile()
	p.Modifiers = nil
	e := New(p)
	e.Learn(LearnBinding, ccShiftProfile().Modifiers)
	e.Handle(shiftDown)
	if _, l := e.Handle(knob(1)); l == nil || len(l.Held) != 1 {
		t.Fatalf("learned %+v", l)
	}
	e.Handle(shiftUp)
	e.SetProfile(ccShiftProfile())
	if h := e.Held(); len(h) != 0 {
		t.Fatalf("held = %v after release and save", h)
	}
}

func TestReleaseAll(t *testing.T) {
	e := New(scrubProfile())
	e.Handle(fnDown)
	e.ReleaseAll()
	if got := frames(t, e, knob(1)); got != 10 {
		t.Fatalf("knob = %d after ReleaseAll, want 10", got)
	}
}

func zoomKnob(v int) midi.Message {
	return midi.Message{Kind: midi.ControlChange, Channel: 1, Number: 6, Value: v}
}

// zoomProfile has the scrub knob on CC 5 and the zoom knob on CC 6, both
// with an Fn version.
func zoomProfile() config.Profile {
	p := scrubProfile()
	for i := range p.Bindings {
		if p.Bindings[i].Action == config.ActionZoom {
			p.Bindings[i].Control.Number = 6
		}
	}
	return p
}

// zoomed returns the zoom clicks m sends, or 0 when it sends no zoom.
func zoomed(t *testing.T, e *Engine, m midi.Message) int {
	t.Helper()
	hit, _ := e.Handle(m)
	if hit.Command == nil {
		return 0
	}
	if hit.Command.Type != "zoom" || hit.Command.Frames != 0 {
		t.Fatalf("command = %+v, want a zoom", hit.Command)
	}
	return hit.Command.Clicks
}

func TestZoomOneAndFnFour(t *testing.T) {
	e := New(zoomProfile())
	if got := zoomed(t, e, zoomKnob(1)); got != 1 {
		t.Fatalf("one click right = %d, want 1 (zoom in)", got)
	}
	if got := zoomed(t, e, zoomKnob(127)); got != -1 {
		t.Fatalf("one click left = %d, want -1 (zoom out)", got)
	}
	if got := zoomed(t, e, zoomKnob(3)); got != 3 {
		t.Fatalf("three clicks at once = %d, want 3", got)
	}
	e.Handle(fnDown)
	if got := zoomed(t, e, zoomKnob(1)); got != 4 {
		t.Fatalf("fn + right = %d, want 4", got)
	}
	if got := zoomed(t, e, zoomKnob(127)); got != -4 {
		t.Fatalf("fn + left = %d, want -4", got)
	}
	e.Handle(fnUp)
	if got := zoomed(t, e, zoomKnob(1)); got != 1 {
		t.Fatalf("after fn release = %d, want 1", got)
	}
}

// The scrub and zoom knobs don't leak into each other, with or without Fn.
func TestZoomAndScrubKnobsApart(t *testing.T) {
	e := New(zoomProfile())
	hit, _ := e.Handle(knob(1))
	if hit.Binding != "scrub" || hit.Command == nil || hit.Command.Type != "scrub" || hit.Command.Clicks != 0 {
		t.Fatalf("scrub knob: %+v", hit)
	}
	hit, _ = e.Handle(zoomKnob(1))
	if hit.Binding != "zoom" || hit.Command == nil || hit.Command.Type != "zoom" {
		t.Fatalf("zoom knob: %+v", hit)
	}
	e.Handle(fnDown)
	if hit, _ = e.Handle(zoomKnob(1)); hit.Binding != "zoom-fast" {
		t.Fatalf("fn + zoom knob: %+v", hit)
	}
	if hit, _ = e.Handle(knob(1)); hit.Binding != "scrub-fine" {
		t.Fatalf("fn + scrub knob: %+v", hit)
	}
}

func TestZoomWithoutFastBindingFallsBack(t *testing.T) {
	p := zoomProfile()
	var kept []config.Binding
	for _, b := range p.Bindings {
		if b.ID != "zoom-fast" {
			kept = append(kept, b)
		}
	}
	p.Bindings = kept
	e := New(p)
	e.Handle(fnDown)
	if got := zoomed(t, e, zoomKnob(1)); got != 1 {
		t.Fatalf("got %d, want 1", got)
	}
}

func TestZoomNotLearnedIgnored(t *testing.T) {
	e := New(scrubProfile()) // zoom bindings still at number -1
	hit, _ := e.Handle(zoomKnob(1))
	if hit.Binding != "" || hit.Command != nil {
		t.Fatalf("unlearned zoom matched: %+v", hit)
	}
}

func TestZoomZeroDeltaSendsNothing(t *testing.T) {
	e := New(zoomProfile())
	hit, _ := e.Handle(zoomKnob(0))
	if hit.Command != nil || hit.Binding != "zoom" {
		t.Fatalf("hit = %+v", hit)
	}
}

func TestZoomOffsetEncoder(t *testing.T) {
	p := zoomProfile()
	for i := range p.Bindings {
		if p.Bindings[i].ID == "zoom" {
			p.Bindings[i].Encoder = config.EncoderOffset
		}
	}
	e := New(p)
	if got := zoomed(t, e, zoomKnob(63)); got != -1 {
		t.Fatalf("offset 63 = %d, want -1", got)
	}
}

func TestZoomCommandJSON(t *testing.T) {
	data, _ := json.Marshal(Command{Type: "zoom", Clicks: -2})
	if string(data) != `{"type":"zoom","clicks":-2}` {
		t.Fatalf("got %s", data)
	}
}

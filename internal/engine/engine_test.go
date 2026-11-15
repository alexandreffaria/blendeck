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

// Like the scrub knob: a plain click is coarse (4 steps), Fn makes it fine (1).
func TestZoomFourAndFnOne(t *testing.T) {
	e := New(zoomProfile())
	if got := zoomed(t, e, zoomKnob(1)); got != 4 {
		t.Fatalf("one click right = %d, want 4 (zoom in)", got)
	}
	if got := zoomed(t, e, zoomKnob(127)); got != -4 {
		t.Fatalf("one click left = %d, want -4 (zoom out)", got)
	}
	if got := zoomed(t, e, zoomKnob(3)); got != 12 {
		t.Fatalf("three clicks at once = %d, want 12", got)
	}
	e.Handle(fnDown)
	if got := zoomed(t, e, zoomKnob(1)); got != 1 {
		t.Fatalf("fn + right = %d, want 1", got)
	}
	if got := zoomed(t, e, zoomKnob(127)); got != -1 {
		t.Fatalf("fn + left = %d, want -1", got)
	}
	e.Handle(fnUp)
	if got := zoomed(t, e, zoomKnob(1)); got != 4 {
		t.Fatalf("after fn release = %d, want 4", got)
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
	if hit, _ = e.Handle(zoomKnob(1)); hit.Binding != "zoom-fine" {
		t.Fatalf("fn + zoom knob: %+v", hit)
	}
	if hit, _ = e.Handle(knob(1)); hit.Binding != "scrub-fine" {
		t.Fatalf("fn + scrub knob: %+v", hit)
	}
}

func TestZoomWithoutFineBindingFallsBack(t *testing.T) {
	p := zoomProfile()
	var kept []config.Binding
	for _, b := range p.Bindings {
		if b.ID != "zoom-fine" {
			kept = append(kept, b)
		}
	}
	p.Bindings = kept
	e := New(p)
	e.Handle(fnDown)
	if got := zoomed(t, e, zoomKnob(1)); got != 4 {
		t.Fatalf("got %d, want 4 from the plain binding", got)
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
	if got := zoomed(t, e, zoomKnob(63)); got != -4 {
		t.Fatalf("offset 63 = %d, want -4", got)
	}
}

func TestZoomCommandJSON(t *testing.T) {
	data, _ := json.Marshal(Command{Type: "zoom", Clicks: -2})
	if string(data) != `{"type":"zoom","clicks":-2}` {
		t.Fatalf("got %s", data)
	}
}

// The owner's cut button: note 53 on channel 1, pressed with velocity 64.
var cutDown = midi.Message{Kind: midi.NoteOn, Channel: 1, Number: 53, Value: 64}
var cutUp = midi.Message{Kind: midi.NoteOff, Channel: 1, Number: 53, Value: 64}

// cutProfile is zoomProfile with the cut button learned as note 53.
func cutProfile() config.Profile {
	p := zoomProfile()
	for i := range p.Bindings {
		if p.Bindings[i].Action == config.ActionCut {
			p.Bindings[i].Control.Number = 53
		}
	}
	return p
}

// cuts reports whether m sends a cut, and fails on any other command.
func cuts(t *testing.T, e *Engine, m midi.Message) bool {
	t.Helper()
	hit, _ := e.Handle(m)
	if hit.Command == nil {
		return false
	}
	if *hit.Command != (Command{Type: "cut"}) {
		t.Fatalf("command = %+v, want a cut", hit.Command)
	}
	return true
}

func TestCutOncePerPress(t *testing.T) {
	e := New(cutProfile())
	hit, _ := e.Handle(cutDown)
	if hit.Binding != "cut" || hit.Command == nil || hit.Command.Type != "cut" {
		t.Fatalf("press: %+v", hit)
	}
	hit, _ = e.Handle(cutUp)
	if hit.Binding != "cut" || hit.Command != nil {
		t.Fatalf("release: %+v", hit)
	}
	// Note on with velocity 0 is a release too (running status)
	if cuts(t, e, midi.Message{Kind: midi.NoteOff, Channel: 1, Number: 53}) {
		t.Fatal("velocity-0 release cut")
	}
	if !cuts(t, e, cutDown) {
		t.Fatal("second press didn't cut")
	}
}

// With Fn held and no Fn cut binding, the plain one still cuts; releasing
// Fn in between changes nothing.
func TestCutWithFnHeld(t *testing.T) {
	e := New(cutProfile())
	e.Handle(fnDown)
	if !cuts(t, e, cutDown) {
		t.Fatal("Fn + press didn't cut")
	}
	if cuts(t, e, cutUp) {
		t.Fatal("Fn + release cut")
	}
	e.Handle(fnUp)
	if !cuts(t, e, cutDown) {
		t.Fatal("press after Fn release didn't cut")
	}
}

// An Fn cut binding wins while Fn is held, and only then.
func TestCutFnBindingWins(t *testing.T) {
	p := cutProfile()
	p.Bindings = append(p.Bindings, config.Binding{ID: "cut-fn", Name: "Cut (Fn)", Control: config.Control{Type: "note", Channel: 1, Number: 53}, Modifier: "Fn", Action: config.ActionCut})
	e := New(p)
	if hit, _ := e.Handle(cutDown); hit.Binding != "cut" {
		t.Fatalf("plain press matched %q", hit.Binding)
	}
	e.Handle(fnDown)
	if hit, _ := e.Handle(cutDown); hit.Binding != "cut-fn" || hit.Command == nil {
		t.Fatalf("Fn press: %+v", hit)
	}
}

func TestCutNotLearnedIgnored(t *testing.T) {
	e := New(zoomProfile()) // cut still at -1
	hit, _ := e.Handle(cutDown)
	if hit.Binding != "" || hit.Command != nil {
		t.Fatalf("unlearned cut matched: %+v", hit)
	}
}

// A cc used as a button cuts at 64 and up, and not on its release.
func TestCutOnCCButton(t *testing.T) {
	p := cutProfile()
	for i := range p.Bindings {
		if p.Bindings[i].Action == config.ActionCut {
			p.Bindings[i].Control = config.Control{Type: "cc", Channel: 1, Number: 20}
		}
	}
	e := New(p)
	cc := func(v int) midi.Message {
		return midi.Message{Kind: midi.ControlChange, Channel: 1, Number: 20, Value: v}
	}
	if !cuts(t, e, cc(127)) || !cuts(t, e, cc(64)) {
		t.Fatal("cc press didn't cut")
	}
	if cuts(t, e, cc(0)) || cuts(t, e, cc(63)) {
		t.Fatal("cc release cut")
	}
}

// The cut button doesn't disturb the knobs, and a knob doesn't cut.
func TestCutApartFromKnobs(t *testing.T) {
	e := New(cutProfile())
	if got := frames(t, e, knob(1)); got != 10 {
		t.Fatalf("scrub = %d", got)
	}
	if cuts(t, e, midi.Message{Kind: midi.NoteOn, Channel: 2, Number: 53, Value: 64}) {
		t.Fatal("note 53 on channel 2 cut")
	}
	if cuts(t, e, midi.Message{Kind: midi.NoteOn, Channel: 1, Number: 52, Value: 64}) {
		t.Fatal("note 52 cut")
	}
}

func TestCutDuringLearnIsCaptured(t *testing.T) {
	e := New(cutProfile())
	e.Learn(LearnBinding, nil)
	hit, l := e.Handle(cutDown)
	if hit.Command != nil || l == nil || l.Control != (config.Control{Type: "note", Channel: 1, Number: 53}) {
		t.Fatalf("hit %+v learned %+v", hit, l)
	}
	if cuts(t, e, cutUp) {
		t.Fatal("the release after learning cut")
	}
}

func TestCutCommandJSON(t *testing.T) {
	data, _ := json.Marshal(Command{Type: "cut"})
	if string(data) != `{"type":"cut"}` {
		t.Fatalf("got %s", data)
	}
}

// The owner's undo button: note 95 on channel 1, pressed with velocity 64.
var undoDown = midi.Message{Kind: midi.NoteOn, Channel: 1, Number: 95, Value: 64}
var undoUp = midi.Message{Kind: midi.NoteOff, Channel: 1, Number: 95, Value: 64}

// undoProfile is cutProfile with the undo button learned as note 95.
func undoProfile() config.Profile {
	p := cutProfile()
	for i := range p.Bindings {
		if p.Bindings[i].Action == config.ActionUndo {
			p.Bindings[i].Control.Number = 95
		}
	}
	return p
}

// undoes reports whether m sends an undo, and fails on any other command.
func undoes(t *testing.T, e *Engine, m midi.Message) bool {
	t.Helper()
	hit, _ := e.Handle(m)
	if hit.Command == nil {
		return false
	}
	if *hit.Command != (Command{Type: "undo"}) {
		t.Fatalf("command = %+v, want an undo", hit.Command)
	}
	return true
}

func TestUndoOncePerPress(t *testing.T) {
	e := New(undoProfile())
	hit, _ := e.Handle(undoDown)
	if hit.Binding != "undo" || hit.Command == nil || hit.Command.Type != "undo" {
		t.Fatalf("press: %+v", hit)
	}
	hit, _ = e.Handle(undoUp)
	if hit.Binding != "undo" || hit.Command != nil {
		t.Fatalf("release: %+v", hit)
	}
	if undoes(t, e, midi.Message{Kind: midi.NoteOff, Channel: 1, Number: 95}) {
		t.Fatal("velocity-0 release undid")
	}
	// Three presses are three undos, one each
	for i := 0; i < 3; i++ {
		if !undoes(t, e, undoDown) {
			t.Fatalf("press %d didn't undo", i+2)
		}
		if undoes(t, e, undoUp) {
			t.Fatalf("release %d undid", i+2)
		}
	}
}

// With Fn held and no Fn undo binding, the plain one still undoes;
// releasing Fn in between changes nothing.
func TestUndoWithFnHeld(t *testing.T) {
	e := New(undoProfile())
	e.Handle(fnDown)
	if !undoes(t, e, undoDown) {
		t.Fatal("Fn + press didn't undo")
	}
	if undoes(t, e, undoUp) {
		t.Fatal("Fn + release undid")
	}
	e.Handle(fnUp)
	if !undoes(t, e, undoDown) {
		t.Fatal("press after Fn release didn't undo")
	}
}

// An Fn binding on the same button wins while Fn is held, and only then.
func TestUndoFnBindingWins(t *testing.T) {
	p := undoProfile()
	p.Bindings = append(p.Bindings, config.Binding{ID: "undo-fn", Name: "Undo (Fn)", Control: config.Control{Type: "note", Channel: 1, Number: 95}, Modifier: "Fn", Action: config.ActionUndo})
	e := New(p)
	if hit, _ := e.Handle(undoDown); hit.Binding != "undo" {
		t.Fatalf("plain press matched %q", hit.Binding)
	}
	e.Handle(fnDown)
	if hit, _ := e.Handle(undoDown); hit.Binding != "undo-fn" || hit.Command == nil {
		t.Fatalf("Fn press: %+v", hit)
	}
	e.Handle(fnUp)
	if hit, _ := e.Handle(undoDown); hit.Binding != "undo" {
		t.Fatalf("press after Fn release matched %q", hit.Binding)
	}
}

func TestUndoNotLearnedIgnored(t *testing.T) {
	e := New(cutProfile()) // undo still at -1
	hit, _ := e.Handle(undoDown)
	if hit.Binding != "" || hit.Command != nil {
		t.Fatalf("unlearned undo matched: %+v", hit)
	}
}

// A cc used as a button undoes at 64 and up, and not on its release.
func TestUndoOnCCButton(t *testing.T) {
	p := undoProfile()
	for i := range p.Bindings {
		if p.Bindings[i].Action == config.ActionUndo {
			p.Bindings[i].Control = config.Control{Type: "cc", Channel: 1, Number: 21}
		}
	}
	e := New(p)
	cc := func(v int) midi.Message {
		return midi.Message{Kind: midi.ControlChange, Channel: 1, Number: 21, Value: v}
	}
	if !undoes(t, e, cc(127)) || !undoes(t, e, cc(64)) {
		t.Fatal("cc press didn't undo")
	}
	if undoes(t, e, cc(0)) || undoes(t, e, cc(63)) {
		t.Fatal("cc release undid")
	}
}

// Undo and cut are separate buttons, and the knobs still work.
func TestUndoApartFromOthers(t *testing.T) {
	e := New(undoProfile())
	if !cuts(t, e, cutDown) {
		t.Fatal("cut button didn't cut")
	}
	if got := frames(t, e, knob(1)); got != 10 {
		t.Fatalf("scrub = %d", got)
	}
	if undoes(t, e, midi.Message{Kind: midi.NoteOn, Channel: 2, Number: 95, Value: 64}) {
		t.Fatal("note 95 on channel 2 undid")
	}
	if undoes(t, e, midi.Message{Kind: midi.NoteOn, Channel: 1, Number: 94, Value: 64}) {
		t.Fatal("note 94 undid")
	}
}

func TestUndoDuringLearnIsCaptured(t *testing.T) {
	e := New(undoProfile())
	e.Learn(LearnBinding, nil)
	hit, l := e.Handle(undoDown)
	if hit.Command != nil || l == nil || l.Control != (config.Control{Type: "note", Channel: 1, Number: 95}) {
		t.Fatalf("hit %+v learned %+v", hit, l)
	}
	if undoes(t, e, undoUp) {
		t.Fatal("the release after learning undid")
	}
}

func TestUndoCommandJSON(t *testing.T) {
	data, _ := json.Marshal(Command{Type: "undo"})
	if string(data) != `{"type":"undo"}` {
		t.Fatalf("got %s", data)
	}
}

// The owner's ripple delete button: note 54 on channel 1, pressed with
// velocity 64.
var rippleDown = midi.Message{Kind: midi.NoteOn, Channel: 1, Number: 54, Value: 64}
var rippleUp = midi.Message{Kind: midi.NoteOff, Channel: 1, Number: 54, Value: 64}

// rippleProfile is cutProfile with the ripple delete button learned as
// note 54.
func rippleProfile() config.Profile {
	p := cutProfile()
	for i := range p.Bindings {
		if p.Bindings[i].ID == "ripple-delete" {
			p.Bindings[i].Control.Number = 54
		}
	}
	return p
}

// ripples reports whether m sends a ripple delete, and fails on any other
// command.
func ripples(t *testing.T, e *Engine, m midi.Message) bool {
	t.Helper()
	hit, _ := e.Handle(m)
	if hit.Command == nil {
		return false
	}
	if *hit.Command != (Command{Type: "ripple_delete"}) {
		t.Fatalf("command = %+v, want a ripple delete", hit.Command)
	}
	return true
}

func TestRippleDeleteOncePerPress(t *testing.T) {
	e := New(rippleProfile())
	hit, _ := e.Handle(rippleDown)
	if hit.Binding != "ripple-delete" || hit.Command == nil || hit.Command.Type != "ripple_delete" {
		t.Fatalf("press: %+v", hit)
	}
	hit, _ = e.Handle(rippleUp)
	if hit.Binding != "ripple-delete" || hit.Command != nil {
		t.Fatalf("release: %+v", hit)
	}
	if ripples(t, e, midi.Message{Kind: midi.NoteOff, Channel: 1, Number: 54}) {
		t.Fatal("velocity-0 release deleted")
	}
	if !ripples(t, e, rippleDown) {
		t.Fatal("second press didn't delete")
	}
}

// With Fn held and no Fn binding for the button, the plain one still
// ripple deletes, and releasing Fn in between changes nothing.
func TestRippleDeleteWithFnHeld(t *testing.T) {
	e := New(rippleProfile())
	e.Handle(fnDown)
	if !ripples(t, e, rippleDown) {
		t.Fatal("Fn + press didn't delete")
	}
	if ripples(t, e, rippleUp) {
		t.Fatal("Fn + release deleted")
	}
	e.Handle(fnUp)
	if !ripples(t, e, rippleDown) {
		t.Fatal("press after Fn release didn't delete")
	}
}

// An Fn binding on the same button wins while Fn is held, and only then.
func TestRippleDeleteFnBindingWins(t *testing.T) {
	p := rippleProfile()
	p.Bindings = append(p.Bindings, config.Binding{ID: "ripple-fn", Name: "Ripple delete (Fn)", Control: config.Control{Type: "note", Channel: 1, Number: 54}, Modifier: "Fn", Action: config.ActionRippleDelete})
	e := New(p)
	if hit, _ := e.Handle(rippleDown); hit.Binding != "ripple-delete" {
		t.Fatalf("plain press matched %q", hit.Binding)
	}
	e.Handle(fnDown)
	if hit, _ := e.Handle(rippleDown); hit.Binding != "ripple-fn" || hit.Command == nil {
		t.Fatalf("Fn press: %+v", hit)
	}
}

func TestRippleDeleteNotLearnedIgnored(t *testing.T) {
	e := New(cutProfile()) // ripple delete still at -1
	hit, _ := e.Handle(rippleDown)
	if hit.Binding != "" || hit.Command != nil {
		t.Fatalf("unlearned ripple delete matched: %+v", hit)
	}
}

// A cc used as a button deletes at 64 and up, and not on its release.
func TestRippleDeleteOnCCButton(t *testing.T) {
	p := rippleProfile()
	for i := range p.Bindings {
		if p.Bindings[i].ID == "ripple-delete" {
			p.Bindings[i].Control = config.Control{Type: "cc", Channel: 1, Number: 21}
		}
	}
	e := New(p)
	cc := func(v int) midi.Message {
		return midi.Message{Kind: midi.ControlChange, Channel: 1, Number: 21, Value: v}
	}
	if !ripples(t, e, cc(127)) || !ripples(t, e, cc(64)) {
		t.Fatal("cc press didn't delete")
	}
	if ripples(t, e, cc(0)) || ripples(t, e, cc(63)) {
		t.Fatal("cc release deleted")
	}
}

// The cut button next to it still cuts, and only note 54 on channel 1
// ripple deletes.
func TestRippleDeleteApartFromCut(t *testing.T) {
	e := New(rippleProfile())
	if !cuts(t, e, cutDown) {
		t.Fatal("cut button didn't cut")
	}
	if ripples(t, e, midi.Message{Kind: midi.NoteOn, Channel: 2, Number: 54, Value: 64}) {
		t.Fatal("note 54 on channel 2 deleted")
	}
	if got := frames(t, e, knob(1)); got != 10 {
		t.Fatalf("scrub = %d", got)
	}
}

func TestRippleDeleteCommandJSON(t *testing.T) {
	data, _ := json.Marshal(Command{Type: "ripple_delete"})
	if string(data) != `{"type":"ripple_delete"}` {
		t.Fatalf("got %s", data)
	}
}

// Fn + the cut button only cuts: ripple delete has no Fn variant
// (decision 202611156qj4).
func TestFnCutButtonStillCuts(t *testing.T) {
	e := New(rippleProfile())
	e.Handle(fnDown)
	if !cuts(t, e, cutDown) {
		t.Fatal("Fn + cut button didn't cut")
	}
}

// The owner's play / pause button: note 67 on channel 1, pressed with
// velocity 64.
var playDown = midi.Message{Kind: midi.NoteOn, Channel: 1, Number: 67, Value: 64}
var playUp = midi.Message{Kind: midi.NoteOff, Channel: 1, Number: 67, Value: 64}

// playProfile is rippleProfile with the play / pause button learned as
// note 67.
func playProfile() config.Profile {
	p := rippleProfile()
	for i := range p.Bindings {
		if p.Bindings[i].ID == "play-pause" {
			p.Bindings[i].Control.Number = 67
		}
	}
	return p
}

// toggles reports whether m sends a play / pause, and fails on any other
// command.
func toggles(t *testing.T, e *Engine, m midi.Message) bool {
	t.Helper()
	hit, _ := e.Handle(m)
	if hit.Command == nil {
		return false
	}
	if *hit.Command != (Command{Type: "play_pause"}) {
		t.Fatalf("command = %+v, want a play / pause", hit.Command)
	}
	return true
}

// One toggle per press: the release must not toggle it back.
func TestPlayPauseOncePerPress(t *testing.T) {
	e := New(playProfile())
	hit, _ := e.Handle(playDown)
	if hit.Binding != "play-pause" || hit.Command == nil || hit.Command.Type != "play_pause" {
		t.Fatalf("press: %+v", hit)
	}
	hit, _ = e.Handle(playUp)
	if hit.Binding != "play-pause" || hit.Command != nil {
		t.Fatalf("release: %+v", hit)
	}
	if toggles(t, e, midi.Message{Kind: midi.NoteOff, Channel: 1, Number: 67}) {
		t.Fatal("velocity-0 release toggled")
	}
	// Play, pause, play: one toggle each
	for i := 0; i < 3; i++ {
		if !toggles(t, e, playDown) {
			t.Fatalf("press %d didn't toggle", i+2)
		}
		if toggles(t, e, playUp) {
			t.Fatalf("release %d toggled", i+2)
		}
	}
}

// With Fn held and no Fn binding for the button, the plain one still
// toggles, and releasing Fn in between changes nothing.
func TestPlayPauseWithFnHeld(t *testing.T) {
	e := New(playProfile())
	e.Handle(fnDown)
	if !toggles(t, e, playDown) {
		t.Fatal("Fn + press didn't toggle")
	}
	if toggles(t, e, playUp) {
		t.Fatal("Fn + release toggled")
	}
	e.Handle(fnUp)
	if !toggles(t, e, playDown) {
		t.Fatal("press after Fn release didn't toggle")
	}
}

// An Fn binding on the same button wins while Fn is held, and only then.
func TestPlayPauseFnBindingWins(t *testing.T) {
	p := playProfile()
	p.Bindings = append(p.Bindings, config.Binding{ID: "play-fn", Name: "Play / pause (Fn)", Control: config.Control{Type: "note", Channel: 1, Number: 67}, Modifier: "Fn", Action: config.ActionPlayPause})
	e := New(p)
	if hit, _ := e.Handle(playDown); hit.Binding != "play-pause" {
		t.Fatalf("plain press matched %q", hit.Binding)
	}
	e.Handle(fnDown)
	if hit, _ := e.Handle(playDown); hit.Binding != "play-fn" || hit.Command == nil {
		t.Fatalf("Fn press: %+v", hit)
	}
	e.Handle(fnUp)
	if hit, _ := e.Handle(playDown); hit.Binding != "play-pause" {
		t.Fatalf("press after Fn release matched %q", hit.Binding)
	}
}

func TestPlayPauseNotLearnedIgnored(t *testing.T) {
	e := New(rippleProfile()) // play / pause still at -1
	hit, _ := e.Handle(playDown)
	if hit.Binding != "" || hit.Command != nil {
		t.Fatalf("unlearned play / pause matched: %+v", hit)
	}
}

// A cc used as a button toggles at 64 and up, and not on its release.
func TestPlayPauseOnCCButton(t *testing.T) {
	p := playProfile()
	for i := range p.Bindings {
		if p.Bindings[i].ID == "play-pause" {
			p.Bindings[i].Control = config.Control{Type: "cc", Channel: 1, Number: 21}
		}
	}
	e := New(p)
	cc := func(v int) midi.Message {
		return midi.Message{Kind: midi.ControlChange, Channel: 1, Number: 21, Value: v}
	}
	if !toggles(t, e, cc(127)) || !toggles(t, e, cc(64)) {
		t.Fatal("cc press didn't toggle")
	}
	if toggles(t, e, cc(0)) || toggles(t, e, cc(63)) {
		t.Fatal("cc release toggled")
	}
}

// The other buttons and the knob still do their own thing, and only note
// 67 on channel 1 toggles.
func TestPlayPauseApartFromOthers(t *testing.T) {
	e := New(playProfile())
	if !cuts(t, e, cutDown) {
		t.Fatal("cut button didn't cut")
	}
	if !ripples(t, e, rippleDown) {
		t.Fatal("ripple delete button didn't delete")
	}
	if got := frames(t, e, knob(1)); got != 10 {
		t.Fatalf("scrub = %d", got)
	}
	if toggles(t, e, midi.Message{Kind: midi.NoteOn, Channel: 2, Number: 67, Value: 64}) {
		t.Fatal("note 67 on channel 2 toggled")
	}
	if toggles(t, e, midi.Message{Kind: midi.NoteOn, Channel: 1, Number: 68, Value: 64}) {
		t.Fatal("note 68 toggled")
	}
}

func TestPlayPauseDuringLearnIsCaptured(t *testing.T) {
	e := New(playProfile())
	e.Learn(LearnBinding, nil)
	hit, l := e.Handle(playDown)
	if hit.Command != nil || l == nil || l.Control != (config.Control{Type: "note", Channel: 1, Number: 67}) {
		t.Fatalf("hit %+v learned %+v", hit, l)
	}
	if toggles(t, e, playUp) {
		t.Fatal("the release after learning toggled")
	}
}

func TestPlayPauseCommandJSON(t *testing.T) {
	data, _ := json.Marshal(Command{Type: "play_pause"})
	if string(data) != `{"type":"play_pause"}` {
		t.Fatalf("got %s", data)
	}
}

// The owner's shuttle buttons: note 66 on channel 1 plays backwards, note
// 68 forward, pressed with velocity 64.
var revDown = midi.Message{Kind: midi.NoteOn, Channel: 1, Number: 66, Value: 64}
var revUp = midi.Message{Kind: midi.NoteOff, Channel: 1, Number: 66, Value: 64}
var fwdDown = midi.Message{Kind: midi.NoteOn, Channel: 1, Number: 68, Value: 64}
var fwdUp = midi.Message{Kind: midi.NoteOff, Channel: 1, Number: 68, Value: 64}

// shuttleProfile is playProfile with the backwards button learned as note
// 66 and the forward one as note 68.
func shuttleProfile() config.Profile {
	p := playProfile()
	for i := range p.Bindings {
		switch p.Bindings[i].ID {
		case "reverse":
			p.Bindings[i].Control.Number = 66
		case "forward":
			p.Bindings[i].Control.Number = 68
		}
	}
	return p
}

// shuttles returns the direction m shuttles in (1, -1), or 0 for none,
// and fails on any other command.
func shuttles(t *testing.T, e *Engine, m midi.Message) int {
	t.Helper()
	hit, _ := e.Handle(m)
	if hit.Command == nil {
		return 0
	}
	if hit.Command.Type != "shuttle" || (hit.Command.Clicks != 1 && hit.Command.Clicks != -1) || hit.Command.Frames != 0 {
		t.Fatalf("command = %+v, want a shuttle", hit.Command)
	}
	return hit.Command.Clicks
}

// One step per press, never on the release, each button its own way.
func TestShuttleOncePerPress(t *testing.T) {
	e := New(shuttleProfile())
	hit, _ := e.Handle(revDown)
	if hit.Binding != "reverse" || hit.Command == nil || *hit.Command != (Command{Type: "shuttle", Clicks: -1}) {
		t.Fatalf("backwards press: %+v", hit)
	}
	hit, _ = e.Handle(fwdDown)
	if hit.Binding != "forward" || hit.Command == nil || *hit.Command != (Command{Type: "shuttle", Clicks: 1}) {
		t.Fatalf("forward press: %+v", hit)
	}
	if got := shuttles(t, e, revUp); got != 0 {
		t.Fatal("backwards release shuttled")
	}
	if got := shuttles(t, e, fwdUp); got != 0 {
		t.Fatal("forward release shuttled")
	}
	if got := shuttles(t, e, midi.Message{Kind: midi.NoteOff, Channel: 1, Number: 68}); got != 0 {
		t.Fatal("velocity-0 release shuttled")
	}
	// Three presses are three steps
	for i := 0; i < 3; i++ {
		if shuttles(t, e, fwdDown) != 1 || shuttles(t, e, fwdUp) != 0 {
			t.Fatalf("forward press %d", i+2)
		}
		if shuttles(t, e, revDown) != -1 || shuttles(t, e, revUp) != 0 {
			t.Fatalf("backwards press %d", i+2)
		}
	}
}

// With Fn held and no Fn bindings, the plain ones still shuttle.
func TestShuttleWithFnHeld(t *testing.T) {
	e := New(shuttleProfile())
	e.Handle(fnDown)
	if shuttles(t, e, fwdDown) != 1 || shuttles(t, e, revDown) != -1 {
		t.Fatal("Fn + press didn't shuttle")
	}
	if shuttles(t, e, fwdUp) != 0 {
		t.Fatal("Fn + release shuttled")
	}
	e.Handle(fnUp)
	if shuttles(t, e, fwdDown) != 1 {
		t.Fatal("press after Fn release didn't shuttle")
	}
}

// An Fn binding on the same button wins while Fn is held, and only then.
func TestShuttleFnBindingWins(t *testing.T) {
	p := shuttleProfile()
	p.Bindings = append(p.Bindings, config.Binding{ID: "fwd-fn", Name: "Forward (Fn)", Control: config.Control{Type: "note", Channel: 1, Number: 68}, Modifier: "Fn", Action: config.ActionReverse})
	e := New(p)
	if hit, _ := e.Handle(fwdDown); hit.Binding != "forward" {
		t.Fatalf("plain press matched %q", hit.Binding)
	}
	e.Handle(fnDown)
	if hit, _ := e.Handle(fwdDown); hit.Binding != "fwd-fn" || hit.Command == nil || hit.Command.Clicks != -1 {
		t.Fatalf("Fn press: %+v", hit)
	}
}

func TestShuttleNotLearnedIgnored(t *testing.T) {
	e := New(playProfile()) // both still at -1
	for _, m := range []midi.Message{fwdDown, revDown} {
		if hit, _ := e.Handle(m); hit.Binding != "" || hit.Command != nil {
			t.Fatalf("unlearned shuttle matched: %+v", hit)
		}
	}
}

// A cc used as a button shuttles at 64 and up, and not on its release.
func TestShuttleOnCCButton(t *testing.T) {
	p := shuttleProfile()
	for i := range p.Bindings {
		if p.Bindings[i].ID == "reverse" {
			p.Bindings[i].Control = config.Control{Type: "cc", Channel: 1, Number: 21}
		}
	}
	e := New(p)
	cc := func(v int) midi.Message {
		return midi.Message{Kind: midi.ControlChange, Channel: 1, Number: 21, Value: v}
	}
	if shuttles(t, e, cc(127)) != -1 || shuttles(t, e, cc(64)) != -1 {
		t.Fatal("cc press didn't shuttle")
	}
	if shuttles(t, e, cc(0)) != 0 || shuttles(t, e, cc(63)) != 0 {
		t.Fatal("cc release shuttled")
	}
}

// Play / pause between them still plays or pauses, and only notes 66 and
// 68 on channel 1 shuttle.
func TestShuttleApartFromOthers(t *testing.T) {
	e := New(shuttleProfile())
	if !toggles(t, e, playDown) {
		t.Fatal("play button didn't toggle")
	}
	if got := frames(t, e, knob(1)); got != 10 {
		t.Fatalf("scrub = %d", got)
	}
	for _, m := range []midi.Message{
		{Kind: midi.NoteOn, Channel: 2, Number: 66, Value: 64},
		{Kind: midi.NoteOn, Channel: 2, Number: 68, Value: 64},
		{Kind: midi.NoteOn, Channel: 1, Number: 65, Value: 64},
		{Kind: midi.NoteOn, Channel: 1, Number: 69, Value: 64},
	} {
		if shuttles(t, e, m) != 0 {
			t.Fatalf("%+v shuttled", m)
		}
	}
}

func TestShuttleDuringLearnIsCaptured(t *testing.T) {
	e := New(shuttleProfile())
	e.Learn(LearnBinding, nil)
	hit, l := e.Handle(fwdDown)
	if hit.Command != nil || l == nil || l.Control != (config.Control{Type: "note", Channel: 1, Number: 68}) {
		t.Fatalf("hit %+v learned %+v", hit, l)
	}
	if shuttles(t, e, fwdUp) != 0 {
		t.Fatal("the release after learning shuttled")
	}
}

func TestShuttleCommandJSON(t *testing.T) {
	for cmd, want := range map[Command]string{
		{Type: "shuttle", Clicks: 1}:  `{"type":"shuttle","clicks":1}`,
		{Type: "shuttle", Clicks: -1}: `{"type":"shuttle","clicks":-1}`,
	} {
		data, _ := json.Marshal(cmd)
		if string(data) != want {
			t.Fatalf("got %s, want %s", data, want)
		}
	}
}

func jumpProfile() config.Profile {
	return config.Profile{
		Name:      "p",
		Modifiers: []config.Modifier{{Name: "Fn", Control: config.Control{Type: "note", Channel: 1, Number: 110}}},
		Bindings: []config.Binding{
			{ID: "scrub", Name: "Scrub", Control: config.Control{Type: "cc", Channel: 1, Number: 42}, Encoder: config.EncoderTwos, Action: config.ActionScrub, Step: 10},
			{ID: "jump-edit", Name: "Jump", Control: config.Control{Type: "cc", Channel: 1, Number: 48}, Encoder: config.EncoderTwos, Action: config.ActionJumpEdit},
		},
	}
}

func jumpKnob(v int) midi.Message {
	return midi.Message{Kind: midi.ControlChange, Channel: 1, Number: 48, Value: v}
}

// The owner's knob sends 1 clockwise and 127 counter-clockwise: one cut
// right, one cut left. A faster turn's bigger values are that many cuts.
func TestJumpEditClicks(t *testing.T) {
	e := New(jumpProfile())
	for v, want := range map[int]int{1: 1, 127: -1, 3: 3, 125: -3} {
		hit, _ := e.Handle(jumpKnob(v))
		if hit.Binding != "jump-edit" || hit.Command == nil || *hit.Command != (Command{Type: "jump", Clicks: want}) {
			t.Fatalf("value %d: %+v", v, hit)
		}
	}
	if hit, _ := e.Handle(jumpKnob(0)); hit.Command != nil {
		t.Fatalf("value 0 sent %+v", hit.Command)
	}
	// The scrub knob still scrubs
	hit, _ := e.Handle(midi.Message{Kind: midi.ControlChange, Channel: 1, Number: 42, Value: 1})
	if hit.Command == nil || *hit.Command != (Command{Type: "scrub", Frames: 10}) {
		t.Fatalf("scrub: %+v", hit)
	}
}

// With Fn held and no Fn binding, the knob still jumps.
func TestJumpEditWithFnHeld(t *testing.T) {
	e := New(jumpProfile())
	e.Handle(midi.Message{Kind: midi.NoteOn, Channel: 1, Number: 110, Value: 127})
	hit, _ := e.Handle(jumpKnob(127))
	if hit.Command == nil || *hit.Command != (Command{Type: "jump", Clicks: -1}) {
		t.Fatalf("Fn + turn: %+v", hit)
	}
}

func TestJumpEditNotLearnedIgnored(t *testing.T) {
	p := jumpProfile()
	p.Bindings[1].Control.Number = -1
	e := New(p)
	if hit, _ := e.Handle(jumpKnob(1)); hit.Binding != "" || hit.Command != nil {
		t.Fatalf("unlearned knob: %+v", hit)
	}
}

func TestJumpCommandJSON(t *testing.T) {
	b, err := json.Marshal(Command{Type: "jump", Clicks: -2})
	if err != nil || string(b) != `{"type":"jump","clicks":-2}` {
		t.Fatalf("%s %v", b, err)
	}
}

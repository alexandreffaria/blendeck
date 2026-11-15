// Package engine turns MIDI messages into Blender commands using the active
// profile, tracks held modifiers, and captures controls in learn mode.
package engine

import (
	"sync"
	"time"

	"github.com/alexandreffaria/blendeck/internal/config"
	"github.com/alexandreffaria/blendeck/internal/midi"
)

// Command is what the bridge sends to Blender: {"type":"scrub","frames":N}
// moves the playhead N frames, {"type":"zoom","clicks":N} zooms the timeline
// N steps (positive is in), {"type":"cut"} cuts the strips under the
// playhead, {"type":"undo"} runs Blender's undo once.
type Command struct {
	Type   string `json:"type"`
	Frames int    `json:"frames,omitempty"`
	Clicks int    `json:"clicks,omitempty"`
}

// Hit says what a message did, for the live monitor.
type Hit struct {
	Binding  string   `json:"binding,omitempty"`  // binding id
	Modifier string   `json:"modifier,omitempty"` // modifier pressed or released
	Held     bool     `json:"held,omitempty"`
	Command  *Command `json:"command,omitempty"`
}

// Engine is safe for concurrent use.
type Engine struct {
	mu      sync.Mutex
	profile config.Profile
	// down is which buttons are physically held, by control: every note, and
	// cc buttons that are modifiers. At most 16*128 notes plus the modifiers.
	// Keeping all of them means a modifier pressed before it was saved (or
	// before Learn was clicked) still counts once it is known.
	down map[config.Control]bool
	// learnMods are the modifiers a binding learn passes through: the page's
	// unsaved ones, or the profile's when the page sent none
	learnMods []config.Modifier
	learn     Target
	until     time.Time
	now       func() time.Time
}

// Target says what learn mode is capturing for.
type Target string

const (
	// LearnOff means not learning.
	LearnOff Target = ""
	// LearnBinding captures a control for a binding. Learned modifiers are
	// passed through, so a binding can be learned with Fn held.
	LearnBinding Target = "binding"
	// LearnModifier captures any control, learned modifiers included.
	LearnModifier Target = "modifier"
)

// LearnTimeout is how long learn mode waits before giving up, so a page that
// was closed mid-learn can't leave the next press swallowed.
const LearnTimeout = 30 * time.Second

// Learned is what learn mode captured: the control, and the modifiers held
// at the time.
type Learned struct {
	Control config.Control `json:"control"`
	Held    []string       `json:"held"`
}

// New makes an Engine running profile p.
func New(p config.Profile) *Engine {
	e := &Engine{down: map[config.Control]bool{}, now: time.Now}
	e.SetProfile(p)
	return e
}

// SetProfile switches to p. Buttons held stay held.
func (e *Engine) SetProfile(p config.Profile) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.profile = p
}

// ReleaseAll forgets every held button, for when the device goes away and
// its releases can never arrive.
func (e *Engine) ReleaseAll() {
	e.mu.Lock()
	e.down = map[config.Control]bool{}
	e.mu.Unlock()
}

// Held lists the modifiers currently held down.
func (e *Engine) Held() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.heldLocked()
}

func (e *Engine) heldLocked() []string { return e.heldOf(e.profile.Modifiers) }

func (e *Engine) heldOf(mods []config.Modifier) []string {
	out := []string{}
	for _, m := range mods {
		if m.Control.Learned() && e.down[m.Control] {
			out = append(out, m.Name)
		}
	}
	return out
}

func (e *Engine) isHeld(name string) bool {
	for _, m := range e.profile.Modifiers {
		if m.Name == name && m.Control.Learned() && e.down[m.Control] {
			return true
		}
	}
	return false
}

// modifierFor returns the name of the modifier on ctl, looking at the
// profile and, during a binding learn, the learn modifiers.
func (e *Engine) modifierFor(ctl config.Control, target Target) (string, bool) {
	if target == LearnBinding && e.learnMods != nil {
		for _, m := range e.learnMods {
			if m.Control.Learned() && m.Control == ctl {
				return m.Name, true
			}
		}
	}
	for _, m := range e.profile.Modifiers {
		if m.Control.Learned() && m.Control == ctl {
			return m.Name, true
		}
	}
	return "", false
}

// Learn arms learn mode for t (or turns it off with LearnOff): the next
// message, other than a release, is returned by Handle as learned instead of
// being acted on. It turns itself off after LearnTimeout. For a binding
// learn, mods are the modifiers to pass through and report as held (the
// page's unsaved ones); nil means the profile's.
func (e *Engine) Learn(t Target, mods []config.Modifier) {
	e.mu.Lock()
	e.learn = t
	e.learnMods = mods
	e.until = e.now().Add(LearnTimeout)
	e.mu.Unlock()
}

// Learning reports what learn mode is armed for, or LearnOff.
func (e *Engine) Learning() Target {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.learningLocked()
}

func (e *Engine) learningLocked() Target {
	if e.learn != LearnOff && !e.now().Before(e.until) {
		e.learn = LearnOff
	}
	return e.learn
}

// ControlOf says which control sent m.
func ControlOf(m midi.Message) (config.Control, bool) {
	switch m.Kind {
	case midi.ControlChange:
		return config.Control{Type: "cc", Channel: m.Channel, Number: m.Number}, true
	case midi.NoteOn, midi.NoteOff:
		return config.Control{Type: "note", Channel: m.Channel, Number: m.Number}, true
	case midi.PitchBend:
		return config.Control{Type: "pitch_bend", Channel: m.Channel, Number: 0}, true
	}
	return config.Control{}, false
}

// pressed says whether a button message means down (true) or up (false).
func pressed(m midi.Message) bool {
	switch m.Kind {
	case midi.NoteOn:
		return true
	case midi.NoteOff:
		return false
	}
	return m.Value >= 64
}

// Delta decodes a relative encoder value into signed clicks.
func Delta(value int, encoder string) int {
	value &= 0x7F
	switch encoder {
	case config.EncoderOffset:
		return value - 64
	case config.EncoderSignBit:
		if value >= 64 {
			return -(value - 64)
		}
		return value
	default: // two's complement
		if value >= 64 {
			return value - 128
		}
		return value
	}
}

// Handle processes one message. learned is set when learn mode captured it;
// learn mode then turns off.
func (e *Engine) Handle(m midi.Message) (hit Hit, learned *Learned) {
	ctl, ok := ControlOf(m)
	if !ok {
		return Hit{}, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	target := e.learningLocked()
	name, isMod := e.modifierFor(ctl, target)

	// Button state is tracked before anything else, learn included, so a
	// release is never lost (Fn stuck held) and a press is never missed
	// A cc button counts as a button only while it is a modifier, but its
	// release always clears it, even if it stopped being one meanwhile
	ccButton := ctl.Type == "cc" && (isMod || (e.down[ctl] && !pressed(m)))
	if m.Kind == midi.NoteOn || m.Kind == midi.NoteOff || ccButton {
		if pressed(m) {
			e.down[ctl] = true
		} else {
			delete(e.down, ctl)
		}
	}
	if isMod && !pressed(m) {
		return Hit{Modifier: name, Held: false}, nil
	}
	if target == LearnModifier {
		return e.capture(m, ctl, e.profile.Modifiers)
	}
	if isMod {
		return Hit{Modifier: name, Held: true}, nil
	}
	if target == LearnBinding {
		mods := e.learnMods
		if mods == nil {
			mods = e.profile.Modifiers
		}
		return e.capture(m, ctl, mods)
	}

	b := e.match(ctl)
	if b == nil {
		return Hit{}, nil
	}
	hit = Hit{Binding: b.ID}
	switch b.Action {
	case config.ActionScrub:
		if d := Delta(m.Value, b.Encoder); d != 0 {
			hit.Command = &Command{Type: "scrub", Frames: d * b.Step}
		}
	case config.ActionZoom:
		if d := Delta(m.Value, b.Encoder); d != 0 {
			hit.Command = &Command{Type: "zoom", Clicks: d * b.Step}
		}
	case config.ActionCut:
		// Once per press: a note off (or a cc below 64) is the release
		if pressed(m) {
			hit.Command = &Command{Type: "cut"}
		}
	case config.ActionUndo:
		// Once per press, like cut: never on the release
		if pressed(m) {
			hit.Command = &Command{Type: "undo"}
		}
	}
	return hit, nil
}

func (e *Engine) capture(m midi.Message, ctl config.Control, mods []config.Modifier) (Hit, *Learned) {
	// A button release isn't a choice
	if m.Kind == midi.NoteOff {
		return Hit{}, nil
	}
	e.learn = LearnOff
	e.learnMods = nil
	return Hit{}, &Learned{Control: ctl, Held: e.heldOf(mods)}
}

// match picks the binding for ctl: one whose modifier is held wins over one
// with no modifier. Bindings on a modifier the profile doesn't define, or
// on a control not learned yet, never match.
func (e *Engine) match(ctl config.Control) *config.Binding {
	var plain *config.Binding
	for i := range e.profile.Bindings {
		b := &e.profile.Bindings[i]
		if !b.Control.Learned() || b.Control != ctl {
			continue
		}
		if b.Modifier == "" {
			if plain == nil {
				plain = b
			}
			continue
		}
		if e.isHeld(b.Modifier) {
			return b
		}
	}
	return plain
}

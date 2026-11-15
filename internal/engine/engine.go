// Package engine turns MIDI messages into Blender commands using the active
// profile, tracks held modifiers, and captures controls in learn mode.
package engine

import (
	"sync"

	"github.com/alexandreffaria/blendeck/internal/config"
	"github.com/alexandreffaria/blendeck/internal/midi"
)

// Command is what the bridge sends to Blender.
type Command struct {
	Type   string `json:"type"`
	Frames int    `json:"frames,omitempty"`
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
	mu       sync.Mutex
	profile  config.Profile
	held     map[string]bool
	learning bool
}

// New makes an Engine running profile p.
func New(p config.Profile) *Engine {
	e := &Engine{held: map[string]bool{}}
	e.SetProfile(p)
	return e
}

// SetProfile switches to p. Modifiers still held in both stay held.
func (e *Engine) SetProfile(p config.Profile) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.profile = p
	held := map[string]bool{}
	for _, m := range p.Modifiers {
		if e.held[m.Name] {
			held[m.Name] = true
		}
	}
	e.held = held
}

// Held lists the modifiers currently held down.
func (e *Engine) Held() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := []string{}
	for _, m := range e.profile.Modifiers {
		if e.held[m.Name] {
			out = append(out, m.Name)
		}
	}
	return out
}

// Learn arms learn mode: the next message (other than a release) is returned
// by Handle as learned instead of being acted on.
func (e *Engine) Learn(on bool) {
	e.mu.Lock()
	e.learning = on
	e.mu.Unlock()
}

// Learning reports whether learn mode is armed.
func (e *Engine) Learning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.learning
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
func (e *Engine) Handle(m midi.Message) (hit Hit, learned *config.Control) {
	ctl, ok := ControlOf(m)
	if !ok {
		return Hit{}, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.learning {
		// A button release (or a knob coming to rest) isn't a choice
		if m.Kind == midi.NoteOff {
			return Hit{}, nil
		}
		e.learning = false
		return Hit{}, &ctl
	}

	for _, mod := range e.profile.Modifiers {
		if mod.Control.Learned() && mod.Control == ctl {
			down := pressed(m)
			e.held[mod.Name] = down
			return Hit{Modifier: mod.Name, Held: down}, nil
		}
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
	}
	return hit, nil
}

// match picks the binding for ctl: one whose modifier is held wins over one
// with no modifier. Bindings on a modifier the profile doesn't define, or
// on a control not learned yet, never match.
func (e *Engine) match(ctl config.Control) *config.Binding {
	defined := map[string]bool{}
	for _, mod := range e.profile.Modifiers {
		if mod.Control.Learned() {
			defined[mod.Name] = true
		}
	}
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
		if defined[b.Modifier] && e.held[b.Modifier] {
			return b
		}
	}
	return plain
}

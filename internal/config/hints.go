package config

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Limits on the hints sent to the add-on. With every text at its longest
// and every character at its longest in JSON (6 bytes), the whole message
// stays under the link's 64 KiB line: 40 hints of 6 texts of 40 characters.
const (
	// MaxHints caps how many controls the hints list.
	MaxHints = 40
	// MaxHintHeld caps how many modifiers one control's hint lists.
	MaxHintHeld = 2
	// MaxHintLen caps each text in a hint, in characters. Binding and
	// modifier names have no cap of their own.
	MaxHintLen = 40
)

// Hint says what one control does in a profile: a line of the list the
// add-on shows when the user has forgotten.
type Hint struct {
	// Control is the user's name for the control, or "CC 42" without one.
	Control string `json:"control"`
	// Name is the binding with no modifier held; empty when the control
	// only does something with one.
	Name string `json:"name,omitempty"`
	// Held is what it does with each modifier held.
	Held []HeldHint `json:"held,omitempty"`
}

// HeldHint is what a control does while a modifier is held.
type HeldHint struct {
	Modifier string `json:"modifier"`
	Name     string `json:"name"`
}

// Hints lists what each control of the active profile does, in the order
// the bindings are in, one hint per control. Like the engine, it takes the
// first binding of a control for each modifier and skips controls not
// learned yet and modifiers the profile doesn't define. Past MaxHints
// controls the rest are left out.
func (c Config) Hints() []Hint {
	p := c.Active()
	if p == nil {
		return nil
	}
	names := map[Control]string{}
	for _, n := range c.Controls {
		if name := strings.TrimSpace(n.Name); name != "" {
			names[n.Control] = name
		}
	}
	mods := map[string]bool{}
	for _, m := range p.Modifiers {
		if m.Control.Learned() {
			mods[m.Name] = true
		}
	}
	var hints []Hint
	at := map[Control]int{}
	for _, b := range p.Bindings {
		if !b.Control.Learned() || (b.Modifier != "" && !mods[b.Modifier]) {
			continue
		}
		i, ok := at[b.Control]
		if !ok {
			if len(hints) >= MaxHints {
				continue
			}
			label := names[b.Control]
			if label == "" {
				label = rawControl(b.Control)
			}
			i = len(hints)
			at[b.Control] = i
			hints = append(hints, Hint{Control: clip(label)})
		}
		h := &hints[i]
		name := strings.TrimSpace(b.Name)
		if name == "" {
			name = b.Action
		}
		if b.Modifier == "" {
			if h.Name == "" {
				h.Name = clip(name)
			}
			continue
		}
		if len(h.Held) >= MaxHintHeld || heldHas(h.Held, clip(b.Modifier)) {
			continue
		}
		h.Held = append(h.Held, HeldHint{Modifier: clip(b.Modifier), Name: clip(name)})
	}
	return hints
}

func heldHas(held []HeldHint, modifier string) bool {
	for _, h := range held {
		if h.Modifier == modifier {
			return true
		}
	}
	return false
}

// rawControl names a control by its MIDI number, as the page does.
func rawControl(k Control) string {
	switch k.Type {
	case "cc":
		return "CC " + strconv.Itoa(k.Number)
	case "note":
		return "Note " + strconv.Itoa(k.Number)
	}
	return "Bend"
}

// clip cuts s to MaxHintLen characters and drops invalid UTF-8.
func clip(s string) string {
	s = strings.ToValidUTF8(s, "")
	if utf8.RuneCountInString(s) <= MaxHintLen {
		return s
	}
	return string([]rune(s)[:MaxHintLen-1]) + "…"
}

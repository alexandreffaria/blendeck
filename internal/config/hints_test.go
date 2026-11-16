package config

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func cc(n int) Control   { return Control{Type: "cc", Channel: 1, Number: n} }
func note(n int) Control { return Control{Type: "note", Channel: 1, Number: n} }

func TestHints(t *testing.T) {
	cfg := Config{
		ActiveProfile: "Edit",
		Profiles: []Profile{{Name: "Other", Bindings: []Binding{{ID: "x", Name: "Not shown", Control: note(1), Action: ActionCut}}}, {
			Name:      "Edit",
			Modifiers: []Modifier{{Name: "Fn", Control: note(110)}, {Name: "Unlearned", Control: note(-1)}},
			Bindings: []Binding{
				{ID: "a", Name: "Scrub timeline", Control: cc(42), Action: ActionScrub, Step: 10},
				{ID: "b", Name: "Cut at playhead", Control: note(53), Action: ActionCut},
				{ID: "c", Name: "Scrub timeline (fine)", Control: cc(42), Modifier: "Fn", Action: ActionScrub, Step: 1},
				{ID: "d", Name: "Second plain binding never runs", Control: cc(42), Action: ActionZoom, Step: 1},
				{ID: "e", Name: "Not learned", Control: cc(-1), Action: ActionZoom, Step: 1},
				{ID: "f", Name: "Modifier not defined", Control: note(53), Modifier: "Shift", Action: ActionUndo},
				{ID: "g", Name: "Modifier not learned", Control: note(53), Modifier: "Unlearned", Action: ActionUndo},
				{ID: "h", Name: "Jump between all cuts", Control: cc(48), Modifier: "Fn", Action: ActionJumpCut},
				{ID: "i", Name: " ", Control: note(49), Action: ActionSave},
			},
		}},
		Controls: []ControlName{{Control: cc(42), Name: "D2"}, {Control: note(53), Name: "C5"}},
	}
	want := []Hint{
		{Control: "D2", Name: "Scrub timeline", Held: []HeldHint{{Modifier: "Fn", Name: "Scrub timeline (fine)"}}},
		{Control: "C5", Name: "Cut at playhead"},
		{Control: "CC 48", Held: []HeldHint{{Modifier: "Fn", Name: "Jump between all cuts"}}},
		{Control: "Note 49", Name: ActionSave},
	}
	if got := cfg.Hints(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Hints() =\n%+v\nwant\n%+v", got, want)
	}
	if got := (Config{ActiveProfile: "gone"}).Hints(); got != nil {
		t.Fatalf("no active profile: %+v", got)
	}
}

// Names have no cap of their own and the add-on drops a line over 64 KiB:
// the worst config must still fit, with room for the rest of the message.
func TestHintsFitOneLine(t *testing.T) {
	long := strings.Repeat("<", 500) // 6 bytes each in Go's JSON
	p := Profile{Name: "Edit"}
	for i := 0; i < 8; i++ {
		p.Modifiers = append(p.Modifiers, Modifier{Name: fmt.Sprintf("%d%s", i, long), Control: note(100 + i)})
	}
	var names []ControlName
	for i := 0; i < 100; i++ {
		names = append(names, ControlName{Control: cc(i), Name: long[:MaxControlNameLen]})
		p.Bindings = append(p.Bindings, Binding{ID: fmt.Sprint("p", i), Name: long, Control: cc(i), Action: ActionZoom, Step: 1})
		for m := range p.Modifiers {
			p.Bindings = append(p.Bindings, Binding{ID: fmt.Sprint("m", i, "-", m), Name: long, Control: cc(i), Modifier: p.Modifiers[m].Name, Action: ActionZoom, Step: 1})
		}
	}
	cfg := Config{ActiveProfile: "Edit", Profiles: []Profile{p}, Controls: names}
	hints := cfg.Hints()
	if len(hints) != MaxHints {
		t.Fatalf("%d hints, want %d", len(hints), MaxHints)
	}
	for _, h := range hints {
		if len(h.Held) != MaxHintHeld {
			t.Fatalf("%d held, want %d", len(h.Held), MaxHintHeld)
		}
		for _, s := range []string{h.Control, h.Name, h.Held[0].Modifier, h.Held[0].Name} {
			if n := len([]rune(s)); n > MaxHintLen {
				t.Fatalf("a text of %d characters", n)
			}
		}
	}
	line, err := json.Marshal(hints)
	if err != nil {
		t.Fatal(err)
	}
	if len(line) > 63*1024 {
		t.Fatalf("the worst hints are %d bytes", len(line))
	}
}

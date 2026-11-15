package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultIsValid(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]func(c *Config){
		"no profiles":     func(c *Config) { c.Profiles = nil },
		"missing active":  func(c *Config) { c.ActiveProfile = "nope" },
		"duplicate name":  func(c *Config) { c.Profiles = append(c.Profiles, c.Profiles[0]) },
		"empty name":      func(c *Config) { c.Profiles[0].Name = " "; c.ActiveProfile = " " },
		"step zero":       func(c *Config) { c.Profiles[0].Bindings[0].Step = 0 },
		"step huge":       func(c *Config) { c.Profiles[0].Bindings[0].Step = MaxStep + 1 },
		"bad encoder":     func(c *Config) { c.Profiles[0].Bindings[0].Encoder = "x" },
		"bad action":      func(c *Config) { c.Profiles[0].Bindings[0].Action = "x" },
		"bad channel":     func(c *Config) { c.Profiles[0].Bindings[0].Control.Channel = 17 },
		"bad number":      func(c *Config) { c.Profiles[0].Bindings[0].Control.Number = 128 },
		"bad type":        func(c *Config) { c.Profiles[0].Bindings[0].Control.Type = "sysex" },
		"scrub on button": func(c *Config) { c.Profiles[0].Bindings[0].Control.Type = "note" },
		"duplicate id":    func(c *Config) { c.Profiles[0].Bindings[1].ID = c.Profiles[0].Bindings[0].ID },
		"empty id":        func(c *Config) { c.Profiles[0].Bindings[0].ID = "" },
		"pitchbend fn": func(c *Config) {
			c.Profiles[0].Modifiers = []Modifier{{Name: "Fn", Control: Control{"pitch_bend", 1, 0}}}
		},
		"duplicate mod": func(c *Config) {
			m := Modifier{"Fn", Control{"note", 1, 1}}
			c.Profiles[0].Modifiers = []Modifier{m, m}
		},
		"zoom step zero":    func(c *Config) { zoom(c).Step = 0 },
		"zoom step huge":    func(c *Config) { zoom(c).Step = MaxZoomStep + 1 },
		"zoom on button":    func(c *Config) { zoom(c).Control.Type = "note" },
		"zoom pitchbend":    func(c *Config) { zoom(c).Control = Control{"pitch_bend", 1, 0} },
		"zoom no encoder":   func(c *Config) { zoom(c).Encoder = "" },
		"cut pitchbend":     func(c *Config) { cut(c).Control = Control{"pitch_bend", 1, 0} },
		"cut with step":     func(c *Config) { cut(c).Step = 1 },
		"cut with encoder":  func(c *Config) { cut(c).Encoder = EncoderTwos },
		"undo pitchbend":    func(c *Config) { undo(c).Control = Control{"pitch_bend", 1, 0} },
		"undo with step":    func(c *Config) { undo(c).Step = 1 },
		"undo with encoder": func(c *Config) { undo(c).Encoder = EncoderTwos },
		"too many mods":     func(c *Config) { c.Profiles[0].Modifiers = make([]Modifier, MaxModifiers+1) },
		"too many profile":  func(c *Config) { c.Profiles = make([]Profile, MaxProfiles+1) },
	}
	for name, mutate := range cases {
		c := Default()
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// zoom returns the default profile's plain zoom binding.
func zoom(c *Config) *Binding {
	for i := range c.Profiles[0].Bindings {
		if c.Profiles[0].Bindings[i].ID == "zoom" {
			return &c.Profiles[0].Bindings[i]
		}
	}
	panic("no zoom binding in the default config")
}

// cut returns the default profile's cut binding.
func cut(c *Config) *Binding {
	for i := range c.Profiles[0].Bindings {
		if c.Profiles[0].Bindings[i].ID == "cut" {
			return &c.Profiles[0].Bindings[i]
		}
	}
	panic("no cut binding in the default config")
}

func TestCutBinding(t *testing.T) {
	c := Default()
	k := cut(&c)
	if k.Action != ActionCut || k.Control.Learned() || k.Control.Type != "note" || k.Modifier != "" || k.Step != 0 || k.Encoder != "" {
		t.Fatalf("default cut binding = %+v", k)
	}
	// The owner's button, and a cc used as a button, are both fine
	for _, ctl := range []Control{{"note", 1, 53}, {"cc", 1, 20}} {
		k.Control = ctl
		if err := c.Validate(); err != nil {
			t.Fatalf("%+v: %v", ctl, err)
		}
	}
	k.Control = Control{"pitch_bend", 1, 0}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "needs a button") {
		t.Fatalf("err = %v", err)
	}
}

// undo returns the default profile's undo binding.
func undo(c *Config) *Binding {
	for i := range c.Profiles[0].Bindings {
		if c.Profiles[0].Bindings[i].ID == "undo" {
			return &c.Profiles[0].Bindings[i]
		}
	}
	panic("no undo binding in the default config")
}

func TestUndoBinding(t *testing.T) {
	c := Default()
	u := undo(&c)
	if u.Action != ActionUndo || u.Name != "Undo" || u.Control.Learned() || u.Control.Type != "note" || u.Modifier != "" || u.Step != 0 || u.Encoder != "" {
		t.Fatalf("default undo binding = %+v", u)
	}
	// The owner's button (note 95), and a cc used as a button, are fine
	for _, ctl := range []Control{{"note", 1, 95}, {"cc", 1, 20}} {
		u.Control = ctl
		if err := c.Validate(); err != nil {
			t.Fatalf("%+v: %v", ctl, err)
		}
	}
	u.Control = Control{"pitch_bend", 1, 0}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "undo needs a button") {
		t.Fatalf("err = %v", err)
	}
	u.Control = Control{"note", 1, 95}
	u.Step = 2
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "undo has no step") {
		t.Fatalf("err = %v", err)
	}
}

func TestZoomBindings(t *testing.T) {
	c := Default()
	z := zoom(&c)
	if z.Action != ActionZoom || z.Control.Learned() || z.Step != 4 {
		t.Fatalf("default zoom binding = %+v", z)
	}
	for _, step := range []int{1, MaxZoomStep} {
		z.Step = step
		z.Control.Number = 42
		if err := c.Validate(); err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
	}
	// A zoom step is far bigger than a frame: a scrub-sized step is refused
	z.Step = 10 * MaxZoomStep
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "zoom steps per click") {
		t.Fatalf("err = %v", err)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Get()
	c.Profiles[0].Bindings[0].Control.Number = 7
	if err := s.Set(c); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Get().Profiles[0].Bindings[0].Control.Number; got != 7 {
		t.Fatalf("number = %d after reload", got)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temp file left behind")
	}
}

func TestStoreRejectsInvalidAndKeepsOld(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "c.json"))
	bad := s.Get()
	bad.Profiles = nil
	if err := s.Set(bad); err == nil {
		t.Fatal("expected error")
	}
	if len(s.Get().Profiles) != 1 {
		t.Fatal("invalid config replaced the old one")
	}
}

func TestOpenBadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(path, []byte("{nope"), 0o644)
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "c.json") {
		t.Fatalf("err = %v", err)
	}
}

func TestGetIsACopy(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "c.json"))
	c := s.Get()
	c.Profiles[0].Bindings[0].Step = 99
	if s.Get().Profiles[0].Bindings[0].Step == 99 {
		t.Fatal("Get shares memory with the store")
	}
}

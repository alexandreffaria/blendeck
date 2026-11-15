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
		"too many profile": func(c *Config) { c.Profiles = make([]Profile, MaxProfiles+1) },
	}
	for name, mutate := range cases {
		c := Default()
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
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

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
		"ripple pitchbend":  func(c *Config) { ripple(c).Control = Control{"pitch_bend", 1, 0} },
		"ripple with step":  func(c *Config) { ripple(c).Step = 1 },
		"ripple encoder":    func(c *Config) { ripple(c).Encoder = EncoderTwos },
		"play pitchbend":    func(c *Config) { play(c).Control = Control{"pitch_bend", 1, 0} },
		"play with step":    func(c *Config) { play(c).Step = 1 },
		"play encoder":      func(c *Config) { play(c).Encoder = EncoderTwos },
		"forward pitchbend": func(c *Config) { byID(c, "forward").Control = Control{"pitch_bend", 1, 0} },
		"forward with step": func(c *Config) { byID(c, "forward").Step = 1 },
		"reverse encoder":   func(c *Config) { byID(c, "reverse").Encoder = EncoderTwos },
		"reverse knob only": func(c *Config) { byID(c, "reverse").Control = Control{"pitch_bend", 1, 0} },
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

// ripple returns the default profile's ripple delete binding.
func ripple(c *Config) *Binding {
	for i := range c.Profiles[0].Bindings {
		if c.Profiles[0].Bindings[i].ID == "ripple-delete" {
			return &c.Profiles[0].Bindings[i]
		}
	}
	panic("no ripple delete binding in the default config")
}

func TestRippleDeleteBinding(t *testing.T) {
	c := Default()
	r := ripple(&c)
	if r.Action != ActionRippleDelete || r.Name != "Ripple delete" || r.Control.Learned() || r.Control.Type != "note" || r.Modifier != "" || r.Step != 0 || r.Encoder != "" {
		t.Fatalf("default ripple delete binding = %+v", r)
	}
	// The owner's button (note 54), and a cc used as a button, are both fine
	for _, ctl := range []Control{{"note", 1, 54}, {"cc", 1, 20}} {
		r.Control = ctl
		if err := c.Validate(); err != nil {
			t.Fatalf("%+v: %v", ctl, err)
		}
	}
	// With Fn too
	r.Modifier = "Fn"
	if err := c.Validate(); err != nil {
		t.Fatalf("with Fn: %v", err)
	}
	r.Modifier = ""
	r.Control = Control{"pitch_bend", 1, 0}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "ripple delete needs a button") {
		t.Fatalf("err = %v", err)
	}
	r.Control = Control{"note", 1, 54}
	r.Step = 4
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "no step or encoder") {
		t.Fatalf("step err = %v", err)
	}
}

// No Fn variant of ripple delete (decision 202611156qj4): the only ripple
// delete binding has no modifier.
func TestNoFnRippleDeleteBinding(t *testing.T) {
	c := Default()
	n := 0
	for _, b := range c.Profiles[0].Bindings {
		if b.Action == ActionRippleDelete {
			n++
			if b.Modifier != "" {
				t.Fatalf("ripple delete binding with a modifier: %+v", b)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d ripple delete bindings, want 1", n)
	}
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

// play returns the default profile's play / pause binding.
func play(c *Config) *Binding {
	for i := range c.Profiles[0].Bindings {
		if c.Profiles[0].Bindings[i].ID == "play-pause" {
			return &c.Profiles[0].Bindings[i]
		}
	}
	panic("no play / pause binding in the default config")
}

func TestPlayPauseBinding(t *testing.T) {
	c := Default()
	b := play(&c)
	if b.Action != ActionPlayPause || b.Name != "Play / pause" || b.Control.Learned() || b.Control.Type != "note" || b.Modifier != "" || b.Step != 0 || b.Encoder != "" {
		t.Fatalf("default play / pause binding = %+v", b)
	}
	// The owner's button (note 67), and a cc used as a button, are both fine
	for _, ctl := range []Control{{"note", 1, 67}, {"cc", 1, 20}} {
		b.Control = ctl
		if err := c.Validate(); err != nil {
			t.Fatalf("%+v: %v", ctl, err)
		}
	}
	b.Modifier = "Fn"
	if err := c.Validate(); err != nil {
		t.Fatalf("with Fn: %v", err)
	}
	b.Modifier = ""
	b.Control = Control{"pitch_bend", 1, 0}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "play / pause needs a button") {
		t.Fatalf("err = %v", err)
	}
	b.Control = Control{"note", 1, 67}
	b.Step = 4
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "no step or encoder") {
		t.Fatalf("step err = %v", err)
	}
}

// One play / pause binding by default, with no modifier: Fn + the button
// plays or pauses too.
func TestOnePlayPauseBinding(t *testing.T) {
	c := Default()
	n := 0
	for _, b := range c.Profiles[0].Bindings {
		if b.Action == ActionPlayPause {
			n++
			if b.Modifier != "" {
				t.Fatalf("play / pause binding with a modifier: %+v", b)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d play / pause bindings, want 1", n)
	}
}

// byID returns the default profile's binding with that id.
func byID(c *Config, id string) *Binding {
	for i := range c.Profiles[0].Bindings {
		if c.Profiles[0].Bindings[i].ID == id {
			return &c.Profiles[0].Bindings[i]
		}
	}
	panic("no binding " + id + " in the default config")
}

func TestShuttleBindings(t *testing.T) {
	c := Default()
	for id, want := range map[string]struct{ action, name, what string }{
		"forward": {ActionForward, "Play forward / faster", "play forward"},
		"reverse": {ActionReverse, "Play backwards / faster", "play backwards"},
	} {
		b := byID(&c, id)
		if b.Action != want.action || b.Name != want.name || b.Control.Learned() || b.Control.Type != "note" || b.Modifier != "" || b.Step != 0 || b.Encoder != "" {
			t.Fatalf("default %s binding = %+v", id, b)
		}
		// The owner's buttons (notes 66 and 68), and a cc used as a button
		for _, ctl := range []Control{{"note", 1, 66}, {"note", 1, 68}, {"cc", 1, 20}} {
			b.Control = ctl
			if err := c.Validate(); err != nil {
				t.Fatalf("%s %+v: %v", id, ctl, err)
			}
		}
		b.Control = Control{"pitch_bend", 1, 0}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), want.what+" needs a button") {
			t.Fatalf("%s err = %v", id, err)
		}
		b.Control = Control{"note", 1, 68}
		b.Step = 2
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "no step or encoder") {
			t.Fatalf("%s step err = %v", id, err)
		}
		b.Step = 0
	}
}

func TestJumpEditBinding(t *testing.T) {
	c := Default()
	b := byID(&c, "jump-edit")
	if b.Action != ActionJumpEdit || b.Name != "Jump between video edits" || b.Control.Learned() || b.Control.Type != "cc" || b.Modifier != "" || b.Step != 0 || b.Encoder != EncoderTwos {
		t.Fatalf("default jump-edit binding = %+v", b)
	}
	// Fn + the same knob stops at every cut, sound included (decision 20261115s2rt)
	all := byID(&c, "jump-cut")
	if all.Action != ActionJumpCut || all.Name != "Jump between all cuts" || all.Control.Learned() || all.Control.Type != "cc" || all.Modifier != "Fn" || all.Step != 0 || all.Encoder != EncoderTwos {
		t.Fatalf("default jump-cut binding = %+v", all)
	}
	all.Control = Control{"cc", 1, 48}
	// The owner's knob: cc 48, 1 clockwise and 127 counter-clockwise
	b.Control = Control{"cc", 1, 48}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		mod  func(*Binding)
		want string
	}{
		"on a button": {func(b *Binding) { b.Control = Control{"note", 1, 48} }, "needs a knob"},
		"pitch bend":  {func(b *Binding) { b.Control = Control{"pitch_bend", 1, 0} }, "needs a knob"},
		"with a step": {func(b *Binding) { b.Step = 2 }, "has no step"},
		"no encoder":  {func(b *Binding) { b.Encoder = "" }, "unknown encoder"},
		"odd encoder": {func(b *Binding) { b.Encoder = "absolute" }, "unknown encoder"},
	} {
		for _, id := range []string{"jump-edit", "jump-cut"} {
			c := Default()
			b := byID(&c, id)
			b.Control = Control{"cc", 1, 48}
			tc.mod(b)
			if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s %s: err = %v", id, name, err)
			}
		}
	}
}

func TestMoveBindings(t *testing.T) {
	c := Default()
	// 10 frames a click, 1 with Fn held; moving earlier strips has no
	// default control but can still be bound
	after, fine := byID(&c, "move-after"), byID(&c, "move-after-fine")
	if after.Action != ActionMoveAfter || after.Control.Learned() || after.Control.Type != "cc" || after.Modifier != "" || after.Step != 10 || after.Encoder != EncoderTwos {
		t.Fatalf("default move-after binding = %+v", after)
	}
	if fine.Action != ActionMoveAfter || fine.Control.Learned() || fine.Modifier != "Fn" || fine.Step != 1 || fine.Encoder != EncoderTwos {
		t.Fatalf("default move-after-fine binding = %+v", fine)
	}
	after.Control = Control{"cc", 1, 36}
	fine.Control = Control{"cc", 1, 36}
	c.Profiles[0].Bindings = append(c.Profiles[0].Bindings, Binding{ID: "before", Name: "Move earlier", Control: Control{"cc", 1, 37}, Encoder: EncoderTwos, Action: ActionMoveBefore, Step: 1})
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		mod  func(*Binding)
		want string
	}{
		"on a button": {func(b *Binding) { b.Control = Control{"note", 1, 36} }, "needs a knob"},
		"no step":     {func(b *Binding) { b.Step = 0 }, "frames per click"},
		"huge step":   {func(b *Binding) { b.Step = MaxStep + 1 }, "frames per click"},
		"no encoder":  {func(b *Binding) { b.Encoder = "" }, "unknown encoder"},
	} {
		for _, id := range []string{"move-after", "move-after-fine", "trim"} {
			c := Default()
			b := byID(&c, id)
			b.Control = Control{"cc", 1, 36}
			tc.mod(b)
			if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s %s: err = %v", id, name, err)
			}
		}
	}
}

func TestTrimBinding(t *testing.T) {
	c := Default()
	b := byID(&c, "trim")
	if b.Action != ActionTrim || b.Name != "Trim handle at playhead" || b.Control.Learned() || b.Control.Type != "cc" || b.Modifier != "" || b.Step != 1 || b.Encoder != EncoderTwos {
		t.Fatalf("default trim binding = %+v", b)
	}
	b.Control = Control{"cc", 1, 33}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestTrimShrinkBinding(t *testing.T) {
	c := Default()
	b := byID(&c, "trim-shrink")
	if b.Action != ActionTrimShrink || b.Modifier != "Fn" || b.Control.Learned() || b.Control.Type != "cc" || b.Step != 1 || b.Encoder != EncoderTwos {
		t.Fatalf("default trim-shrink binding = %+v", b)
	}
	b.Control = Control{"cc", 1, 33}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	b.Control.Type = "note"
	if c.Validate() == nil {
		t.Fatal("shrinking on a button accepted")
	}
	b.Control.Type = "cc"
	b.Step = 0
	if c.Validate() == nil {
		t.Fatal("step 0 accepted")
	}
}

func TestPanBinding(t *testing.T) {
	c := Default()
	b := byID(&c, "pan")
	// Nothing on Fn: one plain knob, 5% of the view per click
	if b.Action != ActionPan || b.Name != "Pan timeline" || b.Control.Learned() || b.Control.Type != "cc" || b.Modifier != "" || b.Step != 5 || b.Encoder != EncoderTwos {
		t.Fatalf("default pan binding = %+v", b)
	}
	// The owner's knob: cc 35, 1 clockwise and 127 counter-clockwise
	b.Control = Control{"cc", 1, 35}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		mod  func(*Binding)
		want string
	}{
		"on a button": {func(b *Binding) { b.Control = Control{"note", 1, 35} }, "needs a knob"},
		"no step":     {func(b *Binding) { b.Step = 0 }, "percent of the view"},
		"over a view": {func(b *Binding) { b.Step = MaxPanStep + 1 }, "percent of the view"},
		"no encoder":  {func(b *Binding) { b.Encoder = "" }, "unknown encoder"},
	} {
		c := Default()
		b := byID(&c, "pan")
		b.Control = Control{"cc", 1, 35}
		tc.mod(b)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}

func TestViewAllBinding(t *testing.T) {
	c := Default()
	b := byID(&c, "view-all")
	// A plain button, nothing on Fn: Fn + the button views all too
	if b.Action != ActionViewAll || b.Name != "View whole timeline" || b.Control.Learned() || b.Control.Type != "note" || b.Modifier != "" || b.Step != 0 || b.Encoder != "" {
		t.Fatalf("default view-all binding = %+v", b)
	}
	// The owner's button (note 41), and a cc used as a button
	for _, ctl := range []Control{{"note", 1, 41}, {"cc", 1, 20}} {
		b.Control = ctl
		if err := c.Validate(); err != nil {
			t.Fatalf("%+v: %v", ctl, err)
		}
	}
	b.Control = Control{"pitch_bend", 1, 0}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "view whole timeline needs a button") {
		t.Fatalf("err = %v", err)
	}
	b.Control = Control{"note", 1, 41}
	b.Step = 1
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "no step or encoder") {
		t.Fatalf("step err = %v", err)
	}
	b.Step = 0
	b.Encoder = EncoderTwos
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "no step or encoder") {
		t.Fatalf("encoder err = %v", err)
	}
}

func TestSaveBinding(t *testing.T) {
	c := Default()
	b := byID(&c, "save")
	// A plain button, nothing on Fn: Fn + the button saves too
	if b.Action != ActionSave || b.Name != "Save file" || b.Control.Learned() || b.Control.Type != "note" || b.Modifier != "" || b.Step != 0 || b.Encoder != "" {
		t.Fatalf("default save binding = %+v", b)
	}
	// The owner's button (note 49), and a cc used as a button
	for _, ctl := range []Control{{"note", 1, 49}, {"cc", 1, 20}} {
		b.Control = ctl
		if err := c.Validate(); err != nil {
			t.Fatalf("%+v: %v", ctl, err)
		}
	}
	b.Control = Control{"pitch_bend", 1, 0}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "save file needs a button") {
		t.Fatalf("err = %v", err)
	}
	b.Control = Control{"note", 1, 49}
	b.Step = 1
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "no step or encoder") {
		t.Fatalf("step err = %v", err)
	}
	b.Step = 0
	b.Encoder = EncoderTwos
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "no step or encoder") {
		t.Fatalf("encoder err = %v", err)
	}
}

func TestControlNames(t *testing.T) {
	knob := Control{Type: "cc", Channel: 1, Number: 3}
	named := func(names ...ControlName) Config {
		c := Default()
		c.Controls = names
		return c
	}
	ok := named(ControlName{Control: knob, Name: "Knob 1"}, ControlName{Control: Control{Type: "pitch_bend", Channel: 2, Number: 0}, Name: "Wheel"})
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("é", MaxControlNameLen)
	if c := named(ControlName{Control: knob, Name: long}); c.Validate() != nil {
		t.Fatalf("%d characters should be allowed: %v", MaxControlNameLen, c.Validate())
	}
	many := make([]ControlName, MaxControlNames+1)
	for i := range many {
		many[i] = ControlName{Control: Control{Type: "cc", Channel: 1 + i/128, Number: i % 128}, Name: "k"}
	}
	bad := map[string]Config{
		"empty name":     named(ControlName{Control: knob, Name: "  "}),
		"padded name":    named(ControlName{Control: knob, Name: " Knob"}),
		"long name":      named(ControlName{Control: knob, Name: long + "x"}),
		"not learned":    named(ControlName{Control: Control{Type: "cc", Channel: 1, Number: -1}, Name: "Knob"}),
		"bad channel":    named(ControlName{Control: Control{Type: "cc", Channel: 0, Number: 3}, Name: "Knob"}),
		"bad type":       named(ControlName{Control: Control{Type: "sysex", Channel: 1, Number: 3}, Name: "Knob"}),
		"bend number":    named(ControlName{Control: Control{Type: "pitch_bend", Channel: 1, Number: 5}, Name: "Wheel"}),
		"named twice":    named(ControlName{Control: knob, Name: "A"}, ControlName{Control: knob, Name: "B"}),
		"too many names": named(many...),
	}
	for name, c := range bad {
		if c.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestControlNamesRoundTripAndCopy(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "c.json"))
	c := s.Get()
	c.Controls = []ControlName{{Control: Control{Type: "note", Channel: 1, Number: 7}, Name: "Circle"}}
	if err := s.Set(c); err != nil {
		t.Fatal(err)
	}
	got := s.Get()
	got.Controls[0].Name = "changed"
	s2, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range []Config{s.Get(), s2.Get()} {
		if len(g.Controls) != 1 || g.Controls[0].Name != "Circle" {
			t.Fatalf("names not kept, or Get shares memory: %+v", g.Controls)
		}
	}
}

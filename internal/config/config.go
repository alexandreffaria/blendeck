// Package config holds the profiles, bindings and settings, stored as JSON.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Control identifies one physical control by the messages it sends.
// Type is "cc", "note" or "pitch_bend". Channel is 1 to 16. Number is the CC
// or note number (0 for pitch bend).
type Control struct {
	Type    string `json:"type"`
	Channel int    `json:"channel"`
	Number  int    `json:"number"`
}

// Encoder says how a knob reports turns.
const (
	// EncoderTwos: 1..63 is that many clicks clockwise, 127..65 is 1..63
	// counter-clockwise (two's complement). Most controllers.
	EncoderTwos = "relative_twos"
	// EncoderOffset: 64 is still, 65 is one click clockwise, 63 one counter.
	EncoderOffset = "relative_offset"
	// EncoderSignBit: 1..63 clockwise, 65..127 is 1..63 counter-clockwise.
	EncoderSignBit = "relative_signbit"
)

// Action types.
const (
	// ActionScrub moves the playhead Step frames per encoder click.
	ActionScrub = "timeline.scrub"
	// ActionZoom zooms the video editor's timeline Step zoom steps per
	// encoder click: clockwise zooms in.
	ActionZoom = "timeline.zoom"
	// ActionPan slides the video editor's timeline view Step percent of its
	// width per encoder click: clockwise shows later frames. The playhead
	// and the strips stay put.
	ActionPan = "timeline.pan"
	// ActionCut cuts the strips under the playhead, once per button press.
	// It has no step and no encoder.
	ActionCut = "timeline.cut"
	// ActionUndo runs Blender's undo, once per button press. It has no step
	// and no encoder.
	ActionUndo = "edit.undo"
	// ActionRippleDelete deletes the strips under the playhead and closes
	// the gap they leave, once per button press. It has no step and no
	// encoder.
	ActionRippleDelete = "timeline.ripple_delete"
	// ActionPlayPause starts or pauses playback in the video editor, like
	// Space, once per button press. It has no step and no encoder.
	ActionPlayPause = "playback.toggle"
	// ActionForward plays forward, and each further press plays faster,
	// once per button press. It has no step and no encoder.
	ActionForward = "playback.forward"
	// ActionReverse plays backwards, and each further press plays faster
	// backwards, once per button press. It has no step and no encoder.
	ActionReverse = "playback.reverse"
	// ActionJumpEdit moves the playhead to the next video edit (the start or
	// end of a strip that isn't sound) per encoder click clockwise, the
	// previous one counter-clockwise; sound strips are skipped (decision
	// 20261115s2rt). It has an encoder and no step.
	ActionJumpEdit = "timeline.jump_edit"
	// ActionJumpCut is ActionJumpEdit stopping at every cut, sound strips
	// included. It has an encoder and no step.
	ActionJumpCut = "timeline.jump_cut"
	// ActionMoveAfter moves the selected strips and every strip after them,
	// on all channels, Step frames per encoder click (right clockwise), like
	// G in Blender; they stop against a strip that stays instead of going
	// over it.
	ActionMoveAfter = "timeline.move_after"
	// ActionMoveBefore is ActionMoveAfter for the selected strips and every
	// strip before them.
	ActionMoveBefore = "timeline.move_before"
)

// Modifier is a button that changes what other controls do while held (Fn).
type Modifier struct {
	Name    string  `json:"name"`
	Control Control `json:"control"`
}

// Binding maps a control, optionally with a modifier held, to an action.
type Binding struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Control  Control `json:"control"`
	Modifier string  `json:"modifier,omitempty"` // a Modifier's name, or "" for none
	Encoder  string  `json:"encoder,omitempty"`
	Action   string  `json:"action"`
	Step     int     `json:"step,omitempty"`
}

// Profile is a named set of modifiers and bindings.
type Profile struct {
	Name      string     `json:"name"`
	Modifiers []Modifier `json:"modifiers"`
	Bindings  []Binding  `json:"bindings"`
}

// Config is everything stored on disk.
type Config struct {
	Device        string    `json:"device"`
	ActiveProfile string    `json:"active_profile"`
	Profiles      []Profile `json:"profiles"`
}

// Limits on what a config may hold, so a bad file or request can't blow up.
const (
	MaxProfiles  = 64
	MaxBindings  = 512
	MaxModifiers = 32
	MaxStep      = 100000
	// MaxZoomStep caps zoom steps per click: each step is one zoom in
	// Blender, and a few already go from a second to the whole edit.
	MaxZoomStep = 20
	// MaxPanStep caps how far one click pans: a whole view width.
	MaxPanStep = 100
)

// Default is the config a fresh install starts with: one profile with the
// timeline bindings and an Fn modifier, all waiting to be learned.
func Default() Config {
	return Config{
		Device:        "Loupedeck+",
		ActiveProfile: "Video editing",
		Profiles: []Profile{{
			Name:      "Video editing",
			Modifiers: []Modifier{{Name: "Fn", Control: Control{Type: "note", Channel: 1, Number: -1}}},
			Bindings: []Binding{
				{ID: "scrub", Name: "Scrub timeline", Control: Control{Type: "cc", Channel: 1, Number: -1}, Encoder: EncoderTwos, Action: ActionScrub, Step: 10},
				{ID: "scrub-fine", Name: "Scrub timeline (fine)", Control: Control{Type: "cc", Channel: 1, Number: -1}, Modifier: "Fn", Encoder: EncoderTwos, Action: ActionScrub, Step: 1},
				{ID: "zoom", Name: "Zoom timeline", Control: Control{Type: "cc", Channel: 1, Number: -1}, Encoder: EncoderTwos, Action: ActionZoom, Step: 4},
				{ID: "zoom-fine", Name: "Zoom timeline (fine)", Control: Control{Type: "cc", Channel: 1, Number: -1}, Modifier: "Fn", Encoder: EncoderTwos, Action: ActionZoom, Step: 1},
				{ID: "pan", Name: "Pan timeline", Control: Control{Type: "cc", Channel: 1, Number: -1}, Encoder: EncoderTwos, Action: ActionPan, Step: 5},
				{ID: "cut", Name: "Cut at playhead", Control: Control{Type: "note", Channel: 1, Number: -1}, Action: ActionCut},
				{ID: "undo", Name: "Undo", Control: Control{Type: "note", Channel: 1, Number: -1}, Action: ActionUndo},
				{ID: "ripple-delete", Name: "Ripple delete", Control: Control{Type: "note", Channel: 1, Number: -1}, Action: ActionRippleDelete},
				{ID: "play-pause", Name: "Play / pause", Control: Control{Type: "note", Channel: 1, Number: -1}, Action: ActionPlayPause},
				{ID: "reverse", Name: "Play backwards / faster", Control: Control{Type: "note", Channel: 1, Number: -1}, Action: ActionReverse},
				{ID: "forward", Name: "Play forward / faster", Control: Control{Type: "note", Channel: 1, Number: -1}, Action: ActionForward},
				{ID: "jump-edit", Name: "Jump between video edits", Control: Control{Type: "cc", Channel: 1, Number: -1}, Encoder: EncoderTwos, Action: ActionJumpEdit},
				{ID: "jump-cut", Name: "Jump between all cuts", Control: Control{Type: "cc", Channel: 1, Number: -1}, Modifier: "Fn", Encoder: EncoderTwos, Action: ActionJumpCut},
				{ID: "move-after", Name: "Move selected and later strips", Control: Control{Type: "cc", Channel: 1, Number: -1}, Encoder: EncoderTwos, Action: ActionMoveAfter, Step: 1},
				{ID: "move-before", Name: "Move selected and earlier strips", Control: Control{Type: "cc", Channel: 1, Number: -1}, Modifier: "Fn", Encoder: EncoderTwos, Action: ActionMoveBefore, Step: 1},
			},
		}},
	}
}

// Active returns the active profile, or nil if there is none.
func (c *Config) Active() *Profile {
	for i := range c.Profiles {
		if c.Profiles[i].Name == c.ActiveProfile {
			return &c.Profiles[i]
		}
	}
	return nil
}

// Learned reports whether the control has been assigned (Number -1 means not yet).
func (k Control) Learned() bool { return k.Number >= 0 }

// Validate checks a config before it's saved or used.
func (c *Config) Validate() error {
	if len(c.Profiles) == 0 {
		return errors.New("at least one profile is needed")
	}
	if len(c.Profiles) > MaxProfiles {
		return fmt.Errorf("at most %d profiles", MaxProfiles)
	}
	names := map[string]bool{}
	for pi := range c.Profiles {
		p := &c.Profiles[pi]
		name := strings.TrimSpace(p.Name)
		if name == "" {
			return errors.New("every profile needs a name")
		}
		if names[strings.ToLower(name)] {
			return fmt.Errorf("two profiles are called %q", name)
		}
		names[strings.ToLower(name)] = true
		if len(p.Bindings) > MaxBindings {
			return fmt.Errorf("profile %q: at most %d bindings", name, MaxBindings)
		}
		if len(p.Modifiers) > MaxModifiers {
			return fmt.Errorf("profile %q: at most %d modifiers", name, MaxModifiers)
		}
		mods := map[string]bool{}
		for _, m := range p.Modifiers {
			if strings.TrimSpace(m.Name) == "" {
				return fmt.Errorf("profile %q: every modifier needs a name", name)
			}
			if mods[m.Name] {
				return fmt.Errorf("profile %q: two modifiers are called %q", name, m.Name)
			}
			mods[m.Name] = true
			if err := m.Control.validate(); err != nil {
				return fmt.Errorf("profile %q, modifier %q: %w", name, m.Name, err)
			}
			if m.Control.Type != "note" && m.Control.Type != "cc" {
				return fmt.Errorf("profile %q, modifier %q: a modifier must be a button (note or cc)", name, m.Name)
			}
		}
		ids := map[string]bool{}
		for _, b := range p.Bindings {
			if b.ID == "" {
				return fmt.Errorf("profile %q: a binding has no id", name)
			}
			if ids[b.ID] {
				return fmt.Errorf("profile %q: two bindings have id %q", name, b.ID)
			}
			ids[b.ID] = true
			if err := b.Control.validate(); err != nil {
				return fmt.Errorf("binding %q: %w", b.Name, err)
			}
			// A modifier not defined yet is allowed: the binding stays
			// inactive until the user learns that button.
			switch b.Action {
			case ActionScrub:
				if b.Control.Type != "cc" {
					return fmt.Errorf("binding %q: scrubbing needs a knob (cc)", b.Name)
				}
				if b.Step < 1 || b.Step > MaxStep {
					return fmt.Errorf("binding %q: frames per click must be 1 to %d", b.Name, MaxStep)
				}
				if err := validEncoder(b); err != nil {
					return err
				}
			case ActionMoveAfter, ActionMoveBefore:
				if b.Control.Type != "cc" {
					return fmt.Errorf("binding %q: moving strips needs a knob (cc)", b.Name)
				}
				if b.Step < 1 || b.Step > MaxStep {
					return fmt.Errorf("binding %q: frames per click must be 1 to %d", b.Name, MaxStep)
				}
				if err := validEncoder(b); err != nil {
					return err
				}
			case ActionZoom:
				if b.Control.Type != "cc" {
					return fmt.Errorf("binding %q: zooming needs a knob (cc)", b.Name)
				}
				if b.Step < 1 || b.Step > MaxZoomStep {
					return fmt.Errorf("binding %q: zoom steps per click must be 1 to %d", b.Name, MaxZoomStep)
				}
				if err := validEncoder(b); err != nil {
					return err
				}
			case ActionPan:
				if b.Control.Type != "cc" {
					return fmt.Errorf("binding %q: panning needs a knob (cc)", b.Name)
				}
				if b.Step < 1 || b.Step > MaxPanStep {
					return fmt.Errorf("binding %q: percent of the view per click must be 1 to %d", b.Name, MaxPanStep)
				}
				if err := validEncoder(b); err != nil {
					return err
				}
			case ActionJumpEdit, ActionJumpCut:
				if b.Control.Type != "cc" {
					return fmt.Errorf("binding %q: jumping needs a knob (cc)", b.Name)
				}
				if b.Step != 0 {
					return fmt.Errorf("binding %q: jumping has no step: each click is one edit or cut", b.Name)
				}
				if err := validEncoder(b); err != nil {
					return err
				}
			case ActionCut, ActionUndo, ActionRippleDelete, ActionPlayPause, ActionForward, ActionReverse:
				what := map[string]string{ActionCut: "cutting", ActionUndo: "undo", ActionRippleDelete: "ripple delete", ActionPlayPause: "play / pause", ActionForward: "play forward", ActionReverse: "play backwards"}[b.Action]
				if b.Control.Type != "note" && b.Control.Type != "cc" {
					return fmt.Errorf("binding %q: %s needs a button (note or cc)", b.Name, what)
				}
				if b.Step != 0 || b.Encoder != "" {
					return fmt.Errorf("binding %q: %s has no step or encoder", b.Name, what)
				}
			default:
				return fmt.Errorf("binding %q: unknown action %q", b.Name, b.Action)
			}
		}
	}
	if c.Active() == nil {
		return fmt.Errorf("active profile %q does not exist", c.ActiveProfile)
	}
	return nil
}

func validEncoder(b Binding) error {
	switch b.Encoder {
	case EncoderTwos, EncoderOffset, EncoderSignBit:
		return nil
	}
	return fmt.Errorf("binding %q: unknown encoder type %q", b.Name, b.Encoder)
}

func (k Control) validate() error {
	switch k.Type {
	case "cc", "note", "pitch_bend":
	default:
		return fmt.Errorf("unknown control type %q", k.Type)
	}
	if k.Channel < 1 || k.Channel > 16 {
		return errors.New("channel must be 1 to 16")
	}
	if k.Number < -1 || k.Number > 127 {
		return errors.New("number must be 0 to 127, or -1 for not learned")
	}
	return nil
}

// Clone returns a deep copy.
func (c Config) Clone() Config {
	out := c
	out.Profiles = make([]Profile, len(c.Profiles))
	for i, p := range c.Profiles {
		p.Modifiers = append([]Modifier{}, p.Modifiers...)
		p.Bindings = append([]Binding{}, p.Bindings...)
		out.Profiles[i] = p
	}
	return out
}

// Store reads and writes the config file and hands out copies.
type Store struct {
	path string
	mu   sync.Mutex
	cfg  Config
}

// Open loads path, or starts from Default if the file doesn't exist yet.
func Open(path string) (*Store, error) {
	s := &Store{path: path, cfg: Default()}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s.cfg = cfg
	return s, nil
}

// Path is where the config is stored.
func (s *Store) Path() string { return s.path }

// Get returns a copy of the current config.
func (s *Store) Get() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Clone()
}

// Set validates cfg and writes it to disk, replacing the old file atomically.
func (s *Store) Set(cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	s.cfg = cfg.Clone()
	return nil
}

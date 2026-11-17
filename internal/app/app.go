// Package app wires the MIDI connection, the mapping engine and the Blender
// link together, and keeps the recent-events log the page shows.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"

	"github.com/alexandreffaria/consort/internal/config"
	"github.com/alexandreffaria/consort/internal/engine"
	"github.com/alexandreffaria/consort/internal/link"
	"github.com/alexandreffaria/consort/internal/midi"
)

// EventLogSize is how many recent events the monitor keeps.
const EventLogSize = 300

// Event is one message from the controller and what it did.
type Event struct {
	Seq     int64        `json:"seq"`
	Time    time.Time    `json:"time"`
	Message midi.Message `json:"message"`
	Hit     engine.Hit   `json:"hit"`
	// Delivered is false when a command was made but no app was connected
	// to take it
	Delivered bool `json:"delivered"`
	// Switched names the app the Switch app button moved to
	Switched string `json:"switched,omitempty"`
}

// Learned is the control captured by the latest learn, and the modifiers
// held when it was captured.
type Learned struct {
	Seq     int64          `json:"seq"`
	Control config.Control `json:"control"`
	Held    []string       `json:"held"`
}

// Devices lists MIDI inputs; replaced in tests.
type Devices func() ([]string, error)

// App is the running bridge.
type App struct {
	log     *slog.Logger
	store   *config.Store
	conn    *midi.Conn
	engine  *engine.Engine
	link    *link.Link
	devices Devices
	run     string // changes every start, so the page can tell a restart

	cfgMu sync.Mutex // serializes SetConfig so disk and engine agree

	// hintMod is the modifier being held on its own, with nothing else
	// touched since its press at hintAt; "" when there is none. Once it has
	// been held for HintDelay the add-on shows the hints (hintShown).
	// switchUntil is when the hints shown for a press of Switch app go
	// again; shown says what the add-ons were last told.
	hintMu      sync.Mutex
	hintMod     string
	hintAt      time.Time
	hintShown   bool
	switchUntil time.Time
	shown       bool

	mu      sync.Mutex
	events  []Event // ring buffer, oldest first once full
	seq     int64
	learned Learned
}

// New makes an App from a loaded store.
func New(log *slog.Logger, store *config.Store, conn *midi.Conn, lnk *link.Link, devices Devices) *App {
	cfg := store.Get()
	lnk.SetHints(cfg.ActiveProfile, cfg.Hints())
	return &App{
		log:     log.With("component", "app"),
		store:   store,
		conn:    conn,
		engine:  engine.New(*cfg.Active()),
		link:    lnk,
		devices: devices,
		run:     newRunID(),
		learned: Learned{Held: []string{}},
	}
}

func newRunID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Run is this process's run id.
func (a *App) RunID() string { return a.run }

// HintDelay is how long a modifier is held on its own before Blender shows
// what the controls do (decision 202611167kkw). Long enough that reaching
// for Fn on the way to a fine turn rarely shows them. A variable so tests
// can shorten it.
var HintDelay = 500 * time.Millisecond

// SwitchShow is how long the hints, which name the app the controls drive,
// show after a press of Switch app. A variable so tests can shorten it.
var SwitchShow = 1500 * time.Millisecond

// hintOn follows one controller message for the hints: a modifier's press
// starts the wait, and anything else (its release, any other control,
// bound or not) ends it and hides the hints.
func (a *App) hintOn(m midi.Message, hit engine.Hit, now time.Time) {
	if _, ok := engine.ControlOf(m); !ok {
		return // not a control: aftertouch, a program change
	}
	a.hintMu.Lock()
	defer a.hintMu.Unlock()
	a.hintMod = ""
	// On its own: a second modifier pressed while one is held shows
	// nothing, and neither does letting go of one of the two
	if hit.Modifier != "" && hit.Held && len(a.engine.Held()) == 1 {
		a.hintMod, a.hintAt = hit.Modifier, now
	}
	a.hideHints()
}

// hintTick shows the hints once the modifier has been held long enough.
func (a *App) hintTick(now time.Time) {
	a.hintMu.Lock()
	defer a.hintMu.Unlock()
	a.showHints(now) // a switch's time may be up
	if a.hintMod == "" || a.hintShown || now.Sub(a.hintAt) < HintDelay {
		return
	}
	if a.engine.Learning() != engine.LearnOff {
		// The page is learning a control, whether that began before the
		// press or during the wait: this hold shows nothing
		a.hintMod = ""
		return
	}
	a.hintShown = true
	a.showHints(now)
}

// hintOff ends the wait and hides the hints: the device went away, so the
// modifier's release will never come.
func (a *App) hintOff() {
	a.hintMu.Lock()
	defer a.hintMu.Unlock()
	a.hintMod = ""
	a.hideHints()
}

// hideHints hides the hints a held modifier showed, if they are shown;
// those shown for a switch stay their time. The caller holds hintMu.
func (a *App) hideHints() {
	a.hintShown = false
	a.showHints(time.Now())
}

// showHints tells the add-ons to show the hints while a modifier has been
// held long enough or a switch is fresh, and to hide them otherwise. The
// caller holds hintMu.
func (a *App) showHints(now time.Time) {
	show := a.hintShown || now.Before(a.switchUntil)
	if show != a.shown {
		a.shown = show
		a.link.ShowHints(show)
	}
}

// switchApp moves the controls to the next connected app and shows the
// hints, which name it, for SwitchShow. It returns the app's name, "" when
// none is connected.
func (a *App) switchApp(now time.Time) string {
	_, name := a.link.Switch()
	if name == "" {
		return ""
	}
	a.hintMu.Lock()
	defer a.hintMu.Unlock()
	a.switchUntil = now.Add(SwitchShow)
	a.showHints(now)
	return name
}

// Run handles controller messages until ctx ends. When the device goes away,
// held buttons are released: their note-offs will never come.
func (a *App) Run(ctx context.Context, messages <-chan midi.Message) {
	check := time.NewTicker(250 * time.Millisecond)
	defer check.Stop()
	hint := time.NewTicker(50 * time.Millisecond)
	defer hint.Stop()
	wasConnected := false
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-messages:
			a.Handle(m)
		case now := <-hint.C:
			a.hintTick(now)
		case <-check.C:
			connected := a.conn.Status().State == "connected"
			if wasConnected && !connected {
				a.engine.ReleaseAll()
				a.hintOff()
			}
			wasConnected = connected
		}
	}
}

// Handle processes one controller message.
func (a *App) Handle(m midi.Message) {
	hit, learned := a.engine.Handle(m)
	a.hintOn(m, hit, time.Now())
	delivered, switched := false, ""
	switch {
	case hit.Command != nil && hit.Command.Type == engine.SwitchApp:
		switched = a.switchApp(time.Now())
		delivered = switched != ""
	case hit.Command != nil:
		delivered = a.link.Send(*hit.Command)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seq++
	ev := Event{Seq: a.seq, Time: time.Now(), Message: m, Hit: hit, Delivered: delivered, Switched: switched}
	if len(a.events) < EventLogSize {
		a.events = append(a.events, ev)
	} else {
		copy(a.events, a.events[1:])
		a.events[len(a.events)-1] = ev
	}
	if learned != nil {
		a.learned = Learned{Seq: a.seq, Control: learned.Control, Held: learned.Held}
	}
}

// EventsSince returns the logged events newer than seq, oldest first. A seq
// from the future (the page saw an earlier run) starts from the beginning.
func (a *App) EventsSince(seq int64) []Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	if seq > a.seq {
		seq = 0
	}
	out := []Event{}
	for _, e := range a.events {
		if e.Seq > seq {
			out = append(out, e)
		}
	}
	return out
}

// Snapshot is the live state the page polls.
type Snapshot struct {
	MIDI     midi.Status `json:"midi"`
	Dropped  int64       `json:"dropped"`
	Blender  link.State  `json:"blender"`
	Held     []string    `json:"held"`
	Run      string      `json:"run"`
	Learning string      `json:"learning"` // "", "binding" or "modifier"
	Learned  Learned     `json:"learned"`
	LastSeq  int64       `json:"last_seq"`
}

// Snapshot returns the live state.
func (a *App) Snapshot() Snapshot {
	a.mu.Lock()
	learned, seq := a.learned, a.seq
	a.mu.Unlock()
	return Snapshot{
		MIDI:     a.conn.Status(),
		Dropped:  a.conn.Dropped(),
		Blender:  a.link.State(),
		Held:     a.engine.Held(),
		Run:      a.run,
		Learning: string(a.engine.Learning()),
		Learned:  learned,
		LastSeq:  seq,
	}
}

// Learn arms learn mode for t, or cancels it with engine.LearnOff. mods are
// the page's current (maybe unsaved) modifiers for a binding learn; nil
// means the saved profile's.
func (a *App) Learn(t engine.Target, mods []config.Modifier) { a.engine.Learn(t, mods) }

// Config returns the saved config.
func (a *App) Config() config.Config { return a.store.Get() }

// SetConfig validates and saves cfg, then applies it.
func (a *App) SetConfig(cfg config.Config) error {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	if err := a.store.Set(cfg); err != nil {
		return err
	}
	a.engine.SetProfile(*cfg.Active())
	a.conn.SetDevice(cfg.Device)
	a.link.SetHints(cfg.ActiveProfile, cfg.Hints())
	return nil
}

// Devices lists the MIDI inputs.
func (a *App) Devices() ([]string, error) { return a.devices() }

// Test sends cmd to Blender as if a control had made it, whichever app the
// controls drive: the page's test buttons are Blender's.
func (a *App) Test(cmd engine.Command) bool { return a.link.SendTo(link.Blender, cmd) }

// ConfigPath is where the config is saved.
func (a *App) ConfigPath() string { return a.store.Path() }

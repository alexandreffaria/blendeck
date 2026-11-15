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

	"github.com/alexandreffaria/blendeck/internal/config"
	"github.com/alexandreffaria/blendeck/internal/engine"
	"github.com/alexandreffaria/blendeck/internal/link"
	"github.com/alexandreffaria/blendeck/internal/midi"
)

// EventLogSize is how many recent events the monitor keeps.
const EventLogSize = 300

// Event is one message from the controller and what it did.
type Event struct {
	Seq     int64        `json:"seq"`
	Time    time.Time    `json:"time"`
	Message midi.Message `json:"message"`
	Hit     engine.Hit   `json:"hit"`
	// Delivered is false when a command was made but no Blender was connected
	Delivered bool `json:"delivered"`
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

	mu      sync.Mutex
	events  []Event // ring buffer, oldest first once full
	seq     int64
	learned Learned
}

// New makes an App from a loaded store.
func New(log *slog.Logger, store *config.Store, conn *midi.Conn, lnk *link.Link, devices Devices) *App {
	cfg := store.Get()
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

// Run handles controller messages until ctx ends.
func (a *App) Run(ctx context.Context, messages <-chan midi.Message) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-messages:
			a.Handle(m)
		}
	}
}

// Handle processes one controller message.
func (a *App) Handle(m midi.Message) {
	hit, learned := a.engine.Handle(m)
	delivered := false
	if hit.Command != nil {
		delivered = a.link.Send(*hit.Command)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seq++
	ev := Event{Seq: a.seq, Time: time.Now(), Message: m, Hit: hit, Delivered: delivered}
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
	return nil
}

// Devices lists the MIDI inputs.
func (a *App) Devices() ([]string, error) { return a.devices() }

// Test sends cmd to Blender as if a control had made it.
func (a *App) Test(cmd engine.Command) bool { return a.link.Send(cmd) }

// ConfigPath is where the config is saved.
func (a *App) ConfigPath() string { return a.store.Path() }

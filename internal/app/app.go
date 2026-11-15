// Package app wires the MIDI connection, the mapping engine and the Blender
// link together, and keeps the recent-events log the page shows.
package app

import (
	"context"
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

// Learned is the control captured by the latest learn.
type Learned struct {
	Seq     int64          `json:"seq"`
	Control config.Control `json:"control"`
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
	}
}

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
		a.learned = Learned{Seq: a.seq, Control: *learned}
	}
}

// EventsSince returns the logged events newer than seq, oldest first.
func (a *App) EventsSince(seq int64) []Event {
	a.mu.Lock()
	defer a.mu.Unlock()
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
	Learning bool        `json:"learning"`
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
		Learning: a.engine.Learning(),
		Learned:  learned,
		LastSeq:  seq,
	}
}

// Learn arms or cancels learn mode.
func (a *App) Learn(on bool) { a.engine.Learn(on) }

// Config returns the saved config.
func (a *App) Config() config.Config { return a.store.Get() }

// SetConfig validates and saves cfg, then applies it.
func (a *App) SetConfig(cfg config.Config) error {
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

package midi

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type input interface {
	Close() error
	// Closed is closed when the device goes away or Close is called.
	Closed() <-chan struct{}
}

// Status says whether the wanted device is open.
type Status struct {
	State  string `json:"state"` // "connected", "searching" or "error"
	Device string `json:"device"`
	Error  string `json:"error,omitempty"`
}

// Conn keeps one input device open by name, reopening it after an unplug, and
// delivers its messages on Messages.
type Conn struct {
	log      *slog.Logger
	inputs   func() ([]string, error)
	open     func(int, func(Message)) (input, error)
	poll     time.Duration
	messages chan Message
	dropped  atomic.Int64
	changed  chan struct{}

	mu     sync.Mutex
	want   string
	status Status
}

// NewConn makes a Conn for the device named want. Call Run to start it.
func NewConn(log *slog.Logger, want string) *Conn {
	return &Conn{
		log:      log.With("component", "midi"),
		inputs:   Inputs,
		open:     openInput,
		poll:     2 * time.Second,
		messages: make(chan Message, 512),
		changed:  make(chan struct{}, 1),
		want:     want,
		status:   Status{State: "searching", Device: want},
	}
}

// Messages delivers every message from the device. When the reader falls more
// than 512 messages behind, new ones are dropped and counted in Dropped.
func (c *Conn) Messages() <-chan Message { return c.messages }

// Dropped is how many messages were dropped because nobody read them in time.
func (c *Conn) Dropped() int64 { return c.dropped.Load() }

// Status is the current connection state.
func (c *Conn) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// SetDevice switches to another device name; the current one is closed.
func (c *Conn) SetDevice(name string) {
	c.mu.Lock()
	if c.want == name {
		c.mu.Unlock()
		return
	}
	c.want = name
	c.status = Status{State: "searching", Device: name}
	c.mu.Unlock()
	select {
	case c.changed <- struct{}{}:
	default:
	}
}

func (c *Conn) setStatus(s Status) {
	c.mu.Lock()
	c.status = s
	c.mu.Unlock()
}

func (c *Conn) wanted() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.want
}

func (c *Conn) deliver(m Message) {
	select {
	case c.messages <- m:
	default:
		c.dropped.Add(1)
	}
}

// findDevice returns the index of the input called want, matched without case.
func findDevice(names []string, want string) int {
	for i, n := range names {
		if strings.EqualFold(strings.TrimSpace(n), strings.TrimSpace(want)) {
			return i
		}
	}
	return -1
}

// Run keeps the device open until ctx ends.
func (c *Conn) Run(ctx context.Context) {
	ticker := time.NewTicker(c.poll)
	defer ticker.Stop()
	for {
		want := c.wanted()
		in, err := c.connect(want)
		if err != nil {
			c.setStatus(Status{State: "error", Device: want, Error: err.Error()})
		} else if in == nil {
			c.setStatus(Status{State: "searching", Device: want})
		} else {
			c.setStatus(Status{State: "connected", Device: want})
			c.log.Info("device open", "device", want)
			c.watch(ctx, in, want, ticker.C)
			_ = in.Close()
			c.log.Info("device closed", "device", want)
			if ctx.Err() != nil {
				return
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-c.changed:
		case <-ticker.C:
		}
	}
}

// connect opens want, or returns nil, nil when it isn't plugged in.
func (c *Conn) connect(want string) (input, error) {
	if want == "" {
		return nil, nil
	}
	names, err := c.inputs()
	if err != nil {
		return nil, err
	}
	i := findDevice(names, want)
	if i < 0 {
		return nil, nil
	}
	return c.open(i, c.deliver)
}

// watch returns when the device closes, disappears from the list, the wanted
// name changes, or ctx ends.
func (c *Conn) watch(ctx context.Context, in input, want string, tick <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-in.Closed():
			return
		case <-c.changed:
			if c.wanted() != want {
				return
			}
		case <-tick:
			names, err := c.inputs()
			if err != nil || findDevice(names, want) < 0 {
				return
			}
		}
	}
}

// Package link is the local TCP connection to the Blender add-on. The bridge
// listens; the add-on connects and reconnects by itself. Both sides speak
// newline-delimited JSON objects with a "type" field.
//
// Bridge to add-on:  {"type":"scrub","frames":10} or {"type":"zoom","clicks":-1}
// Add-on to bridge:  {"type":"hello","blender":"5.1.0","addon":"0.1.0"} first,
//
//	then {"type":"state","scene":"Scene","frame":120,"start":1,"end":250,"fps":24}
//	on every change and {"type":"ping"} at least every PingEvery.
//
// A connection counts as an add-on only once its first line is a hello, sent
// within HelloTimeout. That keeps stray connections (a web page's request to
// this port, a port scanner) from taking a slot or posting a fake state.
package link

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/alexandreffaria/blendeck/internal/engine"
)

// DefaultAddr is where the add-on looks for the bridge.
const DefaultAddr = "127.0.0.1:7871"

const (
	// MaxClients caps how many add-ons may be connected at once.
	MaxClients = 8
	// MaxPending caps connections still waiting to say hello.
	MaxPending = 16
	maxLine    = 64 * 1024
	// MaxQueued caps the commands waiting for one add-on that isn't reading
	// (Blender is busy). Knob turns in a row are merged into one command, so
	// the queue only grows when different controls alternate. Past this,
	// knob turns are dropped and counted.
	MaxQueued = 256
	// PressReserve is the room kept for button presses once MaxQueued is
	// reached: a cut or an undo is never dropped to make way for a knob
	// turn (decision 202611163di7).
	PressReserve = 64
)

// Timeouts; variables so tests can shorten them.
var (
	HelloTimeout = 2 * time.Second
	// IdleTimeout drops an add-on that has sent nothing (not even a ping) for this long.
	IdleTimeout = 15 * time.Second
	// WriteWait is how long one write waits for an add-on that isn't
	// reading before checking whether it is still connected.
	WriteWait = 2 * time.Second
)

// State is what the bridge knows about the connected Blender.
type State struct {
	Connected bool    `json:"connected"`
	Clients   int     `json:"clients"`
	Dropped   int64   `json:"dropped"`
	Blender   string  `json:"blender,omitempty"`
	Addon     string  `json:"addon,omitempty"`
	Scene     string  `json:"scene,omitempty"`
	Frame     int     `json:"frame"`
	Start     int     `json:"start"`
	End       int     `json:"end"`
	FPS       float64 `json:"fps,omitempty"`
}

type client struct {
	conn  net.Conn
	queue []engine.Command // waiting to be written; guarded by Link.mu
	wake  chan struct{}    // signals writeLoop that queue has something
	done  chan struct{}
}

// turn says whether cmd is a knob turn: an amount that adds up with the
// next turn of the same knob. Everything else is a button press, which
// counts once each and is never merged.
func turn(cmd engine.Command) bool {
	switch cmd.Type {
	case "scrub", "zoom", "pan", "jump", "move", "trim":
		return true
	}
	return false
}

// push queues cmd for c and reports whether it was taken. A knob turn right
// after another turn of the same knob is added to it, which keeps the order
// of everything else: only turns with nothing between them become one.
// Turns that cancel out leave nothing, as the add-on does within one read.
func (c *client) push(cmd engine.Command) bool {
	if n := len(c.queue); n > 0 && turn(cmd) {
		last := &c.queue[n-1]
		if last.Type == cmd.Type && last.Sound == cmd.Sound && last.Before == cmd.Before {
			last.Frames += cmd.Frames
			last.Clicks += cmd.Clicks
			if last.Frames == 0 && last.Clicks == 0 {
				c.queue = c.queue[:n-1]
			}
			return true
		}
	}
	limit := MaxQueued
	if !turn(cmd) {
		limit += PressReserve
	}
	if len(c.queue) >= limit {
		return false
	}
	c.queue = append(c.queue, cmd)
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return true
}

// Link accepts add-on connections and fans commands out to them.
type Link struct {
	log     *slog.Logger
	mu      sync.Mutex
	clients map[*client]bool
	pending int
	state   State
	dropped int64
}

// New makes a Link. Call Serve to start accepting.
func New(log *slog.Logger) *Link {
	return &Link{log: log.With("component", "link"), clients: map[*client]bool{}}
}

// State returns what the most recent add-on message said.
func (l *Link) State() State {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.state
	s.Clients = len(l.clients)
	s.Connected = s.Clients > 0
	s.Dropped = l.dropped
	return s
}

// Send queues cmd for every connected add-on. It reports whether at least
// one add-on took it. An add-on that isn't reading keeps its commands in
// order, knob turns in a row merged into one; a command is dropped and
// counted only once MaxQueued are waiting (MaxQueued + PressReserve for a
// button press).
func (l *Link) Send(cmd engine.Command) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	queued := false
	for c := range l.clients {
		if c.push(cmd) {
			queued = true
		} else {
			l.dropped++
		}
	}
	return queued
}

// take empties c's queue and returns it as lines ready to write.
func (l *Link) take(c *client) []byte {
	l.mu.Lock()
	cmds := c.queue
	c.queue = nil
	l.mu.Unlock()
	var data []byte
	for _, cmd := range cmds {
		line, err := json.Marshal(cmd)
		if err != nil {
			continue
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	return data
}

// Serve accepts connections on ln until ctx ends.
func (l *Link) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		l.mu.Lock()
		if l.pending >= MaxPending {
			l.mu.Unlock()
			_ = conn.Close()
			continue
		}
		l.pending++
		l.mu.Unlock()
		go l.handle(ctx, conn)
	}
}

// incoming covers every field an add-on message may carry.
type incoming struct {
	Type    string  `json:"type"`
	Blender string  `json:"blender"`
	Addon   string  `json:"addon"`
	Scene   string  `json:"scene"`
	Frame   int     `json:"frame"`
	Start   int     `json:"start"`
	End     int     `json:"end"`
	FPS     float64 `json:"fps"`
}

// handle waits for the hello, registers the client, then reads until the
// connection ends or goes quiet.
func (l *Link) handle(ctx context.Context, conn net.Conn) {
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 4096), maxLine)

	_ = conn.SetReadDeadline(time.Now().Add(HelloTimeout))
	var hello incoming
	ok := sc.Scan() && json.Unmarshal(sc.Bytes(), &hello) == nil && hello.Type == "hello"

	l.mu.Lock()
	l.pending--
	if !ok || len(l.clients) >= MaxClients {
		full := ok
		l.mu.Unlock()
		if full {
			l.log.Warn("too many add-on connections; refusing one", "remote", conn.RemoteAddr())
		} else {
			l.log.Debug("connection closed without a hello", "remote", conn.RemoteAddr())
		}
		_ = conn.Close()
		return
	}
	c := &client{conn: conn, wake: make(chan struct{}, 1), done: make(chan struct{})}
	l.clients[c] = true
	l.state.Blender = truncate(hello.Blender)
	l.state.Addon = truncate(hello.Addon)
	l.mu.Unlock()
	l.log.Info("add-on connected", "remote", conn.RemoteAddr(), "blender", hello.Blender, "addon", hello.Addon)

	go l.writeLoop(ctx, c)
	defer l.remove(c)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(IdleTimeout))
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				l.log.Debug("add-on read ended", "error", err)
			}
			return
		}
		var m incoming
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			l.log.Debug("bad message from add-on", "error", err)
			continue
		}
		if m.Type != "state" {
			continue // ping, or something newer than this bridge knows
		}
		l.mu.Lock()
		l.state.Scene = truncate(m.Scene)
		l.state.Frame, l.state.Start, l.state.End, l.state.FPS = m.Frame, m.Start, m.End, m.FPS
		l.mu.Unlock()
	}
}

func (l *Link) remove(c *client) {
	l.mu.Lock()
	if !l.clients[c] {
		l.mu.Unlock()
		return
	}
	delete(l.clients, c)
	close(c.done)
	if len(l.clients) == 0 {
		l.state = State{}
	}
	l.mu.Unlock()
	_ = c.conn.Close()
	l.log.Info("add-on disconnected", "remote", c.conn.RemoteAddr())
}

// writeLoop writes c's queue whenever it has something. An add-on that has
// stopped reading (Blender is busy) is waited for, not dropped: what was
// already taken from the queue is written once it reads again, and commands
// arriving meanwhile wait, merged, in the queue. The read side decides when
// an add-on is gone (IdleTimeout), which closes done.
func (l *Link) writeLoop(ctx context.Context, c *client) {
	defer l.remove(c)
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case <-c.wake:
		}
		data := l.take(c)
		for len(data) > 0 {
			_ = c.conn.SetWriteDeadline(time.Now().Add(WriteWait))
			n, err := c.conn.Write(data)
			data = data[n:]
			if err == nil {
				continue
			}
			var ne net.Error
			if !errors.As(err, &ne) || !ne.Timeout() {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-c.done:
				return
			default:
			}
		}
	}
}

func truncate(s string) string {
	const max = 200
	if len(s) > max {
		return s[:max]
	}
	return s
}

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
)

// Timeouts; variables so tests can shorten them.
var (
	HelloTimeout = 2 * time.Second
	// IdleTimeout drops an add-on that has sent nothing (not even a ping) for this long.
	IdleTimeout = 15 * time.Second
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
	conn net.Conn
	out  chan []byte
	done chan struct{}
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
// one add-on took it; a command for an add-on that is 256 commands behind is
// dropped and counted.
func (l *Link) Send(cmd engine.Command) bool {
	data, err := json.Marshal(cmd)
	if err != nil {
		return false
	}
	data = append(data, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	queued := false
	for c := range l.clients {
		select {
		case c.out <- data:
			queued = true
		default:
			l.dropped++
		}
	}
	return queued
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
	c := &client{conn: conn, out: make(chan []byte, 256), done: make(chan struct{})}
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

func (l *Link) writeLoop(ctx context.Context, c *client) {
	defer l.remove(c)
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case data := <-c.out:
			_ = c.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if _, err := c.conn.Write(data); err != nil {
				return
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

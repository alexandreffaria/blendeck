// Package link is the local TCP connection to the Blender add-on. The bridge
// listens; the add-on connects and reconnects by itself. Both sides speak
// newline-delimited JSON objects with a "type" field.
//
// Bridge to add-on:  {"type":"scrub","frames":10}
// Add-on to bridge:  {"type":"hello","blender":"5.1.0","addon":"0.1.0"}
//
//	{"type":"state","scene":"Scene","frame":120,"start":1,"end":250,"fps":24}
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

// MaxClients caps how many add-ons may be connected at once.
const MaxClients = 8

// State is what the bridge knows about the connected Blender.
type State struct {
	Connected bool    `json:"connected"`
	Clients   int     `json:"clients"`
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
	return s
}

// Send queues cmd for every connected add-on. It reports whether any add-on
// was connected to receive it.
func (l *Link) Send(cmd engine.Command) bool {
	data, err := json.Marshal(cmd)
	if err != nil {
		return false
	}
	data = append(data, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	for c := range l.clients {
		select {
		case c.out <- data:
		default:
			l.dropped++
		}
	}
	return len(l.clients) > 0
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
		c := &client{conn: conn, out: make(chan []byte, 256), done: make(chan struct{})}
		l.mu.Lock()
		if len(l.clients) >= MaxClients {
			l.mu.Unlock()
			l.log.Warn("too many add-on connections; refusing one", "remote", conn.RemoteAddr())
			_ = conn.Close()
			continue
		}
		l.clients[c] = true
		l.mu.Unlock()
		l.log.Info("add-on connected", "remote", conn.RemoteAddr())
		go l.writeLoop(ctx, c)
		go l.readLoop(c)
	}
}

func (l *Link) remove(c *client) {
	l.mu.Lock()
	if l.clients[c] {
		delete(l.clients, c)
		close(c.done)
		if len(l.clients) == 0 {
			l.state = State{}
		}
		l.mu.Unlock()
		_ = c.conn.Close()
		l.log.Info("add-on disconnected", "remote", c.conn.RemoteAddr())
		return
	}
	l.mu.Unlock()
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

func (l *Link) readLoop(c *client) {
	defer l.remove(c)
	sc := bufio.NewScanner(c.conn)
	sc.Buffer(make([]byte, 4096), 64*1024)
	for sc.Scan() {
		var m incoming
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			l.log.Debug("bad message from add-on", "error", err)
			continue
		}
		l.mu.Lock()
		switch m.Type {
		case "hello":
			l.state.Blender = truncate(m.Blender)
			l.state.Addon = truncate(m.Addon)
		case "state":
			l.state.Scene = truncate(m.Scene)
			l.state.Frame, l.state.Start, l.state.End, l.state.FPS = m.Frame, m.Start, m.End, m.FPS
		}
		l.mu.Unlock()
	}
}

func truncate(s string) string {
	const max = 200
	if len(s) > max {
		return s[:max]
	}
	return s
}

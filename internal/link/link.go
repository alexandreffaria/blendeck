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
// Every command also carries "t", the time the bridge took it, in
// milliseconds since 1970. The add-on measures from it and, while commands
// arrive, sends about once a second
//
//	{"type":"latency","samples":12,"wait_med":4,"wait_max":9,"picture_med":11,
//	 "picture_max":170,"total_med":16,"total_max":178,"tick_max":171,"dropped":0}
//
// in milliseconds: wait is from the bridge to the add-on applying the
// command, picture from there to the preview having drawn, total both,
// tick the longest gap between two of the add-on's ticks (Blender busy),
// and dropped the presses the add-on left out (see State.Latency).
//
// The bridge also tells each add-on what the controls do, when it connects
// and whenever that changes, so Blender can show it (see SetHints):
//
//	{"type":"hints","profile":"Video editing","hints":[{"control":"D2",
//	 "name":"Scrub timeline","held":[{"modifier":"Fn","name":"Scrub timeline (fine)"}]}]}
//
// A connection counts as an add-on only once its first line is a hello, sent
// within HelloTimeout. That keeps stray connections (a web page's request to
// this port, a port scanner) from taking a slot or posting a fake state.
package link

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/alexandreffaria/blendeck/internal/config"
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
	// Latency is the add-on's latest report, nil until one came.
	Latency *Latency `json:"latency,omitempty"`
}

// Latency is how long commands took, as the add-on measured it over about a
// second, in milliseconds. Wait is from the bridge taking a command to the
// add-on applying it, Picture from there to the preview having drawn, Total
// both. TickMax is the longest gap between two of the add-on's ticks, which
// is how long Blender was busy. Dropped is the presses the add-on left out
// of a read. WorstMinute is the largest TotalMax of the last minute and
// AgeMS how old this report is, both filled in by State.
type Latency struct {
	Samples     int     `json:"samples"`
	WaitMed     float64 `json:"wait_med"`
	WaitMax     float64 `json:"wait_max"`
	PictureMed  float64 `json:"picture_med"`
	PictureMax  float64 `json:"picture_max"`
	TotalMed    float64 `json:"total_med"`
	TotalMax    float64 `json:"total_max"`
	TickMax     float64 `json:"tick_max"`
	Dropped     int     `json:"dropped"`
	WorstMinute float64 `json:"worst_minute"`
	AgeMS       int64   `json:"age_ms"`
}

const (
	// latencyKept is how many reports are kept for WorstMinute: one a
	// second for a minute.
	latencyKept = 60
	// maxLatencyMS bounds what an add-on can report: ten minutes.
	maxLatencyMS = 600_000
)

type latencyAt struct {
	at    time.Time
	worst float64
}

// queued is a command waiting for an add-on, with when the bridge took it.
type queued struct {
	engine.Command
	T int64 `json:"t"`
}

type client struct {
	conn  net.Conn
	queue []queued      // waiting to be written; guarded by Link.mu
	hints []byte        // a hints line not written yet; guarded by Link.mu
	wake  chan struct{} // signals writeLoop that queue has something
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
// A merged turn keeps the time of its first, so its age is the oldest's.
func (c *client) push(cmd engine.Command, t int64) bool {
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
	c.queue = append(c.queue, queued{Command: cmd, T: t})
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
	// hints is the line that tells an add-on what each control does: sent
	// to each one as it connects and to all when it changes. Never
	// modified once set, so clients share it.
	hints []byte
	// latency is the latest report and when it came; recent holds the
	// worst total of the last latencyKept reports, oldest first
	latency   *Latency
	latencyAt time.Time
	recent    []latencyAt
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
	if l.latency != nil && s.Connected {
		now := time.Now()
		lat := *l.latency
		lat.AgeMS = now.Sub(l.latencyAt).Milliseconds()
		for _, r := range l.recent {
			if now.Sub(r.at) <= time.Minute && r.worst > lat.WorstMinute {
				lat.WorstMinute = r.worst
			}
		}
		s.Latency = &lat
	}
	return s
}

// ms bounds a duration an add-on reported: never negative, never NaN, at
// most maxLatencyMS.
func ms(v float64) float64 {
	if !(v > 0) {
		return 0
	}
	if v > maxLatencyMS {
		return maxLatencyMS
	}
	return v
}

// setLatency records an add-on's report.
func (l *Link) setLatency(m incoming) {
	lat := &Latency{
		Samples:    max(0, min(m.Samples, 1_000_000)),
		WaitMed:    ms(m.WaitMed),
		WaitMax:    ms(m.WaitMax),
		PictureMed: ms(m.PictureMed),
		PictureMax: ms(m.PictureMax),
		TotalMed:   ms(m.TotalMed),
		TotalMax:   ms(m.TotalMax),
		TickMax:    ms(m.TickMax),
		Dropped:    max(0, min(m.Dropped, 1_000_000)),
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.latency, l.latencyAt = lat, now
	if len(l.recent) >= latencyKept {
		l.recent = append(l.recent[:0], l.recent[1:]...)
	}
	l.recent = append(l.recent, latencyAt{at: now, worst: lat.TotalMax})
}

// Send queues cmd for every connected add-on. It reports whether at least
// one add-on took it. An add-on that isn't reading keeps its commands in
// order, knob turns in a row merged into one; a command is dropped and
// counted only once MaxQueued are waiting (MaxQueued + PressReserve for a
// button press).
func (l *Link) Send(cmd engine.Command) bool {
	t := time.Now().UnixMilli()
	l.mu.Lock()
	defer l.mu.Unlock()
	taken := false
	for c := range l.clients {
		if c.push(cmd, t) {
			taken = true
		} else {
			l.dropped++
		}
	}
	return taken
}

// SetHints tells every add-on, and each one that connects later, what the
// controls of the active profile do, so Blender can show it. The add-on
// replaces its list with each message. A list that would not fit in one
// line is sent empty rather than cut mid-way.
func (l *Link) SetHints(profile string, hints []config.Hint) {
	if hints == nil {
		hints = []config.Hint{}
	}
	msg := hintsMsg{Type: "hints", Profile: truncate(profile), Hints: hints}
	line, err := json.Marshal(msg)
	if err != nil || len(line) >= maxLine {
		l.log.Warn("control hints don't fit in one message; sending none", "bytes", len(line), "error", err)
		msg.Hints = []config.Hint{}
		line, _ = json.Marshal(msg)
	}
	line = append(line, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if bytes.Equal(line, l.hints) {
		return
	}
	l.hints = line
	for c := range l.clients {
		c.hints = line
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
}

type hintsMsg struct {
	Type    string        `json:"type"`
	Profile string        `json:"profile"`
	Hints   []config.Hint `json:"hints"`
}

// take empties c's queue and returns it as lines ready to write.
func (l *Link) take(c *client) []byte {
	l.mu.Lock()
	cmds := c.queue
	c.queue = nil
	data := append([]byte(nil), c.hints...) // a copy: clients share the line
	c.hints = nil
	l.mu.Unlock()
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

	Samples    int     `json:"samples"`
	WaitMed    float64 `json:"wait_med"`
	WaitMax    float64 `json:"wait_max"`
	PictureMed float64 `json:"picture_med"`
	PictureMax float64 `json:"picture_max"`
	TotalMed   float64 `json:"total_med"`
	TotalMax   float64 `json:"total_max"`
	TickMax    float64 `json:"tick_max"`
	Dropped    int     `json:"dropped"`
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
	if l.hints != nil {
		c.hints = l.hints
		c.wake <- struct{}{} // buffered and new: never blocks
	}
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
		if m.Type == "latency" {
			l.setLatency(m)
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
		l.latency, l.recent = nil, nil
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

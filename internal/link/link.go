// Package link is the local TCP connection to the apps Consort drives: the
// Blender add-on, and other apps such as nomnom. The bridge listens; each app
// connects and reconnects by itself. Both sides speak newline-delimited JSON
// objects with a "type" field.
//
// Bridge to add-on:  {"type":"scrub","frames":10} or {"type":"zoom","clicks":-1}
// Add-on to bridge:  {"type":"hello","app":"blender","blender":"5.1.0","addon":"0.1.0"} first,
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
// and dropped the presses the add-on left out (see State.Latency). When the
// slowest picture was slow it adds "slow":true and "causes", the reasons it
// found: any of "proxies_off", "prefetch_on" and "cache_window".
//
// The bridge also tells each add-on what the controls do, when it connects
// and whenever that changes, so Blender can show it (see SetHints):
//
//	{"type":"hints","profile":"Video editing","show":false,"hints":[{"control":"D2",
//	 "name":"Scrub timeline","held":[{"modifier":"Fn","name":"Scrub timeline (fine)"}]}]}
//
// "show" says whether Blender shows the list now: true while a modifier
// has been held on its own for a moment (decision 202611167kkw).
//
// The hello's "app" names the app that connected ("blender", "nomnom"); a
// hello without one is Blender's, as add-ons before 0.20.0 sent it. What an
// app says about itself (state, latency) is kept per app, so one app never
// overwrites another's; State reports Blender's and lists every app.
// Commands and hints go to every connected app (SendTo picks one app).
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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alexandreffaria/consort/internal/config"
	"github.com/alexandreffaria/consort/internal/engine"
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

// Blender is the app a hello without "app" comes from.
const Blender = "blender"

// maxAppLen caps an app's id in its hello.
const maxAppLen = 32

// appNames are how the page and the tray name the apps Consort knows; any
// other app goes by its id.
var appNames = map[string]string{Blender: "Blender", "nomnom": "nomnom"}

// appID checks the app a hello names: "" is Blender, letters are lowered,
// and anything but a short id of letters, digits, '.', '_' and '-' is
// refused.
func appID(s string) (string, bool) {
	if s == "" {
		return Blender, true
	}
	if len(s) > maxAppLen {
		return "", false
	}
	s = strings.ToLower(s)
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return "", false
		}
	}
	return s, true
}

// App is one connected app: its id from the hello, the name to show for it
// and how many of its windows are connected.
type App struct {
	App     string `json:"app"`
	Name    string `json:"name"`
	Clients int    `json:"clients"`
}

// State is what the bridge knows about the connected Blender, and which
// apps are connected. Connected, Clients and everything below Dropped are
// Blender's; Apps lists every connected app, Blender first, then by id;
// Dropped counts the commands dropped for any of them.
type State struct {
	Connected bool    `json:"connected"`
	Clients   int     `json:"clients"`
	Apps      []App   `json:"apps"`
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
	Samples    int     `json:"samples"`
	WaitMed    float64 `json:"wait_med"`
	WaitMax    float64 `json:"wait_max"`
	PictureMed float64 `json:"picture_med"`
	PictureMax float64 `json:"picture_max"`
	TotalMed   float64 `json:"total_med"`
	TotalMax   float64 `json:"total_max"`
	TickMax    float64 `json:"tick_max"`
	Dropped    int     `json:"dropped"`
	// Slow says the add-on found a picture slow in the last slowKept; SlowMS
	// is how long that picture took and Causes the reasons the add-on could
	// name, from knownSlowCauses. A later report with no picture in it (a zoom)
	// doesn't end it; one with a picture that wasn't slow does.
	Slow        bool     `json:"slow,omitempty"`
	SlowMS      float64  `json:"slow_ms,omitempty"`
	Causes      []string `json:"causes,omitempty"`
	WorstMinute float64  `json:"worst_minute"`
	AgeMS       int64    `json:"age_ms"`
}

const (
	// latencyKept is how many reports are kept for WorstMinute: one a
	// second for a minute.
	latencyKept = 60
	// maxLatencyMS bounds what an add-on can report: ten minutes.
	maxLatencyMS = 600_000
	// slowKept is how long a slow picture is still said. The add-on read its
	// reasons when it happened; they may have been fixed since.
	slowKept = 30 * time.Second
)

// knownSlowCauses are the reasons for a slow picture an add-on may name; anything
// else it sends is left out.
var knownSlowCauses = []string{"proxies_off", "prefetch_on", "cache_window"}

// knownCauses keeps the causes in got that the bridge knows, once each, in
// that order: at most len(knownSlowCauses), whatever the add-on sent.
func knownCauses(got []string) []string {
	var out []string
	for _, c := range knownSlowCauses {
		for _, g := range got {
			if g == c {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

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
	app   string // from the hello: Blender, "nomnom", ...
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

// appData is what one app has said about itself, kept while any of its
// windows is connected.
type appData struct {
	clients int
	// state holds what the hello and the latest state message said
	state State
	// latency is the latest report and when it came; recent holds the
	// worst total of the last latencyKept reports, oldest first
	latency   *Latency
	latencyAt time.Time
	recent    []latencyAt
	// slowMS, slowCauses and slowAt are the last slow picture, kept apart
	// from latency so a report without pictures doesn't erase it
	slowMS     float64
	slowCauses []string
	slowAt     time.Time
}

// Link accepts add-on connections and fans commands out to them.
type Link struct {
	log     *slog.Logger
	mu      sync.Mutex
	clients map[*client]bool
	// apps holds each connected app's data by its id; an app goes with its
	// last window
	apps    map[string]*appData
	pending int
	dropped int64
	// hints is the line that tells an add-on what each control does: sent
	// to each one as it connects and to all when it changes. Never
	// modified once set, so clients share it. hintProfile, hintList and
	// hintShow are what it was made from.
	hints       []byte
	hintProfile string
	hintList    []config.Hint
	hintShow    bool
}

// New makes a Link. Call Serve to start accepting.
func New(log *slog.Logger) *Link {
	return &Link{log: log.With("component", "link"), clients: map[*client]bool{}, apps: map[string]*appData{}}
}

// State returns what Blender's most recent messages said, and which apps
// are connected.
func (l *Link) State() State {
	l.mu.Lock()
	defer l.mu.Unlock()
	var s State
	if a := l.apps[Blender]; a != nil {
		s = a.report(time.Now())
	}
	s.Apps = l.connectedApps()
	s.Dropped = l.dropped
	return s
}

// report is what a has said, as State returns it.
func (a *appData) report(now time.Time) State {
	s := a.state
	s.Clients = a.clients
	s.Connected = a.clients > 0
	if a.latency != nil && s.Connected {
		lat := *a.latency
		if !a.slowAt.IsZero() && now.Sub(a.slowAt) <= slowKept {
			lat.Slow, lat.SlowMS = true, a.slowMS
			lat.Causes = append([]string(nil), a.slowCauses...)
		}
		lat.AgeMS = now.Sub(a.latencyAt).Milliseconds()
		for _, r := range a.recent {
			if now.Sub(r.at) <= time.Minute && r.worst > lat.WorstMinute {
				lat.WorstMinute = r.worst
			}
		}
		s.Latency = &lat
	}
	return s
}

// connectedApps lists the connected apps, Blender first, then by id. The
// caller holds l.mu.
func (l *Link) connectedApps() []App {
	out := []App{}
	for id, a := range l.apps {
		name := appNames[id]
		if name == "" {
			name = id
		}
		out = append(out, App{App: id, Name: name, Clients: a.clients})
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].App == Blender) != (out[j].App == Blender) {
			return out[i].App == Blender
		}
		return out[i].App < out[j].App
	})
	return out
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

// setLatency records a report from one of app's windows.
func (l *Link) setLatency(app string, m incoming) {
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
	a := l.apps[app]
	if a == nil {
		return // its last window just went
	}
	switch {
	case m.Slow && lat.PictureMax > 0:
		a.slowMS, a.slowCauses, a.slowAt = lat.PictureMax, knownCauses(m.Causes), now
	case lat.PictureMax > 0:
		// A picture was timed and it wasn't slow: over
		a.slowMS, a.slowCauses, a.slowAt = 0, nil, time.Time{}
	}
	a.latency, a.latencyAt = lat, now
	if len(a.recent) >= latencyKept {
		a.recent = append(a.recent[:0], a.recent[1:]...)
	}
	a.recent = append(a.recent, latencyAt{at: now, worst: lat.TotalMax})
}

// Send queues cmd for every connected app; see SendTo.
func (l *Link) Send(cmd engine.Command) bool { return l.SendTo("", cmd) }

// SendTo queues cmd for every connected window of app, or of every app when
// app is "". It reports whether at least one window took it. A window that
// isn't reading keeps its commands in order, knob turns in a row merged
// into one; a command is dropped and counted only once MaxQueued are
// waiting (MaxQueued + PressReserve for a button press).
func (l *Link) SendTo(app string, cmd engine.Command) bool {
	t := time.Now().UnixMilli()
	l.mu.Lock()
	defer l.mu.Unlock()
	taken := false
	for c := range l.clients {
		if app != "" && c.app != app {
			continue
		}
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
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hintProfile, l.hintList = truncate(profile), hints
	l.sendHints()
}

// ShowHints has every add-on show the hints, or hide them. It is part of
// the hints message, not a command: it is never dropped or left waiting
// behind a full queue, and an add-on that connects gets it as it is now.
func (l *Link) ShowHints(show bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hintShow = show
	l.sendHints()
}

// HintsShown reports whether add-ons are told to show the hints.
func (l *Link) HintsShown() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.hintShow
}

// sendHints queues the hints line for every add-on if it changed. The
// caller holds l.mu.
func (l *Link) sendHints() {
	msg := hintsMsg{Type: "hints", Profile: l.hintProfile, Show: l.hintShow, Hints: l.hintList}
	line, err := json.Marshal(msg)
	if err != nil || len(line) >= maxLine {
		l.log.Warn("control hints don't fit in one message; sending none", "bytes", len(line), "error", err)
		msg.Hints = []config.Hint{}
		line, _ = json.Marshal(msg)
	}
	line = append(line, '\n')
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
	Show    bool          `json:"show"`
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
	App     string  `json:"app"`
	Blender string  `json:"blender"`
	Addon   string  `json:"addon"`
	Scene   string  `json:"scene"`
	Frame   int     `json:"frame"`
	Start   int     `json:"start"`
	End     int     `json:"end"`
	FPS     float64 `json:"fps"`

	Samples    int      `json:"samples"`
	WaitMed    float64  `json:"wait_med"`
	WaitMax    float64  `json:"wait_max"`
	PictureMed float64  `json:"picture_med"`
	PictureMax float64  `json:"picture_max"`
	TotalMed   float64  `json:"total_med"`
	TotalMax   float64  `json:"total_max"`
	TickMax    float64  `json:"tick_max"`
	Dropped    int      `json:"dropped"`
	Slow       bool     `json:"slow"`
	Causes     []string `json:"causes"`
}

// handle waits for the hello, registers the client, then reads until the
// connection ends or goes quiet.
func (l *Link) handle(ctx context.Context, conn net.Conn) {
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 4096), maxLine)

	_ = conn.SetReadDeadline(time.Now().Add(HelloTimeout))
	var hello incoming
	ok := sc.Scan() && json.Unmarshal(sc.Bytes(), &hello) == nil && hello.Type == "hello"
	app, named := appID(hello.App)
	ok = ok && named

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
	c := &client{app: app, conn: conn, wake: make(chan struct{}, 1), done: make(chan struct{})}
	if l.hints != nil {
		c.hints = l.hints
		c.wake <- struct{}{} // buffered and new: never blocks
	}
	a := l.add(c)
	a.state.Blender = truncate(hello.Blender)
	a.state.Addon = truncate(hello.Addon)
	l.mu.Unlock()
	l.log.Info("app connected", "app", app, "remote", conn.RemoteAddr(), "blender", hello.Blender, "addon", hello.Addon)

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
			l.setLatency(app, m)
			continue
		}
		if m.Type != "state" {
			continue // ping, or something newer than this bridge knows
		}
		l.mu.Lock()
		if a := l.apps[app]; a != nil {
			a.state.Scene = truncate(m.Scene)
			a.state.Frame, a.state.Start, a.state.End, a.state.FPS = m.Frame, m.Start, m.End, m.FPS
		}
		l.mu.Unlock()
	}
}

// add registers c and returns its app's data. The caller holds l.mu.
func (l *Link) add(c *client) *appData {
	l.clients[c] = true
	a := l.apps[c.app]
	if a == nil {
		a = &appData{}
		l.apps[c.app] = a
	}
	a.clients++
	return a
}

func (l *Link) remove(c *client) {
	l.mu.Lock()
	if !l.clients[c] {
		l.mu.Unlock()
		return
	}
	delete(l.clients, c)
	close(c.done)
	if a := l.apps[c.app]; a != nil {
		a.clients--
		if a.clients <= 0 {
			delete(l.apps, c.app) // what it said goes with it
		}
	}
	l.mu.Unlock()
	_ = c.conn.Close()
	l.log.Info("app disconnected", "app", c.app, "remote", c.conn.RemoteAddr())
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

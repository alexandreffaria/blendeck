package link

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alexandreffaria/consort/internal/config"
	"github.com/alexandreffaria/consort/internal/engine"
)

func start(t *testing.T) (*Link, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go l.Serve(ctx, ln)
	return l, ln.Addr().String()
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func dialHello(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	fmt.Fprintln(c, `{"type":"hello","blender":"5.1.0","addon":"0.1.0"}`)
	return c
}

func closedByPeer(t *testing.T, c net.Conn) bool {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, err := c.Read(make([]byte, 1))
	return err != nil && !strings.Contains(err.Error(), "timeout")
}

func TestRoundTrip(t *testing.T) {
	l, addr := start(t)
	if l.Send(engine.Command{Type: "scrub", Frames: 1}) {
		t.Fatal("Send reported a receiver with nobody connected")
	}
	conn := dialHello(t, addr)
	fmt.Fprintln(conn, `not json`)
	fmt.Fprintln(conn, `{"type":"ping"}`)
	fmt.Fprintln(conn, `{"type":"state","scene":"Edit","frame":42,"start":1,"end":250,"fps":24}`)
	eventually(t, "state", func() bool { s := l.State(); return s.Frame == 42 && s.Blender == "5.1.0" })
	s := l.State()
	if !s.Connected || s.Clients != 1 || s.Scene != "Edit" || s.End != 250 {
		t.Fatalf("state = %+v", s)
	}

	if !l.Send(engine.Command{Type: "scrub", Frames: -10}) {
		t.Fatal("Send found no receiver")
	}
	r := bufio.NewReader(conn)
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var got engine.Command
	if err := json.Unmarshal(line, &got); err != nil || got != (engine.Command{Type: "scrub", Frames: -10}) {
		t.Fatalf("got %q (%v)", line, err)
	}

	if !l.Send(engine.Command{Type: "zoom", Clicks: -3}) {
		t.Fatal("Send found no receiver for zoom")
	}
	line, err = r.ReadBytes('\n')
	if err != nil || !strings.HasPrefix(string(line), `{"type":"zoom","clicks":-3,"t":`) {
		t.Fatalf("zoom line %q (%v)", line, err)
	}

	conn.Close()
	eventually(t, "disconnect", func() bool { return !l.State().Connected })
	if l.State().Frame != 0 {
		t.Fatal("state kept after last add-on left")
	}
}

// A web page can make a no-CORS request to this port; it must not count as
// Blender or be able to post a state.
func TestHTTPRequestIsNotAnAddon(t *testing.T) {
	l, addr := start(t)
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprint(c, "POST / HTTP/1.1\r\nHost: x\r\nContent-Type: text/plain\r\n\r\n"+
		`{"type":"hello"}`+"\n"+`{"type":"state","scene":"forged","frame":999}`+"\n")
	if !closedByPeer(t, c) {
		t.Fatal("HTTP connection was kept open")
	}
	if s := l.State(); s.Connected || s.Scene == "forged" {
		t.Fatalf("state = %+v", s)
	}
}

func TestSilentConnectionTimesOut(t *testing.T) {
	old := HelloTimeout
	HelloTimeout = 50 * time.Millisecond
	t.Cleanup(func() { HelloTimeout = old })
	l, addr := start(t)
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !closedByPeer(t, c) {
		t.Fatal("silent connection kept open")
	}
	if l.State().Connected {
		t.Fatal("silent connection counted as an add-on")
	}
}

func TestIdleAddonDropped(t *testing.T) {
	old := IdleTimeout
	IdleTimeout = 80 * time.Millisecond
	t.Cleanup(func() { IdleTimeout = old })
	l, addr := start(t)
	c := dialHello(t, addr)
	eventually(t, "connected", func() bool { return l.State().Connected })
	// Pings keep it alive past the timeout
	for range 4 {
		time.Sleep(40 * time.Millisecond)
		fmt.Fprintln(c, `{"type":"ping"}`)
	}
	if !l.State().Connected {
		t.Fatal("dropped despite pings")
	}
	eventually(t, "dropped when quiet", func() bool { return !l.State().Connected })
}

func TestClientCap(t *testing.T) {
	l, addr := start(t)
	for range MaxClients {
		dialHello(t, addr)
	}
	eventually(t, "all connected", func() bool { return l.State().Clients == MaxClients })
	extra := dialHello(t, addr)
	if !closedByPeer(t, extra) {
		t.Fatal("extra connection was not closed")
	}
	if l.State().Clients != MaxClients {
		t.Fatalf("clients = %d", l.State().Clients)
	}
}

func TestLongLineDropsClient(t *testing.T) {
	l, addr := start(t)
	c := dialHello(t, addr)
	eventually(t, "connected", func() bool { return l.State().Connected })
	c.Write([]byte(strings.Repeat("a", maxLine+10)))
	eventually(t, "dropped", func() bool { return !l.State().Connected })
}

func cmds(c *client) []engine.Command {
	out := []engine.Command{}
	for _, q := range c.queue {
		out = append(out, q.Command)
	}
	return out
}

// push with no time, for the tests that are about order and merging.
func (c *client) add(cmd engine.Command) bool { return c.push(cmd, 0) }

func newClient() *client {
	return &client{app: Blender, wake: make(chan struct{}, 1), done: make(chan struct{})}
}

// Knob turns in a row become one command; a press between them keeps its
// place, so a cut still lands where the turns before it left the playhead.
func TestPushMergesOnlyTurnsInARow(t *testing.T) {
	c := newClient()
	for _, cmd := range []engine.Command{
		{Type: "scrub", Frames: 10},
		{Type: "scrub", Frames: 5},
		{Type: "cut"},
		{Type: "cut"},
		{Type: "scrub", Frames: -3},
		{Type: "jump", Clicks: 1},
		{Type: "jump", Clicks: 1, Sound: true},
		{Type: "move", Frames: 2},
		{Type: "move", Frames: 2, Before: true},
		{Type: "undo"},
		{Type: "undo"},
		{Type: "zoom", Clicks: 2},
		{Type: "zoom", Clicks: 1},
	} {
		if !c.add(cmd) {
			t.Fatalf("push(%+v) refused", cmd)
		}
	}
	want := []engine.Command{
		{Type: "scrub", Frames: 15},
		{Type: "cut"},
		{Type: "cut"},
		{Type: "scrub", Frames: -3},
		{Type: "jump", Clicks: 1},
		{Type: "jump", Clicks: 1, Sound: true},
		{Type: "move", Frames: 2},
		{Type: "move", Frames: 2, Before: true},
		{Type: "undo"},
		{Type: "undo"},
		{Type: "zoom", Clicks: 3},
	}
	got := cmds(c)
	if len(got) != len(want) {
		t.Fatalf("queue = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("queue[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Turns that cancel out leave nothing behind, not a command for 0 frames.
func TestPushDropsTurnsThatCancel(t *testing.T) {
	c := newClient()
	c.add(engine.Command{Type: "cut"})
	c.add(engine.Command{Type: "scrub", Frames: 4})
	c.add(engine.Command{Type: "scrub", Frames: -4})
	if got := cmds(c); len(got) != 1 || got[0].Type != "cut" {
		t.Fatalf("queue = %+v", got)
	}
}

// A full queue refuses knob turns but still takes button presses, up to
// the reserve; turns of the knob already last in the queue still add up.
func TestPushKeepsRoomForPresses(t *testing.T) {
	c := newClient()
	for i := 0; i < MaxQueued/2; i++ {
		if !c.add(engine.Command{Type: "scrub", Frames: 1}) || !c.add(engine.Command{Type: "pan", Clicks: 1}) {
			t.Fatalf("refused before the cap, at %d", len(c.queue))
		}
	}
	if len(c.queue) != MaxQueued {
		t.Fatalf("queue holds %d, want %d", len(c.queue), MaxQueued)
	}
	if c.add(engine.Command{Type: "scrub", Frames: 1}) {
		t.Fatal("a knob turn was taken past the cap")
	}
	if !c.add(engine.Command{Type: "pan", Clicks: 1}) || c.queue[MaxQueued-1].Clicks != 2 {
		t.Fatal("a turn of the last knob in the queue didn't add up")
	}
	for i := 0; i < PressReserve; i++ {
		if !c.add(engine.Command{Type: "cut"}) {
			t.Fatalf("press %d refused inside the reserve", i)
		}
	}
	if c.add(engine.Command{Type: "undo"}) {
		t.Fatal("a press was taken past the reserve")
	}
	if len(c.queue) != MaxQueued+PressReserve {
		t.Fatalf("queue holds %d", len(c.queue))
	}
}

// An add-on that stops reading (Blender is busy) is waited for: nothing is
// lost, the order holds, and the turns made meanwhile arrive as one.
func TestBusyAddonLosesNothing(t *testing.T) {
	old := WriteWait
	WriteWait = 20 * time.Millisecond
	t.Cleanup(func() { WriteWait = old })

	l := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	ours, theirs := net.Pipe() // unbuffered: a write waits for a read
	t.Cleanup(func() { ours.Close(); theirs.Close() })
	c := newClient()
	c.conn = ours
	l.mu.Lock()
	l.add(c)
	l.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go l.writeLoop(ctx, c)

	l.Send(engine.Command{Type: "scrub", Frames: 1})
	time.Sleep(5 * WriteWait) // the first write times out, more than once
	for i := 0; i < 100; i++ {
		l.Send(engine.Command{Type: "scrub", Frames: 1})
	}
	l.Send(engine.Command{Type: "cut"})
	for i := 0; i < 100; i++ {
		l.Send(engine.Command{Type: "scrub", Frames: -1})
	}
	l.Send(engine.Command{Type: "ripple_delete"})
	time.Sleep(3 * WriteWait)
	if s := l.State(); !s.Connected || s.Dropped != 0 {
		t.Fatalf("state while busy = %+v", s)
	}

	r := bufio.NewReader(theirs)
	var got []engine.Command
	for len(got) == 0 || got[len(got)-1].Type != "ripple_delete" {
		theirs.SetReadDeadline(time.Now().Add(3 * time.Second))
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read after %+v: %v", got, err)
		}
		var cmd engine.Command
		if err := json.Unmarshal(line, &cmd); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		got = append(got, cmd)
	}
	want := []engine.Command{
		{Type: "scrub", Frames: 1},
		{Type: "scrub", Frames: 100},
		{Type: "cut"},
		{Type: "scrub", Frames: -100},
		{Type: "ripple_delete"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("command %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Once the add-on is gone, the writer stops waiting for it.
func TestWriterStopsWhenAddonIsRemoved(t *testing.T) {
	old := WriteWait
	WriteWait = 20 * time.Millisecond
	t.Cleanup(func() { WriteWait = old })

	l := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	ours, theirs := net.Pipe()
	t.Cleanup(func() { ours.Close(); theirs.Close() })
	c := newClient()
	c.conn = ours
	l.mu.Lock()
	l.add(c)
	l.mu.Unlock()
	stopped := make(chan struct{})
	go func() { l.writeLoop(context.Background(), c); close(stopped) }()
	l.Send(engine.Command{Type: "cut"})
	time.Sleep(2 * WriteWait)
	l.remove(c) // what the read side does on IdleTimeout
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("writeLoop kept waiting for a removed add-on")
	}
}

// Every command carries when the bridge took it; turns merged while the
// add-on was busy keep the time of the first, so the add-on measures the
// longest wait.
func TestCommandsCarryTheirTime(t *testing.T) {
	c := newClient()
	c.push(engine.Command{Type: "scrub", Frames: 1}, 1000)
	c.push(engine.Command{Type: "scrub", Frames: 2}, 1400)
	c.push(engine.Command{Type: "cut"}, 1500)
	if len(c.queue) != 2 || c.queue[0].T != 1000 || c.queue[0].Frames != 3 || c.queue[1].T != 1500 {
		t.Fatalf("queue = %+v", c.queue)
	}

	l, addr := start(t)
	conn := dialHello(t, addr)
	eventually(t, "connect", func() bool { return l.State().Connected })
	before := time.Now().UnixMilli()
	l.Send(engine.Command{Type: "cut"})
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Type string `json:"type"`
		T    int64  `json:"t"`
	}
	if err := json.Unmarshal(line, &got); err != nil || got.Type != "cut" || got.T < before || got.T > time.Now().UnixMilli() {
		t.Fatalf("line %q (%v)", line, err)
	}
}

// The add-on's latency report shows in the state, bounded, with the worst
// of the last minute, and goes away with the add-on.
func TestLatencyReport(t *testing.T) {
	l, addr := start(t)
	conn := dialHello(t, addr)
	eventually(t, "connect", func() bool { return l.State().Connected })
	if l.State().Latency != nil {
		t.Fatal("latency before any report")
	}
	fmt.Fprintln(conn, `{"type":"latency","samples":7,"wait_med":4,"wait_max":9,"picture_med":11,"picture_max":170,"total_med":16,"total_max":178,"tick_max":171,"dropped":2}`)
	eventually(t, "report", func() bool { return l.State().Latency != nil })
	got := *l.State().Latency
	got.AgeMS = 0
	want := Latency{Samples: 7, WaitMed: 4, WaitMax: 9, PictureMed: 11, PictureMax: 170, TotalMed: 16, TotalMax: 178, TickMax: 171, Dropped: 2, WorstMinute: 178}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("latency = %+v", got)
	}

	// A quieter second: the latest numbers, but the minute's worst stays
	fmt.Fprintln(conn, `{"type":"latency","samples":3,"total_med":15,"total_max":20}`)
	eventually(t, "second report", func() bool { return l.State().Latency.Samples == 3 })
	if lat := l.State().Latency; lat.TotalMax != 20 || lat.WorstMinute != 178 {
		t.Fatalf("latency = %+v", lat)
	}

	// Nonsense is bounded, never passed on
	fmt.Fprintln(conn, `{"type":"latency","samples":-5,"total_med":-1,"total_max":1e300,"dropped":-3}`)
	eventually(t, "bounded report", func() bool { return l.State().Latency.Samples == 0 })
	if lat := l.State().Latency; lat.TotalMed != 0 || lat.TotalMax != maxLatencyMS || lat.Dropped != 0 {
		t.Fatalf("latency = %+v", lat)
	}

	// Many reports: only the last latencyKept are kept
	for i := 0; i < latencyKept*2; i++ {
		fmt.Fprintln(conn, `{"type":"latency","samples":1,"total_max":5}`)
	}
	eventually(t, "old reports forgotten", func() bool { return l.State().Latency.WorstMinute == 5 })
	l.mu.Lock()
	kept := len(l.apps[Blender].recent)
	l.mu.Unlock()
	if kept != latencyKept {
		t.Fatalf("kept %d reports", kept)
	}

	conn.Close()
	eventually(t, "disconnect", func() bool { return !l.State().Connected })
	if l.State().Latency != nil {
		t.Fatal("latency kept after the add-on left")
	}
}

func readHints(t *testing.T, r *bufio.Reader) hintsMsg {
	t.Helper()
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var m hintsMsg
	if err := json.Unmarshal(line, &m); err != nil || m.Type != "hints" {
		t.Fatalf("got %q (%v)", line, err)
	}
	return m
}

// An add-on is told what the controls do when it connects and whenever
// that changes, and only then.
func TestHints(t *testing.T) {
	l, addr := start(t)
	first := []config.Hint{{Control: "D2", Name: "Scrub timeline", Held: []config.HeldHint{{Modifier: "Fn", Name: "fine"}}}}
	l.SetHints("Video editing", first)

	conn := dialHello(t, addr)
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	r := bufio.NewReader(conn)
	if m := readHints(t, r); m.Profile != "Video editing" || len(m.Hints) != 1 || m.Hints[0].Held[0].Name != "fine" {
		t.Fatalf("on connect: %+v", m)
	}

	l.SetHints("Video editing", first) // the same again: nothing is sent
	l.SetHints("Colour", nil)
	if m := readHints(t, r); m.Profile != "Colour" || m.Hints == nil || len(m.Hints) != 0 {
		t.Fatalf("after a change: %+v", m)
	}

	l.ShowHints(false) // already hidden: nothing is sent
	l.ShowHints(true)
	if m := readHints(t, r); !m.Show || m.Profile != "Colour" || !l.HintsShown() {
		t.Fatalf("shown: %+v", m)
	}
	l.SetHints("Colour", first) // a new list keeps them shown
	if m := readHints(t, r); !m.Show || len(m.Hints) != 1 {
		t.Fatalf("new list while shown: %+v", m)
	}
	late := dialHello(t, addr) // an add-on connecting now shows them too
	late.SetReadDeadline(time.Now().Add(3 * time.Second))
	if m := readHints(t, bufio.NewReader(late)); !m.Show {
		t.Fatalf("connected while shown: %+v", m)
	}
	late.Close()
	eventually(t, "late add-on gone", func() bool { return l.State().Clients == 1 })
	l.ShowHints(false)
	if m := readHints(t, r); m.Show || l.HintsShown() {
		t.Fatalf("hidden: %+v", m)
	}

	// commands still arrive, after the hints
	eventually(t, "client", func() bool { return l.State().Clients == 1 })
	l.Send(engine.Command{Type: "cut"})
	line, err := r.ReadBytes('\n')
	if err != nil || !strings.HasPrefix(string(line), `{"type":"cut"`) {
		t.Fatalf("got %q (%v)", line, err)
	}
}

// A list too long for one line is sent empty: the add-on would drop the
// connection over a line past its limit.
func TestHintsTooLong(t *testing.T) {
	l, addr := start(t)
	var many []config.Hint
	for i := 0; i < 2000; i++ {
		many = append(many, config.Hint{Control: strings.Repeat("x", 40), Name: strings.Repeat("y", 40)})
	}
	l.SetHints("Big", many)
	conn := dialHello(t, addr)
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if m := readHints(t, bufio.NewReader(conn)); m.Profile != "Big" || len(m.Hints) != 0 {
		t.Fatalf("got %d hints", len(m.Hints))
	}
}

// A slow picture and its reasons reach the state; reasons the bridge doesn't
// know, repeats and a flood of them don't. A report with no picture in it
// leaves the warning, one with a fast picture ends it, and so does time.
func TestSlowPictureCauses(t *testing.T) {
	l, addr := start(t)
	conn := dialHello(t, addr)
	eventually(t, "connect", func() bool { return l.State().Connected })

	fmt.Fprintln(conn, `{"type":"latency","samples":1,"picture_max":430,"total_max":440,"slow":true,"causes":["cache_window","proxies_off"]}`)
	eventually(t, "slow report", func() bool { lat := l.State().Latency; return lat != nil && lat.Slow })
	if lat := l.State().Latency; lat.SlowMS != 430 || !reflect.DeepEqual(lat.Causes, []string{"proxies_off", "cache_window"}) {
		t.Fatalf("latency = %+v", lat)
	}

	many := strings.Repeat(`"prefetch_on","<b>x</b>","nonsense",`, 1000)
	fmt.Fprintln(conn, `{"type":"latency","samples":2,"picture_max":300,"slow":true,"causes":[`+many+`"prefetch_on"]}`)
	eventually(t, "flooded report", func() bool { return l.State().Latency.Samples == 2 })
	if got := l.State().Latency.Causes; !reflect.DeepEqual(got, []string{"prefetch_on"}) {
		t.Fatalf("causes = %q", got)
	}

	// A zoom: no picture was timed, so the page keeps what it said
	fmt.Fprintln(conn, `{"type":"latency","samples":3,"wait_max":5}`)
	eventually(t, "report without pictures", func() bool { return l.State().Latency.Samples == 3 })
	if lat := l.State().Latency; !lat.Slow || lat.SlowMS != 300 || lat.PictureMax != 0 || !reflect.DeepEqual(lat.Causes, []string{"prefetch_on"}) {
		t.Fatalf("latency = %+v", lat)
	}

	// The state hands out its own copy of the reasons
	l.State().Latency.Causes[0] = "changed"
	if got := l.State().Latency.Causes[0]; got != "prefetch_on" {
		t.Fatalf("causes shared with the caller: %q", got)
	}

	// A fast picture: over, and reasons on a report that isn't slow are ignored
	fmt.Fprintln(conn, `{"type":"latency","samples":4,"picture_max":12,"causes":["prefetch_on"]}`)
	eventually(t, "fast report", func() bool { return l.State().Latency.Samples == 4 })
	if lat := l.State().Latency; lat.Slow || lat.SlowMS != 0 || lat.Causes != nil {
		t.Fatalf("latency = %+v", lat)
	}

	// Slow again, then nothing more: it stops being said after slowKept
	fmt.Fprintln(conn, `{"type":"latency","samples":5,"picture_max":500,"slow":true}`)
	eventually(t, "slow again", func() bool { return l.State().Latency.Slow })
	l.mu.Lock()
	l.apps[Blender].slowAt = l.apps[Blender].slowAt.Add(-slowKept - time.Second)
	l.mu.Unlock()
	if lat := l.State().Latency; lat.Slow || lat.Causes != nil {
		t.Fatalf("still slow after slowKept: %+v", lat)
	}

	// "slow" with no picture time to go with it isn't one
	fmt.Fprintln(conn, `{"type":"latency","samples":6,"slow":true,"causes":["prefetch_on"]}`)
	eventually(t, "slow without a picture", func() bool { return l.State().Latency.Samples == 6 })
	if lat := l.State().Latency; lat.Slow {
		t.Fatalf("latency = %+v", lat)
	}

	// A slow picture doesn't outlive its add-on: after a reconnect, a report
	// with no picture in it must not bring the old warning back
	fmt.Fprintln(conn, `{"type":"latency","samples":7,"picture_max":430,"slow":true,"causes":["prefetch_on"]}`)
	eventually(t, "slow before leaving", func() bool { return l.State().Latency.Slow })
	conn.Close()
	eventually(t, "disconnect", func() bool { return !l.State().Connected })
	conn = dialHello(t, addr)
	eventually(t, "reconnect", func() bool { return l.State().Connected })
	fmt.Fprintln(conn, `{"type":"latency","samples":3,"wait_max":5}`)
	eventually(t, "report after reconnect", func() bool { return l.State().Latency != nil })
	if lat := l.State().Latency; lat.Slow || lat.Causes != nil {
		t.Fatalf("the last add-on's warning came back: %+v", lat)
	}
}

func dial(t *testing.T, addr, hello string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	fmt.Fprintln(c, hello)
	return c
}

// readCommand reads lines from r until a command (not hints) comes.
func readCommand(t *testing.T, c net.Conn, r *bufio.Reader) engine.Command {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var got engine.Command
		if err := json.Unmarshal(line, &got); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		if got.Type != "hints" {
			return got
		}
	}
}

// A hello names its app; without one it is Blender's, as the add-on sent it
// before it named itself.
func TestHelloNamesItsApp(t *testing.T) {
	for _, c := range []struct{ hello, want string }{
		{`{"type":"hello","blender":"5.1.0","addon":"0.19.0"}`, Blender},
		{`{"type":"hello","app":"blender","blender":"5.1.0","addon":"0.20.0"}`, Blender},
		{`{"type":"hello","app":"","blender":"5.1.0"}`, Blender},
		{`{"type":"hello","app":"nomnom"}`, "nomnom"},
		{`{"type":"hello","app":"NomNom"}`, "nomnom"},
		{`{"type":"hello","app":"other-app_2.0"}`, "other-app_2.0"},
	} {
		l, addr := start(t)
		conn := dial(t, addr, c.hello)
		eventually(t, c.hello, func() bool { return len(l.State().Apps) == 1 })
		if got := l.State().Apps[0].App; got != c.want {
			t.Errorf("%s: app %q, want %q", c.hello, got, c.want)
		}
		if l.State().Connected != (c.want == Blender) {
			t.Errorf("%s: Blender connected = %v", c.hello, l.State().Connected)
		}
		conn.Close()
		eventually(t, "gone", func() bool { return len(l.State().Apps) == 0 })
	}
}

// An app that isn't a short plain name is refused like a bad hello.
func TestHelloWithBadAppRefused(t *testing.T) {
	for _, hello := range []string{
		`{"type":"hello","app":"<b>x</b>"}`,
		`{"type":"hello","app":"has space"}`,
		`{"type":"hello","app":"` + strings.Repeat("a", maxAppLen+1) + `"}`,
		`{"type":"hello","app":5}`,
	} {
		l, addr := start(t)
		c := dial(t, addr, hello)
		if !closedByPeer(t, c) {
			t.Errorf("%s: kept open", hello)
		}
		if s := l.State(); len(s.Apps) != 0 || s.Connected {
			t.Errorf("%s: state = %+v", hello, s)
		}
	}
}

// With Blender and nomnom both connected, each keeps what it said: nomnom's
// state and latency never show as Blender's, both are listed, and a command
// goes to the target only (SendTo picks any).
func TestTwoApps(t *testing.T) {
	l, addr := start(t)
	blender := dial(t, addr, `{"type":"hello","blender":"5.1.0","addon":"0.19.0"}`)
	eventually(t, "Blender connected", func() bool { return l.State().Connected })
	nomnom := dial(t, addr, `{"type":"hello","app":"nomnom"}`)
	eventually(t, "both connected", func() bool { return len(l.State().Apps) == 2 })
	want := []App{{App: Blender, Name: "Blender", Clients: 1, Target: true}, {App: "nomnom", Name: "nomnom", Clients: 1}}
	if got := l.State().Apps; !reflect.DeepEqual(got, want) {
		t.Fatalf("apps = %+v", got)
	}

	fmt.Fprintln(blender, `{"type":"state","scene":"Edit","frame":42,"start":1,"end":250,"fps":24}`)
	eventually(t, "Blender state", func() bool { return l.State().Frame == 42 })
	fmt.Fprintln(nomnom, `{"type":"state","scene":"clip","frame":7,"start":0,"end":9,"fps":30}`)
	fmt.Fprintln(nomnom, `{"type":"latency","samples":3,"total_max":99}`)
	fmt.Fprintln(nomnom, `{"type":"ping"}`)
	eventually(t, "nomnom's messages read", func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		a := l.apps["nomnom"]
		return a.state.Frame == 7 && a.latency != nil
	})
	s := l.State()
	if s.Frame != 42 || s.Scene != "Edit" || s.FPS != 24 || s.Blender != "5.1.0" || s.Latency != nil || !s.Connected || s.Clients != 1 {
		t.Fatalf("Blender's state = %+v", s)
	}

	if !l.Send(engine.Command{Type: "scrub", Frames: 3}) {
		t.Fatal("Send found no receiver")
	}
	br, nr := bufio.NewReader(blender), bufio.NewReader(nomnom)
	if got := readCommand(t, blender, br); got != (engine.Command{Type: "scrub", Frames: 3}) {
		t.Errorf("Blender got %+v", got)
	}

	// SendTo reaches one app only
	if !l.SendTo("nomnom", engine.Command{Type: "zoom", Clicks: 1}) {
		t.Fatal("SendTo found no nomnom")
	}
	if l.SendTo("premiere", engine.Command{Type: "cut"}) {
		t.Fatal("SendTo reported an app that isn't connected")
	}
	l.SendTo(Blender, engine.Command{Type: "zoom", Clicks: -1})
	if got := readCommand(t, nomnom, nr); got != (engine.Command{Type: "zoom", Clicks: 1}) {
		t.Errorf("nomnom got %+v", got)
	}
	if got := readCommand(t, blender, br); got != (engine.Command{Type: "zoom", Clicks: -1}) {
		t.Errorf("Blender got %+v", got)
	}

	// nomnom leaving keeps Blender's state; Blender leaving keeps nomnom listed
	nomnom.Close()
	eventually(t, "nomnom gone", func() bool { return len(l.State().Apps) == 1 })
	if s := l.State(); s.Frame != 42 || !s.Connected || s.Apps[0].App != Blender {
		t.Fatalf("after nomnom left: %+v", s)
	}
	nomnom = dial(t, addr, `{"type":"hello","app":"nomnom"}`)
	eventually(t, "nomnom back", func() bool { return len(l.State().Apps) == 2 })
	blender.Close()
	eventually(t, "Blender gone", func() bool { return !l.State().Connected })
	if s := l.State(); s.Frame != 0 || s.Scene != "" || len(s.Apps) != 1 || s.Apps[0].App != "nomnom" {
		t.Fatalf("after Blender left: %+v", s)
	}
}

// Two Blender windows count as one app with two clients.
func TestAppCountsItsWindows(t *testing.T) {
	l, addr := start(t)
	dialHello(t, addr)
	second := dial(t, addr, `{"type":"hello","app":"blender","blender":"5.1.0","addon":"0.20.0"}`)
	eventually(t, "both windows", func() bool { return l.State().Clients == 2 })
	if got := l.State().Apps; len(got) != 1 || got[0].Clients != 2 {
		t.Fatalf("apps = %+v", got)
	}
	second.Close()
	eventually(t, "one window", func() bool { return l.State().Clients == 1 })
	if got := l.State().Apps; len(got) != 1 || got[0].Clients != 1 {
		t.Fatalf("apps = %+v", got)
	}
}

// lines reads what c was sent within a moment: hints and commands.
func lines(t *testing.T, c net.Conn, r *bufio.Reader) []string {
	t.Helper()
	var out []string
	for {
		c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		line, err := r.ReadString('\n')
		if err != nil {
			return out
		}
		out = append(out, line)
	}
}

// commandsIn keeps the commands of lines, leaving the hints out.
func commandsIn(t *testing.T, ls []string) []engine.Command {
	t.Helper()
	var out []engine.Command
	for _, line := range ls {
		var got engine.Command
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		if got.Type != "hints" {
			out = append(out, got)
		}
	}
	return out
}

// Commands go to the target app only: the other app gets none of them,
// and Switch moves them over (decision 20261117h3jl).
func TestCommandsGoToTheTargetOnly(t *testing.T) {
	l, addr := start(t)
	blender := dialHello(t, addr)
	eventually(t, "Blender connected", func() bool { return l.State().Connected })
	nomnom := dial(t, addr, `{"type":"hello","app":"nomnom"}`)
	eventually(t, "both connected", func() bool { return len(l.State().Apps) == 2 })
	br, nr := bufio.NewReader(blender), bufio.NewReader(nomnom)
	if id, name := l.Target(); id != Blender || name != "Blender" || l.State().Target != Blender {
		t.Fatalf("target %q %q, want the first app to connect", id, name)
	}

	l.Send(engine.Command{Type: "cut"})
	if got := commandsIn(t, lines(t, blender, br)); !reflect.DeepEqual(got, []engine.Command{{Type: "cut"}}) {
		t.Fatalf("Blender got %+v", got)
	}
	if got := commandsIn(t, lines(t, nomnom, nr)); got != nil {
		t.Fatalf("nomnom got %+v while Blender is the target", got)
	}

	if id, name := l.Switch(); id != "nomnom" || name != "nomnom" {
		t.Fatalf("switched to %q %q", id, name)
	}
	l.Send(engine.Command{Type: "undo"})
	if got := commandsIn(t, lines(t, nomnom, nr)); !reflect.DeepEqual(got, []engine.Command{{Type: "undo"}}) {
		t.Fatalf("nomnom got %+v", got)
	}
	if got := commandsIn(t, lines(t, blender, br)); got != nil {
		t.Fatalf("Blender got %+v after the switch", got)
	}
	want := []App{{App: Blender, Name: "Blender", Clients: 1}, {App: "nomnom", Name: "nomnom", Clients: 1, Target: true}}
	if s := l.State(); !reflect.DeepEqual(s.Apps, want) || s.Target != "nomnom" {
		t.Fatalf("state after the switch: %+v", s)
	}
}

// Switch cycles through the connected apps in the order State lists them,
// back to the first after the last; with one app it stays, with none there
// is nothing.
func TestSwitchCycles(t *testing.T) {
	l, addr := start(t)
	if id, name := l.Switch(); id != "" || name != "" {
		t.Fatalf("switched to %q %q with nothing connected", id, name)
	}
	if l.Send(engine.Command{Type: "cut"}) {
		t.Fatal("sent with nothing connected")
	}
	dial(t, addr, `{"type":"hello","app":"nomnom"}`)
	eventually(t, "nomnom", func() bool { return len(l.State().Apps) == 1 })
	if id, _ := l.Target(); id != "nomnom" {
		t.Fatalf("one app connected is the target by itself; got %q", id)
	}
	if id, _ := l.Switch(); id != "nomnom" {
		t.Fatalf("switch with one app went to %q", id)
	}
	dial(t, addr, `{"type":"hello","app":"premiere"}`)
	dialHello(t, addr)
	eventually(t, "three apps", func() bool { return len(l.State().Apps) == 3 })
	if id, _ := l.Target(); id != "nomnom" {
		t.Fatalf("an app connecting took the target: %q", id)
	}
	var got []string
	for range 4 {
		_, name := l.Switch()
		got = append(got, name)
	}
	if want := []string{"premiere", "Blender", "nomnom", "premiere"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("switched through %q, want %q", got, want)
	}
}

// When the target's last window goes, another connected app is the target;
// when nothing is left there is none, and the next app to connect is it.
func TestTargetFallsBack(t *testing.T) {
	l, addr := start(t)
	blender := dialHello(t, addr)
	eventually(t, "Blender", func() bool { return l.State().Connected })
	second := dialHello(t, addr)
	eventually(t, "two windows", func() bool { return l.State().Clients == 2 })
	nomnom := dial(t, addr, `{"type":"hello","app":"nomnom"}`)
	eventually(t, "nomnom", func() bool { return len(l.State().Apps) == 2 })

	second.Close() // Blender still has a window: still the target
	eventually(t, "one window", func() bool { return l.State().Clients == 1 })
	if id, _ := l.Target(); id != Blender {
		t.Fatalf("target %q with a Blender window left", id)
	}
	blender.Close()
	eventually(t, "Blender gone", func() bool { return !l.State().Connected })
	if id, _ := l.Target(); id != "nomnom" {
		t.Fatalf("target %q after Blender left, want nomnom", id)
	}
	nr := bufio.NewReader(nomnom)
	l.Send(engine.Command{Type: "cut"})
	if got := commandsIn(t, lines(t, nomnom, nr)); !reflect.DeepEqual(got, []engine.Command{{Type: "cut"}}) {
		t.Fatalf("nomnom got %+v", got)
	}

	back := dialHello(t, addr) // Blender coming back doesn't take it back
	eventually(t, "Blender back", func() bool { return l.State().Connected })
	if id, _ := l.Target(); id != "nomnom" {
		t.Fatalf("target %q after Blender came back", id)
	}
	nomnom.Close()
	eventually(t, "nomnom gone", func() bool { return len(l.State().Apps) == 1 })
	if id, _ := l.Target(); id != Blender {
		t.Fatalf("target %q after nomnom left", id)
	}
	back.Close()
	eventually(t, "nothing", func() bool { return len(l.State().Apps) == 0 })
	if id, name := l.Target(); id != "" || name != "" || l.State().Target != "" {
		t.Fatalf("target %q %q with nothing connected", id, name)
	}
	if l.Send(engine.Command{Type: "cut"}) {
		t.Fatal("sent with nothing connected")
	}
	dial(t, addr, `{"type":"hello","app":"nomnom"}`)
	eventually(t, "nomnom again", func() bool { id, _ := l.Target(); return id == "nomnom" })
}

// The hints name the target, and every app hears when it changes.
func TestHintsNameTheTarget(t *testing.T) {
	l, addr := start(t)
	l.SetHints("Video editing", nil)
	blender := dialHello(t, addr)
	blender.SetReadDeadline(time.Now().Add(3 * time.Second))
	br := bufio.NewReader(blender)
	if m := readHints(t, br); m.Target != "Blender" {
		t.Fatalf("on connect: %+v", m)
	}
	eventually(t, "Blender", func() bool { return l.State().Connected })
	nomnom := dial(t, addr, `{"type":"hello","app":"nomnom"}`)
	nomnom.SetReadDeadline(time.Now().Add(3 * time.Second))
	nr := bufio.NewReader(nomnom)
	if m := readHints(t, nr); m.Target != "Blender" {
		t.Fatalf("nomnom on connect: %+v", m)
	}
	l.Switch()
	for name, r := range map[string]*bufio.Reader{"Blender": br, "nomnom": nr} {
		if m := readHints(t, r); m.Target != "nomnom" || m.Profile != "Video editing" {
			t.Fatalf("%s after the switch: %+v", name, m)
		}
	}
	nomnom.Close()
	if m := readHints(t, br); m.Target != "Blender" {
		t.Fatalf("after nomnom left: %+v", m)
	}
}

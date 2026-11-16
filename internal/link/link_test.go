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

	"github.com/alexandreffaria/blendeck/internal/engine"
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
	return &client{wake: make(chan struct{}, 1), done: make(chan struct{})}
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
	l.clients[c] = true
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
	l.clients[c] = true
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
	kept := len(l.recent)
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

// A slow picture and its reasons reach the state; reasons the bridge doesn't
// know, repeats and a flood of them don't, and neither do reasons on a
// report that isn't slow.
func TestSlowPictureCauses(t *testing.T) {
	l, addr := start(t)
	conn := dialHello(t, addr)
	eventually(t, "connect", func() bool { return l.State().Connected })

	fmt.Fprintln(conn, `{"type":"latency","samples":1,"picture_max":430,"total_max":440,"slow":true,"causes":["cache_window","proxies_off"]}`)
	eventually(t, "slow report", func() bool { lat := l.State().Latency; return lat != nil && lat.Slow })
	if got := l.State().Latency.Causes; !reflect.DeepEqual(got, []string{"proxies_off", "cache_window"}) {
		t.Fatalf("causes = %q", got)
	}

	many := strings.Repeat(`"prefetch_on","<b>x</b>","nonsense",`, 1000)
	fmt.Fprintln(conn, `{"type":"latency","samples":2,"picture_max":300,"slow":true,"causes":[`+many+`"prefetch_on"]}`)
	eventually(t, "flooded report", func() bool { return l.State().Latency.Samples == 2 })
	if got := l.State().Latency.Causes; !reflect.DeepEqual(got, []string{"prefetch_on"}) {
		t.Fatalf("causes = %q", got)
	}

	fmt.Fprintln(conn, `{"type":"latency","samples":3,"picture_max":12,"causes":["prefetch_on"]}`)
	eventually(t, "fast report", func() bool { return l.State().Latency.Samples == 3 })
	if lat := l.State().Latency; lat.Slow || lat.Causes != nil {
		t.Fatalf("latency = %+v", lat)
	}
}

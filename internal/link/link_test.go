package link

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
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
	if err != nil || string(line) != `{"type":"zoom","clicks":-3}`+"\n" {
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

func cmds(c *client) []engine.Command { return append([]engine.Command(nil), c.queue...) }

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
		if !c.push(cmd) {
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
	c.push(engine.Command{Type: "cut"})
	c.push(engine.Command{Type: "scrub", Frames: 4})
	c.push(engine.Command{Type: "scrub", Frames: -4})
	if got := cmds(c); len(got) != 1 || got[0].Type != "cut" {
		t.Fatalf("queue = %+v", got)
	}
}

// A full queue refuses knob turns but still takes button presses, up to
// the reserve; turns of the knob already last in the queue still add up.
func TestPushKeepsRoomForPresses(t *testing.T) {
	c := newClient()
	for i := 0; i < MaxQueued/2; i++ {
		if !c.push(engine.Command{Type: "scrub", Frames: 1}) || !c.push(engine.Command{Type: "pan", Clicks: 1}) {
			t.Fatalf("refused before the cap, at %d", len(c.queue))
		}
	}
	if len(c.queue) != MaxQueued {
		t.Fatalf("queue holds %d, want %d", len(c.queue), MaxQueued)
	}
	if c.push(engine.Command{Type: "scrub", Frames: 1}) {
		t.Fatal("a knob turn was taken past the cap")
	}
	if !c.push(engine.Command{Type: "pan", Clicks: 1}) || c.queue[MaxQueued-1].Clicks != 2 {
		t.Fatal("a turn of the last knob in the queue didn't add up")
	}
	for i := 0; i < PressReserve; i++ {
		if !c.push(engine.Command{Type: "cut"}) {
			t.Fatalf("press %d refused inside the reserve", i)
		}
	}
	if c.push(engine.Command{Type: "undo"}) {
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

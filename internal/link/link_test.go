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
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var got engine.Command
	if err := json.Unmarshal(line, &got); err != nil || got != (engine.Command{Type: "scrub", Frames: -10}) {
		t.Fatalf("got %q (%v)", line, err)
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

package link

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
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
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestRoundTrip(t *testing.T) {
	l, addr := start(t)
	if l.Send(engine.Command{Type: "scrub", Frames: 1}) {
		t.Fatal("Send reported a receiver with nobody connected")
	}
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintln(conn, `{"type":"hello","blender":"5.1.0","addon":"0.1.0"}`)
	fmt.Fprintln(conn, `not json`)
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

func TestClientCap(t *testing.T) {
	l, addr := start(t)
	var conns []net.Conn
	for i := 0; i < MaxClients; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		defer c.Close()
	}
	eventually(t, "all connected", func() bool { return l.State().Clients == MaxClients })
	extra, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Close()
	extra.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := extra.Read(make([]byte, 1)); err == nil {
		t.Fatal("extra connection was not closed")
	}
	if l.State().Clients != MaxClients {
		t.Fatalf("clients = %d", l.State().Clients)
	}
}

func TestLongLineDropsClient(t *testing.T) {
	l, addr := start(t)
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	eventually(t, "connected", func() bool { return l.State().Connected })
	big := make([]byte, 70*1024)
	for i := range big {
		big[i] = 'a'
	}
	c.Write(big)
	eventually(t, "dropped", func() bool { return !l.State().Connected })
}

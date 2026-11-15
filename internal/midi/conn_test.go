package midi

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type fakeInput struct {
	closed chan struct{}
	once   sync.Once
}

func (f *fakeInput) Close() error            { f.once.Do(func() { close(f.closed) }); return nil }
func (f *fakeInput) Closed() <-chan struct{} { return f.closed }

type fakeSystem struct {
	mu      sync.Mutex
	names   []string
	opened  []int
	deliver func(Message)
	last    *fakeInput
}

func (s *fakeSystem) inputs() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.names...), nil
}

func (s *fakeSystem) open(i int, f func(Message)) (input, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opened = append(s.opened, i)
	s.deliver = f
	s.last = &fakeInput{closed: make(chan struct{})}
	return s.last, nil
}

func (s *fakeSystem) setNames(n ...string) {
	s.mu.Lock()
	s.names = n
	s.mu.Unlock()
}

func newTestConn(sys *fakeSystem, want string) *Conn {
	c := NewConn(slog.New(slog.NewTextHandler(io.Discard, nil)), want)
	c.inputs = sys.inputs
	c.open = sys.open
	c.poll = 5 * time.Millisecond
	return c
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestConnReconnectsAfterUnplug(t *testing.T) {
	sys := &fakeSystem{names: []string{"Other", "loupedeck+"}}
	c := newTestConn(sys, "Loupedeck+")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	waitFor(t, "connected", func() bool { return c.Status().State == "connected" })
	sys.mu.Lock()
	sys.deliver(Message{Kind: ControlChange, Channel: 1, Number: 2, Value: 1})
	sys.mu.Unlock()
	select {
	case m := <-c.Messages():
		if m.Number != 2 {
			t.Fatalf("got %+v", m)
		}
	case <-time.After(time.Second):
		t.Fatal("no message delivered")
	}

	sys.setNames("Other")
	waitFor(t, "searching after unplug", func() bool { return c.Status().State == "searching" })
	sys.setNames("Loupedeck+")
	waitFor(t, "reconnected", func() bool { return c.Status().State == "connected" })
	sys.mu.Lock()
	n := len(sys.opened)
	sys.mu.Unlock()
	if n != 2 {
		t.Fatalf("opened %d times, want 2", n)
	}
}

func TestConnSwitchesDevice(t *testing.T) {
	sys := &fakeSystem{names: []string{"A", "B"}}
	c := newTestConn(sys, "A")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	waitFor(t, "connected to A", func() bool { return c.Status().State == "connected" })
	sys.mu.Lock()
	first := sys.last
	sys.mu.Unlock()
	c.SetDevice("B")
	waitFor(t, "first closed", func() bool {
		select {
		case <-first.closed:
			return true
		default:
			return false
		}
	})
	waitFor(t, "connected to B", func() bool {
		s := c.Status()
		return s.State == "connected" && s.Device == "B"
	})
}

func TestConnDropsWhenFull(t *testing.T) {
	c := NewConn(slog.New(slog.NewTextHandler(io.Discard, nil)), "x")
	for i := 0; i < 600; i++ {
		c.deliver(Message{})
	}
	if got := c.Dropped(); got != 600-512 {
		t.Fatalf("dropped %d, want %d", got, 600-512)
	}
}

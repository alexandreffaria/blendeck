package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexandreffaria/synergy/internal/app"
	"github.com/alexandreffaria/synergy/internal/autostart"
	"github.com/alexandreffaria/synergy/internal/config"
	"github.com/alexandreffaria/synergy/internal/link"
	"github.com/alexandreffaria/synergy/internal/midi"
)

type fakeAutostart struct {
	supported, on bool
	setErr        error
	sets          int
}

func (f *fakeAutostart) Supported() bool        { return f.supported }
func (f *fakeAutostart) Enabled() (bool, error) { return f.on, nil }
func (f *fakeAutostart) Set(on bool) error {
	f.sets++
	if f.setErr != nil {
		return f.setErr
	}
	f.on = on
	return nil
}

func newAutostartServer(t *testing.T, a Autostart) http.Handler {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := config.Open(filepath.Join(t.TempDir(), "c.json"))
	if err != nil {
		t.Fatal(err)
	}
	ap := app.New(log, store, midi.NewConn(log, "Loupedeck+"), link.New(log), func() ([]string, error) { return nil, nil })
	s := New(log, ap, "test", "127.0.0.1:7870")
	if a != nil {
		s.WithAutostart(a)
	}
	return s.Handler()
}

func stateAutostart(t *testing.T, h http.Handler) autostartState {
	t.Helper()
	rec := do(h, "GET", "/api/state", "127.0.0.1:7870", "", "")
	var st stateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	return st.Autostart
}

func TestAutostartOnAndOff(t *testing.T) {
	f := &fakeAutostart{supported: true}
	h := newAutostartServer(t, f)
	if st := stateAutostart(t, h); !st.Supported || st.Enabled {
		t.Fatalf("state = %+v", st)
	}
	rec := do(h, "PUT", "/api/autostart", "127.0.0.1:7870", "application/json", `{"enabled":true}`)
	if rec.Code != http.StatusOK || !f.on {
		t.Fatalf("on: %d %s", rec.Code, rec.Body)
	}
	var got autostartState
	json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.Enabled {
		t.Fatalf("response = %+v", got)
	}
	if st := stateAutostart(t, h); !st.Enabled {
		t.Fatalf("state after on = %+v", st)
	}
	if rec := do(h, "PUT", "/api/autostart", "127.0.0.1:7870", "application/json", `{"enabled":false}`); rec.Code != http.StatusOK || f.on {
		t.Fatalf("off: %d %s", rec.Code, rec.Body)
	}
}

func TestAutostartGuardsAndBadBodies(t *testing.T) {
	f := &fakeAutostart{supported: true}
	h := newAutostartServer(t, f)
	for _, c := range []struct {
		host, ctype, body string
		code              int
	}{
		{"evil.example:7870", "application/json", `{"enabled":true}`, http.StatusForbidden},
		{"127.0.0.1:7870", "text/plain", `{"enabled":true}`, http.StatusUnsupportedMediaType},
		{"127.0.0.1:7870", "application/json", `{}`, http.StatusBadRequest},
		{"127.0.0.1:7870", "application/json", `{"enabled":"yes"}`, http.StatusBadRequest},
		{"127.0.0.1:7870", "application/json", `{"enabled":true,"x":1}`, http.StatusBadRequest},
	} {
		if rec := do(h, "PUT", "/api/autostart", c.host, c.ctype, c.body); rec.Code != c.code {
			t.Errorf("%s %s %s: %d, want %d", c.host, c.ctype, c.body, rec.Code, c.code)
		}
	}
	if f.sets != 0 || f.on {
		t.Fatalf("a rejected request changed it: %+v", f)
	}
}

func TestAutostartUnavailable(t *testing.T) {
	for name, a := range map[string]Autostart{"none": nil, "unsupported": &fakeAutostart{}} {
		h := newAutostartServer(t, a)
		if st := stateAutostart(t, h); st.Supported || st.Enabled {
			t.Errorf("%s: state = %+v", name, st)
		}
		if rec := do(h, "PUT", "/api/autostart", "127.0.0.1:7870", "application/json", `{"enabled":true}`); rec.Code != http.StatusConflict {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
}

func TestAutostartSetFails(t *testing.T) {
	f := &fakeAutostart{supported: true, setErr: errors.New("access denied")}
	h := newAutostartServer(t, f)
	rec := do(h, "PUT", "/api/autostart", "127.0.0.1:7870", "application/json", `{"enabled":true}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestAutostartTakenByAnotherProgram(t *testing.T) {
	f := &fakeAutostart{supported: true, setErr: fmt.Errorf("%w: %q starts x.exe", autostart.ErrTaken, "Synergy")}
	h := newAutostartServer(t, f)
	rec := do(h, "PUT", "/api/autostart", "127.0.0.1:7870", "application/json", `{"enabled":true}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "x.exe") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

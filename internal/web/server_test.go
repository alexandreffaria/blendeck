package web

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexandreffaria/blendeck/internal/app"
	"github.com/alexandreffaria/blendeck/internal/config"
	"github.com/alexandreffaria/blendeck/internal/link"
	"github.com/alexandreffaria/blendeck/internal/midi"
)

func newServer(t *testing.T) http.Handler {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := config.Open(filepath.Join(t.TempDir(), "c.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(log, store, midi.NewConn(log, "Loupedeck+"), link.New(log), func() ([]string, error) { return []string{"Loupedeck+"}, nil })
	return New(log, a, "test", "127.0.0.1:7870").Handler()
}

func do(h http.Handler, method, path, host, ctype, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = host
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGuard(t *testing.T) {
	h := newServer(t)
	if rec := do(h, "GET", "/api/state", "evil.example:7870", "", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign host: %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/state", "localhost:7870", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("localhost: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/learn", "127.0.0.1:7870", "text/plain", `{"target":"binding"}`); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain post: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/learn", "127.0.0.1:7870", "application/json; charset=utf-8", `{"target":"binding"}`); rec.Code != http.StatusOK {
		t.Fatalf("json post: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/learn", "127.0.0.1:7870", "application/json", `{"target":"bogus"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad target: %d", rec.Code)
	}
}

func TestPutConfig(t *testing.T) {
	h := newServer(t)
	rec := do(h, "GET", "/api/state", "127.0.0.1:7870", "", "")
	var st stateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	st.Config.Profiles[0].Bindings[0].Control.Number = 3
	body, _ := json.Marshal(st.Config)
	if rec := do(h, "PUT", "/api/config", "127.0.0.1:7870", "application/json", string(body)); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	st.Config.Profiles[0].Bindings[0].Step = 0
	body, _ = json.Marshal(st.Config)
	rec = do(h, "PUT", "/api/config", "127.0.0.1:7870", "application/json", string(body))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "frames per click") {
		t.Fatalf("invalid put: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "PUT", "/api/config", "127.0.0.1:7870", "application/json", `{"bogus":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", rec.Code)
	}
}

func TestTestEndpoint(t *testing.T) {
	h := newServer(t)
	rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "application/json", `{"frames":10}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"delivered":false`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "application/json", `{"frames":0}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("zero frames: %d", rec.Code)
	}
}

func TestTestEndpointZoom(t *testing.T) {
	h := newServer(t)
	for _, body := range []string{`{"zoom":1}`, `{"zoom":-1}`, `{"zoom":20}`, `{"zoom":-20}`} {
		rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "application/json", body)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"delivered":false`) {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	for _, body := range []string{`{"zoom":21}`, `{"zoom":-21}`, `{"zoom":1,"frames":1}`, `{}`, `{"zoom":1.5}`, `{"zoom":"1"}`} {
		if rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "application/json", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", body, rec.Code)
		}
	}
	if rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "text/plain", `{"zoom":1}`); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain zoom: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/test", "evil.example:7870", "application/json", `{"zoom":1}`); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign host zoom: %d", rec.Code)
	}
}

func TestIndexServed(t *testing.T) {
	h := newServer(t)
	rec := do(h, "GET", "/", "127.0.0.1:7870", "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>Blendeck</title>") {
		t.Fatalf("%d", rec.Code)
	}
}

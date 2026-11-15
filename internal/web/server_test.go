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

func TestTestEndpointCut(t *testing.T) {
	h := newServer(t)
	rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "application/json", `{"cut":true}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"delivered":false`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, body := range []string{`{"cut":true,"frames":1}`, `{"cut":true,"zoom":1}`, `{"cut":false}`, `{"cut":1}`, `{"cut":"yes"}`} {
		if rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "application/json", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "text/plain", `{"cut":true}`); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain cut: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/test", "evil.example:7870", "application/json", `{"cut":true}`); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign host cut: %d", rec.Code)
	}
}

// The page saves a cut binding learned on the owner's button as it
// sends it: no step, no encoder.
func TestPutConfigCutBinding(t *testing.T) {
	h := newServer(t)
	rec := do(h, "GET", "/api/state", "127.0.0.1:7870", "", "")
	var st stateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	found := false
	for i, b := range st.Config.Profiles[0].Bindings {
		if b.Action == config.ActionCut {
			st.Config.Profiles[0].Bindings[i].Control = config.Control{Type: "note", Channel: 1, Number: 53}
			found = true
		}
	}
	if !found {
		t.Fatal("no cut binding in the default config")
	}
	body, _ := json.Marshal(st.Config)
	if strings.Contains(string(body), `"action":"timeline.cut","step"`) {
		t.Fatalf("cut binding carries a step: %s", body)
	}
	if rec := do(h, "PUT", "/api/config", "127.0.0.1:7870", "application/json", string(body)); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
}

func TestIndexServed(t *testing.T) {
	h := newServer(t)
	rec := do(h, "GET", "/", "127.0.0.1:7870", "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>Blendeck</title>") {
		t.Fatalf("%d", rec.Code)
	}
}

func TestTestEndpointUndo(t *testing.T) {
	h := newServer(t)
	rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "application/json", `{"undo":true}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"delivered":false`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, body := range []string{`{"undo":true,"cut":true}`, `{"undo":true,"frames":1}`, `{"undo":true,"zoom":1}`, `{"undo":false}`, `{"undo":1}`, `{"undo":"yes"}`} {
		if rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "application/json", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "text/plain", `{"undo":true}`); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain undo: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/test", "evil.example:7870", "application/json", `{"undo":true}`); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign host undo: %d", rec.Code)
	}
}

// The page saves an undo binding learned on the owner's button (note 95)
// as it sends it: no step, no encoder.
func TestPutConfigUndoBinding(t *testing.T) {
	h := newServer(t)
	rec := do(h, "GET", "/api/state", "127.0.0.1:7870", "", "")
	var st stateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	found := false
	for i, b := range st.Config.Profiles[0].Bindings {
		if b.Action == config.ActionUndo {
			st.Config.Profiles[0].Bindings[i].Control = config.Control{Type: "note", Channel: 1, Number: 95}
			found = true
		}
	}
	if !found {
		t.Fatal("no undo binding in the default config")
	}
	body, _ := json.Marshal(st.Config)
	if strings.Contains(string(body), `"action":"edit.undo","step"`) {
		t.Fatalf("undo binding carries a step: %s", body)
	}
	if rec := do(h, "PUT", "/api/config", "127.0.0.1:7870", "application/json", string(body)); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	// An undo binding with a step is refused
	bad := strings.Replace(string(body), `"action":"edit.undo"`, `"action":"edit.undo","step":3`, 1)
	if rec := do(h, "PUT", "/api/config", "127.0.0.1:7870", "application/json", bad); rec.Code != http.StatusBadRequest {
		t.Fatalf("put with step: %d %s", rec.Code, rec.Body)
	}
}

// The page offers Undo in the Action list and a test undo button.
func TestIndexHasUndo(t *testing.T) {
	h := newServer(t)
	rec := do(h, "GET", "/", "127.0.0.1:7870", "", "")
	for _, want := range []string{`'edit.undo': 'Undo'`, `data-test-undo`, `sendTest({ undo: true })`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("page lacks %s", want)
		}
	}
}

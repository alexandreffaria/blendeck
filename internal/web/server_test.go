package web

import (
	"encoding/json"
	"fmt"
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

func TestTestEndpointRippleDelete(t *testing.T) {
	h := newServer(t)
	rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "application/json", `{"ripple_delete":true}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"delivered":false`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, body := range []string{`{"ripple_delete":true,"cut":true}`, `{"ripple_delete":true,"frames":1}`, `{"ripple_delete":true,"zoom":1}`, `{"ripple_delete":false}`, `{"ripple_delete":1}`, `{"ripple_delete":"yes"}`} {
		if rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "application/json", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "text/plain", `{"ripple_delete":true}`); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain ripple delete: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/test", "evil.example:7870", "application/json", `{"ripple_delete":true}`); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign host ripple delete: %d", rec.Code)
	}
}

// The page saves a ripple delete binding learned on the owner's button
// as it sends it: no step, no encoder.
func TestPutConfigRippleDeleteBinding(t *testing.T) {
	h := newServer(t)
	rec := do(h, "GET", "/api/state", "127.0.0.1:7870", "", "")
	var st stateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	found := false
	for i, b := range st.Config.Profiles[0].Bindings {
		if b.Action == config.ActionRippleDelete {
			st.Config.Profiles[0].Bindings[i].Control = config.Control{Type: "note", Channel: 1, Number: 54}
			found = true
		}
	}
	if !found {
		t.Fatal("no ripple delete binding in the default config")
	}
	body, _ := json.Marshal(st.Config)
	if strings.Contains(string(body), `"action":"timeline.ripple_delete","step"`) {
		t.Fatalf("ripple delete binding carries a step: %s", body)
	}
	if rec := do(h, "PUT", "/api/config", "127.0.0.1:7870", "application/json", string(body)); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
}

func TestTestEndpointPlayPause(t *testing.T) {
	h := newServer(t)
	rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "application/json", `{"play_pause":true}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"delivered":false`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, body := range []string{`{"play_pause":true,"cut":true}`, `{"play_pause":true,"undo":true}`, `{"play_pause":true,"ripple_delete":true}`, `{"play_pause":true,"frames":1}`, `{"play_pause":true,"zoom":1}`, `{"play_pause":false}`, `{"play_pause":1}`, `{"play_pause":"yes"}`} {
		if rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "application/json", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "text/plain", `{"play_pause":true}`); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain play / pause: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/test", "evil.example:7870", "application/json", `{"play_pause":true}`); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign host play / pause: %d", rec.Code)
	}
}

// The page saves a play / pause binding learned on the owner's button as
// it sends it: no step, no encoder.
func TestPutConfigPlayPauseBinding(t *testing.T) {
	h := newServer(t)
	rec := do(h, "GET", "/api/state", "127.0.0.1:7870", "", "")
	var st stateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	found := false
	for i, b := range st.Config.Profiles[0].Bindings {
		if b.Action == config.ActionPlayPause {
			st.Config.Profiles[0].Bindings[i].Control = config.Control{Type: "note", Channel: 1, Number: 67}
			found = true
		}
	}
	if !found {
		t.Fatal("no play / pause binding in the default config")
	}
	body, _ := json.Marshal(st.Config)
	if strings.Contains(string(body), `"action":"playback.toggle","step"`) {
		t.Fatalf("play / pause binding carries a step: %s", body)
	}
	if rec := do(h, "PUT", "/api/config", "127.0.0.1:7870", "application/json", string(body)); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
}

func TestTestEndpointShuttle(t *testing.T) {
	h := newServer(t)
	for _, body := range []string{`{"shuttle":1}`, `{"shuttle":-1}`} {
		rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "application/json", body)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"delivered":false`) {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	for _, body := range []string{`{"shuttle":2}`, `{"shuttle":-5}`, `{"shuttle":0}`, `{"shuttle":1,"play_pause":true}`, `{"shuttle":-1,"frames":1}`, `{"shuttle":true}`, `{"shuttle":"1"}`, `{"shuttle":1.5}`} {
		if rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "application/json", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "text/plain", `{"shuttle":1}`); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain shuttle: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/test", "evil.example:7870", "application/json", `{"shuttle":1}`); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign host shuttle: %d", rec.Code)
	}
}

// The page saves the shuttle bindings learned on the owner's buttons as it
// sends them: no step, no encoder.
func TestPutConfigShuttleBindings(t *testing.T) {
	h := newServer(t)
	rec := do(h, "GET", "/api/state", "127.0.0.1:7870", "", "")
	var st stateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	n := 0
	for i, b := range st.Config.Profiles[0].Bindings {
		switch b.Action {
		case config.ActionReverse:
			st.Config.Profiles[0].Bindings[i].Control = config.Control{Type: "note", Channel: 1, Number: 66}
			n++
		case config.ActionForward:
			st.Config.Profiles[0].Bindings[i].Control = config.Control{Type: "note", Channel: 1, Number: 68}
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d shuttle bindings in the default config, want 2", n)
	}
	body, _ := json.Marshal(st.Config)
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

func TestTestEndpointJump(t *testing.T) {
	h := newServer(t)
	for _, body := range []string{`{"jump":1}`, `{"jump":-1}`, `{"jump":1,"sound":true}`, `{"jump":-1,"sound":false}`} {
		rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "application/json", body)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"delivered":false`) {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	for _, body := range []string{`{"jump":2}`, `{"jump":-5}`, `{"jump":0}`, `{"jump":1,"shuttle":1}`, `{"jump":-1,"frames":1}`, `{"jump":true}`, `{"jump":"1"}`, `{"jump":1.5}`, `{"sound":true}`, `{"frames":1,"sound":true}`, `{"jump":1,"sound":1}`} {
		if rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "application/json", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if rec := do(h, "POST", "/api/test", "evil.example:7870", "application/json", `{"jump":1}`); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign host jump: %d", rec.Code)
	}
}

func TestTestEndpointMove(t *testing.T) {
	h := newServer(t)
	for _, body := range []string{`{"move":1}`, `{"move":-1}`, `{"move":5,"before":true}`} {
		rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "application/json", body)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"delivered":false`) {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	for _, body := range []string{`{"move":0}`, `{"before":true}`, `{"jump":1,"before":true}`, `{"move":1,"jump":1}`, `{"move":100001}`, `{"move":"1"}`, `{"move":1,"before":1}`} {
		if rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "application/json", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
}

func TestTestEndpointPan(t *testing.T) {
	h := newServer(t)
	for _, body := range []string{`{"pan":5}`, `{"pan":-5}`, `{"pan":100}`} {
		rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "application/json", body)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"delivered":false`) {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	for _, body := range []string{`{"pan":0}`, `{"pan":101}`, `{"pan":-101}`, `{"pan":5,"zoom":1}`, `{"pan":"5"}`, `{"pan":5,"before":true}`} {
		if rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "application/json", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
}

func TestTestEndpointViewAll(t *testing.T) {
	h := newServer(t)
	rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "application/json", `{"view_all":true}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"delivered":false`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, body := range []string{`{"view_all":true,"zoom":1}`, `{"view_all":true,"pan":5}`, `{"view_all":true,"cut":true}`, `{"view_all":false}`, `{"view_all":1}`, `{"view_all":"yes"}`} {
		if rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "application/json", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if rec := do(h, "POST", "/api/test", "evil.example:7870", "application/json", `{"view_all":true}`); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign host view all: %d", rec.Code)
	}
}

func TestTestEndpointSave(t *testing.T) {
	h := newServer(t)
	rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "application/json", `{"save":true}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"delivered":false`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, body := range []string{`{"save":true,"cut":true}`, `{"save":true,"view_all":true}`, `{"save":false}`, `{"save":1}`, `{"save":"yes"}`} {
		if rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "application/json", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if rec := do(h, "POST", "/api/test", "evil.example:7870", "application/json", `{"save":true}`); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign host save: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/test", "127.0.0.1:7870", "text/plain", `{"save":true}`); rec.Code == http.StatusOK {
		t.Fatalf("non-JSON save accepted")
	}
}

func TestPutConfigControlNames(t *testing.T) {
	h := newServer(t)
	rec := do(h, "GET", "/api/state", "127.0.0.1:7870", "", "")
	var st stateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	st.Config.Controls = []config.ControlName{{Control: config.Control{Type: "cc", Channel: 1, Number: 3}, Name: "Knob 1"}}
	body, _ := json.Marshal(st.Config)
	if rec := do(h, "PUT", "/api/config", "127.0.0.1:7870", "application/json", string(body)); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	rec = do(h, "GET", "/api/state", "127.0.0.1:7870", "", "")
	st = stateResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Config.Controls) != 1 || st.Config.Controls[0].Name != "Knob 1" {
		t.Fatalf("names not kept: %+v", st.Config.Controls)
	}
	st.Config.Controls[0].Name = strings.Repeat("x", config.MaxControlNameLen+1)
	body, _ = json.Marshal(st.Config)
	if rec := do(h, "PUT", "/api/config", "127.0.0.1:7870", "application/json", string(body)); rec.Code != http.StatusBadRequest {
		t.Fatalf("too long a name: %d", rec.Code)
	}
}

func TestIndexHasControlNames(t *testing.T) {
	h := newServer(t)
	rec := do(h, "GET", "/", "127.0.0.1:7870", "", "")
	for _, want := range []string{`id="controls"`, "data-cname", fmt.Sprintf("MAX_NAME = %d", config.MaxControlNameLen)} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("page is missing %q", want)
		}
	}
}

// Package web serves the page for watching, assigning and profiling controls,
// and the small JSON API behind it. It only answers requests addressed to
// localhost, and state changes need a JSON body, so other web pages open in
// the browser can't drive it.
package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/alexandreffaria/blendeck/internal/app"
	"github.com/alexandreffaria/blendeck/internal/config"
	"github.com/alexandreffaria/blendeck/internal/engine"
)

//go:embed static
var staticFS embed.FS

const maxBody = 1 << 20

// Server holds the handlers.
type Server struct {
	log     *slog.Logger
	app     *app.App
	version string
	port    string
}

// New makes a Server for an app listening on addr (host:port).
func New(log *slog.Logger, a *app.App, version, addr string) *Server {
	_, port, _ := net.SplitHostPort(addr)
	return &Server{log: log.With("component", "web"), app: a, version: version, port: port}
}

// Handler returns the routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /{$}", http.FileServerFS(static))
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))

	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("PUT /api/config", s.handlePutConfig)
	mux.HandleFunc("GET /api/devices", s.handleDevices)
	mux.HandleFunc("POST /api/learn", s.handleLearn)
	mux.HandleFunc("POST /api/test", s.handleTest)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	return s.guard(mux)
}

// guard rejects requests not addressed to this machine by name (a DNS
// rebinding page would send its own host) and state changes without a JSON
// body (a plain cross-site form can't send one without a CORS preflight).
func (s *Server) guard(next http.Handler) http.Handler {
	allowed := map[string]bool{
		"127.0.0.1:" + s.port: true,
		"localhost:" + s.port: true,
		"[::1]:" + s.port:     true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[r.Host] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if mt != "application/json" {
				http.Error(w, "send JSON", http.StatusUnsupportedMediaType)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

type stateResponse struct {
	Version    string        `json:"version"`
	ConfigPath string        `json:"config_path"`
	Config     config.Config `json:"config"`
	Live       app.Snapshot  `json:"live"`
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, stateResponse{
		Version:    s.version,
		ConfigPath: s.app.ConfigPath(),
		Config:     s.app.Config(),
		Live:       s.app.Snapshot(),
	})
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var cfg config.Config
	if err := decode(r, &cfg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.app.SetConfig(cfg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.log.Info("config saved")
	writeJSON(w, http.StatusOK, s.app.Config())
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	names, err := s.app.Devices()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if names == nil {
		names = []string{}
	}
	writeJSON(w, http.StatusOK, names)
}

func (s *Server) handleLearn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target    engine.Target     `json:"target"`
		Modifiers []config.Modifier `json:"modifiers"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch req.Target {
	case engine.LearnOff, engine.LearnBinding, engine.LearnModifier:
	default:
		writeError(w, http.StatusBadRequest, `target must be "", "binding" or "modifier"`)
		return
	}
	if len(req.Modifiers) > config.MaxModifiers {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d modifiers", config.MaxModifiers))
		return
	}
	s.app.Learn(req.Target, req.Modifiers)
	writeJSON(w, http.StatusOK, s.app.Snapshot())
}

// handleTest sends Blender a command as if a control had been used:
// {"frames":N} scrubs N frames, {"zoom":N} zooms N steps (positive is in),
// {"cut":true} cuts at the playhead, {"undo":true} runs Blender's undo
// once. Exactly one of them.
func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Frames int  `json:"frames"`
		Zoom   int  `json:"zoom"`
		Cut    bool `json:"cut"`
		Undo   bool `json:"undo"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	n := 0
	for _, set := range []bool{req.Frames != 0, req.Zoom != 0, req.Cut, req.Undo} {
		if set {
			n++
		}
	}
	var cmd engine.Command
	switch {
	case n > 1:
		writeError(w, http.StatusBadRequest, "send one of frames, zoom, cut or undo")
		return
	case req.Undo:
		cmd = engine.Command{Type: "undo"}
	case req.Cut:
		cmd = engine.Command{Type: "cut"}
	case req.Zoom != 0:
		if req.Zoom > config.MaxZoomStep || req.Zoom < -config.MaxZoomStep {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("zoom must be between -%d and %d, not 0", config.MaxZoomStep, config.MaxZoomStep))
			return
		}
		cmd = engine.Command{Type: "zoom", Clicks: req.Zoom}
	default:
		if req.Frames == 0 || req.Frames > config.MaxStep || req.Frames < -config.MaxStep {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("frames must be between -%d and %d, not 0", config.MaxStep, config.MaxStep))
			return
		}
		cmd = engine.Command{Type: "scrub", Frames: req.Frames}
	}
	delivered := s.app.Test(cmd)
	writeJSON(w, http.StatusOK, map[string]bool{"delivered": delivered})
}

type eventsPayload struct {
	Live   app.Snapshot `json:"live"`
	Events []app.Event  `json:"events"`
}

// handleEvents streams the live state and new controller events. The client
// passes ?since=<seq> to resume without repeats.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	if r.URL.Query().Get("run") != s.app.RunID() {
		since = 0 // the page last saw an earlier run of the bridge
	}
	w.Header().Set("Content-Type", "text/event-stream")
	flusher.Flush()

	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	var last string
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			p := eventsPayload{Live: s.app.Snapshot(), Events: s.app.EventsSince(since)}
			if n := len(p.Events); n > 0 {
				since = p.Events[n-1].Seq
			}
			data, _ := json.Marshal(p)
			live, _ := json.Marshal(p.Live)
			if len(p.Events) == 0 && string(live) == last {
				continue
			}
			last = string(live)
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("bad request body: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

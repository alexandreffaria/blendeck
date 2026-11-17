// Command synergy is the bridge between a MIDI controller and Blender: it
// reads the controller, maps it through the active profile, sends commands to
// the Blender add-on, and serves the page for setting it all up.
//
// On Windows it is a background app: no console window, an icon in the
// notification area, a log file next to the config, and optionally a
// per-user entry that starts it at sign-in.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/alexandreffaria/synergy/internal/app"
	"github.com/alexandreffaria/synergy/internal/autostart"
	"github.com/alexandreffaria/synergy/internal/config"
	"github.com/alexandreffaria/synergy/internal/link"
	"github.com/alexandreffaria/synergy/internal/logfile"
	"github.com/alexandreffaria/synergy/internal/midi"
	"github.com/alexandreffaria/synergy/internal/tray"
	"github.com/alexandreffaria/synergy/internal/web"
)

var version = "dev"

// maxLog caps synergy.log; one older copy is kept as synergy.log.1.
const maxLog = 1 << 20

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "synergy:", err)
		// Built for Windows there is no console to read that in.
		if runtime.GOOS == "windows" {
			tray.Alert("Synergy couldn't start:\n\n" + err.Error())
		}
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", defaultConfigPath(), "config file")
	webAddr := flag.String("addr", "127.0.0.1:7870", "address for the settings page (keep it on 127.0.0.1)")
	linkAddr := flag.String("link", link.DefaultAddr, "address the Blender add-on connects to")
	noBrowser := flag.Bool("no-browser", false, "don't open the settings page on start")
	verbose := flag.Bool("verbose", false, "debug logging")
	flag.Parse()

	for _, a := range []string{*webAddr, *linkAddr} {
		if !isLoopback(a) {
			return fmt.Errorf("%s is not a loopback address; Synergy only listens on this computer", a)
		}
	}
	url := "http://" + *webAddr + "/"

	// One copy at a time: starting it again (say, from the Start menu while
	// it already runs from sign-in) just opens the page.
	release, first, err := claimInstance(*webAddr)
	if err != nil {
		return err
	}
	if !first {
		openBrowser(url)
		return nil
	}
	defer release()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	out := console{os.Stderr}
	logPath := filepath.Join(filepath.Dir(*configPath), "synergy.log")
	var logw io.Writer = out
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	lf, logErr := logfile.Open(logPath, maxLog)
	if logErr == nil {
		defer lf.Close()
		logw = io.MultiWriter(lf, out)
	}
	log := slog.New(slog.NewTextHandler(logw, &slog.HandlerOptions{Level: level}))
	if logErr != nil {
		log.Warn("no log file", "error", logErr)
	}

	store, err := config.Open(*configPath)
	if err != nil {
		return err
	}
	cfg := store.Get()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	linkLn, err := net.Listen("tcp", *linkAddr)
	if err != nil {
		return fmt.Errorf("listen for Blender on %s (is Synergy already running?): %w", *linkAddr, err)
	}
	webLn, err := net.Listen("tcp", *webAddr)
	if err != nil {
		linkLn.Close()
		return fmt.Errorf("listen for the settings page on %s (is Synergy already running?): %w", *webAddr, err)
	}

	conn := midi.NewConn(log, cfg.Device)
	lnk := link.New(log)
	a := app.New(log, store, conn, lnk, midi.Inputs)
	start := autostart.New("Synergy", autostartExe(), "-no-browser")
	// Before the rename the sign-in entry was called Blendeck: move it, so
	// Start with Windows stays on under the new name.
	if moved, err := start.Migrate("Blendeck", "blendeck.exe"); err != nil {
		log.Warn("left the old Blendeck sign-in entry as it is", "error", err)
	} else if moved {
		log.Info("moved the Blendeck sign-in entry to Synergy", "command", start.Command())
	}

	// Everything that holds a device, socket or file stops when ctx ends;
	// wg says when it has.
	var wg sync.WaitGroup
	wg.Go(func() { conn.Run(ctx) })
	wg.Go(func() { a.Run(ctx, conn.Messages()) })
	wg.Go(func() {
		if err := lnk.Serve(ctx, linkLn); err != nil {
			log.Error("blender link stopped", "error", err)
		}
	})

	srv := &http.Server{
		Handler:           web.New(log, a, version, *webAddr).WithAutostart(start).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		// The page's event stream ends with ctx, so Shutdown doesn't wait on it.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	serveErr := make(chan error, 1)
	wg.Go(func() {
		if err := srv.Serve(webLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		cancel()
	})
	stopped := make(chan struct{}) // closed once all of the above has stopped
	go func() { wg.Wait(); close(stopped) }()

	tr := tray.New(tray.Options{
		Log:       log.With("component", "tray"),
		Icon:      tray.Icon,
		Tooltip:   func() string { return tooltip(a.Snapshot()) },
		OpenPage:  func() { openBrowser(url) },
		Autostart: start,
		// Windows ends the process right after: stop here, not in run's tail.
		OnEndSession: func() {
			log.Info("windows is signing out")
			cancel()
			select {
			case <-stopped:
			case <-time.After(3 * time.Second):
			}
		},
	})
	trayDone := make(chan struct{})
	go func() {
		defer close(trayDone)
		switch err := tr.Run(); {
		case errors.Is(err, tray.ErrUnsupported):
		case err != nil:
			// Keep running without it; the page still works. Built for
			// Windows there would be no other sign it's running.
			log.Error("no tray icon", "error", err)
			go tray.Alert("Synergy is running, but couldn't put its icon in the notification area:\n" +
				err.Error() + "\n\nThe page is at " + url + ". To quit Synergy, end synergy.exe in Task Manager.")
		default:
			cancel() // Quit, or Close below
		}
	}()

	log.Info("synergy running", "version", version, "page", url, "config", store.Path(), "blender_link", *linkAddr)
	if !*noBrowser {
		openBrowser(url)
	}

	<-ctx.Done()
	log.Info("shutting down")
	tr.Close()
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelShutdown()
	_ = srv.Shutdown(shutdown)
	done := make(chan struct{})
	go func() { <-stopped; <-trayDone; close(done) }()
	select {
	case <-done:
		log.Info("stopped")
	case <-time.After(3 * time.Second):
		log.Warn("gave up waiting for everything to stop")
	}
	select {
	case err := <-serveErr:
		return err
	default:
		return nil
	}
}

// tooltip is the tray icon's hover text.
func tooltip(s app.Snapshot) string {
	ctl := "looking for " + s.MIDI.Device
	switch {
	case s.MIDI.Device == "":
		ctl = "none picked"
	case s.MIDI.State == "connected":
		ctl = s.MIDI.Device
	case s.MIDI.State == "error":
		ctl = "error"
	}
	blender := "not connected"
	if s.Blender.Connected {
		blender = "connected"
	}
	return "Synergy\nController: " + ctl + "\nBlender: " + blender
}

// console writes to stderr when there is one; built for Windows there isn't,
// and its errors are ignored.
type console struct{ w io.Writer }

func (c console) Write(p []byte) (int, error) {
	_, _ = c.w.Write(p)
	return len(p), nil
}

// autostartExe is this program's path for the sign-in entry, or "" for a
// `go run` binary that will be deleted.
func autostartExe() string {
	exe, err := os.Executable()
	if err != nil || isGoRunTemp(exe) {
		return ""
	}
	if abs, err := filepath.EvalSymlinks(exe); err == nil {
		exe = abs
	}
	return exe
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// defaultConfigPath is next to the executable on Windows (like a portable
// app), and in the user config folder elsewhere.
func defaultConfigPath() string {
	if runtime.GOOS == "windows" {
		if exe, err := os.Executable(); err == nil && !isGoRunTemp(exe) {
			return filepath.Join(filepath.Dir(exe), "config.json")
		}
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return userConfigPath(dir)
	}
	return "config.json"
}

// userConfigPath is the config in the user config folder dir. Before the
// rename to Synergy it lived under "blendeck": while there is no new one, the
// old one is kept in use (with the log next to it), so profiles, bindings and
// learned controls carry over.
func userConfigPath(dir string) string {
	path := filepath.Join(dir, "synergy", "config.json")
	if old := filepath.Join(dir, "blendeck", "config.json"); !fileExists(path) && fileExists(old) {
		return old
	}
	return path
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// isGoRunTemp is true for binaries built by `go run`, which live in a temp
// folder that disappears.
func isGoRunTemp(exe string) bool {
	rel, err := filepath.Rel(os.TempDir(), exe)
	return err == nil && !filepath.IsAbs(rel) && len(rel) > 2 && rel[:2] != ".."
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if cmd.Start() == nil {
		go cmd.Wait() // don't leave a zombie
	}
}

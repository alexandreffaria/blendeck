// Command blendeck is the bridge between a MIDI controller and Blender: it
// reads the controller, maps it through the active profile, sends commands to
// the Blender add-on, and serves the page for setting it all up.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	"github.com/alexandreffaria/blendeck/internal/app"
	"github.com/alexandreffaria/blendeck/internal/config"
	"github.com/alexandreffaria/blendeck/internal/link"
	"github.com/alexandreffaria/blendeck/internal/midi"
	"github.com/alexandreffaria/blendeck/internal/web"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "blendeck:", err)
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

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	for _, a := range []string{*webAddr, *linkAddr} {
		if !isLoopback(a) {
			return fmt.Errorf("%s is not a loopback address; Blendeck only listens on this computer", a)
		}
	}

	store, err := config.Open(*configPath)
	if err != nil {
		return err
	}
	cfg := store.Get()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	linkLn, err := net.Listen("tcp", *linkAddr)
	if err != nil {
		return fmt.Errorf("listen for Blender on %s (is Blendeck already running?): %w", *linkAddr, err)
	}
	webLn, err := net.Listen("tcp", *webAddr)
	if err != nil {
		linkLn.Close()
		return fmt.Errorf("listen for the settings page on %s (is Blendeck already running?): %w", *webAddr, err)
	}

	conn := midi.NewConn(log, cfg.Device)
	lnk := link.New(log)
	a := app.New(log, store, conn, lnk, midi.Inputs)

	go conn.Run(ctx)
	go a.Run(ctx, conn.Messages())
	go func() {
		if err := lnk.Serve(ctx, linkLn); err != nil {
			log.Error("blender link stopped", "error", err)
		}
	}()

	srv := &http.Server{Handler: web.New(log, a, version, *webAddr).Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	url := "http://" + *webAddr + "/"
	log.Info("blendeck running", "page", url, "config", store.Path(), "blender_link", *linkAddr)
	if !*noBrowser {
		openBrowser(url)
	}
	if err := srv.Serve(webLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
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
		return filepath.Join(dir, "blendeck", "config.json")
	}
	return "config.json"
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
	_ = cmd.Start()
}

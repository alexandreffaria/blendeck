// Package tray puts the bridge in the Windows notification area: an icon
// whose tooltip shows what's connected, and a menu to open the settings
// page, start with Windows, or quit. Elsewhere Run returns ErrUnsupported.
package tray

import (
	_ "embed"
	"encoding/binary"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// Icon is the mascot (scripts/mkicon draws it).
//
//go:embed synergy.ico
var Icon []byte

// ErrUnsupported is returned by Run where there is no tray.
var ErrUnsupported = errors.New("no system tray here")

// Toggle is an on/off setting the menu shows with a check mark.
type Toggle interface {
	Supported() bool
	Enabled() (bool, error)
	Set(on bool) error
}

// Options say what the tray shows and does. Every func is called on the
// tray's own thread, so it must return quickly.
type Options struct {
	Log      *slog.Logger
	Icon     []byte        // an .ico file
	Tooltip  func() string // asked again every couple of seconds
	OpenPage func()
	// Autostart is the "Start with Windows" item; nil leaves it out.
	Autostart Toggle
	// OnEndSession is called when Windows is signing out or shutting down
	// and will end the process once it returns: stop cleanly in it (it may
	// block for a few seconds).
	OnEndSession func()
}

// Tray is the icon. Make it with New, then Run it.
type Tray struct {
	opts  Options
	state // the platform's own, touched only on the tray's thread

	lastOpen time.Time // tray thread only

	mu     sync.Mutex
	closed bool
	hwnd   uintptr
}

// New makes a tray icon.
func New(opts Options) *Tray {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Tooltip == nil {
		opts.Tooltip = func() string { return "Synergy" }
	}
	if opts.OpenPage == nil {
		opts.OpenPage = func() {}
	}
	if opts.OnEndSession == nil {
		opts.OnEndSession = func() {}
	}
	return &Tray{opts: opts}
}

// Close removes the icon and makes Run return. It can be called from any
// goroutine, before, during or after Run.
func (t *Tray) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	if t.hwnd != 0 {
		postClose(t.hwnd)
	}
}

// clickOpen opens the page for a click on the icon. A double click sends
// two clicks; the second, within a second of the first, is ignored.
func (t *Tray) clickOpen(now time.Time) {
	if !t.lastOpen.IsZero() && now.Sub(t.lastOpen) < time.Second {
		return
	}
	t.lastOpen = now
	t.opts.OpenPage()
}

// Menu item ids.
const (
	cmdOpen = iota + 1
	cmdAutostart
	cmdQuit
)

// iconImage finds the image in an .ico file that best fits size×size: the
// smallest one at least that big, or else the biggest. It returns the
// image's bytes (a DIB or PNG, as CreateIconFromResourceEx wants them).
func iconImage(ico []byte, size int) ([]byte, error) {
	bad := errors.New("not an .ico file")
	if len(ico) < 6 || binary.LittleEndian.Uint16(ico[2:]) != 1 {
		return nil, bad
	}
	count := int(binary.LittleEndian.Uint16(ico[4:]))
	if count == 0 || len(ico) < 6+16*count {
		return nil, bad
	}
	best, bestDim := -1, 0
	for i := range count {
		e := ico[6+16*i:]
		dim := int(e[0])
		if dim == 0 {
			dim = 256
		}
		better := best < 0 ||
			(dim >= size && (bestDim < size || dim < bestDim)) ||
			(dim < size && bestDim < size && dim > bestDim)
		if better {
			best, bestDim = i, dim
		}
	}
	e := ico[6+16*best:]
	n := int(binary.LittleEndian.Uint32(e[8:]))
	off := int(binary.LittleEndian.Uint32(e[12:]))
	if off < 0 || n <= 0 || off+n > len(ico) || off+n < off {
		return nil, bad
	}
	return ico[off : off+n], nil
}

// clip cuts s to fit a tooltip of max UTF-16 units (including the NUL),
// on a rune boundary.
func clip(s string, max int) []uint16 {
	var out []uint16
	for _, r := range s {
		var units []uint16
		if r >= 0x10000 {
			r -= 0x10000
			units = []uint16{uint16(0xd800 + r>>10), uint16(0xdc00 + r&0x3ff)}
		} else {
			units = []uint16{uint16(r)}
		}
		if len(out)+len(units) > max-1 {
			break
		}
		out = append(out, units...)
	}
	return out
}

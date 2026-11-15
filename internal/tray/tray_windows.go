package tray

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW         = user32.NewProc("RegisterClassExW")
	procCreateWindowExW          = user32.NewProc("CreateWindowExW")
	procDefWindowProcW           = user32.NewProc("DefWindowProcW")
	procDestroyWindow            = user32.NewProc("DestroyWindow")
	procGetMessageW              = user32.NewProc("GetMessageW")
	procTranslateMessage         = user32.NewProc("TranslateMessage")
	procDispatchMessageW         = user32.NewProc("DispatchMessageW")
	procPostMessageW             = user32.NewProc("PostMessageW")
	procPostQuitMessage          = user32.NewProc("PostQuitMessage")
	procRegisterWindowMessageW   = user32.NewProc("RegisterWindowMessageW")
	procCreatePopupMenu          = user32.NewProc("CreatePopupMenu")
	procAppendMenuW              = user32.NewProc("AppendMenuW")
	procSetMenuDefaultItem       = user32.NewProc("SetMenuDefaultItem")
	procTrackPopupMenu           = user32.NewProc("TrackPopupMenu")
	procDestroyMenu              = user32.NewProc("DestroyMenu")
	procGetCursorPos             = user32.NewProc("GetCursorPos")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procSetTimer                 = user32.NewProc("SetTimer")
	procKillTimer                = user32.NewProc("KillTimer")
	procCreateIconFromResourceEx = user32.NewProc("CreateIconFromResourceEx")
	procDestroyIcon              = user32.NewProc("DestroyIcon")
	procGetSystemMetrics         = user32.NewProc("GetSystemMetrics")
	procMessageBoxW              = user32.NewProc("MessageBoxW")
	procShellNotifyIconW         = shell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandleW         = kernel32.NewProc("GetModuleHandleW")
)

const (
	wmDestroy     = 0x0002
	wmClose       = 0x0010
	wmNull        = 0x0000
	wmContextMenu = 0x007b
	wmTimer       = 0x0113
	wmLButtonUp   = 0x0202
	wmRButtonUp   = 0x0205
	wmApp         = 0x8000
	wmTrayIcon    = wmApp + 1

	nimAdd     = 0
	nimModify  = 1
	nimDelete  = 2
	nifMessage = 0x1
	nifIcon    = 0x2
	nifTip     = 0x4

	mfString    = 0x0
	mfChecked   = 0x8
	mfGrayed    = 0x1
	mfSeparator = 0x800

	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100
	tpmNoNotify    = 0x0080

	smCxSmIcon = 49
	mbIconWarn = 0x30

	tooltipTimer = 1
	tooltipEvery = 2000 // ms

	errClassAlreadyExists = 1410
)

// state is what the tray's thread keeps.
type state struct{ nid notifyIconData }

var className = windows.StringToUTF16Ptr("BlendeckTray")

type wndClassEx struct {
	size       uint32
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   uintptr
	icon       uintptr
	cursor     uintptr
	background uintptr
	menuName   *uint16
	className  *uint16
	iconSm     uintptr
}

type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
	private uint32
}

type point struct{ x, y int32 }

type notifyIconData struct {
	size            uint32
	hwnd            uintptr
	id              uint32
	flags           uint32
	callbackMessage uint32
	icon            uintptr
	tip             [128]uint16
	state           uint32
	stateMask       uint32
	info            [256]uint16
	version         uint32
	infoTitle       [64]uint16
	infoFlags       uint32
	guid            windows.GUID
	balloonIcon     uintptr
}

// The window procedure is a windows.NewCallback, and those are a limited,
// process-wide resource: make it once, and route its calls to the one tray
// that is running.
var (
	setup      sync.Once
	setupErr   error
	wndProcPtr uintptr
	taskbarMsg uint32

	running *Tray // only touched on the tray's thread, between setup and exit
	runMu   sync.Mutex
)

func register() error {
	setup.Do(func() {
		wndProcPtr = windows.NewCallback(wndProc)
		r, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("TaskbarCreated"))))
		taskbarMsg = uint32(r)
		inst, _, _ := procGetModuleHandleW.Call(0)
		wc := wndClassEx{wndProc: wndProcPtr, instance: inst, className: className}
		wc.size = uint32(unsafe.Sizeof(wc))
		if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 && !errors.Is(err, windows.Errno(errClassAlreadyExists)) {
			setupErr = fmt.Errorf("register window class: %w", err)
		}
	})
	return setupErr
}

// Run shows the icon and handles it until Quit is chosen or Close is called.
// It returns nil in both cases; only one Tray can run at a time.
func (t *Tray) Run() error {
	runMu.Lock()
	if running != nil {
		runMu.Unlock()
		return errors.New("a tray icon is already running")
	}
	running = t
	runMu.Unlock()
	defer func() {
		runMu.Lock()
		running = nil
		runMu.Unlock()
	}()

	// A window's messages arrive on the thread that made it.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := register(); err != nil {
		return err
	}
	inst, _, _ := procGetModuleHandleW.Call(0)
	hwnd, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("Blendeck"))), 0, 0, 0, 0, 0, 0, 0, inst, 0)
	if hwnd == 0 {
		return fmt.Errorf("create tray window: %w", err)
	}

	icon, err := loadIcon(t.opts.Icon)
	if err != nil {
		t.opts.Log.Warn("tray icon image", "error", err) // the icon still works, blank
	}
	defer func() {
		if icon != 0 {
			procDestroyIcon.Call(icon)
		}
	}()
	t.nid = notifyIconData{hwnd: hwnd, id: 1, flags: nifMessage | nifIcon | nifTip, callbackMessage: wmTrayIcon, icon: icon}
	t.nid.size = uint32(unsafe.Sizeof(t.nid))
	t.setTip(t.opts.Tooltip())
	if !t.notify(nimAdd) {
		// Explorer may not be up yet (starting at sign-in): TaskbarCreated
		// adds it when it is.
		t.opts.Log.Warn("tray icon not added yet; waiting for the taskbar")
	}
	procSetTimer.Call(hwnd, tooltipTimer, tooltipEvery, 0)

	t.mu.Lock()
	t.hwnd = hwnd
	closed := t.closed
	t.mu.Unlock()
	if closed {
		postClose(hwnd)
	}

	var m msg
	for {
		r, _, err := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		switch int32(r) {
		case 0:
			return nil
		case -1:
			return fmt.Errorf("tray message loop: %w", err)
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func postClose(hwnd uintptr) { procPostMessageW.Call(hwnd, wmClose, 0, 0) }

func wndProc(hwnd, message, wParam, lParam uintptr) uintptr {
	t := running
	if t == nil || t.nid.hwnd != hwnd {
		r, _, _ := procDefWindowProcW.Call(hwnd, message, wParam, lParam)
		return r
	}
	switch uint32(message) {
	case wmTrayIcon:
		switch uint32(lParam) & 0xffff {
		case wmLButtonUp:
			t.opts.OpenPage()
		case wmRButtonUp, wmContextMenu:
			t.showMenu()
		}
		return 0
	case wmTimer:
		if t.setTip(t.opts.Tooltip()) {
			t.notify(nimModify)
		}
		return 0
	case wmClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procKillTimer.Call(hwnd, tooltipTimer)
		t.notify(nimDelete)
		t.mu.Lock()
		t.hwnd = 0
		t.mu.Unlock()
		procPostQuitMessage.Call(0)
		return 0
	}
	if taskbarMsg != 0 && uint32(message) == taskbarMsg {
		t.notify(nimAdd) // Explorer restarted, or started after us
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, message, wParam, lParam)
	return r
}

func (t *Tray) notify(op uintptr) bool {
	r, _, _ := procShellNotifyIconW.Call(op, uintptr(unsafe.Pointer(&t.nid)))
	return r != 0
}

// setTip sets the tooltip text and says whether it changed.
func (t *Tray) setTip(s string) bool {
	var tip [128]uint16
	copy(tip[:], clip(s, len(tip)))
	if tip == t.nid.tip {
		return false
	}
	t.nid.tip = tip
	return true
}

func (t *Tray) showMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	appendItem(menu, mfString, cmdOpen, "Open settings page")
	procSetMenuDefaultItem.Call(menu, cmdOpen, 0)
	if a := t.opts.Autostart; a != nil {
		flags := uintptr(mfString)
		if !a.Supported() {
			flags |= mfGrayed
		} else if on, err := a.Enabled(); err != nil {
			t.opts.Log.Warn("read start with Windows", "error", err)
		} else if on {
			flags |= mfChecked
		}
		appendItem(menu, mfSeparator, 0, "")
		appendItem(menu, flags, cmdAutostart, "Start with Windows")
	}
	appendItem(menu, mfSeparator, 0, "")
	appendItem(menu, mfString, cmdQuit, "Quit Blendeck")

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// Without this the menu doesn't close when you click elsewhere.
	procSetForegroundWindow.Call(t.nid.hwnd)
	cmd, _, _ := procTrackPopupMenu.Call(menu, tpmRightButton|tpmReturnCmd|tpmNoNotify,
		uintptr(pt.x), uintptr(pt.y), 0, t.nid.hwnd, 0)
	procPostMessageW.Call(t.nid.hwnd, wmNull, 0, 0)

	switch cmd {
	case cmdOpen:
		t.opts.OpenPage()
	case cmdAutostart:
		a := t.opts.Autostart
		on, _ := a.Enabled()
		if err := a.Set(!on); err != nil {
			t.opts.Log.Error("set start with Windows", "error", err)
			messageBox("Blendeck couldn't change \"Start with Windows\":\n" + err.Error())
		} else {
			t.opts.Log.Info("start with Windows", "on", !on)
		}
	case cmdQuit:
		postClose(t.nid.hwnd)
	}
}

func appendItem(menu, flags, id uintptr, text string) {
	var p uintptr
	if text != "" {
		p = uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(text)))
	}
	procAppendMenuW.Call(menu, flags, id, p)
}

// loadIcon makes an icon of the small-icon size from an .ico file.
func loadIcon(ico []byte) (uintptr, error) {
	size, _, _ := procGetSystemMetrics.Call(smCxSmIcon)
	if size == 0 {
		size = 16
	}
	img, err := iconImage(ico, int(size))
	if err != nil {
		return 0, err
	}
	h, _, err := procCreateIconFromResourceEx.Call(uintptr(unsafe.Pointer(&img[0])), uintptr(len(img)),
		1, 0x00030000, size, size, 0)
	if h == 0 {
		return 0, fmt.Errorf("CreateIconFromResourceEx: %w", err)
	}
	return h, nil
}

func messageBox(text string) {
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(text))),
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("Blendeck"))), mbIconWarn)
}

// Alert shows a message box; for errors when there is no console to print to.
func Alert(text string) { messageBox(text) }

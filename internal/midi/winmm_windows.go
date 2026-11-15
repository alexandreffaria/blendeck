//go:build windows

package midi

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	winmm                = windows.NewLazySystemDLL("winmm.dll")
	procMidiInGetNumDevs = winmm.NewProc("midiInGetNumDevs")
	procMidiInGetDevCaps = winmm.NewProc("midiInGetDevCapsW")
	procMidiInOpen       = winmm.NewProc("midiInOpen")
	procMidiInStart      = winmm.NewProc("midiInStart")
	procMidiInStop       = winmm.NewProc("midiInStop")
	procMidiInReset      = winmm.NewProc("midiInReset")
	procMidiInClose      = winmm.NewProc("midiInClose")
)

const (
	callbackFunction = 0x30000
	mimOpen          = 0x3C1
	mimClose         = 0x3C2
	mimData          = 0x3C3
)

type midiInCaps struct {
	Mid           uint16
	Pid           uint16
	DriverVersion uint32
	Pname         [32]uint16
	Support       uint32
}

// Inputs lists the names of the MIDI input devices, in winmm's order.
func Inputs() ([]string, error) {
	n, _, _ := procMidiInGetNumDevs.Call()
	names := make([]string, 0, n)
	for i := uintptr(0); i < n; i++ {
		var caps midiInCaps
		r, _, _ := procMidiInGetDevCaps.Call(i, uintptr(unsafe.Pointer(&caps)), unsafe.Sizeof(caps))
		if r != 0 {
			return nil, fmt.Errorf("midiInGetDevCaps(%d): error %d", i, r)
		}
		names = append(names, windows.UTF16ToString(caps.Pname[:]))
	}
	return names, nil
}

// winmm calls one callback for every open device; dwInstance says which.
// windows.NewCallback has a process-wide limit, so it is made once.
var (
	handlersMu  sync.Mutex
	handlers    = map[uintptr]*winmmInput{}
	nextID      uintptr
	callbackPtr = windows.NewCallback(midiInProc)
)

func midiInProc(h, msg, instance, param1, param2 uintptr) uintptr {
	handlersMu.Lock()
	in := handlers[instance]
	handlersMu.Unlock()
	if in == nil {
		return 0
	}
	switch msg {
	case mimData:
		if m, ok := Parse(byte(param1), byte(param1>>8), byte(param1>>16)); ok {
			in.onMsg(m)
		}
	case mimClose:
		in.closedOnce.Do(func() { close(in.closed) })
	}
	return 0
}

type winmmInput struct {
	id         uintptr
	handle     uintptr
	onMsg      func(Message)
	closed     chan struct{}
	closedOnce sync.Once
	stopOnce   sync.Once
}

func openInput(index int, onMsg func(Message)) (input, error) {
	handlersMu.Lock()
	nextID++
	in := &winmmInput{id: nextID, onMsg: onMsg, closed: make(chan struct{})}
	handlers[in.id] = in
	handlersMu.Unlock()

	r, _, _ := procMidiInOpen.Call(uintptr(unsafe.Pointer(&in.handle)), uintptr(index), callbackPtr, in.id, callbackFunction)
	if r != 0 {
		in.forget()
		return nil, fmt.Errorf("midiInOpen: error %d%s", r, openHint(r))
	}
	if r, _, _ := procMidiInStart.Call(in.handle); r != 0 {
		procMidiInClose.Call(in.handle)
		in.forget()
		return nil, fmt.Errorf("midiInStart: error %d", r)
	}
	return in, nil
}

func openHint(code uintptr) string {
	if code == 4 { // MMSYSERR_ALLOCATED
		return " (another program has the device open; close the Loupedeck software or other MIDI apps)"
	}
	return ""
}

func (in *winmmInput) forget() {
	handlersMu.Lock()
	delete(handlers, in.id)
	handlersMu.Unlock()
}

func (in *winmmInput) Closed() <-chan struct{} { return in.closed }

func (in *winmmInput) Close() error {
	var err error
	in.stopOnce.Do(func() {
		procMidiInStop.Call(in.handle)
		procMidiInReset.Call(in.handle)
		if r, _, _ := procMidiInClose.Call(in.handle); r != 0 {
			err = errors.New("midiInClose failed")
		}
		in.forget()
		in.closedOnce.Do(func() { close(in.closed) })
	})
	return err
}

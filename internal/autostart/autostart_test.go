package autostart

import "testing"

func TestCommandQuotesPathsWithSpaces(t *testing.T) {
	e := New("Blendeck", `C:\Program Files\Blendeck\blendeck.exe`, "-no-browser")
	if got, want := e.Command(), `"C:\Program Files\Blendeck\blendeck.exe" -no-browser`; got != want {
		t.Fatalf("command = %s, want %s", got, want)
	}
	if got := New("x", `C:\b\blendeck.exe`).Command(); got != `C:\b\blendeck.exe` {
		t.Fatalf("command = %s", got)
	}
}

func TestNoExeIsUnsupported(t *testing.T) {
	e := New("Blendeck", "")
	if e.Supported() {
		t.Fatal("an entry with no exe is supported")
	}
	if on, err := e.Enabled(); on || err != nil {
		t.Fatalf("Enabled = %v, %v", on, err)
	}
	if err := e.Set(true); err != ErrUnsupported {
		t.Fatalf("Set = %v, want ErrUnsupported", err)
	}
}

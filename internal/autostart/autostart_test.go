package autostart

import "testing"

func TestCommandQuotesPathsWithSpaces(t *testing.T) {
	e := New("Consort", `C:\Program Files\Consort\consort.exe`, "-no-browser")
	if got, want := e.Command(), `"C:\Program Files\Consort\consort.exe" -no-browser`; got != want {
		t.Fatalf("command = %s, want %s", got, want)
	}
	if got := New("x", `C:\b\consort.exe`).Command(); got != `C:\b\consort.exe` {
		t.Fatalf("command = %s", got)
	}
}

func TestNoExeIsUnsupported(t *testing.T) {
	e := New("Consort", "")
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

func TestCommandExe(t *testing.T) {
	for cmd, want := range map[string]string{
		`"C:\Program Files\Blendeck\blendeck.exe" -no-browser`: `C:\Program Files\Blendeck\blendeck.exe`,
		`C:\b\blendeck.exe -no-browser`:                        `C:\b\blendeck.exe`,
		`  C:\b\blendeck.exe  `:                                `C:\b\blendeck.exe`,
		`"C:\no end`:                                           `C:\no end`,
		``:                                                     ``,
	} {
		if got := commandExe(cmd); got != want {
			t.Errorf("commandExe(%q) = %q, want %q", cmd, got, want)
		}
	}
	if got := baseName(`C:\b\Blendeck.EXE`); got != "Blendeck.EXE" {
		t.Errorf("baseName = %q", got)
	}
}

func TestMigrateUnsupportedDoesNothing(t *testing.T) {
	if moved, err := New("Consort", "").Migrate("Blendeck", "blendeck.exe"); moved || err != nil {
		t.Fatalf("Migrate = %v, %v", moved, err)
	}
}

func TestOwns(t *testing.T) {
	e := New("Consort", `C:\Tools\consort.exe`, "-no-browser")
	for v, want := range map[string]bool{
		`C:\Tools\consort.exe -no-browser`:              true,
		`c:\tools\CONSORT.exe`:                          true, // this exe, whatever it passes
		`"C:\Old Place\consort.exe" -no-browser`:        true, // a moved copy
		`"C:\Program Files\Consort\consort.exe" --tray`: false,
		`"C:\Program Files\Consort\consort.exe"`:        false,
		`C:\Program Files\Other\consort-service.exe`:    false,
	} {
		if got := e.owns(v); got != want {
			t.Errorf("owns(%q) = %v, want %v", v, got, want)
		}
	}
}

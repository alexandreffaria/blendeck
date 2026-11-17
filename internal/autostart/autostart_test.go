package autostart

import "testing"

func TestCommandQuotesPathsWithSpaces(t *testing.T) {
	e := New("Synergy", `C:\Program Files\Synergy\synergy.exe`, "-no-browser")
	if got, want := e.Command(), `"C:\Program Files\Synergy\synergy.exe" -no-browser`; got != want {
		t.Fatalf("command = %s, want %s", got, want)
	}
	if got := New("x", `C:\b\synergy.exe`).Command(); got != `C:\b\synergy.exe` {
		t.Fatalf("command = %s", got)
	}
}

func TestNoExeIsUnsupported(t *testing.T) {
	e := New("Synergy", "")
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
	if moved, err := New("Synergy", "").Migrate("Blendeck", "blendeck.exe"); moved || err != nil {
		t.Fatalf("Migrate = %v, %v", moved, err)
	}
}

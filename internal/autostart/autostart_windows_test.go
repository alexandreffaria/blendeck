package autostart

import (
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// testEntry works under a throwaway key instead of the real Run key.
func testEntry(t *testing.T, exe string) *Entry {
	t.Helper()
	key := fmt.Sprintf(`Software\BlendeckTest-%d`, os.Getpid())
	t.Cleanup(func() { _ = registry.DeleteKey(registry.CURRENT_USER, key) })
	e := New("Blendeck", exe, "-no-browser")
	e.key = key
	return e
}

func TestSetOnOff(t *testing.T) {
	e := testEntry(t, `C:\Tools\Blend Deck\blendeck.exe`)
	if on, err := e.Enabled(); on || err != nil {
		t.Fatalf("before: Enabled = %v, %v", on, err)
	}
	if err := e.Set(false); err != nil {
		t.Fatalf("turning off a missing entry: %v", err)
	}
	if err := e.Set(true); err != nil {
		t.Fatal(err)
	}
	if on, err := e.Enabled(); !on || err != nil {
		t.Fatalf("after on: Enabled = %v, %v", on, err)
	}
	if v, err := e.read(); err != nil || v != `"C:\Tools\Blend Deck\blendeck.exe" -no-browser` {
		t.Fatalf("value = %q, %v", v, err)
	}
	if err := e.Set(false); err != nil {
		t.Fatal(err)
	}
	if on, err := e.Enabled(); on || err != nil {
		t.Fatalf("after off: Enabled = %v, %v", on, err)
	}
}

func TestEntryForAnotherCopyIsOff(t *testing.T) {
	old := testEntry(t, `C:\old\blendeck.exe`)
	if err := old.Set(true); err != nil {
		t.Fatal(err)
	}
	moved := New("Blendeck", `C:\new\blendeck.exe`, "-no-browser")
	moved.key = old.key
	if on, _ := moved.Enabled(); on {
		t.Fatal("an entry for another copy counts as on")
	}
	if err := moved.Set(true); err != nil {
		t.Fatal(err)
	}
	if on, _ := moved.Enabled(); !on {
		t.Fatal("turning it on didn't point it at this copy")
	}
}

package autostart

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// testEntry works under a throwaway key instead of the real Run key.
func testEntry(t *testing.T, exe string) *Entry {
	t.Helper()
	key := fmt.Sprintf(`Software\ConsortTest-%d`, os.Getpid())
	t.Cleanup(func() { _ = registry.DeleteKey(registry.CURRENT_USER, key) })
	e := New("Consort", exe, "-no-browser")
	e.key = key
	return e
}

func TestSetOnOff(t *testing.T) {
	e := testEntry(t, `C:\Tools\Blend Deck\consort.exe`)
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
	if v, err := e.read(); err != nil || v != `"C:\Tools\Blend Deck\consort.exe" -no-browser` {
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
	old := testEntry(t, `C:\old\consort.exe`)
	if err := old.Set(true); err != nil {
		t.Fatal(err)
	}
	moved := New("Consort", `C:\new\consort.exe`, "-no-browser")
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

// oldEntry writes a Run value under the name the program had before.
func oldEntry(t *testing.T, key, command string) {
	t.Helper()
	k, _, err := registry.CreateKey(registry.CURRENT_USER, key, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if err := k.SetStringValue("Blendeck", command); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateMovesTheOldEntry(t *testing.T) {
	for _, old := range []string{
		`"C:\Tools\Blend Deck\blendeck.exe" -no-browser`, // the old exe, maybe elsewhere
		`C:\Tools\consort.exe -no-browser`,               // this exe already, by its old name
	} {
		e := testEntry(t, `C:\Tools\consort.exe`)
		e.name = "Consort"
		oldEntry(t, e.key, old)
		moved, err := e.Migrate("Blendeck", "blendeck.exe")
		if !moved || err != nil {
			t.Fatalf("%s: Migrate = %v, %v", old, moved, err)
		}
		if on, err := e.Enabled(); !on || err != nil {
			t.Fatalf("%s: the new entry isn't on: %v, %v", old, on, err)
		}
		if v, err := e.readValue("Blendeck"); v != "" || err != nil {
			t.Fatalf("%s: the old entry is still there: %q, %v", old, v, err)
		}
		if moved, err := e.Migrate("Blendeck", "blendeck.exe"); moved || err != nil {
			t.Fatalf("%s: a second Migrate = %v, %v", old, moved, err)
		}
	}
}

func TestMigrateLeavesOthersAlone(t *testing.T) {
	e := testEntry(t, `C:\Tools\consort.exe`)
	e.name = "Consort"
	if moved, err := e.Migrate("Blendeck", "blendeck.exe"); moved || err != nil {
		t.Fatalf("no old entry: Migrate = %v, %v", moved, err)
	}
	if on, _ := e.Enabled(); on {
		t.Fatal("no old entry, but the new one was turned on")
	}

	oldEntry(t, e.key, `"C:\Other\something.exe" /quiet`)
	if moved, err := e.Migrate("Blendeck", "blendeck.exe"); moved || err != nil {
		t.Fatalf("another program's entry: Migrate = %v, %v", moved, err)
	}
	if v, _ := e.readValue("Blendeck"); v == "" {
		t.Fatal("another program's entry was removed")
	}
	if on, _ := e.Enabled(); on {
		t.Fatal("another program's entry turned the new one on")
	}

	// A `go run` binary has no entry of its own: the old one stays.
	oldEntry(t, e.key, `C:\Tools\blendeck.exe -no-browser`)
	gone := New("Consort", "")
	gone.key = e.key
	if moved, err := gone.Migrate("Blendeck", "blendeck.exe"); moved || err != nil {
		t.Fatalf("unsupported: Migrate = %v, %v", moved, err)
	}
	if v, _ := e.readValue("Blendeck"); v == "" {
		t.Fatal("unsupported, but the old entry was removed")
	}
}

// foreign is a "Consort" value another program of that name keeps.
const foreign = `"C:\Program Files\Consort\consort.exe" --tray`

func setValue(t *testing.T, key, name, command string) {
	t.Helper()
	k, _, err := registry.CreateKey(registry.CURRENT_USER, key, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if err := k.SetStringValue(name, command); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateLeavesAnotherProgramsConsortAlone(t *testing.T) {
	e := testEntry(t, `C:\Tools\consort.exe`)
	e.name = "Consort"
	setValue(t, e.key, "Consort", foreign)
	oldEntry(t, e.key, `C:\Tools\blendeck.exe -no-browser`)
	moved, err := e.Migrate("Blendeck", "blendeck.exe")
	if moved || !errors.Is(err, ErrTaken) {
		t.Fatalf("Migrate = %v, %v; want ErrTaken", moved, err)
	}
	if v, _ := e.readValue("Consort"); v != foreign {
		t.Fatalf("the other program's value changed: %q", v)
	}
	if v, _ := e.readValue("Blendeck"); v == "" {
		t.Fatal("the old entry was removed")
	}
}

func TestSetNeverTouchesAnotherProgramsValue(t *testing.T) {
	e := testEntry(t, `C:\Tools\consort.exe`)
	e.name = "Consort"
	setValue(t, e.key, "Consort", foreign)
	if on, _ := e.Enabled(); on {
		t.Fatal("another program's value counts as on")
	}
	if err := e.Set(true); !errors.Is(err, ErrTaken) {
		t.Fatalf("Set(true) = %v; want ErrTaken", err)
	}
	if err := e.Set(false); err != nil {
		t.Fatalf("Set(false) = %v", err)
	}
	if v, _ := e.readValue("Consort"); v != foreign {
		t.Fatalf("the other program's value changed: %q", v)
	}

	// A copy of this program elsewhere (same file name and arguments) is
	// still ours: turning it on points it here, turning it off removes it.
	setValue(t, e.key, "Consort", `"C:\Old Place\consort.exe" -no-browser`)
	if err := e.Set(true); err != nil {
		t.Fatal(err)
	}
	if on, _ := e.Enabled(); !on {
		t.Fatal("turning on didn't point it here")
	}
	setValue(t, e.key, "Consort", `"C:\Old Place\consort.exe" -no-browser`)
	if err := e.Set(false); err != nil {
		t.Fatal(err)
	}
	if v, _ := e.readValue("Consort"); v != "" {
		t.Fatalf("our old copy's value stayed: %q", v)
	}
}

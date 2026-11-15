package logfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func TestRotatesPastTheCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blendeck.log")
	l, err := Open(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for _, s := range []string{"aaaa\n", "bbbb\n", "cccc\n", "dddd\n", "eeee\n"} {
		if _, err := l.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	if got := read(t, path); got != "eeee\n" {
		t.Fatalf("log = %q", got)
	}
	if got := read(t, path+".1"); got != "cccc\ndddd\n" {
		t.Fatalf("old log = %q", got)
	}
}

func TestOpenRotatesAnOversizedFileAndAppendsToASmallOne(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.log")
	os.WriteFile(big, []byte(strings.Repeat("x", 20)), 0o644)
	l, err := Open(big, 10)
	if err != nil {
		t.Fatal(err)
	}
	l.Write([]byte("new\n"))
	l.Close()
	if read(t, big) != "new\n" || len(read(t, big+".1")) != 20 {
		t.Fatalf("big = %q, big.1 = %d bytes", read(t, big), len(read(t, big+".1")))
	}

	small := filepath.Join(dir, "small.log")
	os.WriteFile(small, []byte("old\n"), 0o644)
	l, err = Open(small, 100)
	if err != nil {
		t.Fatal(err)
	}
	l.Write([]byte("new\n"))
	l.Close()
	if got := read(t, small); got != "old\nnew\n" {
		t.Fatalf("small = %q", got)
	}
}

func TestOneWriteBiggerThanTheCapStillLands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	l, _ := Open(path, 4)
	defer l.Close()
	if n, err := l.Write([]byte("0123456789")); n != 10 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if _, err := l.Write([]byte("z")); err != nil {
		t.Fatal(err)
	}
	if read(t, path) != "z" || read(t, path+".1") != "0123456789" {
		t.Fatalf("log = %q, old = %q", read(t, path), read(t, path+".1"))
	}
}

func TestWriteAfterClose(t *testing.T) {
	l, _ := Open(filepath.Join(t.TempDir(), "x.log"), 100)
	l.Close()
	if _, err := l.Write([]byte("x")); err == nil {
		t.Fatal("write after close succeeded")
	}
}

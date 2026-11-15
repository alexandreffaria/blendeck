package tray

import (
	"encoding/binary"
	"testing"
	"time"
	"unicode/utf16"
)

func TestIconHasTheTraySizes(t *testing.T) {
	for _, size := range []int{16, 20, 24, 32, 48} {
		img, err := iconImage(Icon, size)
		if err != nil {
			t.Fatal(err)
		}
		if w := int(binary.LittleEndian.Uint32(img[4:])); w != size {
			t.Errorf("for %d px got a %d px image", size, w)
		}
	}
}

func TestIconImagePicksTheClosestFit(t *testing.T) {
	cases := map[int]int{1: 16, 17: 20, 30: 32, 33: 40, 64: 64, 300: 64}
	for want, got := range cases {
		img, err := iconImage(Icon, want)
		if err != nil {
			t.Fatal(err)
		}
		if w := int(binary.LittleEndian.Uint32(img[4:])); w != got {
			t.Errorf("for %d px got %d, want %d", want, w, got)
		}
	}
}

func TestIconImageRejectsJunk(t *testing.T) {
	bad := [][]byte{nil, []byte("hello!"), {0, 0, 1, 0, 1, 0}}
	// An entry pointing past the end.
	e := make([]byte, 6+16)
	binary.LittleEndian.PutUint16(e[2:], 1)
	binary.LittleEndian.PutUint16(e[4:], 1)
	binary.LittleEndian.PutUint32(e[6+8:], 100)
	binary.LittleEndian.PutUint32(e[6+12:], 22)
	bad = append(bad, e)
	for _, b := range bad {
		if _, err := iconImage(b, 16); err == nil {
			t.Errorf("accepted %v", b)
		}
	}
}

func TestClipKeepsWholeCharacters(t *testing.T) {
	if got := string(utf16.Decode(clip("Blendeck", 128))); got != "Blendeck" {
		t.Fatalf("clip = %q", got)
	}
	if got := clip("abcdef", 4); string(utf16.Decode(got)) != "abc" {
		t.Fatalf("clip = %q", string(utf16.Decode(got)))
	}
	// A character outside the BMP takes two units; it doesn't get split.
	if got := clip("ab😀", 4); string(utf16.Decode(got)) != "ab" {
		t.Fatalf("clip = %q", string(utf16.Decode(got)))
	}
}

func TestCloseBeforeRun(t *testing.T) {
	tr := New(Options{})
	tr.Close()
	tr.Close()
}

func TestDoubleClickOpensThePageOnce(t *testing.T) {
	opened := 0
	tr := New(Options{OpenPage: func() { opened++ }})
	t0 := time.Now()
	tr.clickOpen(t0)
	tr.clickOpen(t0.Add(300 * time.Millisecond)) // the double click's second up
	if opened != 1 {
		t.Fatalf("a double click opened the page %d times", opened)
	}
	tr.clickOpen(t0.Add(2 * time.Second))
	if opened != 2 {
		t.Fatalf("a later click didn't open it (%d)", opened)
	}
}

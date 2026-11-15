package midi

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		name           string
		status, d1, d2 byte
		want           Message
		ok             bool
	}{
		{"cc", 0xB0, 3, 1, Message{ControlChange, 1, 3, 1}, true},
		{"cc channel 16", 0xBF, 3, 127, Message{ControlChange, 16, 3, 127}, true},
		{"note on", 0x92, 60, 100, Message{NoteOn, 3, 60, 100}, true},
		{"note on velocity 0 is off", 0x90, 60, 0, Message{NoteOff, 1, 60, 0}, true},
		{"note off", 0x80, 60, 64, Message{NoteOff, 1, 60, 64}, true},
		{"pitch bend centre", 0xE0, 0x00, 0x40, Message{PitchBend, 1, 0, 8192}, true},
		{"pitch bend max", 0xE0, 0x7F, 0x7F, Message{PitchBend, 1, 0, 16383}, true},
		{"program", 0xC1, 5, 99, Message{ProgramChange, 2, 5, 0}, true},
		{"aftertouch", 0xD0, 70, 0, Message{Aftertouch, 1, 0, 70}, true},
		{"high bit on data is masked", 0xB0, 0x83, 0xFF, Message{ControlChange, 1, 3, 127}, true},
		{"system message", 0xF8, 0, 0, Message{}, false},
		{"sysex", 0xF0, 1, 2, Message{}, false},
		{"data byte", 0x40, 0, 0, Message{}, false},
	}
	for _, c := range cases {
		got, ok := Parse(c.status, c.d1, c.d2)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: Parse(%02X %d %d) = %+v, %v; want %+v, %v", c.name, c.status, c.d1, c.d2, got, ok, c.want, c.ok)
		}
	}
}

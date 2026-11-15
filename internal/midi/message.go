// Package midi reads short MIDI messages from an input device and keeps it
// connected across unplugs.
package midi

import "fmt"

// Kind is the type of a channel message.
type Kind string

const (
	NoteOn        Kind = "note_on"
	NoteOff       Kind = "note_off"
	ControlChange Kind = "cc"
	PitchBend     Kind = "pitch_bend"
	ProgramChange Kind = "program"
	Aftertouch    Kind = "aftertouch"
	PolyPressure  Kind = "poly_pressure"
)

// Message is one decoded channel message. Channel is 1 to 16. For pitch bend,
// Value is the 14-bit value (0..16383) and Number is 0.
type Message struct {
	Kind    Kind `json:"kind"`
	Channel int  `json:"channel"`
	Number  int  `json:"number"`
	Value   int  `json:"value"`
}

// Parse decodes the three bytes of a short message as winmm hands them over.
// System messages (status 0xF0 and up) and stray data bytes return ok=false.
func Parse(status, d1, d2 byte) (Message, bool) {
	if status < 0x80 || status >= 0xF0 {
		return Message{}, false
	}
	m := Message{Channel: int(status&0x0F) + 1, Number: int(d1 & 0x7F), Value: int(d2 & 0x7F)}
	switch status & 0xF0 {
	case 0x80:
		m.Kind = NoteOff
	case 0x90:
		m.Kind = NoteOn
		// Velocity 0 is the running-status way of saying note off
		if m.Value == 0 {
			m.Kind = NoteOff
		}
	case 0xA0:
		m.Kind = PolyPressure
	case 0xB0:
		m.Kind = ControlChange
	case 0xC0:
		m.Kind = ProgramChange
		m.Value = 0
	case 0xD0:
		m.Kind = Aftertouch
		m.Value = m.Number
		m.Number = 0
	case 0xE0:
		m.Kind = PitchBend
		m.Value = int(d2&0x7F)<<7 | int(d1&0x7F)
		m.Number = 0
	}
	return m, true
}

// String is a short human form, used in logs.
func (m Message) String() string {
	return fmt.Sprintf("%s ch%d #%d = %d", m.Kind, m.Channel, m.Number, m.Value)
}

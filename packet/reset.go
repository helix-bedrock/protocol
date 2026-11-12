package packet

import "reflect"

// Resetter is a packet that clears itself for reading into again keeping some of its
// storage (PlayerAuthInput keeps its flags and block actions).
type Resetter interface {
	Reset()
}

// Reset clears pk so it can be read into again as if new: its own Reset if it has one,
// else every field set to zero. Reading a packet leaves fields it does not read (an
// absent optional, a field only some flags carry) as they were, so a reused packet must
// be cleared first.
func Reset(pk Packet) {
	if r, ok := pk.(Resetter); ok {
		r.Reset()
		return
	}
	if v := reflect.ValueOf(pk); v.Kind() == reflect.Pointer && !v.IsNil() {
		v.Elem().SetZero()
	}
}

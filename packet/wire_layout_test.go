package packet

import (
	"bytes"
	"testing"

	"github.com/helix-bedrock/protocol"
)

// assertLayout checks that pk writes exactly want, and that want reads back to the same bytes with nothing
// left over.
func assertLayout(t *testing.T, pk, into Packet, want []byte) {
	t.Helper()
	var written bytes.Buffer
	pk.Marshal(protocol.NewWriter(&written, 0))
	if !bytes.Equal(written.Bytes(), want) {
		t.Fatalf("wrote % x, want % x", written.Bytes(), want)
	}
	in := bytes.NewBuffer(want)
	into.Marshal(protocol.NewReader(in, 0, false))
	if in.Len() != 0 {
		t.Fatalf("%d bytes left after reading", in.Len())
	}
	var again bytes.Buffer
	into.Marshal(protocol.NewWriter(&again, 0))
	if !bytes.Equal(again.Bytes(), want) {
		t.Fatalf("read back as % x, want % x", again.Bytes(), want)
	}
}

// TestCameraInstructionAttachLayout pins the attached actor ID to eight fixed bytes, not a varint.
func TestCameraInstructionAttachLayout(t *testing.T) {
	pk := &CameraInstruction{AttachToEntity: protocol.Option(int64(2))}
	assertLayout(t, pk, &CameraInstruction{}, []byte{
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // set, clear, fade, target, remove target, field of view, spline: all absent
		0x01, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // attach
		0x00, // no detach
	})
}

// TestDebugMarkerDurationLayout pins the marker duration to eight fixed bytes, not a varint.
func TestDebugMarkerDurationLayout(t *testing.T) {
	var written bytes.Buffer
	x := DebugMarkerData{Duration: 2}
	x.Marshal(protocol.NewWriter(&written, 0))
	got := written.Bytes()
	want := []byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	if len(got) < len(want) || !bytes.Equal(got[len(got)-len(want):], want) {
		t.Fatalf("wrote % x, want it to end in % x", got, want)
	}
}

// TestPlayAudioContentPlaybackTypeLayout pins the playback type to its name, as a string.
func TestPlayAudioContentPlaybackTypeLayout(t *testing.T) {
	var written bytes.Buffer
	pk := &ClientboundPlayAudioContent{PlaybackType: AudioContentPlaybackMusic}
	pk.Marshal(protocol.NewWriter(&written, 0))
	want := []byte{0x00, 0x00, 0x05, 'M', 'u', 's', 'i', 'c'}
	if got := written.Bytes(); len(got) < len(want) || !bytes.Equal(got[:len(want)], want) {
		t.Fatalf("wrote % x, want it to start with % x", got, want)
	}
}

// TestPlayerVideoCaptureLayout pins the action as a varuint variant index, with start first: 300 needs two
// varuint bytes, and only start carries the frame rate and prefix.
func TestPlayerVideoCaptureLayout(t *testing.T) {
	start := &PlayerVideoCapture{Action: PlayerVideoCaptureActionStart, FrameRate: 30, FilePrefix: "f"}
	assertLayout(t, start, &PlayerVideoCapture{}, []byte{0x00, 0x1E, 0x00, 0x00, 0x00, 0x01, 'f'})
	assertLayout(t, &PlayerVideoCapture{Action: PlayerVideoCaptureActionStop}, &PlayerVideoCapture{}, []byte{0x01})
	assertLayout(t, &PlayerVideoCapture{Action: 300}, &PlayerVideoCapture{}, []byte{0xAC, 0x02})
}

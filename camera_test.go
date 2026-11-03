package protocol

import (
	"bytes"
	"testing"

	"github.com/go-gl/mathgl/mgl32"
)

// marshaler is the part of the Marshal types the layout tests below need.
type marshaler interface {
	Marshal(r IO)
}

// assertLayout checks that v writes exactly want, and that want reads back to the same bytes with nothing
// left over.
func assertLayout(t *testing.T, v, into marshaler, want []byte) {
	t.Helper()
	var written bytes.Buffer
	v.Marshal(NewWriter(&written, 0))
	if !bytes.Equal(written.Bytes(), want) {
		t.Fatalf("wrote % x, want % x", written.Bytes(), want)
	}
	in := bytes.NewBuffer(want)
	into.Marshal(NewReader(in, 0, false))
	if in.Len() != 0 {
		t.Fatalf("%d bytes left after reading", in.Len())
	}
	var again bytes.Buffer
	into.Marshal(NewWriter(&again, 0))
	if !bytes.Equal(again.Bytes(), want) {
		t.Fatalf("read back as % x, want % x", again.Bytes(), want)
	}
}

// TestCameraInstructionTargetLayout pins the target's actor ID to eight fixed bytes, not a varint.
func TestCameraInstructionTargetLayout(t *testing.T) {
	assertLayout(t, &CameraInstructionTarget{EntityUniqueID: 2}, &CameraInstructionTarget{}, []byte{
		0x00, // no centre offset
		0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	})
}

// TestCameraPresetAimAssistLayout pins the target mode to one byte.
func TestCameraPresetAimAssistLayout(t *testing.T) {
	v := &CameraPresetAimAssist{TargetMode: Option(uint8(AimAssistTargetModeDistance))}
	assertLayout(t, v, &CameraPresetAimAssist{}, []byte{
		0x00,       // no preset
		0x01, 0x01, // target mode
		0x00, // no angle
		0x00, // no distance
	})
}

// TestCameraSplineInstructionLayout pins the type, identifier and JSON flag as plain fields with no
// presence byte, and the key frames' easing as a required string.
func TestCameraSplineInstructionLayout(t *testing.T) {
	v := &CameraSplineInstruction{
		TotalTime:         1,
		SplineType:        1,
		ProgressKeyFrames: []CameraProgressOption{{Value: 1, Time: 1, EaseType: EasingTypeLinear}},
		SplineIdentifier:  "a",
		LoadFromJson:      true,
	}
	assertLayout(t, v, &CameraSplineInstruction{}, []byte{
		0x00, 0x00, 0x80, 0x3F, // total time
		0x01,                   // type
		0x00,                   // no curve points
		0x01,                   // one progress key frame
		0x00, 0x00, 0x80, 0x3F, // value
		0x00, 0x00, 0x80, 0x3F, // time
		0x06, 'l', 'i', 'n', 'e', 'a', 'r', // easing
		0x00,      // no rotation options
		0x01, 'a', // identifier
		0x01, // load from JSON
	})
}

// TestCameraSplineDefinitionLayout pins the type as a plain string and the key frames' easing as an
// optional string.
func TestCameraSplineDefinitionLayout(t *testing.T) {
	v := &CameraSplineDefinition{
		Name:              "a",
		TotalTime:         1,
		SplineType:        SplineTypeLinear,
		ProgressKeyFrames: []CameraSplineProgressKeyFrame{{Progress: 1, Time: 1, Easing: Option("spring")}, {Progress: 1, Time: 1}},
		RotationKeyFrames: []CameraSplineRotationKeyFrame{{Rotation: mgl32.Vec3{1, 1, 1}, Time: 1}},
	}
	assertLayout(t, v, &CameraSplineDefinition{}, []byte{
		0x01, 'a', // name
		0x00, 0x00, 0x80, 0x3F, // total time
		0x06, 'l', 'i', 'n', 'e', 'a', 'r', // type
		0x00,                   // no control points
		0x02,                   // two progress key frames
		0x00, 0x00, 0x80, 0x3F, // progress
		0x00, 0x00, 0x80, 0x3F, // time
		0x01, 0x06, 's', 'p', 'r', 'i', 'n', 'g', // easing
		0x00, 0x00, 0x80, 0x3F, // progress
		0x00, 0x00, 0x80, 0x3F, // time
		0x00,                   // no easing
		0x01,                   // one rotation key frame
		0x00, 0x00, 0x80, 0x3F, // rotation
		0x00, 0x00, 0x80, 0x3F,
		0x00, 0x00, 0x80, 0x3F,
		0x00, 0x00, 0x80, 0x3F, // time
		0x00, // no easing
	})
}

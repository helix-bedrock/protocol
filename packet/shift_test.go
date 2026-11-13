package packet

import (
	"testing"

	"github.com/go-gl/mathgl/mgl64"
	"github.com/helix-bedrock/protocol"
)

// Shifting there and back gives the packet it started from, exactly, and never changes the
// packet it was given.
func TestShiftRoundTrip(t *testing.T) {
	s := Shift{X: -983040, Z: 65536}
	move := &MovePlayer{Position: mgl64.Vec3{1000000.3125, 64, -12.75}}
	got, ok := Shifted(move, s)
	if !ok || got.(*MovePlayer).Position != (mgl64.Vec3{16960.3125, 64, 65523.25}) {
		t.Fatalf("shifted to %v", got.(*MovePlayer).Position)
	}
	if move.Position != (mgl64.Vec3{1000000.3125, 64, -12.75}) {
		t.Fatal("source changed")
	}
	back, _ := Shifted(got, s.Neg())
	if back.(*MovePlayer).Position != move.Position {
		t.Fatalf("round trip gave %v", back.(*MovePlayer).Position)
	}

	be := &BlockActorData{Position: protocol.BlockPos{1000000, 5, 3}, NBTData: map[string]any{"x": int32(1000000), "y": int32(5), "z": int32(3)}}
	sb, _ := Shifted(be, s)
	if sb.(*BlockActorData).NBTData["x"] != int32(16960) || be.NBTData["x"] != int32(1000000) {
		t.Fatalf("nbt %v, source %v", sb.(*BlockActorData).NBTData, be.NBTData)
	}

	lc := &LevelChunk{Position: protocol.ChunkPos{62500, 0}}
	sl, _ := Shifted(lc, s)
	if sl.(*LevelChunk).Position != (protocol.ChunkPos{1060, 4096}) {
		t.Fatalf("chunk %v", sl.(*LevelChunk).Position)
	}

	delta := &MoveActorDelta{PositionX: protocol.Option(1000000.5)}
	sd, _ := Shifted(delta, s)
	if x, _ := sd.(*MoveActorDelta).PositionX.Value(); x != 16960.5 {
		t.Fatalf("delta x %v", x)
	}
	if _, ok := sd.(*MoveActorDelta).PositionZ.Value(); ok {
		t.Fatal("absent Z became present")
	}

	if _, ok := Shifted(&Text{}, s); ok {
		t.Fatal("a packet with no positions was shifted")
	}
}

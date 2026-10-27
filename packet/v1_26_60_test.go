package packet

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/helix-bedrock/protocol"
)

// The IDs below are read from BDS 1.26.60.6 (MinecraftPacketIds and each packet's GetId).
func TestV1_26_60PacketIDs(t *testing.T) {
	want := map[uint32]Packet{
		353: &ClientboundMatchmakingState{},
		354: &ServerboundStonecutterSetRecipe{},
		355: &ClientboundStonecutterSetRecipe{},
		356: &ServerboundMatchmakingCancel{},
		357: &SetPassengerOfBlock{},
		358: &ServerboundCursorItemDrag{},
		359: &ClientboundPlayAudioContent{},
		360: &ServerboundRegisterAudioContent{},
	}
	for id, pk := range want {
		if pk.ID() != id {
			t.Errorf("%T: ID %d, want %d", pk, pk.ID(), id)
		}
	}
	if protocol.ContainerTypeCrafter != 36 {
		t.Errorf("ContainerTypeCrafter = %d, want 36", protocol.ContainerTypeCrafter)
	}
}

func TestV1_26_60RoundTrip(t *testing.T) {
	passenger := protocol.Option(protocol.PassengerOfBlockData{
		BlockPosition: protocol.BlockPos{1, -2, 3}, Offset: mgl32.Vec3{0.5, 1, 0.5},
		Rotation: 90, RotationLimit: 45, EmoteType: protocol.PassengerOfBlockEmoteLaying,
	})
	packets := []Packet{
		&ClientboundMatchmakingState{State: MatchmakingStateMatchFound, DestinationName: "lobby",
			Options: protocol.Option(protocol.MatchmakingStateOptions{TriggeringPlayerName: protocol.Option("Steve")})},
		&ServerboundStonecutterSetRecipe{ContainerID: 4, RecipeIndex: 7},
		&ClientboundStonecutterSetRecipe{EntityUniqueID: -5, ContainerID: 4, RecipeIndex: 7},
		&ServerboundMatchmakingCancel{},
		&SetPassengerOfBlock{EntityUniqueID: 9, PassengerOfBlock: passenger},
		&ServerboundCursorItemDrag{State: CursorItemDragStop},
		&ClientboundPlayAudioContent{SharedMetadata: "a.b.c", PlaybackContent: "d.e.f", PlaybackType: AudioContentPlaybackSound,
			Sound: PlaySound{SoundName: "music", Position: mgl32.Vec3{8, 16, 24}, Volume: 1, Pitch: 1, Handle: protocol.Option(uint64(3))}},
		&ServerboundRegisterAudioContent{Registrations: []protocol.AudioContentRegistration{{AudioContentID: "x", SharedMetadata: "s"}}},
		&Animate{ActionType: AnimateActionSwingArm, EntityRuntimeID: 1, Hand: protocol.HandSlotOffHand},
		&LevelChunk{SubChunkCount: 1, RawPayload: []byte{1, 2}, ClientBiomeUpdate: true},
		&AddActor{EntityType: "minecraft:pig", EntityMetadata: protocol.NewEntityMetadata(), PassengerOfBlock: passenger},
		&StructureBlockUpdate{StructureName: "house", FilteredStructureName: protocol.Option("h***e")},
		&ClientboundUpdateSoundData{ServerSoundHandle: 2, Update: protocol.SoundDataUpdate{Type: protocol.SoundDataUpdateFade, Duration: 1, TargetVolume: 0.5}},
		&InventoryTransaction{TransactionData: &protocol.UseItemOnEntityTransactionData{Hand: protocol.HandSlotOffHand}},
		&InventoryTransaction{TransactionData: &protocol.ReleaseItemTransactionData{Hand: protocol.HandSlotOffHand}},
	}
	for _, want := range packets {
		var buf bytes.Buffer
		want.Marshal(protocol.NewWriter(&buf, 0))
		got := reflect.New(reflect.TypeOf(want).Elem()).Interface().(Packet)
		src := bytes.NewReader(buf.Bytes())
		got.Marshal(protocol.NewReader(src, 0, false))
		if src.Len() != 0 {
			t.Errorf("%T: %d bytes left unread", want, src.Len())
		}
		// Comparing the bytes instead of the structs ignores nil versus empty slices after decoding.
		var again bytes.Buffer
		got.Marshal(protocol.NewWriter(&again, 0))
		if !bytes.Equal(again.Bytes(), buf.Bytes()) {
			t.Errorf("%T round trip:\ngot  %x\nwant %x", want, again.Bytes(), buf.Bytes())
		}
	}
}

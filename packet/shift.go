package packet

import (
	"maps"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/helix-bedrock/protocol"
)

// Shift is a move of the whole world along X and Z, in blocks, for a server that shows a
// client the world around a different origin than the real one. The client works in
// float32, which gets coarse far from 0,0 (a 1/8 block step at a million): a server keeps
// a far player's client near 0,0 by sending everything moved by the same Shift, and moves
// what the client sends back by the opposite one. X and Z must be multiples of 16, so
// chunks line up. Y never moves.
type Shift struct{ X, Z int32 }

// Zero reports whether s moves nothing.
func (s Shift) Zero() bool { return s.X == 0 && s.Z == 0 }

// Neg is the opposite move.
func (s Shift) Neg() Shift { return Shift{-s.X, -s.Z} }

// Shifted returns pk with every world position it carries moved by s, and whether
// anything moved. pk itself is never changed: a packet with positions is copied first
// (and so are the slices and maps of it that change), so one packet shared by many
// players can be shifted for each. A packet with no positions comes back as it is.
//
// 64-bit positions (the fields that hold mgl64.Vec3 or float64) move exactly. Positions
// still kept in float32 (sounds, particles, level events) move from their float32 value,
// so they keep the rounding they were made with. Relative offsets (a clicked point on a
// block, a camera's entity offset) are not world positions and do not move. Not moved:
// debug shapes, camera splines and GameTestRequest.
func Shifted(pk Packet, s Shift) (Packet, bool) {
	if s.Zero() {
		return pk, false
	}
	if out, ok := shiftBlockPacket(pk, s); ok {
		return out, true
	}
	if out, ok := shiftActorPacket(pk, s); ok {
		return out, true
	}
	if out, ok := shiftPlayerPacket(pk, s); ok {
		return out, true
	}
	return shiftOtherPacket(pk, s)
}

// shiftBlockPacket shifts the packets about blocks and chunks.
func shiftBlockPacket(pk Packet, s Shift) (Packet, bool) {
	switch pk := pk.(type) {
	case *UpdateBlock:
		c := *pk
		c.Position = s.block(c.Position)
		return &c, true
	case *UpdateBlockSynced:
		c := *pk
		c.Position = s.block(c.Position)
		return &c, true
	case *UpdateSubChunkBlocks:
		c := *pk
		c.Position = s.block(c.Position)
		c.Blocks = s.changes(c.Blocks)
		c.Extra = s.changes(c.Extra)
		return &c, true
	case *BlockActorData:
		c := *pk
		c.Position = s.block(c.Position)
		c.NBTData = s.BlockNBT(c.NBTData)
		return &c, true
	case *BlockEvent:
		c := *pk
		c.Position = s.block(c.Position)
		return &c, true
	case *BlockPickRequest:
		c := *pk
		c.Position = s.block(c.Position)
		return &c, true
	case *AnvilDamage:
		c := *pk
		c.AnvilPosition = s.block(c.AnvilPosition)
		return &c, true
	case *LevelChunk:
		c := *pk
		c.Position = s.chunk(c.Position)
		return &c, true
	case *SubChunk:
		c := *pk
		c.Position = s.subChunk(c.Position)
		return &c, true
	case *SubChunkRequest:
		c := *pk
		c.Position = s.subChunk(c.Position)
		return &c, true
	case *NetworkChunkPublisherUpdate:
		c := *pk
		c.Position = s.block(c.Position)
		if len(c.SavedChunks) > 0 {
			c.SavedChunks = make([]protocol.ChunkPos, len(pk.SavedChunks))
			for i, p := range pk.SavedChunks {
				c.SavedChunks[i] = s.chunk(p)
			}
		}
		return &c, true
	}
	return pk, false
}

// shiftActorPacket shifts the packets about entities.
func shiftActorPacket(pk Packet, s Shift) (Packet, bool) {
	switch pk := pk.(type) {
	case *AddActor:
		c := *pk
		c.Position = s.vec64(c.Position)
		c.EntityMetadata = s.metadata(c.EntityMetadata)
		c.PassengerOfBlock = s.passenger(c.PassengerOfBlock)
		return &c, true
	case *AddPlayer:
		c := *pk
		c.Position = s.vec64(c.Position)
		c.EntityMetadata = s.metadata(c.EntityMetadata)
		c.PassengerOfBlock = s.passenger(c.PassengerOfBlock)
		return &c, true
	case *AddItemActor:
		c := *pk
		c.Position = s.vec64(c.Position)
		c.EntityMetadata = s.metadata(c.EntityMetadata)
		return &c, true
	case *AddPainting:
		c := *pk
		c.Position = s.vec32(c.Position)
		return &c, true
	case *AddVolumeEntity:
		c := *pk
		c.Bounds = [2]protocol.BlockPos{s.block(c.Bounds[0]), s.block(c.Bounds[1])}
		return &c, true
	case *SetActorData:
		c := *pk
		c.EntityMetadata = s.metadata(c.EntityMetadata)
		return &c, true
	case *SetPassengerOfBlock:
		c := *pk
		c.PassengerOfBlock = s.passenger(c.PassengerOfBlock)
		return &c, true
	case *MoveActorAbsolute:
		c := *pk
		c.Position = s.vec64(c.Position)
		return &c, true
	case *MoveActorDelta:
		c := *pk
		// Only the coordinates the packet carries: an absent one stays absent.
		if x, ok := c.PositionX.Value(); ok {
			c.PositionX = protocol.Option(x + float64(s.X))
		}
		if z, ok := c.PositionZ.Value(); ok {
			c.PositionZ = protocol.Option(z + float64(s.Z))
		}
		return &c, true
	case *ActorEvent:
		c := *pk
		c.FireAtPosition = s.optVec32(c.FireAtPosition)
		return &c, true
	case *SpawnExperienceOrb:
		c := *pk
		c.Position = s.vec32(c.Position)
		return &c, true
	case *Interact:
		c := *pk
		c.Position = s.optVec32(c.Position)
		return &c, true
	}
	return pk, false
}

// shiftPlayerPacket shifts the packets about the client's own player.
func shiftPlayerPacket(pk Packet, s Shift) (Packet, bool) {
	switch pk := pk.(type) {
	case *StartGame:
		c := *pk
		c.PlayerPosition = s.vec64(c.PlayerPosition)
		c.WorldSpawn = s.block(c.WorldSpawn)
		return &c, true
	case *MovePlayer:
		c := *pk
		c.Position = s.vec64(c.Position)
		return &c, true
	case *CorrectPlayerMovePrediction:
		c := *pk
		c.Position = s.vec64(c.Position)
		return &c, true
	case *ServerPlayerPostMovePosition:
		c := *pk
		c.Position = s.vec64(c.Position)
		return &c, true
	case *Respawn:
		c := *pk
		c.Position = s.vec64(c.Position)
		return &c, true
	case *ChangeDimension:
		c := *pk
		c.Position = s.vec64(c.Position)
		return &c, true
	case *SetSpawnPosition:
		c := *pk
		c.Position = s.block(c.Position)
		c.SpawnPosition = s.block(c.SpawnPosition)
		return &c, true
	case *PlayerAuthInput:
		c := *pk
		c.Position = s.vec64(c.Position)
		if d, ok := c.ItemInteractionData.Value(); ok {
			c.ItemInteractionData = protocol.Option(s.useItem(d))
		}
		if acts, ok := c.BlockActions.Value(); ok && len(acts) > 0 {
			moved := make([]protocol.PlayerBlockAction, len(acts))
			for i, a := range acts {
				a.BlockPos = s.block(a.BlockPos)
				moved[i] = a
			}
			c.BlockActions = protocol.Option(moved)
		}
		return &c, true
	case *InventoryTransaction:
		c := *pk
		switch d := c.TransactionData.(type) {
		case *protocol.UseItemTransactionData:
			m := s.useItem(*d)
			c.TransactionData = &m
		case *protocol.UseItemOnEntityTransactionData:
			m := *d
			m.Position = s.vec32(m.Position)
			c.TransactionData = &m
		case *protocol.ReleaseItemTransactionData:
			m := *d
			m.HeadPosition = s.vec32(m.HeadPosition)
			c.TransactionData = &m
		default:
			return pk, false
		}
		return &c, true
	case *PlayerAction:
		c := *pk
		c.BlockPosition = s.block(c.BlockPosition)
		c.ResultPosition = s.block(c.ResultPosition)
		return &c, true
	case *PlayerLocation:
		c := *pk
		c.Position = s.vec32(c.Position)
		return &c, true
	}
	return pk, false
}

// shiftOtherPacket shifts sounds, effects, block screens, maps, the locator bar and the
// camera.
func shiftOtherPacket(pk Packet, s Shift) (Packet, bool) {
	switch pk := pk.(type) {
	case *LevelEvent:
		c := *pk
		c.Position = s.vec32(c.Position)
		return &c, true
	case *LevelSoundEvent:
		c := *pk
		c.Position = s.vec32(c.Position)
		c.FireAtPosition = s.optVec32(c.FireAtPosition)
		return &c, true
	case *PlaySound:
		c := *pk
		c.Position = s.vec32(c.Position)
		return &c, true
	case *SpawnParticleEffect:
		c := *pk
		c.Position = s.vec32(c.Position)
		return &c, true
	case *ContainerOpen:
		c := *pk
		c.ContainerPosition = s.block(c.ContainerPosition)
		return &c, true
	case *OpenSign:
		c := *pk
		c.Position = s.block(c.Position)
		return &c, true
	case *LecternUpdate:
		c := *pk
		c.Position = s.block(c.Position)
		return &c, true
	case *LabTable:
		c := *pk
		c.Position = s.block(c.Position)
		return &c, true
	case *RecordStarted:
		c := *pk
		c.Position = s.block(c.Position)
		return &c, true
	case *CommandBlockUpdate:
		c := *pk
		c.Position = s.block(c.Position)
		return &c, true
	case *StructureBlockUpdate:
		c := *pk
		c.Position = s.block(c.Position)
		return &c, true
	case *StructureTemplateDataRequest:
		c := *pk
		c.Position = s.block(c.Position)
		return &c, true
	case *ClientBoundMapItemData:
		c := *pk
		c.Origin = s.block(c.Origin)
		c.TrackedObjects = s.tracked(c.TrackedObjects)
		return &c, true
	case *LocatorBar:
		c := *pk
		c.Waypoints = s.waypoints(c.Waypoints)
		return &c, true
	case *CameraInstruction:
		c := *pk
		if set, ok := c.Set.Value(); ok {
			set.Position = s.optVec32(set.Position)
			set.Facing = s.optVec32(set.Facing)
			c.Set = protocol.Option(set)
		}
		return &c, true
	}
	return pk, false
}

func (s Shift) block(p protocol.BlockPos) protocol.BlockPos {
	return protocol.BlockPos{p[0] + s.X, p[1], p[2] + s.Z}
}

func (s Shift) chunk(p protocol.ChunkPos) protocol.ChunkPos {
	return protocol.ChunkPos{p[0] + s.X>>4, p[1] + s.Z>>4}
}

func (s Shift) subChunk(p protocol.SubChunkPos) protocol.SubChunkPos {
	return protocol.SubChunkPos{p[0] + s.X>>4, p[1], p[2] + s.Z>>4}
}

func (s Shift) vec64(v mgl64.Vec3) mgl64.Vec3 {
	return mgl64.Vec3{v[0] + float64(s.X), v[1], v[2] + float64(s.Z)}
}

func (s Shift) vec32(v mgl32.Vec3) mgl32.Vec3 {
	return mgl32.Vec3{float32(float64(v[0]) + float64(s.X)), v[1], float32(float64(v[2]) + float64(s.Z))}
}

func (s Shift) optVec32(o protocol.Optional[mgl32.Vec3]) protocol.Optional[mgl32.Vec3] {
	if v, ok := o.Value(); ok {
		return protocol.Option(s.vec32(v))
	}
	return o
}

func (s Shift) changes(entries []protocol.BlockChangeEntry) []protocol.BlockChangeEntry {
	if len(entries) == 0 {
		return entries
	}
	out := make([]protocol.BlockChangeEntry, len(entries))
	for i, e := range entries {
		e.BlockPos = s.block(e.BlockPos)
		out[i] = e
	}
	return out
}

func (s Shift) useItem(d protocol.UseItemTransactionData) protocol.UseItemTransactionData {
	d.BlockPosition = s.block(d.BlockPosition)
	d.Position = s.vec32(d.Position)
	return d
}

func (s Shift) passenger(o protocol.Optional[protocol.PassengerOfBlockData]) protocol.Optional[protocol.PassengerOfBlockData] {
	if p, ok := o.Value(); ok {
		p.BlockPosition = s.block(p.BlockPosition)
		return protocol.Option(p)
	}
	return o
}

// metadata moves the block positions in actor data (a bed, a beam's target). Vec3 values
// are offsets there, not positions, and stay. The map is copied only when it changes.
func (s Shift) metadata(m protocol.EntityMetadata) protocol.EntityMetadata {
	var out protocol.EntityMetadata
	for k, v := range m {
		p, ok := v.(protocol.BlockPos)
		if !ok {
			continue
		}
		if out == nil {
			out = maps.Clone(m)
		}
		out[k] = s.block(p)
	}
	if out == nil {
		return m
	}
	return out
}

// BlockNBT returns a block entity's compound with its "x" and "z" moved by s, copied, or
// data itself when it has no int32 position.
func (s Shift) BlockNBT(data map[string]any) map[string]any {
	x, okX := data["x"].(int32)
	z, okZ := data["z"].(int32)
	if !okX && !okZ {
		return data
	}
	out := maps.Clone(data)
	if okX {
		out["x"] = x + s.X
	}
	if okZ {
		out["z"] = z + s.Z
	}
	return out
}

func (s Shift) tracked(o protocol.Optional[[]protocol.MapTrackedObject]) protocol.Optional[[]protocol.MapTrackedObject] {
	objs, ok := o.Value()
	if !ok || len(objs) == 0 {
		return o
	}
	out := make([]protocol.MapTrackedObject, len(objs))
	for i, obj := range objs {
		if p, ok := obj.BlockPosition.Value(); ok {
			obj.BlockPosition = protocol.Option(s.block(p))
		}
		out[i] = obj
	}
	return protocol.Option(out)
}

func (s Shift) waypoints(ws []protocol.LocatorBarWaypoint) []protocol.LocatorBarWaypoint {
	if len(ws) == 0 {
		return ws
	}
	out := make([]protocol.LocatorBarWaypoint, len(ws))
	for i, w := range ws {
		if p, ok := w.Waypoint.WorldPosition.Value(); ok {
			p.Position = s.vec32(p.Position)
			w.Waypoint.WorldPosition = protocol.Option(p)
		}
		out[i] = w
	}
	return out
}

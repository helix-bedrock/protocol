package packet

import (
	"github.com/helix-bedrock/protocol"
)

// RemoveVolumeEntity indicates a volume entity to be removed from server to client.
type RemoveVolumeEntity struct {
	EntityRuntimeID uint32
	Dimension int32
}

func (*RemoveVolumeEntity) ID() uint32 {
	return IDRemoveVolumeEntity
}

func (pk *RemoveVolumeEntity) Marshal(io protocol.IO) {
	io.ActorRuntimeIDVaruint32(&pk.EntityRuntimeID)
	io.Varint32(&pk.Dimension)
}

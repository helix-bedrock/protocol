package packet

import "github.com/helix-bedrock/protocol"

// SetPassengerOfBlock is sent by the server to place an entity on a block as its passenger, or to remove it
// from the block if PassengerOfBlock is not set.
type SetPassengerOfBlock struct {
	// EntityUniqueID is the unique ID of the passenger.
	EntityUniqueID int64
	// PassengerOfBlock is the block the entity is placed on.
	PassengerOfBlock protocol.Optional[protocol.PassengerOfBlockData]
}

// ID ...
func (*SetPassengerOfBlock) ID() uint32 {
	return IDSetPassengerOfBlock
}

func (pk *SetPassengerOfBlock) Marshal(io protocol.IO) {
	io.ActorUniqueID(&pk.EntityUniqueID)
	protocol.OptionalMarshaler(io, &pk.PassengerOfBlock)
}

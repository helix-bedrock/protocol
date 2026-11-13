package packet

import (
	"github.com/go-gl/mathgl/mgl64"
	"github.com/helix-bedrock/protocol"
)

// ServerPlayerPostMovePosition is sent by the server with the player's position after movement processing.
type ServerPlayerPostMovePosition struct {
	// Position is the player's position after the server has processed movement.
	Position mgl64.Vec3
}

// ID ...
func (*ServerPlayerPostMovePosition) ID() uint32 {
	return IDServerPlayerPostMovePosition
}

func (pk *ServerPlayerPostMovePosition) Marshal(io protocol.IO) {
	io.PosVec3(&pk.Position)
}

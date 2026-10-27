package packet

import "github.com/helix-bedrock/protocol"

// ClientboundStonecutterSetRecipe is sent by the server to set the selected recipe of a player's
// stonecutter.
type ClientboundStonecutterSetRecipe struct {
	// EntityUniqueID is the unique ID of the player.
	EntityUniqueID int64
	// ContainerID is the ID of the stonecutter container.
	ContainerID byte
	// RecipeIndex is the index of the selected recipe.
	RecipeIndex int32
}

// ID ...
func (*ClientboundStonecutterSetRecipe) ID() uint32 {
	return IDClientboundStonecutterSetRecipe
}

func (pk *ClientboundStonecutterSetRecipe) Marshal(io protocol.IO) {
	io.ActorUniqueID(&pk.EntityUniqueID)
	io.Uint8(&pk.ContainerID)
	io.Varint32(&pk.RecipeIndex)
}

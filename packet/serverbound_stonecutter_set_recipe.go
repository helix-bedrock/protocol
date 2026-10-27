package packet

import "github.com/helix-bedrock/protocol"

// ServerboundStonecutterSetRecipe is sent by the client when it selects a recipe in a stonecutter.
type ServerboundStonecutterSetRecipe struct {
	// ContainerID is the ID of the stonecutter container.
	ContainerID byte
	// RecipeIndex is the index of the selected recipe.
	RecipeIndex int32
}

// ID ...
func (*ServerboundStonecutterSetRecipe) ID() uint32 {
	return IDServerboundStonecutterSetRecipe
}

func (pk *ServerboundStonecutterSetRecipe) Marshal(io protocol.IO) {
	io.Uint8(&pk.ContainerID)
	io.Varint32(&pk.RecipeIndex)
}

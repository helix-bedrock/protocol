package packet

import "github.com/helix-bedrock/protocol"

// ServerboundMatchmakingCancel is sent by the client to cancel matchmaking. It has no payload.
type ServerboundMatchmakingCancel struct{}

func (*ServerboundMatchmakingCancel) ID() uint32 {
	return IDServerboundMatchmakingCancel
}

func (*ServerboundMatchmakingCancel) Marshal(protocol.IO) {}

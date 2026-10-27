package packet

import "github.com/helix-bedrock/protocol"

const (
	MatchmakingStateIdle = iota
	MatchmakingStateMatchmaking
	MatchmakingStateMatchFound
	MatchmakingStateCanceled
	MatchmakingStatePlayerLeftParty
	MatchmakingStatePlayerLeftServer
	MatchmakingStateServerShutdown
	MatchmakingStateTimedOut
	MatchmakingStateRequeueAsParty
)

// ClientboundMatchmakingState is sent by the server to tell the client the state of matchmaking for a
// destination.
type ClientboundMatchmakingState struct {
	// State is the matchmaking state. It is one of the MatchmakingState constants above.
	State uint8
	// DestinationName is the name of the destination that is being matched for.
	DestinationName string
	// Options holds extra information about the state change.
	Options protocol.Optional[protocol.MatchmakingStateOptions]
}

// ID ...
func (*ClientboundMatchmakingState) ID() uint32 {
	return IDClientboundMatchmakingState
}

func (pk *ClientboundMatchmakingState) Marshal(io protocol.IO) {
	io.Uint8(&pk.State)
	io.String(&pk.DestinationName)
	protocol.OptionalMarshaler(io, &pk.Options)
}

package protocol

// MatchmakingStateOptions holds extra information about a matchmaking state change.
type MatchmakingStateOptions struct {
	// TriggeringPlayerName is the name of the player that caused the state change.
	TriggeringPlayerName Optional[string]
	// TriggeredByLocalPlayer is true if the receiving player caused the state change.
	TriggeredByLocalPlayer Optional[bool]
}

// Marshal encodes/decodes a MatchmakingStateOptions.
func (x *MatchmakingStateOptions) Marshal(r IO) {
	OptionalFunc(r, &x.TriggeringPlayerName, r.String)
	OptionalFunc(r, &x.TriggeredByLocalPlayer, r.Bool)
}

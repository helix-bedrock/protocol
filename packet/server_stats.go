package packet

import "github.com/helix-bedrock/protocol"

// ServerStats is a packet sent from the server to the client to update the client on server statistics. It is purely
// used for telemetry.
type ServerStats struct {
	ServerTime float32
	NetworkTime float32
}

func (pk *ServerStats) ID() uint32 {
	return IDServerStats
}

func (pk *ServerStats) Marshal(io protocol.IO) {
	io.Float32(&pk.ServerTime)
	io.Float32(&pk.NetworkTime)
}

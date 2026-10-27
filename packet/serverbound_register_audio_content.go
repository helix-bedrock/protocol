package packet

import "github.com/helix-bedrock/protocol"

// ServerboundRegisterAudioContent is sent by the client to register audio content with the server.
type ServerboundRegisterAudioContent struct {
	// Registrations is every piece of audio content registered.
	Registrations []protocol.AudioContentRegistration
}

// ID ...
func (*ServerboundRegisterAudioContent) ID() uint32 {
	return IDServerboundRegisterAudioContent
}

func (pk *ServerboundRegisterAudioContent) Marshal(io protocol.IO) {
	protocol.Slice(io, &pk.Registrations)
}

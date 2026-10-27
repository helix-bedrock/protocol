package protocol

// AudioContentRegistration registers audio content with the server. The metadata and content are signed
// compact JWTs.
type AudioContentRegistration struct {
	// AudioContentID is the identifier of the audio content.
	AudioContentID string
	// SharedMetadata is the signed shared metadata of the content.
	SharedMetadata string
	// ServerContent is the signed content meant for the server.
	ServerContent string
	// PlaybackContent is the signed content used for playback.
	PlaybackContent string
}

// Marshal encodes/decodes an AudioContentRegistration.
func (x *AudioContentRegistration) Marshal(r IO) {
	r.String(&x.AudioContentID)
	r.String(&x.SharedMetadata)
	r.String(&x.ServerContent)
	r.String(&x.PlaybackContent)
}

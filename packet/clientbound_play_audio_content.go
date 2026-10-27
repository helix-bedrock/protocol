package packet

import "github.com/helix-bedrock/protocol"

const (
	AudioContentPlaybackMusic = iota
	AudioContentPlaybackSound
)

// ClientboundPlayAudioContent is sent by the server to play registered audio content. The metadata and
// content are signed compact JWTs.
type ClientboundPlayAudioContent struct {
	// SharedMetadata is the signed shared metadata of the content.
	SharedMetadata string
	// PlaybackContent is the signed content used for playback.
	PlaybackContent string
	// PlaybackType is how the content is played. It is one of the AudioContentPlayback constants above.
	PlaybackType uint8
	// Sound holds the same fields as a PlaySound packet: where and how the content is played.
	Sound PlaySound
}

// ID ...
func (*ClientboundPlayAudioContent) ID() uint32 {
	return IDClientboundPlayAudioContent
}

func (pk *ClientboundPlayAudioContent) Marshal(io protocol.IO) {
	io.String(&pk.SharedMetadata)
	io.String(&pk.PlaybackContent)
	io.Uint8(&pk.PlaybackType)
	pk.Sound.Marshal(io)
}

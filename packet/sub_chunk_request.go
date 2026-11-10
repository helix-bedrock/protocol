package packet

import (
	"github.com/helix-bedrock/protocol"
)

// SubChunkRequest requests specific sub-chunks from the server using a center point.
type SubChunkRequest struct {
	// Dimension is the dimension of the sub-chunk.
	Dimension int32
	// Offsets contains all requested offsets around the center point.
	Offsets []protocol.SubChunkOffset
	// Position is an absolute sub-chunk center point used as a base point for all sub-chunks requested. The X and Z
	// coordinates represent the chunk coordinates, while the Y coordinate is the absolute sub-chunk index.
	Position protocol.SubChunkPos
}

// ID ...
func (*SubChunkRequest) ID() uint32 {
	return IDSubChunkRequest
}

// maxSubChunkRequestOffsets is the most offsets BDS 1.26.60 accepts in one request: the cereal
// SequenceContainerConstraint on "SubChunk Position Offset List" has a maximum of 8192. The client sends more
// than the default 1024 at large view distances, so the default limit disconnected it.
const maxSubChunkRequestOffsets = 8192

func (pk *SubChunkRequest) Marshal(io protocol.IO) {
	io.Varint32(&pk.Dimension)
	protocol.SliceMax(io, &pk.Offsets, maxSubChunkRequestOffsets)
	io.SubChunkPos(&pk.Position)
}

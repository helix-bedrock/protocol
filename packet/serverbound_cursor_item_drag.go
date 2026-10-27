package packet

import "github.com/helix-bedrock/protocol"

const (
	CursorItemDragStart = iota
	CursorItemDragStop
)

// ServerboundCursorItemDrag is sent by the client when it starts or stops dragging the item on its cursor.
type ServerboundCursorItemDrag struct {
	// State is the drag state. It is one of the CursorItemDrag constants above.
	State uint8
}

// ID ...
func (*ServerboundCursorItemDrag) ID() uint32 {
	return IDServerboundCursorItemDrag
}

func (pk *ServerboundCursorItemDrag) Marshal(io protocol.IO) {
	io.Uint8(&pk.State)
}

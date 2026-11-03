package protocol

import (
	"bytes"
	"testing"
)

// TestCraftRecipeOptionalLayout pins the anvil action's bytes: a varuint recipe ID, then
// the filter string index in four bytes, as BDS reads it.
func TestCraftRecipeOptionalLayout(t *testing.T) {
	buf := bytes.NewBuffer([]byte{0x05, 0xFF, 0xFF, 0xFF, 0xFF, 0x2A})
	var a CraftRecipeOptionalStackRequestAction
	a.Marshal(NewReader(buf, 0, false))
	if a.RecipeNetworkID != 5 || a.FilterStringIndex != -1 {
		t.Fatalf("read %+v", a)
	}
	if buf.Len() != 1 {
		t.Fatalf("%d bytes left, want the one after the action", buf.Len())
	}
}

package login

import (
	"encoding/base64"
	"testing"

	"github.com/helix-bedrock/protocol"
)

func TestSkin(t *testing.T) {
	img := make([]byte, 64*64*4)
	d := ClientData{
		SkinID: "steve", SkinData: base64.StdEncoding.EncodeToString(img), SkinImageWidth: 64, SkinImageHeight: 64,
		SkinColour: "#b37b62", ArmSize: "slim",
		PersonaPieces:    []PersonaPiece{{PieceType: "persona_eyes", PackID: "00000000-0000-0000-0000-000000000001"}},
		PieceTintColours: []PersonaPieceTintColour{{PieceType: "persona_eyes", Colours: [4]string{"#ffa12722", "#0", "", "#ff3aafd9"}}},
	}
	s, err := d.Skin()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.SkinData) != 64*64*4 {
		t.Fatalf("%d bytes of skin", len(s.SkinData))
	}
	if s.ArmSize != protocol.ArmSizeSlim || s.SkinColour.R != 0xb3 || s.SkinColour.A != 0xff {
		t.Fatalf("arm size %d, colour %v", s.ArmSize, s.SkinColour)
	}
	if s.PersonaPieces[0].PieceType != protocol.PieceTypeEyes {
		t.Fatalf("eyes piece type %d", s.PersonaPieces[0].PieceType)
	}
	if c := s.PieceTintColours[0].Colours[0]; c.A != 0xff || c.R != 0xa1 {
		t.Fatalf("tint %v", c)
	}
	d.SkinData = "!"
	if _, err := d.Skin(); err == nil {
		t.Fatal("bad base64 accepted")
	}
}

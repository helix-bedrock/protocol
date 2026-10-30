package login

import (
	"encoding/base64"
	"fmt"
	"image/color"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/helix-bedrock/protocol"
)

// pieceTypes maps the persona piece names of the login data to the numbers
// sent on the wire, in the order of the protocol.PieceType constants.
var pieceTypes = [...]string{
	"", "persona_skeleton", "persona_body", "persona_skin", "persona_bottom", "persona_feet", "persona_dress",
	"persona_top", "persona_high_pants", "persona_hands", "persona_outerwear", "persona_facial_hair",
	"persona_mouth", "persona_eyes", "persona_hair", "persona_hood", "persona_back", "persona_face_accessory",
	"persona_head", "persona_legs", "persona_left_leg", "persona_right_leg", "persona_arms", "persona_left_arm",
	"persona_right_arm", "persona_capes", "persona_classic_skin", "persona_emote",
}

// Skin returns the skin in the client data in the form other players are
// sent it (PlayerList, PlayerSkin). Images and JSON are decoded from base64;
// a field that does not decode is an error.
func (data ClientData) Skin() (protocol.Skin, error) {
	s := protocol.Skin{
		SkinID: data.SkinID, CapeID: data.CapeID,
		SkinImageWidth: uint32(max(data.SkinImageWidth, 0)), SkinImageHeight: uint32(max(data.SkinImageHeight, 0)), //nolint:gosec // clamped
		CapeImageWidth: uint32(max(data.CapeImageWidth, 0)), CapeImageHeight: uint32(max(data.CapeImageHeight, 0)), //nolint:gosec // clamped
		PremiumSkin: data.PremiumSkin, PersonaSkin: data.PersonaSkin, PersonaCapeOnClassicSkin: data.CapeOnClassicSkin,
		GeometryDataEngineVersion: []byte(data.SkinGeometryVersion), Trusted: data.TrustedSkin,
		OverrideAppearance: true, ProfileHash: data.ProfileHash,
	}
	var err error
	if s.SkinResourcePatch, err = base64.StdEncoding.DecodeString(data.SkinResourcePatch); err != nil {
		return s, fmt.Errorf("skin resource patch: %w", err)
	}
	if s.SkinData, err = base64.StdEncoding.DecodeString(data.SkinData); err != nil {
		return s, fmt.Errorf("skin data: %w", err)
	}
	if s.CapeData, err = base64.StdEncoding.DecodeString(data.CapeData); err != nil {
		return s, fmt.Errorf("cape data: %w", err)
	}
	if s.SkinGeometry, err = base64.StdEncoding.DecodeString(data.SkinGeometry); err != nil {
		return s, fmt.Errorf("skin geometry: %w", err)
	}
	if s.AnimationData, err = base64.StdEncoding.DecodeString(data.SkinAnimationData); err != nil {
		return s, fmt.Errorf("skin animation data: %w", err)
	}
	if s.SkinColour, err = hexColour(data.SkinColour, false); err != nil {
		return s, fmt.Errorf("skin colour: %w", err)
	}
	if data.ArmSize == "slim" {
		s.ArmSize = protocol.ArmSizeSlim
	} else {
		s.ArmSize = protocol.ArmSizeWide
	}
	for i, a := range data.AnimatedImageData {
		img, err := base64.StdEncoding.DecodeString(a.Image)
		if err != nil {
			return s, fmt.Errorf("animation %d: %w", i, err)
		}
		s.Animations = append(s.Animations, protocol.SkinAnimation{
			ImageWidth: uint32(max(a.ImageWidth, 0)), ImageHeight: uint32(max(a.ImageHeight, 0)), ImageData: img, //nolint:gosec // clamped
			AnimationType: uint32(max(a.Type, 0)), FrameCount: float32(a.Frames), ExpressionType: uint32(max(a.AnimationExpression, 0)), //nolint:gosec // clamped
		})
	}
	for _, p := range data.PersonaPieces {
		pack, _ := uuid.Parse(p.PackID)
		piece := protocol.PersonaPiece{PieceID: p.PieceID, PackID: pack, Default: p.Default, ProductID: p.ProductID, PieceType: protocol.PieceTypeUnsupported}
		for t, name := range pieceTypes {
			if name != "" && name == p.PieceType {
				piece.PieceType = uint32(t) //nolint:gosec // small index
			}
		}
		s.PersonaPieces = append(s.PersonaPieces, piece)
	}
	for _, t := range data.PieceTintColours {
		tint := protocol.PersonaPieceTintColour{PieceType: t.PieceType}
		for i, c := range t.Colours {
			if tint.Colours[i], err = hexColour(c, true); err != nil {
				return s, fmt.Errorf("tint of %s: %w", t.PieceType, err)
			}
		}
		s.PieceTintColours = append(s.PieceTintColours, tint)
	}
	return s, nil
}

// hexColour parses "#RRGGBB" or, with argb, "#AARRGGBB". "#0" and an empty
// string are transparent black, as clients send for unused tints.
func hexColour(s string, argb bool) (color.RGBA, error) {
	h := strings.TrimPrefix(s, "#")
	if h == "" || h == "0" {
		return color.RGBA{}, nil
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return color.RGBA{}, fmt.Errorf("parse %q: %w", s, err)
	}
	c := color.RGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xff} //nolint:gosec // bytes of a 32-bit value
	if argb && len(h) == 8 {
		c.A = uint8(v >> 24) //nolint:gosec // as above
	}
	return c, nil
}

package desktop

import (
	"encoding/binary"
	"errors"
	"image"
	"math/bits"

	"github.com/jezek/xgb/xproto"
)

// FrameInto samples the private X11 framebuffer directly into one output tile.
func (d *Desktop) FrameInto(output *image.RGBA, tile, area image.Rectangle) error {
	setup := xproto.Setup(d.conn)
	screen := setup.DefaultScreen(d.conn)
	var depthOK bool
	for _, format := range setup.PixmapFormats {
		if format.Depth == screen.RootDepth && format.BitsPerPixel == 32 {
			depthOK = true
			break
		}
	}
	if !depthOK {
		return errors.New("the virtual display does not use 32-bit packed pixels")
	}
	var visual xproto.VisualInfo
	for _, depth := range screen.AllowedDepths {
		for _, candidate := range depth.Visuals {
			if candidate.VisualId == screen.RootVisual {
				visual = candidate
			}
		}
	}
	if visual.RedMask == 0 || visual.GreenMask == 0 || visual.BlueMask == 0 {
		return errors.New("the virtual display has no RGB pixel masks")
	}
	w, h := d.Size()
	if !area.In(image.Rect(0, 0, w, h)) || area.Empty() || area.Dx() > 8_000_000/area.Dy() {
		return errors.New("capture region must fit within the desktop and contain at most 8 million pixels")
	}
	frame, err := xproto.GetImage(d.conn, xproto.ImageFormatZPixmap, xproto.Drawable(d.root), int16(area.Min.X), int16(area.Min.Y), uint16(area.Dx()), uint16(area.Dy()), ^uint32(0)).Reply()
	if err != nil {
		return err
	}
	if len(frame.Data) < area.Dx()*area.Dy()*4 {
		return errors.New("short X11 image reply")
	}
	order := binary.ByteOrder(binary.LittleEndian)
	if setup.ImageByteOrder == xproto.ImageOrderMSBFirst {
		order = binary.BigEndian
	}
	r, g, b := visual.RedMask, visual.GreenMask, visual.BlueMask
	rs, gs, bs := bits.TrailingZeros32(r), bits.TrailingZeros32(g), bits.TrailingZeros32(b)
	for y := 0; y < tile.Dy(); y++ {
		sourceY := y * area.Dy() / tile.Dy()
		for x := 0; x < tile.Dx(); x++ {
			sourceX := x * area.Dx() / tile.Dx()
			at := 4 * (sourceY*area.Dx() + sourceX)
			pixel := order.Uint32(frame.Data[at : at+4])
			dest := output.PixOffset(tile.Min.X+x, tile.Min.Y+y)
			output.Pix[dest] = byte(((pixel & r) >> rs) * 255 / (r >> rs))
			output.Pix[dest+1] = byte(((pixel & g) >> gs) * 255 / (g >> gs))
			output.Pix[dest+2] = byte(((pixel & b) >> bs) * 255 / (b >> bs))
			output.Pix[dest+3] = 255
		}
	}
	return nil
}

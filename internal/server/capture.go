package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type frameSampler interface {
	FrameInto(*image.RGBA, image.Rectangle, image.Rectangle) error
}

func captureTileSize(area image.Rectangle, maxSize int) (width, height int) {
	width = min(area.Dx(), maxSize)
	height = max(1, area.Dy()*width/area.Dx())
	if height > maxSize {
		width, height = max(1, area.Dx()*maxSize/area.Dy()), maxSize
	}
	return width, height
}

func frameGuard(area image.Rectangle, pixels []byte) string {
	var encoded [16 + sha256.Size]byte
	binary.BigEndian.PutUint32(encoded[0:4], uint32(area.Min.X))
	binary.BigEndian.PutUint32(encoded[4:8], uint32(area.Min.Y))
	binary.BigEndian.PutUint32(encoded[8:12], uint32(area.Max.X))
	binary.BigEndian.PutUint32(encoded[12:16], uint32(area.Max.Y))
	digest := sha256.Sum256(pixels)
	copy(encoded[16:], digest[:])
	return base64.RawURLEncoding.EncodeToString(encoded[:])
}

func (s *Server) validateFrame(frame string, x, y *int) error {
	if len(frame) != base64.RawURLEncoding.EncodedLen(16+sha256.Size) || x == nil || y == nil {
		return errors.New("expect_frame requires the guard from a single-frame capture and x and y coordinates")
	}
	raw, err := base64.RawURLEncoding.DecodeString(frame)
	if err != nil {
		return errors.New("invalid frame guard")
	}
	area := image.Rect(int(binary.BigEndian.Uint32(raw[0:4])), int(binary.BigEndian.Uint32(raw[4:8])),
		int(binary.BigEndian.Uint32(raw[8:12])), int(binary.BigEndian.Uint32(raw[12:16])))
	width, height := s.desktop.Size()
	if area.Empty() || !area.In(image.Rect(0, 0, width, height)) || !image.Pt(*x, *y).In(area) {
		return errors.New("coordinates must be inside the observed frame region")
	}
	display, ok := s.desktop.(frameSampler)
	if !ok {
		return errors.New("virtual display capture unavailable")
	}
	w, h := captureTileSize(area, 900)
	sample := image.NewRGBA(image.Rect(0, 0, w, h))
	if err = display.FrameInto(sample, sample.Bounds(), area); err != nil {
		return errors.New("cannot validate the observed frame")
	}
	if frameGuard(area, sample.Pix) != frame {
		return errStaleObservation
	}
	return nil
}

func (s *Server) captureHTTP(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	var input struct {
		Frames     int `json:"frames"`
		IntervalMS int `json:"interval_ms"`
		X          int `json:"x"`
		Y          int `json:"y"`
		Width      int `json:"width"`
		Height     int `json:"height"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if input.Frames == 0 {
		input.Frames = 6
	}
	if input.IntervalMS == 0 {
		input.IntervalMS = 220
	}
	if input.Frames < 1 || input.Frames > 9 || input.IntervalMS < 100 || input.IntervalMS > 500 {
		writeError(w, 400, "use 1–9 frames and an interval of 100–500 milliseconds")
		return
	}
	display, ok := s.desktop.(frameSampler)
	if !ok {
		writeError(w, 503, "virtual display capture unavailable")
		return
	}
	if !s.capturing.CompareAndSwap(false, true) {
		writeError(w, 409, "another capture is running")
		return
	}
	defer s.capturing.Store(false)
	wid, hei := s.desktop.Size()
	if input.Width == 0 && input.Height == 0 && input.X == 0 && input.Y == 0 {
		input.Width, input.Height = wid, hei
	}
	if input.X < 0 || input.Y < 0 || input.Width < 1 || input.Height < 1 || input.X > wid || input.Y > hei || input.Width > wid-input.X || input.Height > hei-input.Y {
		writeError(w, 400, "capture region is outside the active desktop")
		return
	}
	area := image.Rect(input.X, input.Y, input.X+input.Width, input.Y+input.Height)
	columns := min(input.Frames, 3)
	rows := (input.Frames + columns - 1) / columns
	maxWidth := 340
	if input.Frames == 1 {
		maxWidth = 900
	}
	tileW, tileH := captureTileSize(area, maxWidth)
	sheet := image.NewRGBA(image.Rect(0, 0, columns*tileW, rows*tileH))
	marks := make([]string, 0, input.Frames)
	start := time.Now()
	for i := 0; i < input.Frames; i++ {
		if i != 0 {
			timer := time.NewTimer(time.Until(start.Add(time.Duration(i*input.IntervalMS) * time.Millisecond)))
			select {
			case <-r.Context().Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		x, y := i%columns*tileW, i/columns*tileH
		marks = append(marks, strconv.FormatInt(time.Since(start).Milliseconds(), 10))
		if err := display.FrameInto(sheet, image.Rect(x, y, x+tileW, y+tileH), area); err != nil {
			writeError(w, 409, err.Error())
			return
		}
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("X-OpticDeck-Frames", strings.Join(marks, ","))
	w.Header().Set("X-OpticDeck-Grid", fmt.Sprintf("%dx%d", columns, rows))
	w.Header().Set("X-OpticDeck-Tile", fmt.Sprintf("%dx%d", tileW, tileH))
	w.Header().Set("X-OpticDeck-Region", fmt.Sprintf("(%d,%d) %dx%d", area.Min.X, area.Min.Y, area.Dx(), area.Dy()))
	if input.Frames == 1 {
		w.Header().Set("X-OpticDeck-Frame-Guard", frameGuard(area, sheet.Pix))
	}
	_ = jpeg.Encode(w, sheet, &jpeg.Options{Quality: 80})
}

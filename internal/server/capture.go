package server

import (
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"strconv"
	"strings"
	"time"
)

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
	display, ok := s.desktop.(interface {
		FrameInto(*image.RGBA, image.Rectangle, image.Rectangle) error
	})
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
	tileW := min(area.Dx(), maxWidth)
	tileH := max(1, area.Dy()*tileW/area.Dx())
	if tileH > maxWidth {
		tileW, tileH = max(1, area.Dx()*maxWidth/area.Dy()), maxWidth
	}
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
	_ = jpeg.Encode(w, sheet, &jpeg.Options{Quality: 80})
}

package server

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/calinrus-dev/semadesk/internal/web"
	"github.com/gorilla/websocket"
)

// NewHostViewer serves only the viewer and its private RFB stream.
// The mux intentionally has no semantic, clipboard, or application APIs.
func NewHostViewer(cfg Config, assets fs.FS, inputToken string, monitorScale float64) (*Server, error) {
	host, _, err := net.SplitHostPort(cfg.Address)
	if err != nil || host != "127.0.0.1" {
		return nil, errors.New("HTTP address must be 127.0.0.1:port")
	}
	if !filepath.IsAbs(cfg.RFBSocket) || cfg.Token == "" {
		return nil, errors.New("host viewer requires a private Unix socket and a session token")
	}
	if math.IsNaN(monitorScale) || monitorScale < 1 || monitorScale > 8 {
		return nil, errors.New("monitor scale must be between 1 and 8")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{cfg: cfg, ctx: ctx, cancel: cancel, hostView: true, hostInputToken: inputToken, hostMonitorScale: monitorScale, rfbs: make(map[*websocket.Conn]net.Conn)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rfb", s.handleRFB)
	shared := http.FileServer(http.FS(web.FS()))
	mux.Handle("GET /novnc/", shared)
	mux.Handle("GET /contact-sheet.js", shared)
	mux.Handle("GET /contact-sheet.css", shared)
	files := http.FileServer(http.FS(assets))
	mux.Handle("GET /{$}", files)
	mux.Handle("GET /host.css", files)
	mux.Handle("GET /host.js", files)
	s.http = &http.Server{Handler: s.authenticate(mux), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	return s, nil
}

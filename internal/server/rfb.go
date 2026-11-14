package server

import (
	"bytes"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// wsStream preserves RFB's byte stream across arbitrary WebSocket boundaries.
// A browser may split one protocol message or combine several in one frame.
type wsStream struct {
	conn   *websocket.Conn
	reader io.Reader
}

func (s *wsStream) Read(p []byte) (int, error) {
	for {
		if s.reader == nil {
			kind, r, err := s.conn.NextReader()
			if err != nil {
				return 0, err
			}
			if kind != websocket.BinaryMessage {
				return 0, errors.New("RFB requires binary WebSocket messages")
			}
			s.reader = r
		}
		n, err := s.reader.Read(p)
		if err == io.EOF {
			s.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

// readRFBMessage validates bounded standard RFB 3.8 client messages. It reports
// whether a message can change input, clipboard, power state or desktop size.
func readRFBMessage(r io.Reader) (message []byte, control bool, err error) {
	head := make([]byte, 1)
	if _, err = io.ReadFull(r, head); err != nil {
		return nil, false, err
	}
	var fixed, extra int
	switch head[0] {
	case 0:
		fixed = 20 // SetPixelFormat
	case 2:
		fixed = 4 // SetEncodings
	case 3:
		fixed = 10 // FramebufferUpdateRequest
	case 4:
		fixed = 8
		control = true // KeyEvent
	case 5:
		fixed = 6
		control = true // PointerEvent
	case 6:
		fixed = 8
		control = true // ClientCutText
	case 150:
		fixed = 10 // EnableContinuousUpdates
	case 248:
		fixed = 9 // Fence
	case 250:
		fixed = 4
		control = true // XVP
	case 251:
		fixed = 8
		control = true // SetDesktopSize
	case 255:
		fixed = 12
		control = true // QEMU ExtendedKeyEvent (subtype zero)
	default:
		return nil, false, fmt.Errorf("unsupported RFB client message %d", head[0])
	}
	message = make([]byte, fixed)
	message[0] = head[0]
	if _, err = io.ReadFull(r, message[1:]); err != nil {
		return nil, false, err
	}
	switch head[0] {
	case 2:
		count := int(binary.BigEndian.Uint16(message[2:4]))
		if count > 512 {
			return nil, false, errors.New("too many RFB encodings")
		}
		extra = count * 4
	case 6:
		length := binary.BigEndian.Uint32(message[4:8])
		if length > maxMessage {
			return nil, false, errors.New("extended or oversized RFB clipboard rejected; use UTF-8 API")
		}
		extra = int(length)
	case 248:
		extra = int(message[8])
		if extra > 64 {
			return nil, false, errors.New("invalid RFB fence length")
		}
	case 251:
		extra = int(message[6]) * 16
		if message[6] == 0 {
			return nil, false, errors.New("empty RFB screen list")
		}
	case 255:
		if message[1] != 0 {
			return nil, false, errors.New("unsupported QEMU RFB message")
		}
	}
	if extra > 0 {
		tail := make([]byte, extra)
		if _, err = io.ReadFull(r, tail); err != nil {
			return nil, false, err
		}
		message = append(message, tail...)
	}
	return message, control, nil
}

// forwardRFBHandshake only supports the authenticated proxy's None-security
// TigerVNC backend. Authentication happens at HTTP, never at SecurityTypes=None.
func forwardRFBHandshake(client io.Reader, backend io.Writer) error {
	version := make([]byte, 12)
	if _, err := io.ReadFull(client, version); err != nil {
		return err
	}
	if !bytes.Equal(version, []byte("RFB 003.008\n")) {
		return errors.New("RFB 3.8 is required")
	}
	if _, err := backend.Write(version); err != nil {
		return err
	}
	selection := make([]byte, 1)
	if _, err := io.ReadFull(client, selection); err != nil {
		return err
	}
	if selection[0] != 1 {
		return errors.New("only RFB None backend security is supported")
	}
	if _, err := backend.Write(selection); err != nil {
		return err
	}
	// Always request a shared display, regardless of a client's request to eject
	// other viewers. This byte is ClientInit, after the server's SecurityResult.
	if _, err := io.ReadFull(client, selection); err != nil {
		return err
	}
	_, err := backend.Write([]byte{1})
	return err
}

func (s *Server) handleRFB(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("client")
	lease := r.URL.Query().Get("lease")
	if !validClientID(id) {
		writeError(w, 400, "client must be 8..128 URL-safe characters")
		return
	}
	dialer := net.Dialer{Timeout: 3 * time.Second}
	backend, err := dialer.DialContext(r.Context(), "unix", s.cfg.RFBSocket)
	if err != nil {
		writeError(w, 503, "virtual desktop RFB unavailable")
		return
	}
	up := s.upgrader()
	ws, err := up.Upgrade(w, r, nil)
	if err != nil {
		_ = backend.Close()
		return
	}
	s.gate.Lock()
	if s.ctx.Err() != nil {
		s.gate.Unlock()
		_ = backend.Close()
		_ = ws.Close()
		return
	}
	s.mu.Lock()
	s.rfbs[ws] = backend
	s.mu.Unlock()
	s.gate.Unlock()
	done := make(chan struct{})
	defer func() {
		close(done)
		_ = backend.Close()
		_ = ws.Close()
		s.mu.Lock()
		delete(s.rfbs, ws)
		s.mu.Unlock()
	}()
	ws.SetReadLimit(maxMessage + 8192)
	_ = ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	ws.SetPongHandler(func(string) error { return ws.SetReadDeadline(time.Now().Add(45 * time.Second)) })
	go func() {
		// Only this goroutine writes data frames; WriteControl is safe concurrently.
		defer backend.Close()
		defer ws.Close()
		buf := make([]byte, 32<<10)
		for {
			n, e := backend.Read(buf)
			if n > 0 {
				_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if ws.WriteMessage(websocket.BinaryMessage, buf[:n]) != nil {
					return
				}
			}
			if e != nil {
				return
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-s.ctx.Done():
				_ = ws.Close()
				return
			case <-ticker.C:
				if ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
					_ = ws.Close()
					return
				}
			}
		}
	}()
	stream := &wsStream{conn: ws}
	_ = backend.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if forwardRFBHandshake(stream, backend) != nil {
		return
	}
	_ = ws.SetReadDeadline(time.Now().Add(45 * time.Second))
	for {
		msg, control, err := readRFBMessage(stream)
		if err != nil {
			return
		}
		if msg[0] == 2 {
			msg = withoutExtendedClipboard(msg)
		}
		s.gate.Lock()
		s.mu.Lock()
		allowed := s.control.Paused && s.control.Owner == id && s.clients[id] != nil && s.manualToken != "" && subtle.ConstantTimeCompare([]byte(lease), []byte(s.manualToken)) == 1
		s.mu.Unlock()
		if msg[0] == 6 || (control && !allowed) {
			s.gate.Unlock()
			continue
		}
		_ = backend.SetWriteDeadline(time.Now().Add(3 * time.Second))
		_, err = backend.Write(msg)
		s.gate.Unlock()
		if err != nil {
			return
		}
	}
}

// Clipboard is transported only by the UTF-8 semantic channel. Negotiating the
// RFB extension would make noVNC automatically send compressed capabilities.
func withoutExtendedClipboard(msg []byte) []byte {
	out := append([]byte{}, msg[:4]...)
	for i := 4; i+4 <= len(msg); i += 4 {
		if binary.BigEndian.Uint32(msg[i:i+4]) != 0xc0a1e5ce {
			out = append(out, msg[i:i+4]...)
		}
	}
	binary.BigEndian.PutUint16(out[2:4], uint16((len(out)-4)/4))
	return out
}

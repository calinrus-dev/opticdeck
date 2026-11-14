package server

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type oneByteReader struct{ r io.Reader }

func (r oneByteReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.r.Read(p)
}

func TestRFBHandshakeAcrossFragmentsAndSharedFlag(t *testing.T) {
	client := append([]byte("RFB 003.008\n"), 1, 0)
	var backend bytes.Buffer
	if err := forwardRFBHandshake(oneByteReader{bytes.NewReader(client)}, &backend); err != nil {
		t.Fatal(err)
	}
	expected := append([]byte("RFB 003.008\n"), 1, 1)
	if !bytes.Equal(backend.Bytes(), expected) {
		t.Fatal("failed to force shared connection")
	}
	var ignored bytes.Buffer
	if forwardRFBHandshake(bytes.NewBufferString("RFB 003.003\n"), &ignored) == nil {
		t.Fatal("accepted unsupported protocol")
	}
}
func TestRFBMessageBoundariesAndClassification(t *testing.T) {
	messages := [][]byte{{0, 0, 0, 0, 32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0}, {3, 0, 0, 0, 0, 0, 3, 32, 2, 88}, {4, 1, 0, 0, 0, 0, 0, 65}, {5, 1, 0, 10, 0, 20}, {255, 0, 0, 1, 0, 0, 0, 65, 0, 0, 0, 30}, {248, 0, 0, 0, 0, 0, 0, 0, 3, 'a', 'b', 'c'}}
	control := []bool{false, false, true, true, true, false}
	stream := oneByteReader{bytes.NewReader(bytes.Join(messages, nil))}
	for i, want := range messages {
		msg, gotControl, err := readRFBMessage(stream)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(msg, want) || gotControl != control[i] {
			t.Fatalf("message %d: %v control=%v", i, msg, gotControl)
		}
	}
	if _, _, err := readRFBMessage(stream); err != io.EOF {
		t.Fatalf("stream not exhausted: %v", err)
	}
}
func TestRFBRejectsUnboundedAndUnknownMessages(t *testing.T) {
	huge := make([]byte, 8)
	huge[0] = 6
	binary.BigEndian.PutUint32(huge[4:], maxMessage+1)
	negative := make([]byte, 8)
	negative[0] = 6
	binary.BigEndian.PutUint32(negative[4:], 0xfffffffc)
	for _, data := range [][]byte{huge, negative, {99}, {255, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, {248, 0, 0, 0, 0, 0, 0, 0, 65}} {
		if _, _, err := readRFBMessage(bytes.NewReader(data)); err == nil {
			t.Fatalf("accepted invalid message %v", data)
		}
	}
}
func TestRFBStripsExtendedClipboardNegotiation(t *testing.T) {
	msg := make([]byte, 4)
	msg[0] = 2
	binary.BigEndian.PutUint16(msg[2:], 3)
	for _, encoding := range []uint32{0, 0xc0a1e5ce, 16} {
		tail := make([]byte, 4)
		binary.BigEndian.PutUint32(tail, encoding)
		msg = append(msg, tail...)
	}
	stripped := withoutExtendedClipboard(msg)
	if len(stripped) != 12 || binary.BigEndian.Uint16(stripped[2:4]) != 2 || binary.BigEndian.Uint32(stripped[8:]) != 16 {
		t.Fatalf("bad encoding filter: %v", stripped)
	}
}

// The proxy must enforce viewOnly even when a client ignores noVNC's setting,
// and knowledge of the broadcast owner ID must not grant manual input.
func TestRFBProxyEnforcesPrivateManualLease(t *testing.T) {
	dir, err := os.MkdirTemp("", "semadesk-rfb-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "rfb.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	_, _, ts := testServerWithSocket(t, socket)
	accepted := make(chan chan []byte, 4)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			messages := make(chan []byte, 8)
			accepted <- messages
			go func() {
				defer conn.Close()
				defer close(messages)
				_, _ = conn.Write([]byte("RFB 003.008\n"))
				version := make([]byte, 12)
				if _, err := io.ReadFull(conn, version); err != nil {
					return
				}
				_, _ = conn.Write([]byte{1, 1})
				selection := make([]byte, 1)
				if _, err := io.ReadFull(conn, selection); err != nil {
					return
				}
				_, _ = conn.Write([]byte{0, 0, 0, 0})
				if _, err := io.ReadFull(conn, selection); err != nil {
					return
				}
				for {
					msg, _, err := readRFBMessage(conn)
					if err != nil {
						return
					}
					messages <- msg
				}
			}()
		}
	}()
	dialRFB := func(id, lease string) (*websocket.Conn, chan []byte) {
		t.Helper()
		url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/rfb?token=test-secret&client=" + id + "&lease=" + lease
		ws, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ws.Close() })
		var incoming chan []byte
		select {
		case incoming = <-accepted:
		case <-time.After(time.Second):
			t.Fatal("RFB backend not accepted")
		}
		if err := ws.WriteMessage(websocket.BinaryMessage, append([]byte("RFB 003.008\n"), 1, 1)); err != nil {
			t.Fatal(err)
		}
		return ws, incoming
	}
	key := []byte{4, 1, 0, 0, 0, 0, 0, 65}
	update := []byte{3, 0, 0, 0, 0, 0, 3, 32, 2, 88}
	expect := func(ch chan []byte, want []byte) {
		t.Helper()
		select {
		case got := <-ch:
			if !bytes.Equal(got, want) {
				t.Fatalf("backend got %v; want %v", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("RFB backend timed out")
		}
	}
	observer, observed := dialRFB("client-observer", "")
	_ = observer.WriteMessage(websocket.BinaryMessage, append(append([]byte{}, key...), update...))
	expect(observed, update)
	owner := connectWS(t, ts, "client-owner")
	readObject(t, owner)
	sendObject(t, owner, map[string]any{"type": "takeover", "paused": true})
	readObject(t, owner)
	lease := readObject(t, owner)["token"].(string)
	readObject(t, owner)
	impersonator, filtered := dialRFB("client-owner", "")
	_ = impersonator.WriteMessage(websocket.BinaryMessage, append(append([]byte{}, key...), update...))
	expect(filtered, update)
	manual, controlled := dialRFB("client-owner", lease)
	_ = manual.WriteMessage(websocket.BinaryMessage, key)
	expect(controlled, key)
	// Even a manual client cannot send the classic Latin-1 clipboard: clipboard
	// payloads use only the bounded UTF-8 API, avoiding leaks to RFB observers.
	cut := []byte{6, 0, 0, 0, 0, 0, 0, 1, 'x'}
	_ = manual.WriteMessage(websocket.BinaryMessage, append(cut, update...))
	expect(controlled, update)
}

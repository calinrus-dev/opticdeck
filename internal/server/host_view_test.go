package server

import (
	"bytes"
	"context"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gorilla/websocket"
)

func TestHostViewerExposesOnlyAuthenticatedReadOnlyRoutes(t *testing.T) {
	addr := "127.0.0.1:32123"
	cfg := Config{Address: addr, RFBSocket: filepath.Join(t.TempDir(), "rfb.sock")}
	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("Host view")}}
	if _, err := NewHostViewer(cfg, assets, "", 1); err == nil {
		t.Fatal("host viewer accepted an absent token")
	}
	cfg.Token = "host-secret"
	for _, invalidScale := range []float64{0, 0.99, 8.01, math.NaN(), math.Inf(1)} {
		if _, err := NewHostViewer(cfg, assets, "", invalidScale); err == nil {
			t.Fatalf("host viewer accepted invalid scale %v", invalidScale)
		}
	}
	viewer, err := NewHostViewer(cfg, assets, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = viewer.Close(context.Background()) })
	request := func(method, path, host, origin string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://"+host+path, nil)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		viewer.http.Handler.ServeHTTP(w, r)
		return w
	}
	if got := request(http.MethodGet, "/", addr, "").Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request returned %d", got)
	}
	if got := request(http.MethodGet, "/?token=host-secret", "untrusted.example", "").Code; got != http.StatusForbidden {
		t.Fatalf("foreign host returned %d", got)
	}
	if got := request(http.MethodGet, "/rfb?token=host-secret", addr, "https://untrusted.example").Code; got != http.StatusForbidden {
		t.Fatalf("foreign WebSocket origin returned %d", got)
	}
	if got := request(http.MethodGet, "/rfb?token=host-secret&client=host-view&lease=forged", addr, "").Code; got != http.StatusForbidden {
		t.Fatalf("read-only viewer accepted an input request: %d", got)
	}
	index := request(http.MethodGet, "/?token=host-secret", addr, "")
	if index.Code != http.StatusOK || index.Body.String() != "Host view" {
		t.Fatalf("host page response: %d %q", index.Code, index.Body.String())
	}
	cookies := index.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "opticdeck_host_session" || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("host viewer is missing its separate HTTP-only session cookie")
	}
	for _, path := range []string{"/api/launch", "/api/tree", "/api/clipboard", "/api/stop", "/api/act", "/ws"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			if got := request(method, path+"?token=host-secret", addr, "").Code; got != http.StatusNotFound {
				t.Errorf("%s %s is exposed in host view: %d", method, path, got)
			}
		}
	}
}

func TestHostRFBAllowsOnlyAuthorizedKeyboardAndPointer(t *testing.T) {
	dir, err := os.MkdirTemp("", "opticdeck-host-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "rfb.sock")
	backend, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	web := httptest.NewUnstartedServer(nil)
	viewer, err := NewHostViewer(Config{Address: web.Listener.Addr().String(), Token: "host-secret", RFBSocket: socket}, fstest.MapFS{}, "input-secret", 3)
	if err != nil {
		t.Fatal(err)
	}
	web.Config.Handler = viewer.http.Handler
	web.Start()
	defer web.Close()
	defer viewer.Close(context.Background())
	url := "ws" + strings.TrimPrefix(web.URL, "http") + "/rfb?token=host-secret&client=host-view"
	init := []byte("RFB 003.008\n\x01\x01")
	update := []byte{3, 0, 0, 0, 0, 0, 3, 32, 2, 88}
	key := []byte{4, 1, 0, 0, 0, 0, 0, 65}
	pointer := []byte{5, 1, 0x0e, 0xff, 0x08, 0x6f}              // (3839, 2159)
	extendedPointer := []byte{5, 128, 0x05, 0xdc, 0x02, 0x5b, 1} // (1500, 603)
	cutText := []byte{6, 0, 0, 0, 0, 0, 0, 1, 'a'}
	power := []byte{250, 0, 1, 2}
	payload := bytes.Join([][]byte{init, key, pointer, extendedPointer, cutText, power, update}, nil)
	operator, _, err := websocket.DefaultDialer.Dial(url+"&lease=input-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer operator.Close()
	operatorBackend, err := backend.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer operatorBackend.Close()
	observer, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	observerBackend, err := backend.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer observerBackend.Close()
	check := func(ws *websocket.Conn, conn net.Conn, want []byte) {
		t.Helper()
		if err := ws.WriteMessage(websocket.BinaryMessage, payload); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		got := make([]byte, len(want))
		if _, err := io.ReadFull(conn, got); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("backend received %v, want %v: %v", got, want, err)
		}
	}
	scaledPointer := []byte{5, 1, 0x04, 0xff, 0x02, 0xcf}              // (1279, 719)
	scaledExtendedPointer := []byte{5, 128, 0x01, 0xf4, 0x00, 0xc9, 1} // (500, 201)
	check(operator, operatorBackend, bytes.Join([][]byte{init, key, scaledPointer, scaledExtendedPointer, update}, nil))
	check(observer, observerBackend, bytes.Join([][]byte{init, update}, nil))
	second, _, err := websocket.DefaultDialer.Dial(url+"&lease=input-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	_ = second.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := second.ReadMessage(); !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("a second controller was not denied: %v", err)
	}
}

package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/calinrus-dev/semadesk/internal/model"
)

type instance struct {
	bin, dir, url, token, rfb, runtimeDir string
	pid                                   int
}

func startInstance(t *testing.T) *instance {
	t.Helper()
	if os.Getenv("SEMADESK_E2E") != "1" {
		t.Skip("set SEMADESK_E2E=1 with Xvnc, openbox and GTK3 installed")
	}
	bin := os.Getenv("SEMADESK_BIN")
	if bin == "" {
		bin = "../bin/semadesk"
	}
	bin, err := filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	i := &instance{bin: bin, dir: filepath.Join(t.TempDir(), "session")}
	out, err := exec.Command(bin, "start", "--state-dir", i.dir, "--width", "1000", "--height", "700").CombinedOutput()
	if err != nil {
		log, _ := os.ReadFile(filepath.Join(i.dir, "daemon.log"))
		t.Fatalf("start: %v %s\n%s", err, out, log)
	}
	var state struct {
		PID   int    `json:"pid"`
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if err = json.Unmarshal(out, &state); err != nil {
		t.Fatalf("start JSON: %v %s", err, out)
	}
	i.pid, i.token = state.PID, state.Token
	i.url = strings.Split(state.URL, "/?")[0]
	privateState, err := os.ReadFile(filepath.Join(i.dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var resources struct {
		RuntimeDir string `json:"runtime_dir"`
	}
	if err = json.Unmarshal(privateState, &resources); err != nil {
		t.Fatal(err)
	}
	i.runtimeDir = resources.RuntimeDir
	i.rfb = filepath.Join(i.runtimeDir, "rfb.sock")
	t.Cleanup(func() { _ = exec.Command(bin, "stop", "--state-dir", i.dir).Run() })
	return i
}

func (i *instance) call(path string, payload any) ([]byte, int, error) {
	var body io.Reader
	method := "GET"
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
		body = bytes.NewReader(b)
		method = "POST"
	}
	r, err := http.NewRequest(method, i.url+path, body)
	if err != nil {
		return nil, 0, err
	}
	r.Header.Set("Authorization", "Bearer "+i.token)
	r.Header.Set("Content-Type", "application/json")
	c := http.Client{Timeout: 12 * time.Second, Transport: &http.Transport{Proxy: nil}}
	resp, err := c.Do(r)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return b, resp.StatusCode, err
}

func (i *instance) awaitTree(t *testing.T, predicate func(model.Snapshot) bool) model.Snapshot {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var last model.Snapshot
	for time.Now().Before(deadline) {
		b, code, err := i.call("/api/tree", nil)
		if err == nil && code == 200 && json.Unmarshal(b, &last) == nil && predicate(last) {
			return last
		}
		time.Sleep(120 * time.Millisecond)
	}
	b, _ := json.Marshal(last)
	t.Fatalf("tree condition timed out: %s", b)
	return last
}

func TestEndToEndGTK(t *testing.T) {
	i := startInstance(t)
	info, err := os.Lstat(i.rfb)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode() != os.ModeSocket|0600 {
		t.Fatalf("RFB socket has mode %v, want srw-------", info.Mode())
	}
	conn, err := net.DialTimeout("unix", i.rfb, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var banner [12]byte
	_, err = io.ReadFull(conn, banner[:])
	_ = conn.Close()
	if err != nil || string(banner[:]) != "RFB 003.008\n" {
		t.Fatalf("RFB handshake from private socket: %q (%v)", banner, err)
	}
	fixture, err := filepath.Abs("fixture.ui")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(i.bin, "launch", "--state-dir", i.dir, "--", "gtk-builder-tool", "preview", fixture).CombinedOutput(); err != nil {
		t.Fatalf("launch GTK: %v %s", err, out)
	}
	var entry, check, button string
	i.awaitTree(t, func(s model.Snapshot) bool {
		for _, n := range s.Nodes {
			if n.Role == "textbox" && n.Name == "Editor Unicode" {
				entry = n.ID
			}
			if n.Name == "Comprobación" && n.Role == "checkbox" {
				check = n.ID
			}
			if n.Name == "Botón de prueba" && n.Role == "button" {
				button = n.ID
			}
		}
		return entry != "" && check != "" && button != ""
	})
	text := "Español: pingüino — 世界 🙂"
	for _, a := range []model.Action{{Node: button, Action: "click"}, {Node: entry, Action: "set_text", Text: text}, {Node: check, Action: "click"}} {
		b, code, err := i.call("/api/act", a)
		if err != nil || code != 200 {
			t.Fatalf("action %s: %d %s %v", a.Action, code, b, err)
		}
	}
	s := i.awaitTree(t, func(s model.Snapshot) bool {
		gotText, checked := false, false
		for _, n := range s.Nodes {
			if n.ID == entry && n.Value == text {
				gotText = true
			}
			if n.ID == check {
				for _, state := range n.States {
					if state == "checked" {
						checked = true
					}
				}
			}
		}
		return gotText && checked
	})
	b, _ := json.Marshal(s)
	if bytes.Contains(b, []byte("fixture-protected-2FA-9381")) {
		t.Fatal("protected password leaked into semantic tree")
	}
	if s.Width != 1000 || s.Height != 700 {
		t.Fatalf("incorrect framebuffer: %dx%d", s.Width, s.Height)
	}
	for _, n := range s.Nodes {
		r := n.Bounds
		if r.X < 0 || r.Y < 0 || r.Width <= 0 || r.Height <= 0 || r.X+r.Width > s.Width || r.Y+r.Height > s.Height {
			t.Fatalf("out of screen hitbox: %+v", n)
		}
	}
	// An opaque X11 application is still explicitly discoverable as a region.
	if out, err := exec.Command(i.bin, "launch", "--state-dir", i.dir, "--", "xmessage", "-fn", "fixed", "-name", "SemaDesk opaque fixture", "-buttons", "OK", "Opaque fixture").CombinedOutput(); err != nil {
		t.Fatalf("launch X11: %v %s", err, out)
	}
	i.awaitTree(t, func(s model.Snapshot) bool {
		for _, n := range s.Nodes {
			if n.VisionFallback {
				return true
			}
		}
		return false
	})
	out, err := exec.Command(i.bin, "stop", "--state-dir", i.dir).CombinedOutput()
	if err != nil {
		t.Fatalf("stop: %v %s", err, out)
	}
	if c, err := net.DialTimeout("unix", i.rfb, 300*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("RFB survived normal shutdown")
	}
}

func groupAlive(pgid int) bool {
	entries, _ := os.ReadDir("/proc")
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		pos := strings.LastIndexByte(string(b), ')')
		if pos < 0 {
			continue
		}
		fields := strings.Fields(string(b[pos+1:]))
		if len(fields) > 2 && fields[0] != "Z" && fields[0] != "X" && fields[2] == strconv.Itoa(pgid) {
			return true
		}
	}
	return false
}

func TestGTKPopupEscapesParentWindow(t *testing.T) {
	i := startInstance(t)
	fixture, err := filepath.Abs("menu.ui")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(i.bin, "launch", "--state-dir", i.dir, "--", "gtk-builder-tool", "preview", fixture).CombinedOutput(); err != nil {
		t.Fatalf("launch menu: %v %s", err, out)
	}
	var button, last model.Node
	i.awaitTree(t, func(s model.Snapshot) bool {
		for _, n := range s.Nodes {
			if n.Role == "button" && n.Name == "Abrir menú fuera" {
				button = n
				return true
			}
		}
		return false
	})
	if b, code, err := i.call("/api/act", model.Action{Node: button.ID, Action: "click"}); err != nil || code != 200 {
		t.Fatalf("open popup: %d %s %v", code, b, err)
	}
	i.awaitTree(t, func(s model.Snapshot) bool {
		count := 0
		for _, n := range s.Nodes {
			if n.Role == "menuitem" && strings.HasPrefix(n.Name, "Opción popup ") {
				count++
				if n.Name == "Opción popup 14" {
					last = n
				}
			}
		}
		return count == 14
	})
	if last.ID == "" || last.Window == button.Window {
		t.Fatal("overflow menu item inherited the main window instead of its popup")
	}
	if b, code, err := i.call("/api/act", model.Action{Node: last.ID, Action: "click"}); err != nil || code != 200 {
		t.Fatalf("activate overflow item: %d %s %v", code, b, err)
	}
	i.awaitTree(t, func(s model.Snapshot) bool {
		for _, n := range s.Nodes {
			if n.Role == "menuitem" && strings.HasPrefix(n.Name, "Opción popup ") {
				return false
			}
		}
		return true
	})
}

func TestGuardianAfterSIGKILL(t *testing.T) {
	i := startInstance(t)
	if !groupAlive(i.pid) {
		t.Fatal("supervisor group not running")
	}
	if err := syscall.Kill(i.pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if !groupAlive(i.pid) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if groupAlive(i.pid) {
		t.Fatalf("guardian left live processes in group %d", i.pid)
	}
	if i.runtimeDir == "" {
		t.Fatal("missing private runtime directory")
	}
	for time.Now().Before(deadline) {
		if _, err := os.Stat(i.runtimeDir); os.IsNotExist(err) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := os.Stat(i.runtimeDir); !os.IsNotExist(err) {
		t.Fatal("guardian left private profiles after SIGKILL")
	}
	if c, err := net.DialTimeout("unix", i.rfb, 300*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("RFB survived supervisor SIGKILL")
	}
	// A stale state file must not prevent an instance from being started again.
	out, err := exec.Command(i.bin, "start", "--state-dir", i.dir).CombinedOutput()
	if err != nil {
		t.Fatalf("restart after kill: %v %s", err, out)
	}
	var result map[string]any
	if json.Unmarshal(out, &result) != nil {
		t.Fatalf("invalid restart response: %s", out)
	}
	t.Log(fmt.Sprintf("guardian cleaned group %d and restart succeeded", i.pid))
}

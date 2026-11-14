package session

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestApplicationOutputDoesNotReachSupervisorLog(t *testing.T) {
	var output bytes.Buffer
	s := &Session{env: os.Environ(), log: &output, dir: t.TempDir(), done: make(chan error, 1), closed: make(chan struct{})}
	if _, err := s.Launch([]string{"/bin/sh", "-c", "printf synthetic-secret; printf synthetic-secret >&2"}); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	children := append([]*child(nil), s.children...)
	s.mu.Unlock()
	for _, c := range children {
		<-c.exited
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatal("application stdout/stderr leaked into supervisor log")
	}
}

func TestIsolatedEnvDoesNotReuseHostDisplayOrBus(t *testing.T) {
	base := []string{"HOME=/home/user", "PATH=/usr/bin", "DISPLAY=:0", "WAYLAND_DISPLAY=wayland-1", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus", "AT_SPI_BUS_ADDRESS=unix:path=/host/a11y", "XAUTHORITY=/host/auth", "XDG_RUNTIME_DIR=/host/run", "DBUS_STARTER_ADDRESS=host", "XDG_ACTIVATION_TOKEN=host", "ICEAUTHORITY=/host/ice", "XDG_DATA_HOME=/host/data", "XDG_SESSION_TYPE=wayland", "SWAYSOCK=/host/sway", "WINEPREFIX=/host/wine", "MOZ_ENABLE_WAYLAND=1", "SDL_VIDEODRIVER=wayland", "NIXOS_OZONE_WL=1"}
	base = append(base, "ATSPI_BUS_ADDRESS=unix:path=/host/atspi", "ATSPI_DBUS_IMPLEMENTATION=dbus-broker")
	base = append(base, "GDK_SCALE=2", "GDK_DPI_SCALE=0.5", "QT_AUTO_SCREEN_SCALE_FACTOR=1", "QT_SCREEN_SCALE_FACTORS=2")
	env := isolatedEnv(base, "/private", ":123", "unix:path=/private/bus", "/private/auth")
	joined := strings.Join(env, "\n")
	for _, forbidden := range []string{"=:0\n", "WAYLAND_DISPLAY=", "/host", "AT_SPI_BUS_ADDRESS=", "ATSPI_BUS_ADDRESS=", "ATSPI_DBUS_IMPLEMENTATION=dbus-broker", "DBUS_STARTER_ADDRESS=", "XDG_ACTIVATION_TOKEN=", "HOME=/home/user", "XDG_SESSION_TYPE=wayland", "SWAYSOCK=", "GDK_SCALE=", "GDK_DPI_SCALE=", "QT_AUTO_SCREEN_SCALE_FACTOR=", "QT_SCREEN_SCALE_FACTORS="} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("host variable leaked: %s in %s", forbidden, joined)
		}
	}
	for _, required := range []string{"HOME=/private/home", "XDG_DATA_HOME=/private/data", "ICEAUTHORITY=/private/home/.ICEauthority", "XDG_SESSION_TYPE=x11", "DISPLAY=:123", "DBUS_SESSION_BUS_ADDRESS=unix:path=/private/bus", "ATSPI_DBUS_IMPLEMENTATION=dbus-daemon", "XAUTHORITY=/private/auth", "GDK_BACKEND=x11", "QT_QPA_PLATFORM=xcb", "MOZ_ENABLE_WAYLAND=0", "SDL_VIDEODRIVER=x11", "ELECTRON_OZONE_PLATFORM_HINT=x11"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing %s", required)
		}
	}
}

// Run with SEMADESK_SESSION_INTEGRATION=1 when the Linux runtime dependencies
// are installed. It verifies real startup, critical-process failure and reaping.
func TestSessionProcessLifetime(t *testing.T) {
	if os.Getenv("SEMADESK_SESSION_INTEGRATION") != "1" {
		t.Skip("set SEMADESK_SESSION_INTEGRATION=1 to start a real virtual desktop")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	s, err := Start(ctx, Config{Width: 800, Height: 600}, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.mu.Lock()
	children := append([]*child(nil), s.children...)
	s.mu.Unlock()
	if len(children) != 3 {
		t.Fatalf("expected bus, display and window manager; got %d", len(children))
	}
	// A critical exit must stop its siblings and notify the supervisor.
	if err = children[2].cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-s.Done():
		if err == nil {
			t.Fatal("critical process exit did not report an error")
		}
	case <-ctx.Done():
		t.Fatal("critical process exit was not detected")
	}
	select {
	case <-s.closed:
	case <-ctx.Done():
		t.Fatal("session did not clean up")
	}
	for _, c := range children {
		if err = syscall.Kill(c.cmd.Process.Pid, 0); err != syscall.ESRCH {
			t.Fatalf("child %d remains after shutdown: %v", c.cmd.Process.Pid, err)
		}
	}
	if _, err = os.Stat(s.dir); !os.IsNotExist(err) {
		t.Fatalf("private runtime directory remains: %v", err)
	}
}

func TestAuthorityPrivateAndExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth")
	if err := writeAuthority(path, "123", make([]byte, 16)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("authority mode %o", info.Mode().Perm())
	}
	if err = writeAuthority(path, "124", make([]byte, 16)); err == nil {
		t.Fatal("authority overwrite must fail")
	}
}

func TestCompletedApplicationsAreReleased(t *testing.T) {
	s := &Session{env: os.Environ(), log: io.Discard, done: make(chan error, 1), closed: make(chan struct{})}
	t.Cleanup(func() { _ = s.Close() })
	for range 32 {
		if _, err := s.Launch([]string{"/bin/true"}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		s.mu.Lock()
		remaining := len(s.children)
		s.mu.Unlock()
		if remaining == 0 {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("session retained %d completed applications", remaining)
		case <-tick.C:
		}
	}
}

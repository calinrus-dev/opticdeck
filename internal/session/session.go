// Package session owns an X11 desktop and the processes it starts.
package session

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Config struct {
	Width  int
	Height int
}

type child struct {
	cmd    *exec.Cmd
	exited chan struct{}
}

type Session struct {
	Display     string
	BusAddress  string
	RFBSocket   string
	dir         string
	env         []string
	log         io.Writer
	displayLock *os.File
	mu          sync.Mutex
	children    []*child
	closing     bool
	done        chan error
	closeOnce   sync.Once
	closed      chan struct{}
}

// Start must run in the daemon's private process group. Children inherit that
// group so the supervisor's independent guardian can also remove grandchildren.
func Start(ctx context.Context, cfg Config, log io.Writer) (_ *Session, err error) {
	if cfg.Width < 320 || cfg.Width > 8192 || cfg.Height < 240 || cfg.Height > 8192 {
		return nil, errors.New("desktop size must be within 320x240 and 8192x8192")
	}
	if log == nil {
		log = io.Discard
	}
	s := &Session{log: log, done: make(chan error, 1), closed: make(chan struct{})}
	s.dir, err = os.MkdirTemp("", "semadesk-session-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = s.Close()
		}
	}()
	s.RFBSocket = filepath.Join(s.dir, "rfb.sock")
	if !filepath.IsAbs(s.RFBSocket) || len(s.RFBSocket) >= len(syscall.RawSockaddrUnix{}.Path) {
		return nil, errors.New("RFB socket path must be absolute and fit in a Unix socket address; use a shorter absolute TMPDIR")
	}
	for _, sub := range []string{"home", "data", "runtime", "config", "cache", "state"} {
		if err = os.Mkdir(filepath.Join(s.dir, sub), 0700); err != nil {
			return nil, err
		}
	}
	s.Display, s.displayLock, err = reserveDisplay()
	if err != nil {
		return nil, err
	}
	cookie := make([]byte, 16)
	if _, err = rand.Read(cookie); err != nil {
		return nil, err
	}
	auth := filepath.Join(s.dir, "Xauthority")
	if err = writeAuthority(auth, strings.TrimPrefix(s.Display, ":"), cookie); err != nil {
		return nil, err
	}
	s.BusAddress = "unix:path=" + filepath.Join(s.dir, "bus")
	s.env = isolatedEnv(os.Environ(), s.dir, s.Display, s.BusAddress, auth)
	bus := exec.Command("dbus-daemon", "--session", "--nofork", "--nopidfile", "--address="+s.BusAddress, "--print-address=1")
	bus.Env = s.env
	bus.Stderr = log
	out, err := bus.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if _, err = s.start(bus, true); err != nil {
		return nil, fmt.Errorf("start session bus: %w", err)
	}
	address := make(chan string, 1)
	go func() {
		r := bufio.NewReader(out)
		line, _ := r.ReadString('\n')
		address <- strings.TrimSpace(line)
		_, _ = io.Copy(log, r)
	}()
	select {
	case s.BusAddress = <-address:
		if !strings.HasPrefix(s.BusAddress, "unix:") {
			return nil, errors.New("session bus did not report a Unix address")
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(5 * time.Second):
		return nil, errors.New("session bus startup timed out")
	}
	s.env = isolatedEnv(os.Environ(), s.dir, s.Display, s.BusAddress, auth)
	xvnc, err := findExecutable("Xvnc", "Xtigervnc")
	if err != nil {
		return nil, err
	}
	// RFB authentication and takeover are enforced by the HTTP server. The
	// backend is reachable only through this socket in the private session dir.
	vc := exec.Command(xvnc, s.Display,
		"-geometry", fmt.Sprintf("%dx%d", cfg.Width, cfg.Height), "-depth", "24", "-AlwaysShared",
		"-SecurityTypes", "None", "-SendCutText=0", "-AcceptCutText=0",
		"-rfbport", "-1", "-rfbunixpath", s.RFBSocket, "-rfbunixmode", "0600",
		"-nolisten", "tcp", "-auth", auth, "-desktop", "SemaDesk")
	vc.Env, vc.Stdout, vc.Stderr = s.env, log, log
	if _, err = s.start(vc, true); err != nil {
		return nil, fmt.Errorf("start Xvnc: %w", err)
	}
	if err = s.waitFor(ctx, 10*time.Second, func() bool {
		return probeX11(s.Display, cookie) && probeRFB(s.RFBSocket)
	}); err != nil {
		return nil, fmt.Errorf("Xvnc readiness: %w", err)
	}
	wc := exec.Command("openbox", "--sm-disable")
	wc.Env, wc.Stdout, wc.Stderr = s.env, log, log
	if _, err = s.start(wc, true); err != nil {
		return nil, fmt.Errorf("start openbox: %w", err)
	}
	if _, err = exec.LookPath("xprop"); err != nil {
		return nil, errors.New("xprop is required to verify window manager readiness (install x11-utils/xorg-xprop)")
	}
	if err = s.waitFor(ctx, 8*time.Second, func() bool {
		pc, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
		defer cancel()
		cmd := exec.CommandContext(pc, "xprop", "-display", s.Display, "-root", "_NET_SUPPORTING_WM_CHECK")
		cmd.Env = s.env
		output, e := cmd.Output()
		return e == nil && strings.Contains(string(output), "window id # 0x") && !strings.Contains(string(output), "window id # 0x0\n")
	}); err != nil {
		return nil, fmt.Errorf("openbox readiness: %w", err)
	}
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-s.closed:
		}
	}()
	return s, nil
}

func (s *Session) waitFor(ctx context.Context, timeout time.Duration, ready func() bool) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(60 * time.Millisecond)
	defer tick.Stop()
	for {
		if ready() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.closed:
			return errors.New("a critical desktop process exited; inspect daemon.log")
		case <-deadline.C:
			return errors.New("timeout; inspect daemon.log")
		case <-tick.C:
		}
	}
}

func (s *Session) Env() []string { return append([]string(nil), s.env...) }

// RuntimeDir allows the independent guardian to remove private profiles after
// an uncatchable supervisor failure. The directory is immutable after Start.
func (s *Session) RuntimeDir() string { return s.dir }

func (s *Session) Done() <-chan error { return s.done }

func (s *Session) Launch(argv []string) (int, error) {
	if len(argv) == 0 || argv[0] == "" {
		return 0, errors.New("application command is empty")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	// GUI applications such as zenity can print entered secrets on stdout.
	// Their output must never become the supervisor's persistent action log.
	cmd.Env, cmd.Stdout, cmd.Stderr = s.env, io.Discard, io.Discard
	return s.start(cmd, false)
}

func (s *Session) start(cmd *exec.Cmd, critical bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return 0, errors.New("desktop session is shutting down")
	}
	// Children inherit the supervisor's private process group. Its independent
	// guardian owns failure cleanup. Linux Pdeathsig is deliberately avoided:
	// it follows the creating OS thread, which is not a Go process lifetime.
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	c := &child{cmd: cmd, exited: make(chan struct{})}
	s.children = append(s.children, c)
	go func() {
		err := cmd.Wait()
		close(c.exited)
		s.mu.Lock()
		closing := s.closing
		for i, active := range s.children {
			if active == c {
				copy(s.children[i:], s.children[i+1:])
				s.children[len(s.children)-1] = nil
				s.children = s.children[:len(s.children)-1]
				break
			}
		}
		s.mu.Unlock()
		if critical && !closing {
			if err == nil {
				err = errors.New("process exited")
			}
			select {
			case s.done <- fmt.Errorf("%s: %w", filepath.Base(cmd.Path), err):
			default:
			}
			_ = s.Close()
		}
	}()
	return cmd.Process.Pid, nil
}

func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		children := append([]*child(nil), s.children...)
		s.mu.Unlock()
		// Reverse order: apps, WM, display, then the bus.
		for i := len(children) - 1; i >= 0; i-- {
			select {
			case <-children[i].exited:
			default:
				_ = children[i].cmd.Process.Signal(syscall.SIGTERM)
			}
		}
		deadline := time.NewTimer(2 * time.Second)
		defer deadline.Stop()
		for _, c := range children {
			select {
			case <-c.exited:
			case <-deadline.C:
				for _, other := range children {
					select {
					case <-other.exited:
					default:
						_ = other.cmd.Process.Kill()
					}
				}
				// Reap every process; SIGKILL cannot be caught.
				for _, other := range children {
					<-other.exited
				}
				goto cleaned
			}
		}
	cleaned:
		if s.displayLock != nil {
			_ = syscall.Flock(int(s.displayLock.Fd()), syscall.LOCK_UN)
			_ = s.displayLock.Close()
		}
		_ = os.RemoveAll(s.dir)
		close(s.closed)
	})
	return nil
}

func findExecutable(names ...string) (string, error) {
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("required executable missing: %s", strings.Join(names, " or "))
}

func probeRFB(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode() != os.ModeSocket|0600 {
		return false
	}
	c, err := net.DialTimeout("unix", path, 150*time.Millisecond)
	if err != nil {
		return false
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	var banner [12]byte
	_, err = io.ReadFull(c, banner[:])
	return err == nil && strings.HasPrefix(string(banner[:]), "RFB ")
}

func isolatedEnv(base []string, dir, display, bus, auth string) []string {
	remove := map[string]bool{
		"HOME": true, "ICEAUTHORITY": true,
		"DISPLAY": true, "WAYLAND_DISPLAY": true, "XAUTHORITY": true,
		"WAYLAND_SOCKET": true, "SWAYSOCK": true, "I3SOCK": true,
		"HYPRLAND_INSTANCE_SIGNATURE": true,
		"DBUS_SESSION_BUS_ADDRESS":    true, "DBUS_STARTER_BUS_TYPE": true,
		"DBUS_STARTER_ADDRESS": true, "AT_SPI_BUS_ADDRESS": true, "ATSPI_BUS_ADDRESS": true,
		"ATSPI_DBUS_IMPLEMENTATION": true,
		"SESSION_MANAGER":           true, "DESKTOP_STARTUP_ID": true,
		"XDG_ACTIVATION_TOKEN": true, "XDG_RUNTIME_DIR": true,
		"XDG_DATA_HOME": true, "XDG_SEAT": true, "XDG_VTNR": true,
		"XDG_SESSION_ID": true, "XDG_SESSION_TYPE": true,
		"XDG_SEAT_PATH": true, "XDG_SESSION_PATH": true,
		"XDG_CURRENT_DESKTOP": true, "XDG_SESSION_DESKTOP": true,
		"DESKTOP_SESSION": true, "GDMSESSION": true,
		"GNOME_DESKTOP_SESSION_ID": true, "KDE_FULL_SESSION": true,
		"KDE_SESSION_VERSION": true, "KDE_SESSION_UID": true,
		"IBUS_ADDRESS": true, "FCITX_DBUS_ADDRESS": true,
		"QT_IM_MODULE": true, "GTK_IM_MODULE": true, "XMODIFIERS": true,
		"XDG_CONFIG_HOME": true, "XDG_CACHE_HOME": true, "XDG_STATE_HOME": true,
		"NO_AT_BRIDGE": true, "GTK_A11Y": true, "GDK_BACKEND": true,
		"QT_QPA_PLATFORM": true, "QT_LINUX_ACCESSIBILITY_ALWAYS_ON": true,
		"GDK_SCALE": true, "GDK_DPI_SCALE": true,
		"QT_SCALE_FACTOR": true, "QT_AUTO_SCREEN_SCALE_FACTOR": true,
		"QT_SCREEN_SCALE_FACTORS": true, "QT_ENABLE_HIGHDPI_SCALING": true,
		"QT_SCALE_FACTOR_ROUNDING_POLICY": true, "QT_USE_PHYSICAL_DPI": true,
		"QT_DEVICE_PIXEL_RATIO": true, "QT_FONT_DPI": true,
		"WINEPREFIX": true, "MOZ_ENABLE_WAYLAND": true, "SDL_VIDEODRIVER": true,
		"NIXOS_OZONE_WL": true, "ELECTRON_OZONE_PLATFORM_HINT": true,
	}
	env := make([]string, 0, len(base)+12)
	for _, e := range base {
		key, _, _ := strings.Cut(e, "=")
		if !remove[key] {
			env = append(env, e)
		}
	}
	return append(env,
		"HOME="+filepath.Join(dir, "home"),
		"DISPLAY="+display, "XAUTHORITY="+auth, "DBUS_SESSION_BUS_ADDRESS="+bus,
		"ICEAUTHORITY="+filepath.Join(dir, "home", ".ICEauthority"),
		"XDG_RUNTIME_DIR="+filepath.Join(dir, "runtime"),
		"XDG_DATA_HOME="+filepath.Join(dir, "data"),
		"XDG_CONFIG_HOME="+filepath.Join(dir, "config"),
		"XDG_CACHE_HOME="+filepath.Join(dir, "cache"),
		"XDG_STATE_HOME="+filepath.Join(dir, "state"),
		"XDG_SESSION_TYPE=x11", "XDG_CURRENT_DESKTOP=Openbox",
		"XDG_SESSION_DESKTOP=openbox", "DESKTOP_SESSION=semadesk",
		// AT-SPI's dbus-broker activation requires a systemd user manager.
		// The private session bus has none, so use dbus-daemon for both buses.
		"ATSPI_DBUS_IMPLEMENTATION=dbus-daemon",
		"NO_AT_BRIDGE=0", "GTK_A11Y=always", "GDK_BACKEND=x11",
		"QT_QPA_PLATFORM=xcb", "QT_LINUX_ACCESSIBILITY_ALWAYS_ON=1",
		"MOZ_ENABLE_WAYLAND=0", "SDL_VIDEODRIVER=x11", "ELECTRON_OZONE_PLATFORM_HINT=x11")
}

// Xauthority records use big-endian lengths regardless of the X11 byte order.
func writeAuthority(path, display string, cookie []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	// FamilyWild is accepted by Xlib/XCB/xgb and avoids hostname dependencies.
	if err = binary.Write(f, binary.BigEndian, uint16(65535)); err != nil {
		return err
	}
	for _, field := range [][]byte{nil, []byte(display), []byte("MIT-MAGIC-COOKIE-1"), cookie} {
		if err = binary.Write(f, binary.BigEndian, uint16(len(field))); err != nil {
			return err
		}
		if _, err = f.Write(field); err != nil {
			return err
		}
	}
	return f.Close()
}

func reserveDisplay() (string, *os.File, error) {
	for n := 100; n < 1000; n++ {
		display := strconv.Itoa(n)
		// O_NOFOLLOW prevents following a symlink planted by another local user.
		fd, err := syscall.Open(filepath.Join(os.TempDir(), "semadesk-display-"+display+".lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
		if err != nil {
			continue
		}
		lock := os.NewFile(uintptr(fd), "display-lock")
		if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			_ = lock.Close()
			continue
		}
		_, socketErr := os.Lstat("/tmp/.X11-unix/X" + display)
		_, pidErr := os.Lstat("/tmp/.X" + display + "-lock")
		if !errors.Is(socketErr, os.ErrNotExist) || !errors.Is(pidErr, os.ErrNotExist) {
			_ = lock.Close()
			continue
		}
		return ":" + display, lock, nil
	}
	return "", nil, errors.New("no unused X11 display available in :100..:999")
}

func probeX11(display string, cookie []byte) bool {
	c, err := net.DialTimeout("unix", "/tmp/.X11-unix/X"+strings.TrimPrefix(display, ":"), 150*time.Millisecond)
	if err != nil {
		return false
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(200 * time.Millisecond))
	name := []byte("MIT-MAGIC-COOKIE-1")
	pad := func(n int) int { return (n + 3) &^ 3 }
	request := make([]byte, 12+pad(len(name))+pad(len(cookie)))
	request[0] = 'l'
	binary.LittleEndian.PutUint16(request[2:], 11)
	binary.LittleEndian.PutUint16(request[6:], uint16(len(name)))
	binary.LittleEndian.PutUint16(request[8:], uint16(len(cookie)))
	copy(request[12:], name)
	copy(request[12+pad(len(name)):], cookie)
	if _, err = c.Write(request); err != nil {
		return false
	}
	header := make([]byte, 8)
	_, err = io.ReadFull(c, header)
	return err == nil && header[0] == 1
}

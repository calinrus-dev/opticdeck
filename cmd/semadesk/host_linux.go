package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"syscall"
	"time"

	"github.com/calinrus-dev/semadesk/internal/host"
	"github.com/calinrus-dev/semadesk/internal/server"
	"github.com/godbus/dbus/v5"
)

var localXDisplay = regexp.MustCompile(`^:[0-9]+(\.[0-9]+)?$`)

func hostView(args []string) error {
	flags := flag.NewFlagSet("host-view", flag.ContinueOnError)
	x11 := flags.Bool("x11", false, "share the current native X11 display instead of a Wayland monitor")
	allowInput := flags.Bool("allow-input", false, "permit one browser to send keyboard and pointer input")
	monitorScale := flags.Float64("monitor-scale", 1, "Wayland monitor scale for pointer input (1 to 8, e.g. 1.5 for 150%)")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: opticdeck host-view [--x11] [--allow-input] [--monitor-scale SCALE]\n\nView the current Linux desktop in a local browser. Wayland shares one monitor.\nWith --x11, TigerVNC shares the current X11 display and asks before each connection.\nPress Ctrl+C to stop.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("host-view takes no positional arguments")
	}
	if math.IsNaN(*monitorScale) || *monitorScale < 1 || *monitorScale > 8 {
		return errors.New("monitor scale must be between 1 and 8")
	}
	if *x11 && *monitorScale != 1 {
		return errors.New("--monitor-scale applies only to Wayland")
	}
	if os.Geteuid() == 0 {
		return errors.New("host-view must run as your desktop user")
	}
	backendName, x11Display, err := hostBackend(*x11, os.Getenv("XDG_SESSION_TYPE"), os.Getenv("DISPLAY"), os.Getenv("WAYLAND_DISPLAY"), os.Getenv("WAYLAND_SOCKET"))
	if err != nil {
		return err
	}
	backend, err := exec.LookPath(backendName)
	if err != nil {
		return fmt.Errorf("host-view requires TigerVNC's %s", backendName)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	if !*x11 {
		if err = checkHostPortal(ctx); err != nil {
			return err
		}
	}
	dir, err := os.MkdirTemp("", "opticdeck-host-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "rfb.sock")
	if !filepath.IsAbs(socket) || len(socket) >= len(syscall.RawSockaddrUnix{}.Path) {
		return errors.New("host-view requires a shorter absolute TMPDIR for its Unix socket")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	var secret [32]byte
	if _, err = rand.Read(secret[:]); err != nil {
		return err
	}
	token := hex.EncodeToString(secret[:])
	inputToken := ""
	if *allowInput {
		if _, err = rand.Read(secret[:]); err != nil {
			return err
		}
		inputToken = hex.EncodeToString(secret[:])
	}
	viewer, err := server.NewHostViewer(server.Config{Address: listener.Addr().String(), Token: token, RFBSocket: socket}, host.Files, inputToken, *monitorScale)
	if err != nil {
		return err
	}
	defer func() {
		shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = viewer.Close(shutdown)
	}()

	// Linux ties the parent-death signal to the OS thread that forks the child.
	// Keep that thread alive until the viewer is closed and the child is reaped.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cmd := exec.CommandContext(ctx, backend, hostBackendArgs(socket, x11Display, *allowInput)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL, Setpgid: true}
	cmd.Stderr = os.Stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 2 * time.Second
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", backendName, err)
	}
	var backendErr error
	backendDone := make(chan struct{})
	go func() {
		backendErr = cmd.Wait()
		close(backendDone)
	}()
	defer func() { cancel(); <-backendDone }()

	ready, stopReady := context.WithTimeout(ctx, 5*time.Second)
	defer stopReady()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for !hostSocketReady(ready, socket) {
		select {
		case <-ready.Done():
			if ctx.Err() != nil {
				return nil
			}
			return errors.New("TigerVNC did not start a private RFB socket")
		case <-backendDone:
			return hostBackendExit(ctx, backendErr)
		case <-tick.C:
		}
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- viewer.Serve(listener) }()
	link := fmt.Sprintf("http://%s/?token=%s", listener.Addr(), token)
	fragment := url.Values{}
	if inputToken != "" {
		fragment.Set("input", inputToken)
	}
	if *x11 {
		fragment.Set("backend", "x11")
	}
	if len(fragment) != 0 {
		link += "#" + fragment.Encode()
	}
	fmt.Printf("OpticDeck host view\nOpen this private URL, choose a sharing mode and accept the desktop prompt:\n%s\nPress Ctrl+C to stop.\n", link)
	select {
	case <-ctx.Done():
		return nil
	case <-backendDone:
		return hostBackendExit(ctx, backendErr)
	case err = <-serveDone:
		return err
	}
}

func hostBackend(x11 bool, sessionType, display, waylandDisplay, waylandSocket string) (name, x11Display string, err error) {
	if x11 {
		if sessionType != "x11" || !localXDisplay.MatchString(display) || waylandDisplay != "" || waylandSocket != "" {
			return "", "", errors.New("--x11 requires a native local X11 session and a DISPLAY such as :0")
		}
		return "x0vncserver", display, nil
	}
	if sessionType != "wayland" || waylandDisplay == "" {
		return "", "", errors.New("host-view requires an interactive Wayland session; for a native X11 session use --x11")
	}
	return "w0vncserver", "", nil
}

func hostBackendArgs(socket, x11Display string, allowInput bool) []string {
	input := "0"
	if allowInput {
		input = "1"
	}
	args := []string{
		"-rfbport", "-1", "-rfbunixpath", socket, "-rfbunixmode", "0600",
		"-SecurityTypes", "None",
		"-AcceptKeyEvents=" + input, "-AcceptPointerEvents=" + input, "-AlwaysShared", "-desktop", "OpticDeck Host View",
	}
	if x11Display != "" {
		return append(args, "-display", x11Display, "-QueryConnect", "-QueryConnectTimeout", "60",
			"-AcceptSetDesktopSize=0", "-AcceptCutText=0", "-SendCutText=0", "-SetPrimary=0", "-SendPrimary=0")
	}
	return append(args, "-RememberDisplayChoice", "Never")
}

func hostBackendExit(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("desktop sharing stopped: %w", err)
	}
	return errors.New("desktop sharing stopped")
}

func hostSocketReady(ctx context.Context, path string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode() != os.ModeSocket|0600 {
		return false
	}
	dialer := net.Dialer{Timeout: 100 * time.Millisecond}
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	var banner [12]byte
	_, err = io.ReadFull(conn, banner[:])
	return err == nil && string(banner[:]) == "RFB 003.008\n"
}

func checkHostPortal(ctx context.Context) error {
	bus, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("connect to the Wayland session bus: %w", err)
	}
	defer bus.Close()
	portal := bus.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, iface := range []string{"RemoteDesktop", "ScreenCast"} {
		var version dbus.Variant
		err = portal.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, "org.freedesktop.portal."+iface, "version").Store(&version)
		if err != nil {
			return fmt.Errorf("the Wayland %s portal is unavailable: %w", iface, err)
		}
	}
	return nil
}

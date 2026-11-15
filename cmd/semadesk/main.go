// OpticDeck provides a private virtual desktop and an opt-in Wayland viewer.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/calinrus-dev/semadesk/internal/desktop"
	"github.com/calinrus-dev/semadesk/internal/model"
	"github.com/calinrus-dev/semadesk/internal/semantic"
	"github.com/calinrus-dev/semadesk/internal/server"
	"github.com/calinrus-dev/semadesk/internal/session"
)

var version = "0.3.0"

type state struct {
	PID        int       `json:"pid"`
	URL        string    `json:"url"`
	Token      string    `json:"token,omitempty"`
	Display    string    `json:"display"`
	RuntimeDir string    `json:"runtime_dir"`
	Started    time.Time `json:"started"`
}

type startOptions struct {
	dir                 string
	width, height, port int
	noToken             bool
}

func defaultDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		if s, err := os.Lstat(d); err == nil && s.IsDir() && s.Mode().Perm()&0077 == 0 {
			if st, ok := s.Sys().(*syscall.Stat_t); ok && int(st.Uid) == os.Getuid() {
				return filepath.Join(d, "semadesk")
			}
		}
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("semadesk-%d", os.Getuid()))
}

func privateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	s, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := s.Sys().(*syscall.Stat_t)
	if !ok || !s.IsDir() || s.Mode()&os.ModeSymlink != 0 || int(st.Uid) != os.Getuid() || s.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("state directory must be owned by this user, a real directory and mode 0700: %s", path)
	}
	return nil
}

func secureFile(path string, flags int) (*os.File, error) {
	fd, err := syscall.Open(path, flags|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	s, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	st, ok := s.Sys().(*syscall.Stat_t)
	if !ok || !s.Mode().IsRegular() || int(st.Uid) != os.Getuid() || s.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, errors.New("unsafe state file permissions or ownership")
	}
	return f, nil
}

func options(args []string) (startOptions, error) {
	o := startOptions{}
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.StringVar(&o.dir, "state-dir", defaultDir(), "private state directory")
	fs.IntVar(&o.width, "width", 1280, "framebuffer width")
	fs.IntVar(&o.height, "height", 800, "framebuffer height")
	fs.IntVar(&o.port, "port", 0, "loopback HTTP port; 0 selects a free port")
	fs.BoolVar(&o.noToken, "no-token", false, "disable web authentication explicitly")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() != 0 {
		return o, errors.New("unexpected arguments to start")
	}
	if o.width < 320 || o.width > 8192 || o.height < 240 || o.height > 8192 || o.port < 0 || o.port > 65535 {
		return o, errors.New("invalid resolution or port")
	}
	var err error
	o.dir, err = filepath.Abs(o.dir)
	return o, err
}

func writeJSON(w io.Writer, v any) error { return json.NewEncoder(w).Encode(v) }
func readState(dir string) (state, error) {
	var s state
	if err := privateDir(dir); err != nil {
		return s, err
	}
	f, err := secureFile(filepath.Join(dir, "state.json"), syscall.O_RDONLY)
	if err != nil {
		return s, err
	}
	defer f.Close()
	err = json.NewDecoder(io.LimitReader(f, 65536)).Decode(&s)
	if err == nil {
		uerr := validateURL(s.URL)
		if uerr != nil {
			return s, uerr
		}
	}
	return s, err
}

func validateURL(raw string) error {
	// State is private, but still never turn it into a request to a remote host.
	if !strings.HasPrefix(raw, "http://127.0.0.1:") {
		return errors.New("invalid loopback URL in state")
	}
	r, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	if r.URL.Hostname() != "127.0.0.1" || r.URL.User != nil || r.URL.RawQuery != "" || r.URL.Path != "" {
		return errors.New("invalid state URL")
	}
	p, err := strconv.Atoi(r.URL.Port())
	if err != nil || p < 1 || p > 65535 {
		return errors.New("invalid state port")
	}
	return nil
}

var localHTTP = &http.Client{Timeout: 12 * time.Second, Transport: &http.Transport{
	Proxy: nil, MaxIdleConns: 4, MaxIdleConnsPerHost: 4, IdleConnTimeout: 20 * time.Second,
}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}

func request(s state, path string, payload any) (json.RawMessage, error) {
	return requestContext(context.Background(), s, path, payload)
}

func requestContext(ctx context.Context, s state, path string, payload any) (json.RawMessage, error) {
	resp, err := localRequest(ctx, s, path, payload)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if !json.Valid(b) {
		return nil, errors.New("server returned invalid JSON")
	}
	return b, nil
}

func localRequest(ctx context.Context, s state, path string, payload any) (*http.Response, error) {
	var body io.Reader
	method := http.MethodGet
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
		method = http.MethodPost
	}
	r, err := http.NewRequestWithContext(ctx, method, s.URL+path, body)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	if s.Token != "" {
		r.Header.Set("Authorization", "Bearer "+s.Token)
	}
	return localHTTP.Do(r)
}

func start(args []string, stdout io.Writer) error {
	o, err := options(args)
	if err != nil {
		return err
	}
	if err = privateDir(o.dir); err != nil {
		return err
	}
	lf, err := secureFile(filepath.Join(o.dir, "daemon.log"), syscall.O_WRONLY|syscall.O_CREAT|syscall.O_APPEND)
	if err != nil {
		return err
	}
	defer lf.Close()
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()
	exe, err := os.Executable()
	if err != nil {
		w.Close()
		return err
	}
	childArgs := []string{"_serve", "--state-dir", o.dir, "--width", strconv.Itoa(o.width), "--height", strconv.Itoa(o.height), "--port", strconv.Itoa(o.port)}
	if o.noToken {
		childArgs = append(childArgs, "--no-token")
	}
	cmd := exec.Command(exe, childArgs...)
	cmd.ExtraFiles = []*os.File{w}
	cmd.Stdout = lf
	cmd.Stderr = lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		w.Close()
		return err
	}
	w.Close()
	result := make(chan error, 1)
	go func() {
		var v json.RawMessage
		err := json.NewDecoder(io.LimitReader(r, 65536)).Decode(&v)
		if err == nil {
			var e struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(v, &e)
			if e.Error != "" {
				err = errors.New(e.Error)
			} else {
				err = writeJSON(stdout, v)
			}
		}
		result <- err
	}()
	select {
	case err = <-result:
		if err != nil {
			_ = cmd.Wait()
			return fmt.Errorf("start: %w (see %s)", err, filepath.Join(o.dir, "daemon.log"))
		}
		return cmd.Process.Release()
	case <-time.After(35 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		_ = cmd.Wait()
		return fmt.Errorf("session startup timed out; see %s", filepath.Join(o.dir, "daemon.log"))
	}
}

func guardian(target string) error {
	pid, err := strconv.Atoi(target)
	if err != nil || pid < 2 {
		return errors.New("invalid process group")
	}
	r := os.NewFile(3, "supervisor-lifetime")
	if r == nil {
		return errors.New("missing guardian pipe")
	}
	syscall.CloseOnExec(3)
	// Metadata contains only the private runtime directory; this read remains
	// blocked until the supervisor closes its lifetime pipe.
	metadata, _ := io.ReadAll(io.LimitReader(r, 16384))
	_, _ = io.Copy(io.Discard, r)
	r.Close()
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	time.Sleep(1500 * time.Millisecond)
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	var resources struct {
		RuntimeDir string `json:"runtime_dir"`
	}
	if json.Unmarshal(metadata, &resources) == nil {
		_ = cleanRuntime(resources.RuntimeDir)
	}
	return nil
}

func cleanRuntime(path string) error {
	if path == "" || filepath.Dir(path) != filepath.Clean(os.TempDir()) || !strings.HasPrefix(filepath.Base(path), "semadesk-session-") {
		return errors.New("invalid guardian runtime directory")
	}
	if err := privateDirExisting(path); err != nil {
		return err
	}
	return os.RemoveAll(path)
}

func privateDirExisting(path string) error {
	s, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := s.Sys().(*syscall.Stat_t)
	if !ok || !s.IsDir() || s.Mode()&os.ModeSymlink != 0 || int(st.Uid) != os.Getuid() || s.Mode().Perm()&0077 != 0 {
		return errors.New("unsafe guardian runtime directory")
	}
	return nil
}

func serve(args []string) (retErr error) {
	ready := os.NewFile(3, "startup-result")
	syscall.CloseOnExec(3)
	var guardianPipe *os.File
	defer func() {
		if ready != nil {
			if retErr != nil {
				_ = writeJSON(ready, map[string]string{"error": retErr.Error()})
			}
			ready.Close()
		}
		// This is the final defer: state, lock and all server/session resources
		// must be released before the guardian is allowed to signal the group.
		if guardianPipe != nil {
			guardianPipe.Close()
		}
	}()
	if syscall.Getpgrp() != os.Getpid() {
		return errors.New("supervisor must run in its own process group")
	}
	o, err := options(args)
	if err != nil {
		return err
	}
	if err = privateDir(o.dir); err != nil {
		return err
	}
	lock, err := secureFile(filepath.Join(o.dir, "session.lock"), syscall.O_RDWR|syscall.O_CREAT)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("session already running or starting")
	}
	defer os.Remove(filepath.Join(o.dir, "state.json"))
	// The guardian is outside this process group and survives an uncatchable kill.
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		r.Close()
		w.Close()
		return err
	}
	g := exec.Command(exe, "_guard", strconv.Itoa(os.Getpid()))
	g.ExtraFiles = []*os.File{r}
	g.Stdout = os.Stdout
	g.Stderr = os.Stderr
	g.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err = g.Start(); err != nil {
		r.Close()
		w.Close()
		return err
	}
	r.Close()
	guardianPipe = w
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer cancel()
	s, err := session.Start(ctx, session.Config{Width: o.width, Height: o.height}, os.Stderr)
	if err != nil {
		return err
	}
	defer s.Close()
	if err = writeJSON(w, map[string]string{"runtime_dir": s.RuntimeDir()}); err != nil {
		return err
	}
	// xgb reads XAUTHORITY from the environment. Only the supervisor is changed.
	for _, env := range s.Env() {
		if k, v, ok := strings.Cut(env, "="); ok {
			_ = os.Setenv(k, v)
		}
	}
	_ = os.Unsetenv("WAYLAND_DISPLAY")
	for _, name := range []string{"AT_SPI_BUS_ADDRESS", "ATSPI_BUS_ADDRESS", "DBUS_STARTER_ADDRESS", "DBUS_STARTER_BUS_TYPE", "SESSION_MANAGER"} {
		_ = os.Unsetenv(name)
	}
	d, err := desktop.Open(s.Display)
	if err != nil {
		return err
	}
	defer d.Close()
	e, err := semantic.New(ctx, s.BusAddress, d)
	if err != nil {
		return err
	}
	defer e.Close()
	s.SetAccessibilityAddress(e.Address())
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", o.port))
	if err != nil {
		return err
	}
	defer listener.Close()
	token := ""
	if !o.noToken {
		b := make([]byte, 32)
		if _, err = rand.Read(b); err != nil {
			return err
		}
		token = hex.EncodeToString(b)
	}
	srv, err := server.New(server.Config{Address: listener.Addr().String(), Token: token, RFBSocket: s.RFBSocket, Width: o.width, Height: o.height}, e, d, s.Launch, cancel)
	if err != nil {
		return err
	}
	defer func() {
		c, cc := context.WithTimeout(context.Background(), 3*time.Second)
		defer cc()
		_ = srv.Close(c)
	}()
	st := state{PID: os.Getpid(), URL: "http://" + listener.Addr().String(), Token: token, Display: s.Display, RuntimeDir: s.RuntimeDir(), Started: time.Now().UTC()}
	file, err := os.CreateTemp(o.dir, "state-*.json")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err = writeJSON(file, st); err == nil {
		err = file.Sync()
	}
	_ = file.Close()
	if err != nil {
		return err
	}
	if err = os.Rename(temp, filepath.Join(o.dir, "state.json")); err != nil {
		return err
	}
	errs := make(chan error, 1)
	go func() { errs <- srv.Serve(listener) }()
	output := map[string]any{"running": true, "pid": st.PID, "url": st.URL, "display": st.Display}
	if token != "" {
		output["token"] = token
		output["url"] = st.URL + "/?token=" + token
	}
	if err = writeJSON(ready, output); err != nil {
		return err
	}
	ready.Close()
	ready = nil
	log.Printf("OpticDeck %s running on %s, display %s", version, st.URL, st.Display)
	select {
	case <-ctx.Done():
		return nil
	case err = <-s.Done():
		if err == nil {
			err = errors.New("critical session process exited")
		}
		return err
	case err = <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func clientCommand(command string, args []string) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	dir := fs.String("state-dir", defaultDir(), "private state directory")
	a := model.Action{}
	var x, y int
	phase, label := "", ""
	toolkit := "auto"
	if command == "launch" {
		fs.StringVar(&toolkit, "toolkit", "auto", "launch preset: auto, qt, electron")
	}
	if command == "agent-status" {
		fs.StringVar(&phase, "phase", "idle", "idle, working, waiting, done, or error")
		fs.StringVar(&label, "label", "", "short activity description shown in the viewer")
	}
	compact := false
	fresh := false
	match := ""
	limit := 20
	if command == "tree" {
		fs.BoolVar(&compact, "compact", false, "list only actionable controls and visual fallback regions")
		fs.BoolVar(&fresh, "fresh", false, "wait for a fresh accessibility scan before returning")
		fs.StringVar(&match, "match", "", "filter compact results by name or role")
		fs.IntVar(&limit, "limit", 20, "maximum compact results (1–100)")
	}
	if command == "act" {
		fs.StringVar(&a.Node, "node", "", "semantic node ID")
		fs.StringVar(&a.Action, "action", "click", "action name")
		fs.StringVar(&a.Text, "text", "", "text content")
		fs.StringVar(&a.Key, "key", "", "key or chord")
		fs.IntVar(&a.DurationMS, "duration-ms", 0, "hold_key duration (100–2000 milliseconds)")
		fs.Func("expect-name", "require the target's last observed name, including an empty name", func(v string) error { a.ExpectName = &v; return nil })
		fs.Func("expect-revision", "require the last observed desktop revision", func(v string) error {
			revision, err := strconv.ParseUint(v, 10, 64)
			if err == nil {
				a.ExpectRevision = &revision
			}
			return err
		})
		fs.IntVar(&x, "x", -1, "native framebuffer x")
		fs.IntVar(&y, "y", -1, "native framebuffer y")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if command != "launch" && fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if command == "tree" && (limit < 1 || limit > 100 || (!compact && (match != "" || limit != 20))) {
		return errors.New("--match and --limit 1–100 require --compact")
	}
	s, err := readState(*dir)
	if err != nil {
		if command == "status" && errors.Is(err, os.ErrNotExist) {
			return writeJSON(os.Stdout, map[string]bool{"running": false})
		}
		return err
	}
	var path string
	var payload any
	switch command {
	case "url":
		if _, err := request(s, "/api/status", nil); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, s.viewerURL())
		return nil
	case "pause", "resume":
		path, payload = "/api/control", map[string]bool{"paused": command == "pause"}
	case "agent-status":
		path, payload = "/api/agent-status", map[string]string{"phase": phase, "label": label}
	case "status":
		path = "/api/status"
	case "tree":
		path = "/api/tree"
		if fresh {
			path += "?fresh=1"
		}
	case "stop":
		path = "/api/stop"
		payload = struct{}{}
	case "launch":
		if fs.NArg() == 0 {
			return errors.New("launch requires -- executable [arguments]")
		}
		argv, err := launchArguments(toolkit, fs.Args())
		if err != nil {
			return err
		}
		path = "/api/launch"
		payload = map[string]any{"argv": argv}
	case "act":
		if x >= 0 || y >= 0 {
			if x < 0 || y < 0 {
				return errors.New("provide both --x and --y")
			}
			a.X = &x
			a.Y = &y
		}
		path = "/api/act"
		payload = a
	default:
		return errors.New("unknown command")
	}
	b, err := request(s, path, payload)
	if err != nil {
		return err
	}
	if compact {
		return writeCompactTree(os.Stdout, b, match, limit)
	}
	if command == "status" {
		var status map[string]any
		if json.Unmarshal(b, &status) == nil {
			status["url"] = s.viewerURL()
			status["display"] = s.Display
			status["pid"] = s.PID
			return writeJSON(os.Stdout, status)
		}
	}
	if command == "stop" {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Lstat(filepath.Join(*dir, "state.json")); errors.Is(err, os.ErrNotExist) {
				return writeJSON(os.Stdout, b)
			}
			time.Sleep(100 * time.Millisecond)
		}
		return errors.New("shutdown did not complete within 10 seconds")
	}
	return writeJSON(os.Stdout, b)
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: opticdeck open|url|start|stop|pause|resume|status|agent-status|launch|tree|act|host-view|hotkeys|mcp|rustdesk|version")
	}
	switch os.Args[1] {
	case "start":
		return start(os.Args[2:], os.Stdout)
	case "open":
		return openViewer(os.Args[2:])
	case "host-view":
		return hostView(os.Args[2:])
	case "hotkeys":
		return hotkeys(os.Args[2:])
	case "mcp":
		return serveMCP(os.Args[2:])
	case "rustdesk":
		return launchRustDesk(os.Args[2:])
	case "_serve":
		return serve(os.Args[2:])
	case "_guard":
		if len(os.Args) != 3 {
			return errors.New("missing process group")
		}
		return guardian(os.Args[2])
	case "url", "stop", "pause", "resume", "status", "agent-status", "launch", "tree", "act":
		return clientCommand(os.Args[1], os.Args[2:])
	case "version", "--version":
		fmt.Println(version)
		return nil
	case "help", "--help", "-h":
		fmt.Println(`opticdeck open|url [--state-dir DIR]
opticdeck start [--width 1280 --height 800 --port 0 --state-dir DIR --no-token]
opticdeck status|pause|resume|stop [--state-dir DIR]
opticdeck agent-status [--state-dir DIR] [--phase PHASE --label TEXT]
opticdeck tree [--state-dir DIR] [--fresh] [--compact [--match TEXT --limit N]]
opticdeck launch [--state-dir DIR] [--toolkit auto|qt|electron] -- executable [arguments]
opticdeck act [--state-dir DIR] --action ACTION [--node ID --text TEXT --key KEY --duration-ms MS --x N --y N]
             [--expect-name NAME] [--expect-revision N]
opticdeck host-view [--x11] [--allow-input] [--monitor-scale SCALE]
opticdeck hotkeys|mcp|rustdesk [--state-dir DIR]`)
		return nil
	default:
		return fmt.Errorf("unknown command %q", os.Args[1])
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "opticdeck:", err)
		os.Exit(1)
	}
}

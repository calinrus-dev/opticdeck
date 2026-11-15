package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/calinrus-dev/semadesk/internal/model"
	"github.com/godbus/dbus/v5"
)

const (
	shortcutPortal = "org.freedesktop.portal.Desktop"
	shortcutServer = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	shortcutIface  = "org.freedesktop.portal.GlobalShortcuts"
	registryIface  = "org.freedesktop.host.portal.Registry"
	requestIface   = "org.freedesktop.portal.Request"
	sessionIface   = "org.freedesktop.portal.Session"
	pauseShortcut  = "toggle_agent_pause"
	stopShortcut   = "stop_virtual_desktop"
)

type portalShortcut struct {
	ID         string
	Properties map[string]dbus.Variant
}

func hotkeys(args []string) error {
	flags := flag.NewFlagSet("hotkeys", flag.ContinueOnError)
	dir := flags.String("state-dir", defaultDir(), "private state directory for the running virtual desktop")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("hotkeys takes no positional arguments")
	}
	s, err := readState(*dir)
	if err != nil {
		return fmt.Errorf("read virtual desktop session (run opticdeck start first): %w", err)
	}
	if s.Token == "" {
		return errors.New("hotkeys requires an authenticated virtual desktop session")
	}
	if _, err = hotkeyControl(s); err != nil {
		return fmt.Errorf("check virtual desktop session: %w", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	bus, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("connect to the desktop session bus: %w", err)
	}
	defer bus.Close()
	portal := bus.Object(shortcutPortal, shortcutServer)
	setup, stopSetup := context.WithTimeout(ctx, 5*time.Second)
	defer stopSetup()
	// Recent portals require the installed desktop entry to identify host apps.
	// Register before any other call on this bus connection.
	err = portal.CallWithContext(setup, registryIface+".Register", 0, "opticdeck", map[string]dbus.Variant{}).Err
	if err != nil {
		var callError dbus.Error
		if !errors.As(err, &callError) || (callError.Name != "org.freedesktop.DBus.Error.UnknownMethod" && callError.Name != "org.freedesktop.DBus.Error.UnknownInterface") {
			return fmt.Errorf("register OpticDeck with the desktop portal (install opticdeck.desktop first): %w", err)
		}
	}
	if err = portal.CallWithContext(setup, "org.freedesktop.DBus.Properties.Get", 0, shortcutIface, "version").Err; err != nil {
		return fmt.Errorf("the desktop does not expose the Global Shortcuts portal: %w", err)
	}
	var owner string
	if err = bus.BusObject().CallWithContext(setup, "org.freedesktop.DBus.GetNameOwner", 0, shortcutPortal).Store(&owner); err != nil {
		return err
	}
	names := bus.Names()
	if len(names) == 0 || !strings.HasPrefix(names[0], ":") {
		return errors.New("could not identify the desktop session bus connection")
	}
	sender := strings.ReplaceAll(names[0][1:], ".", "_")
	requestRoot := "/org/freedesktop/portal/desktop/request/" + sender
	sessionPath := dbus.ObjectPath("/org/freedesktop/portal/desktop/session/" + sender + "/opticdeck_hotkeys")
	signals := make(chan *dbus.Signal, 32)
	bus.Signal(signals)
	defer bus.RemoveSignal(signals)
	for _, match := range [][]dbus.MatchOption{
		{dbus.WithMatchSender(owner), dbus.WithMatchInterface(requestIface), dbus.WithMatchMember("Response"), dbus.WithMatchPathNamespace(dbus.ObjectPath(requestRoot))},
		{dbus.WithMatchSender(owner), dbus.WithMatchInterface(shortcutIface), dbus.WithMatchObjectPath(shortcutServer)},
		{dbus.WithMatchSender(owner), dbus.WithMatchInterface(sessionIface), dbus.WithMatchMember("Closed"), dbus.WithMatchObjectPath(sessionPath)},
	} {
		if err = bus.AddMatchSignalContext(setup, match...); err != nil {
			return err
		}
	}
	// Use the unique service owner for both requests and responses. A restarted
	// portal cannot accidentally inherit this grant or this listener's actions.
	portal = bus.Object(owner, shortcutServer)
	session := bus.Object(owner, sessionPath)
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = session.CallWithContext(closeCtx, sessionIface+".Close", 0).Err
	}()
	created, err := shortcutRequest(setup, portal, signals, owner, requestRoot, "CreateSession", map[string]dbus.Variant{
		"session_handle_token": dbus.MakeVariant("opticdeck_hotkeys"),
	})
	if err != nil {
		return fmt.Errorf("create desktop shortcut session: %w", err)
	}
	handle := created["session_handle"].Value()
	if handle != string(sessionPath) && handle != sessionPath {
		return errors.New("portal did not return the requested shortcut session")
	}
	shortcuts := []portalShortcut{
		{pauseShortcut, map[string]dbus.Variant{"description": dbus.MakeVariant("OpticDeck: Pause or resume agent"), "preferred_trigger": dbus.MakeVariant("LOGO+ALT+p")}},
		{stopShortcut, map[string]dbus.Variant{"description": dbus.MakeVariant("OpticDeck: Stop virtual desktop"), "preferred_trigger": dbus.MakeVariant("LOGO+ALT+Escape")}},
	}
	fmt.Println("Approve or customize the OpticDeck keyboard shortcuts in the desktop dialog.")
	consent, stopConsent := context.WithTimeout(ctx, 2*time.Minute)
	defer stopConsent()
	result, err := shortcutRequest(consent, portal, signals, owner, requestRoot, "BindShortcuts", nil, sessionPath, shortcuts, "")
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("register desktop shortcuts: %w", err)
	}
	values, present := result["shortcuts"]
	if !present {
		return errors.New("desktop did not return any shortcut bindings")
	}
	var bound []portalShortcut
	if err = dbus.Store([]any{values.Value()}, &bound); err != nil {
		return fmt.Errorf("read shortcut bindings: %w", err)
	}
	active := make(map[string]bool, 2)
	for _, shortcut := range bound {
		if shortcut.ID != pauseShortcut && shortcut.ID != stopShortcut {
			continue
		}
		trigger, _ := shortcut.Properties["trigger_description"].Value().(string)
		if trigger != "" {
			active[shortcut.ID] = true
			label, _ := shortcut.Properties["description"].Value().(string)
			fmt.Printf("%s: %s\n", label, trigger)
		}
	}
	if len(active) == 0 {
		return errors.New("no OpticDeck shortcuts were assigned")
	}
	fmt.Println("Global shortcuts are active for this virtual session. Press Ctrl+C to unregister.")
	return watchHotkeys(ctx, bus.Context(), signals, owner, sessionPath, active, s)
}

func shortcutRequest(ctx context.Context, portal dbus.BusObject, signals <-chan *dbus.Signal, owner, requestRoot, method string, options map[string]dbus.Variant, args ...any) (map[string]dbus.Variant, error) {
	if options == nil {
		options = make(map[string]dbus.Variant)
	}
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return nil, err
	}
	token := "opticdeck_" + hex.EncodeToString(entropy[:])
	options["handle_token"] = dbus.MakeVariant(token)
	var path dbus.ObjectPath
	if err := portal.CallWithContext(ctx, shortcutIface+"."+method, 0, append(args, options)...).Store(&path); err != nil {
		return nil, err
	}
	if path != dbus.ObjectPath(requestRoot+"/"+token) {
		return nil, errors.New("portal returned an unexpected request handle")
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case msg, ok := <-signals:
			if !ok {
				return nil, errors.New("desktop portal disconnected")
			}
			if msg.Sender != owner || msg.Name != requestIface+".Response" || msg.Path != path {
				continue
			}
			var response uint32
			var result map[string]dbus.Variant
			if err := dbus.Store(msg.Body, &response, &result); err != nil {
				return nil, err
			}
			if response != 0 {
				return nil, errors.New("desktop shortcut request was cancelled or denied")
			}
			return result, nil
		}
	}
}

func hotkeyEvent(msg *dbus.Signal, owner string, session dbus.ObjectPath) (id string, pressed, released bool) {
	if msg.Sender != owner || msg.Path != shortcutServer || len(msg.Body) != 4 || msg.Body[0] != session {
		return "", false, false
	}
	id, _ = msg.Body[1].(string)
	return id, msg.Name == shortcutIface+".Activated", msg.Name == shortcutIface+".Deactivated"
}

func watchHotkeys(ctx, connection context.Context, signals <-chan *dbus.Signal, owner string, session dbus.ObjectPath, active map[string]bool, s state) error {
	pressed := make(map[string]bool, 2)
	var pauseDone, stopDone chan error
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-connection.Done():
			return errors.New("desktop shortcuts portal disconnected")
		case err := <-pauseDone:
			pauseDone = nil
			if err != nil {
				fmt.Fprintln(os.Stderr, "OpticDeck pause shortcut:", err)
			}
		case err := <-stopDone:
			stopDone = nil
			if err == nil {
				fmt.Println("OpticDeck virtual desktop stopped.")
				return nil
			}
			fmt.Fprintln(os.Stderr, "OpticDeck stop shortcut:", err)
		case msg, ok := <-signals:
			if !ok || (msg.Sender == owner && msg.Name == sessionIface+".Closed" && msg.Path == session) {
				return errors.New("the desktop closed the global shortcut session")
			}
			id, down, up := hotkeyEvent(msg, owner, session)
			if !active[id] {
				continue
			}
			if up {
				pressed[id] = false
			}
			if !down || pressed[id] {
				continue
			}
			pressed[id] = true
			switch {
			case id == pauseShortcut && pauseDone == nil && stopDone == nil:
				pauseDone = make(chan error, 1)
				go func(done chan<- error) { done <- toggleHotkeyPause(s) }(pauseDone)
			case id == stopShortcut && stopDone == nil:
				stopDone = make(chan error, 1)
				go func(done chan<- error) {
					_, err := request(s, "/api/stop", struct{}{})
					done <- err
				}(stopDone)
			}
		}
	}
}

func hotkeyControl(s state) (model.Control, error) {
	var payload struct {
		Control *model.Control `json:"control"`
	}
	raw, err := request(s, "/api/status", nil)
	if err != nil {
		return model.Control{}, err
	}
	if err = json.Unmarshal(raw, &payload); err != nil || payload.Control == nil {
		return model.Control{}, errors.New("virtual desktop did not return its control state")
	}
	return *payload.Control, nil
}

func toggleHotkeyPause(s state) error {
	current, err := hotkeyControl(s)
	if err != nil {
		return err
	}
	if current.Owner != "" {
		return errors.New("return manual control before changing agent pause")
	}
	_, err = request(s, "/api/control", map[string]bool{"paused": !current.Paused})
	if err == nil {
		if current.Paused {
			fmt.Println("OpticDeck agent resumed.")
		} else {
			fmt.Println("OpticDeck agent paused.")
		}
	}
	return err
}

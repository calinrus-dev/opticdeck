package main

import (
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

func launchRustDesk(args []string) error {
	fs := flag.NewFlagSet("rustdesk", flag.ContinueOnError)
	dir := fs.String("state-dir", defaultDir(), "private state directory")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("rustdesk takes no positional arguments; choose the remote device in the app")
	}
	var argv []string
	if binary, err := exec.LookPath("rustdesk"); err == nil {
		argv = []string{binary, "--no-server"}
	} else {
		data := os.Getenv("XDG_DATA_HOME")
		if data == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			data = filepath.Join(home, ".local", "share")
		}
		binary := filepath.Join(data, "opticdeck", "RustDesk.AppImage")
		if _, err = exec.LookPath(binary); err != nil {
			return errors.New("install RustDesk or place its official executable AppImage at " + binary)
		}
		argv = []string{binary, "--appimage-extract-and-run", "--no-server"}
	}
	s, err := readState(*dir)
	if err == nil {
		_, err = request(s, "/api/status", nil)
	}
	if err != nil {
		if err = start([]string{"--state-dir", *dir}, io.Discard); err != nil {
			return err
		}
		if s, err = readState(*dir); err != nil {
			return err
		}
	}
	_, err = request(s, "/api/launch", map[string][]string{"argv": argv})
	if err == nil {
		return writeJSON(os.Stdout, map[string]bool{"ok": true})
	}
	return err
}

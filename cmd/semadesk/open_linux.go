package main

import (
	"errors"
	"flag"
	"io"
	"net/url"
	"os"
	"os/exec"
)

func openViewer(args []string) error {
	fs := flag.NewFlagSet("open", flag.ContinueOnError)
	dir := fs.String("state-dir", defaultDir(), "private state directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	opener, err := exec.LookPath("xdg-open")
	if err != nil {
		return errors.New("xdg-open is required to open the viewer; install xdg-utils or use semadesk start")
	}
	s, err := readState(*dir)
	if err == nil {
		_, err = request(s, "/api/status", nil)
	}
	if err != nil {
		if err = start([]string{"--state-dir", *dir}, io.Discard); err != nil {
			return err
		}
		s, err = readState(*dir)
		if err != nil {
			return err
		}
	}
	u, err := url.Parse(s.URL)
	if err != nil {
		return err
	}
	u.Path = "/"
	if s.Token != "" {
		u.RawQuery = url.Values{"token": {s.Token}}.Encode()
	}
	cmd := exec.Command(opener, u.String())
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

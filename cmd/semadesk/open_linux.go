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
		return errors.New("xdg-open is required to open the viewer; install xdg-utils or use opticdeck start")
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
	cmd := exec.Command(opener, s.viewerURL())
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (s state) viewerURL() string {
	if s.Token == "" {
		return s.URL + "/"
	}
	return s.URL + "/?" + url.Values{"token": {s.Token}}.Encode()
}

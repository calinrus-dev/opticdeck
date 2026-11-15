package main

import (
	"fmt"
	"strings"
)

func launchArguments(toolkit string, argv []string) ([]string, error) {
	switch toolkit {
	case "auto", "qt":
		return argv, nil // Qt's AT-SPI bridge is enabled in the session environment.
	case "electron":
		for _, arg := range argv[1:] {
			if arg == "--" {
				break
			}
			if strings.HasPrefix(arg, "--ozone-platform=") || strings.HasPrefix(arg, "--force-renderer-accessibility=") ||
				arg == "--ozone-platform" || arg == "--force-renderer-accessibility" || arg == "--disable-renderer-accessibility" {
				return nil, fmt.Errorf("%q conflicts with the Electron launch preset; use --toolkit auto to supply custom flags", arg)
			}
		}
		return append([]string{argv[0], "--ozone-platform=x11", "--force-renderer-accessibility"}, argv[1:]...), nil
	default:
		return nil, fmt.Errorf("unknown toolkit %q", toolkit)
	}
}

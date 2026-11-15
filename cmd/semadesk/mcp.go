package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"

	"github.com/calinrus-dev/semadesk/internal/model"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type observeInput struct {
	Match string `json:"match,omitempty" jsonschema:"filter compact controls by name or role"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum controls, from 1 to 100; default is 20"`
	Fresh bool   `json:"fresh,omitempty" jsonschema:"wait for a new semantic scan rather than use the latest published tree"`
}

type captureInput struct {
	Frames   int `json:"frames,omitempty" jsonschema:"frames in one JPEG grid, from 1 to 9; default is 6"`
	Interval int `json:"interval_ms,omitempty" jsonschema:"sample spacing, from 100 to 500 milliseconds; default is 220"`
	X        int `json:"x,omitempty" jsonschema:"crop origin in desktop pixels"`
	Y        int `json:"y,omitempty" jsonschema:"crop origin in desktop pixels"`
	Width    int `json:"width,omitempty" jsonschema:"crop width in desktop pixels; omit width and height for the full display"`
	Height   int `json:"height,omitempty" jsonschema:"crop height in desktop pixels"`
}

type progressInput struct {
	Phase string `json:"phase" jsonschema:"one of idle, working, waiting, done, or error"`
	Label string `json:"label,omitempty" jsonschema:"a short public-facing task update for the local viewer, at most 120 characters"`
}

func serveMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	dir := fs.String("state-dir", defaultDir(), "private state directory for a virtual desktop")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("mcp takes no positional arguments")
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "opticdeck", Version: version}, &mcp.ServerOptions{
		Instructions: "Inspect an authorized virtual desktop by role and name first. Pass the observed name or desktop revision to act. Refresh after a UI change or stale_observation. Use a single frame or contact sheet when pixels matter. This adapter connects only to the selected local virtual session.",
	})
	read := func() (state, error) {
		st, err := readState(*dir)
		if err == nil && st.Token == "" {
			err = errors.New("MCP requires an authenticated virtual session")
		}
		if err != nil {
			return st, fmt.Errorf("read virtual session (start it with opticdeck start): %w", err)
		}
		return st, nil
	}
	query := func(ctx context.Context, path string, body any) (*mcp.CallToolResult, any, error) {
		st, err := read()
		if err != nil {
			return nil, nil, err
		}
		raw, err := requestContext(ctx, st, path, body)
		if err != nil {
			return nil, nil, err
		}
		return textResult(string(raw)), nil, nil
	}
	mcp.AddTool(s, &mcp.Tool{Name: "status", Description: "Get connection, pause, and task status. Does not return a session credential."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			return query(ctx, "/api/status", nil)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "observe", Description: "Read a compact list of actionable semantic controls and visual fallback regions. Query by role or name, optionally refresh."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in observeInput) (*mcp.CallToolResult, any, error) {
			if in.Limit == 0 {
				in.Limit = 20
			}
			if in.Limit < 1 || in.Limit > 100 {
				return nil, nil, errors.New("limit must be between 1 and 100")
			}
			st, err := read()
			if err != nil {
				return nil, nil, err
			}
			path := "/api/tree"
			if in.Fresh {
				path += "?fresh=1"
			}
			raw, err := requestContext(ctx, st, path, nil)
			if err != nil {
				return nil, nil, err
			}
			var result bytes.Buffer
			if err = writeCompactTree(&result, raw, in.Match, in.Limit); err != nil {
				return nil, nil, err
			}
			return textResult(result.String()), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "act", Description: "Send one virtual desktop action: click, double_click, right_click, set_text, press_key, hold_key, shortcut, or focus. Send expect_name for a named target or expect_revision for an observed semantic revision. For coordinate input on opaque apps, pass expect_frame from a single crop including those coordinates. hold_key takes duration_ms (100–2000)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in model.Action) (*mcp.CallToolResult, any, error) {
			return query(ctx, "/api/act", in)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "update_progress", Description: "Show a short task status to the local observer, without typing into any application."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in progressInput) (*mcp.CallToolResult, any, error) {
			return query(ctx, "/api/agent-status", in)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "capture", Description: "Read one JPEG from the virtual desktop. Full framebuffer or optional crop. A multi-frame capture returns one contact sheet in left-to-right, top-to-bottom order."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in captureInput) (*mcp.CallToolResult, any, error) {
			st, err := read()
			if err != nil {
				return nil, nil, err
			}
			res, err := localRequest(ctx, st, "/api/capture", in)
			if err != nil {
				return nil, nil, err
			}
			defer res.Body.Close()
			b, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
			if err != nil {
				return nil, nil, err
			}
			if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/jpeg" {
				return nil, nil, fmt.Errorf("capture failed (HTTP %d): %s", res.StatusCode, b)
			}
			if len(b) > 2<<20 {
				return nil, nil, errors.New("capture exceeded JPEG size limit")
			}
			caption := fmt.Sprintf("Grid %s, tile %s image pixels, native region %s. Actual capture times in milliseconds: %s. Read left to right, then down. For coordinates use the position inside the latest tile, scale to the native region, then add its origin.",
				res.Header.Get("X-OpticDeck-Grid"), res.Header.Get("X-OpticDeck-Tile"), res.Header.Get("X-OpticDeck-Region"), res.Header.Get("X-OpticDeck-Frames"))
			if guard := res.Header.Get("X-OpticDeck-Frame-Guard"); guard != "" {
				caption += "\nexpect_frame: " + guard
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: caption}, &mcp.ImageContent{Data: b, MIMEType: "image/jpeg"}}}, nil, nil
		})
	return s.Run(context.Background(), &mcp.StdioTransport{})
}

func textResult(value string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: value}}}
}

package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCompactTreeBoundsOutputAndOmitsValues(t *testing.T) {
	raw := json.RawMessage(`{"revision":17,"nodes":[
		{"id":"container","role":"frame","name":"Editor"},
		{"id":"entry","role":"textbox","name":"Editor Unicode","value":"private payload","actions":["set_text"]},
		{"id":"canvas","role":"vision_fallback","name":"Editor canvas","vision_fallback":true,"bounds":{"x":1,"y":2,"width":30,"height":40}}
	]}`)
	var output bytes.Buffer
	if err := writeCompactTree(&output, raw, "EDITOR", 1); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Matched  int              `json:"matched"`
		Controls []compactControl `json:"controls"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Matched != 2 || len(result.Controls) != 1 || result.Controls[0].ID != "entry" || bytes.Contains(output.Bytes(), []byte("private payload")) {
		t.Fatalf("unexpected compact tree: %s", output.String())
	}
}

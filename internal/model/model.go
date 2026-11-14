package model

type Rect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type Node struct {
	ID             string   `json:"id"`
	Parent         string   `json:"parent,omitempty"`
	Role           string   `json:"role"`
	Name           string   `json:"name"`
	Value          string   `json:"value,omitempty"`
	States         []string `json:"states"`
	Actions        []string `json:"actions"`
	Shortcut       string   `json:"shortcut,omitempty"`
	Bounds         Rect     `json:"bounds"`
	Window         uint32   `json:"window"`
	Z              int      `json:"z"`
	Depth          int      `json:"depth"`
	VisionFallback bool     `json:"vision_fallback,omitempty"`
}

type Action struct {
	ID     string `json:"id,omitempty"`
	Node   string `json:"node,omitempty"`
	Action string `json:"action"`
	Text   string `json:"text,omitempty"`
	Key    string `json:"key,omitempty"`
	X      *int   `json:"x,omitempty"`
	Y      *int   `json:"y,omitempty"`
}

type Control struct {
	Paused bool   `json:"paused"`
	Owner  string `json:"owner,omitempty"`
}

type Snapshot struct {
	Type     string  `json:"type"`
	Revision uint64  `json:"revision"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Nodes    []Node  `json:"nodes"`
	Control  Control `json:"control"`
}

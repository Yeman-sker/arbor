package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
)

// Client speaks the protocol of the mygo automation plugin: newline-delimited
// JSON over a Unix domain socket (see docs/plugins/automation.md in mygo).
type Client struct {
	conn   net.Conn
	r      *bufio.Reader
	id     int
	window int
}

// CallError is an error answer of the app.
type CallError struct{ Code, Message string }

func (e *CallError) Error() string { return e.Code + ": " + e.Message }

// Dial connects to the socket, retrying until the app listens.
func Dial(path string, wait time.Duration) (*Client, error) {
	deadline := time.Now().Add(wait)
	for {
		conn, err := net.Dial("unix", path)
		if err == nil {
			return &Client{conn: conn, r: bufio.NewReaderSize(conn, 1<<20)}, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("automation socket %s: %w", path, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (c *Client) Close() error { return c.conn.Close() }

// Call sends one request and decodes the answer's result into out (when not nil).
func (c *Client) Call(method string, params, out any) error {
	c.id++
	req := map[string]any{"id": c.id, "method": method}
	if params != nil {
		req["params"] = params
	}
	line, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := c.conn.Write(append(line, '\n')); err != nil {
		return err
	}
	answer, err := c.r.ReadBytes('\n')
	if err != nil {
		return err
	}
	var res struct {
		Result json.RawMessage
		Error  *CallError
	}
	if err := json.Unmarshal(answer, &res); err != nil {
		return err
	}
	if res.Error != nil {
		return res.Error
	}
	if out != nil {
		return json.Unmarshal(res.Result, out)
	}
	return nil
}

// Rect is a box in DIPs relative to the window's content.
type Rect struct{ X, Y, Width, Height float64 }

func (r Rect) Center() (x, y float64) { return r.X + r.Width/2, r.Y + r.Height/2 }

// Node is an element of the window as assistive technology sees it.
type Node struct {
	ID      string
	Role    string
	Key     any
	Label   string
	Value   string
	Bounds  Rect
	States  []string
	Actions []string
}

func (n Node) KeyString() string {
	if n.Key == nil {
		return ""
	}
	return fmt.Sprint(n.Key)
}

// Window finds the first window of native UI.
func (c *Client) Window(wait time.Duration) (int, error) {
	deadline := time.Now().Add(wait)
	for {
		var res struct {
			Windows []struct {
				ID      int
				Kind    string
				Visible bool
			}
		}
		if err := c.Call("windows", nil, &res); err != nil {
			return 0, err
		}
		for _, w := range res.Windows {
			if w.Kind == "content" {
				c.window = w.ID
				return w.ID, nil
			}
		}
		if time.Now().After(deadline) {
			return 0, errors.New("the app opened no window")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Tree reads the elements of the window's last frame.
func (c *Client) Tree() ([]Node, error) {
	var res struct{ Nodes []Node }
	err := c.Call("tree", map[string]any{"window": c.window}, &res)
	return res.Nodes, err
}

// Idle waits until the window asks for no more frames. A window that never
// settles (a blinking caret, a spinner) answers with a timeout, which is not an
// error for a scenario that measures spans instead.
func (c *Client) Idle(timeout time.Duration) bool {
	err := c.Call("idle", map[string]any{"window": c.window, "timeout": timeout.Milliseconds()}, nil)
	return err == nil
}

// Input sends pointer, wheel, key and text events.
func (c *Client) Input(events ...map[string]any) error {
	return c.Call("input", map[string]any{"window": c.window, "events": events}, nil)
}

// ClickAt clicks the point as a pointer does: a move, a press and a release.
func (c *Client) ClickAt(x, y float64) error {
	return c.Input(map[string]any{"type": "click", "x": x, "y": y})
}

// Click clicks the center of a node.
func (c *Client) Click(n Node) error {
	x, y := n.Bounds.Center()
	return c.ClickAt(x, y)
}

// Act performs an accessibility action on the element with the key.
func (c *Client) Act(key, action, text string) error {
	p := map[string]any{"window": c.window, "key": key, "action": action}
	if text != "" {
		p["text"] = text
	}
	return c.Call("act", p, nil)
}

// Wheel scrolls at a point, as a touchpad does.
func (c *Client) Wheel(x, y, dy float64) error {
	return c.Input(map[string]any{"type": "wheel", "x": x, "y": y, "dx": 0, "dy": dy, "precise": true})
}

// Find returns the first node for which match is true.
func Find(nodes []Node, match func(Node) bool) (Node, bool) {
	for _, n := range nodes {
		if match(n) {
			return n, true
		}
	}
	return Node{}, false
}

// ByKey matches a node by its Key.
func ByKey(key string) func(Node) bool { return func(n Node) bool { return n.KeyString() == key } }

// ByLabel matches a node by its exact label.
func ByLabel(label string) func(Node) bool { return func(n Node) bool { return n.Label == label } }

// ByLabelRe matches a node by a regular expression on its label.
func ByLabelRe(re *regexp.Regexp) func(Node) bool {
	return func(n Node) bool { return re.MatchString(n.Label) }
}

// Visible reports whether a node has an on-screen box inside the window.
func Visible(n Node, w, h float64) bool {
	for _, s := range n.States {
		if s == "offscreen" {
			return false
		}
	}
	return n.Bounds.Width > 0 && n.Bounds.Height > 0 && n.Bounds.Y >= 0 && n.Bounds.Y+n.Bounds.Height <= h && n.Bounds.X >= 0 && n.Bounds.X+n.Bounds.Width <= w
}

// Labels lists the labels of the nodes that match, for diagnostics.
func Labels(nodes []Node, max int) string {
	var out []string
	for _, n := range nodes {
		if n.Label != "" {
			out = append(out, n.Label)
		}
		if len(out) == max {
			break
		}
	}
	return strings.Join(out, " | ")
}

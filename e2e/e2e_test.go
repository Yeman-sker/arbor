package main

import (
	"bufio"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestJudgeAppliesBudgetToTheMedian(t *testing.T) {
	cases := []struct {
		name    string
		samples []float64
		budget  float64
		want    Status
	}{
		{"under", []float64{40, 42, 41, 43, 44}, 50, Pass},
		{"one cold run does not fail", []float64{40, 42, 41, 43, 400}, 50, Pass},
		{"over", []float64{60, 62, 61, 63, 64}, 50, Fail},
		{"one lucky run does not rescue", []float64{10, 62, 61, 63, 64}, 50, Fail},
		{"no budget is a baseline", []float64{12, 13, 14}, -1, Info},
		{"a budget of zero is a budget", []float64{0, 0, 0}, 0, Pass},
		{"a budget of zero fails any frame", []float64{0, 3, 2}, 0, Fail},
	}
	for _, c := range cases {
		m := Metric{Scenario: c.name, Unit: "ms", Samples: c.samples, Budget: c.budget, HasBudget: c.budget >= 0}
		m.Judge()
		if m.Status != c.want {
			t.Errorf("%s: status = %s, want %s (median %v)", c.name, m.Status, c.want, m.Median)
		}
	}
	skip := Skipped("new_window", "", "needs ARB-1")
	skip.Judge()
	if skip.Status != Skip {
		t.Errorf("skipped metric = %s", skip.Status)
	}
	if Failed([]Metric{skip}) {
		t.Error("a skipped scenario must not fail the run")
	}
	if !Failed([]Metric{{Status: Fail}}) {
		t.Error("a failed budget must fail the run")
	}
}

func TestParseCPUTime(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"0:01.23":    1230 * time.Millisecond,
		"2:03.50":    123*time.Second + 500*time.Millisecond,
		"1:02:03.00": time.Hour + 2*time.Minute + 3*time.Second,
	} {
		got, err := parseCPUTime(in)
		if err != nil || got != want {
			t.Errorf("parseCPUTime(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseCPUTime("garbage"); err == nil {
		t.Error("garbage parsed")
	}
}

func TestFrameStatsLine(t *testing.T) {
	m := frameLine.FindStringSubmatch("2026/10/31 10:00:00 mygo: frame 42 took 17.3 ms: build 1.0 (2 passes), layout 2.0")
	if m == nil || m[1] != "42" || m[2] != "17.3" {
		t.Fatalf("match = %v", m)
	}
}

// TestClientSpeaksTheProtocol runs the client against a fake automation server.
func TestClientSpeaksTheProtocol(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "s.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip("unix sockets unavailable:", err)
	}
	defer l.Close()
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		sc := bufio.NewScanner(conn)
		for sc.Scan() {
			var req struct {
				ID     int
				Method string
				Params map[string]any
			}
			json.Unmarshal(sc.Bytes(), &req)
			var res any
			switch req.Method {
			case "windows":
				res = map[string]any{"windows": []map[string]any{{"id": 7, "kind": "content", "visible": true}}}
			case "tree":
				res = map[string]any{"window": 7, "nodes": []map[string]any{
					{"id": "1", "role": "button", "key": "seg-git", "label": "Git", "bounds": map[string]float64{"x": 10, "y": 20, "width": 60, "height": 20}},
				}}
			case "input":
				if req.Params["window"].(float64) != 7 {
					json.NewEncoder(conn).Encode(map[string]any{"id": req.ID, "error": map[string]string{"code": "no_window", "message": "x"}})
					continue
				}
				res = map[string]any{}
			default:
				json.NewEncoder(conn).Encode(map[string]any{"id": req.ID, "error": map[string]string{"code": "unknown_method", "message": req.Method}})
				continue
			}
			json.NewEncoder(conn).Encode(map[string]any{"id": req.ID, "result": res})
		}
	}()

	c, err := Dial(sock, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if id, err := c.Window(time.Second); err != nil || id != 7 {
		t.Fatalf("window = %d, %v", id, err)
	}
	nodes, err := c.Tree()
	if err != nil || len(nodes) != 1 {
		t.Fatalf("tree = %+v, %v", nodes, err)
	}
	n, ok := Find(nodes, ByKey("seg-git"))
	if !ok || n.Label != "Git" {
		t.Fatalf("find by key = %+v", n)
	}
	if _, ok := Find(nodes, ByLabel("Files")); ok {
		t.Fatal("found a node that is not there")
	}
	if x, y := n.Bounds.Center(); x != 40 || y != 30 {
		t.Fatalf("center = %v, %v", x, y)
	}
	if err := c.Click(n); err != nil {
		t.Fatal(err)
	}
	err = c.Call("nope", nil, nil)
	if ce, ok := err.(*CallError); !ok || ce.Code != "unknown_method" {
		t.Fatalf("error = %#v", err)
	}
}

func TestVisible(t *testing.T) {
	in := Node{Bounds: Rect{10, 10, 50, 20}}
	if !Visible(in, 1280, 800) {
		t.Error("inside node not visible")
	}
	if Visible(Node{Bounds: Rect{10, 790, 50, 20}}, 1280, 800) {
		t.Error("node crossing the bottom edge is visible")
	}
	if Visible(Node{Bounds: Rect{10, 10, 50, 20}, States: []string{"offscreen"}}, 1280, 800) {
		t.Error("offscreen node is visible")
	}
}

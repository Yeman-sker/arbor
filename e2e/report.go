package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
)

// Status of a metric against its budget.
type Status string

const (
	Pass Status = "PASS"
	Fail Status = "FAIL"
	Skip Status = "SKIP" // cannot be measured yet; the note says why
	Info Status = "INFO" // recorded as a baseline, no budget
)

// Metric is one measured quantity of a scenario: its samples, how they were
// obtained and the budget they are judged against.
type Metric struct {
	Scenario string    `json:"scenario"`
	Size     string    `json:"size,omitempty"`
	Unit     string    `json:"unit"`
	Samples  []float64 `json:"samples,omitempty"`
	Budget   float64   `json:"budget,omitempty"` // meaningful when HasBudget
	// HasBudget is false for a metric recorded as a baseline; a budget of 0 is a real one.
	HasBudget bool   `json:"hasBudget"`
	Source    string `json:"source,omitempty"` // span | wall | frames | ps
	Status    Status `json:"status"`
	Note      string `json:"note,omitempty"`

	Min    float64 `json:"min"`
	Median float64 `json:"median"`
	Max    float64 `json:"max"`
}

// Judge fills in the summary and the status. The budget applies to the median,
// like the Go benchmarks, so one cold run cannot fail or rescue a scenario.
func (m *Metric) Judge() {
	if len(m.Samples) == 0 {
		return
	}
	s := append([]float64(nil), m.Samples...)
	sort.Float64s(s)
	m.Min, m.Max = s[0], s[len(s)-1]
	if n := len(s); n%2 == 1 {
		m.Median = s[n/2]
	} else {
		m.Median = (s[n/2-1] + s[n/2]) / 2
	}
	switch {
	case m.Status == Skip:
	case !m.HasBudget:
		m.Status = Info
	case m.Median <= m.Budget:
		m.Status = Pass
	default:
		m.Status = Fail
	}
}

// Skipped makes a metric for a scenario that cannot run.
func Skipped(scenario, size, note string) Metric {
	return Metric{Scenario: scenario, Size: size, Status: Skip, Note: note}
}

// Failed reports whether any metric failed its budget.
func Failed(ms []Metric) bool {
	for _, m := range ms {
		if m.Status == Fail {
			return true
		}
	}
	return false
}

func fmtVal(v float64, unit string) string {
	switch unit {
	case "%":
		return fmt.Sprintf("%.2f%%", v)
	case "ms":
		return fmt.Sprintf("%.1fms", v)
	}
	return fmt.Sprintf("%.1f%s", v, unit)
}

// PrintReport writes the table of results.
func PrintReport(w io.Writer, ms []Metric) {
	tw := tabwriter.NewWriter(w, 2, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SCENARIO\tSIZE\tN\tMIN\tMEDIAN\tMAX\tBUDGET\tSOURCE\tSTATUS\tNOTE")
	for _, m := range ms {
		if len(m.Samples) == 0 {
			fmt.Fprintf(tw, "%s\t%s\t0\t\t\t\t\t\t%s\t%s\n", m.Scenario, m.Size, m.Status, m.Note)
			continue
		}
		budget := "-"
		if m.HasBudget {
			budget = fmtVal(m.Budget, m.Unit)
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", m.Scenario, m.Size, len(m.Samples),
			fmtVal(m.Min, m.Unit), fmtVal(m.Median, m.Unit), fmtVal(m.Max, m.Unit), budget, m.Source, m.Status, m.Note)
	}
	tw.Flush()
	counts := map[Status]int{}
	for _, m := range ms {
		counts[m.Status]++
	}
	var parts []string
	for _, s := range []Status{Pass, Fail, Skip, Info} {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], strings.ToLower(string(s))))
		}
	}
	fmt.Fprintf(w, "\n%s\n", strings.Join(parts, ", "))
}

// WriteJSON stores the results, for baselines and later reports.
func WriteJSON(path string, ms []Metric, machine string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(map[string]any{"machine": machine, "metrics": ms}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

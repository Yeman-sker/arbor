// Package machine names the class of machine a performance number belongs to.
// The name is built from the CPU model and the OS alone: never a host name, user
// name, path or address.
package machine

import (
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// Machine identifies a machine class.
type Machine struct {
	Key  string `json:"key"`
	CPU  string `json:"cpu"`
	GOOS string `json:"goos"`
	Arch string `json:"goarch"`
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Key builds the file-name-safe key, such as "apple-m4-darwin-arm64".
func Key(cpu, goos, goarch string) string {
	slug := func(s string) string { return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-") }
	cpu = slug(cpu)
	if cpu == "" {
		cpu = "unknown-cpu"
	}
	return cpu + "-" + slug(goos) + "-" + slug(goarch)
}

// Detect reads the CPU model of the machine it runs on.
func Detect() Machine {
	cpu := detectCPU()
	return Machine{Key: Key(cpu, runtime.GOOS, runtime.GOARCH), CPU: cpu, GOOS: runtime.GOOS, Arch: runtime.GOARCH}
}

func detectCPU() string {
	switch runtime.GOOS {
	case "darwin":
		if out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil {
			return strings.TrimSpace(string(out))
		}
	case "linux":
		if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				k, v, ok := strings.Cut(line, ":")
				if ok && (strings.TrimSpace(k) == "model name" || strings.TrimSpace(k) == "Model") {
					return strings.TrimSpace(v)
				}
			}
		}
	case "windows":
		return os.Getenv("PROCESSOR_IDENTIFIER")
	}
	return ""
}

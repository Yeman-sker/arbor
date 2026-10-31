package perfstore

import (
	"os"
	"path/filepath"
)

// AppName is the name of Arbor's user data directory (mygo.json "name"), which
// mygo puts under the user's config directory.
const AppName = "Arbor"

// UserDataDirEnv overrides the user data directory for tools and tests.
const UserDataDirEnv = "ARBOR_USER_DATA"

// UserDataDir is where Arbor keeps its data, as mygo's PathUserData says for the
// packaged app: <config dir>/Arbor. The command line tools use it to find what
// the app wrote. "" when the system has no config directory.
func UserDataDir() string {
	if d := os.Getenv(UserDataDirEnv); d != "" {
		return d
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, AppName)
}

// LogDir is where the logs of real usage are, inside a user data directory.
func LogDir(userData string) string { return filepath.Join(userData, "perf") }

// BenchDir is where the history of benchmark runs is. It is not under LogDir:
// deleting the diagnostics data of the user must not delete the history of the
// developer's own benchmark runs.
func BenchDir(userData string) string { return filepath.Join(userData, "perf-bench") }

// Command e2e runs Arbor's real-app performance scenarios.
//
// It builds Arbor with -tags e2e (which links mygo's automation plugin and the
// span log, and is excluded from release builds), starts the app on the pinned
// fixture repositories, drives it with real pointer and keyboard input through
// the automation socket, and reads timings from the app's own perf spans.
//
//	go run ./e2e                       # every scenario on both fixture sizes
//	go run ./e2e -only 'startup|mode' -sizes small
//
// It needs a desktop session: the app opens a window. Results go to the
// terminal and to .perf/e2e/<machine>.json (outside git). The exit status is 1
// when a budget fails.
package main

import (
	"arbor/internal/fixture"
	"arbor/internal/machine"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Env is what scenarios share.
type Env struct {
	Root, Bin, Tmp string
	Runs           int
	Sizes          []fixture.Spec
	Only           *regexp.Regexp
	pristine       map[string]string
}

func (e *Env) logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "e2e: "+format+"\n", a...)
}

// repoKind says which fixture repository a scenario needs.
type repoKind int

const (
	dirtyRepo repoKind = iota // the pristine dirty fixture, shared: read only
	dirtyCopy                 // a private copy of it, for scenarios that change files
	cleanCopy                 // a private copy of the clean fixture, for checkouts
)

// RepoOf returns the repository of a size and kind, building the pristine
// fixtures on first use.
func (e *Env) RepoOf(s fixture.Spec, kind repoKind) (string, error) {
	pristine := func(clean bool) (string, error) {
		name := "pristine-" + s.Name
		if clean {
			name += "-clean"
		}
		if dir, ok := e.pristine[name]; ok {
			return dir, nil
		}
		dir := filepath.Join(e.Tmp, name)
		e.logf("building the %s fixture (%d files)...", s.Name, s.Files())
		build := fixture.BuildDirty
		if clean {
			build = fixture.Build
		}
		if err := build(dir, s); err != nil {
			return "", err
		}
		e.pristine[name] = dir
		return dir, nil
	}
	src, err := pristine(kind == cleanCopy)
	if err != nil || kind == dirtyRepo {
		return src, err
	}
	dst, err := os.MkdirTemp(e.Tmp, "work-"+s.Name+"-")
	if err != nil {
		return "", err
	}
	return dst, fixture.Copy(src, dst)
}

func main() { os.Exit(run()) }

func run() int {
	root := flag.String("root", "", "repository root (default: found from the working directory)")
	app := flag.String("app", "", "use this e2e build of Arbor instead of building one")
	only := flag.String("only", "", "run the scenarios whose name matches this regular expression")
	sizes := flag.String("sizes", "small,large", "fixture sizes to run")
	runs := flag.Int("runs", 5, "repetitions of each measured action")
	out := flag.String("out", "", "write results here (default .perf/e2e/<machine>.json)")
	noFail := flag.Bool("no-fail", false, "report budgets that fail but exit 0")
	keep := flag.Bool("keep", false, "keep the temporary fixtures and print where they are")
	list := flag.Bool("list", false, "list the scenarios and exit")
	materialize := flag.String("fixture", "", "build the dirty fixture `size:dir` (for example small:/tmp/arbor-small) and exit, to try the app by hand")
	flag.Parse()

	if *materialize != "" {
		size, dir, ok := strings.Cut(*materialize, ":")
		spec := map[string]fixture.Spec{"small": fixture.Small, "large": fixture.Large}[size]
		if !ok || spec.Name == "" {
			fmt.Fprintln(os.Stderr, "e2e: -fixture wants small:dir or large:dir")
			return 2
		}
		if err := fixture.BuildDirty(dir, spec); err != nil {
			fmt.Fprintln(os.Stderr, "e2e:", err)
			return 2
		}
		fmt.Println(dir)
		return 0
	}

	if *list {
		for _, s := range scenarios {
			fmt.Printf("%-16s %s\n", s.Name, s.Doc)
		}
		return 0
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		fmt.Fprintln(os.Stderr, "e2e: unsupported OS")
		return 2
	}
	if *root == "" {
		wd, _ := os.Getwd()
		for dir := wd; ; dir = filepath.Dir(dir) {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				*root = dir
				break
			}
			if dir == filepath.Dir(dir) {
				fmt.Fprintln(os.Stderr, "e2e: no go.mod found; pass -root")
				return 2
			}
		}
	}
	env := &Env{Root: *root, Runs: *runs, pristine: map[string]string{}}
	if *only != "" {
		re, err := regexp.Compile(*only)
		if err != nil {
			fmt.Fprintln(os.Stderr, "e2e: -only:", err)
			return 2
		}
		env.Only = re
	}
	for _, n := range strings.Split(*sizes, ",") {
		switch strings.TrimSpace(n) {
		case "small":
			env.Sizes = append(env.Sizes, fixture.Small)
		case "large":
			env.Sizes = append(env.Sizes, fixture.Large)
		default:
			fmt.Fprintf(os.Stderr, "e2e: unknown size %q\n", n)
			return 2
		}
	}
	tmp, err := os.MkdirTemp("", "arbor-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		return 2
	}
	if *keep {
		fmt.Fprintln(os.Stderr, "e2e: keeping", tmp)
	} else {
		defer os.RemoveAll(tmp)
	}
	env.Tmp = tmp

	env.Bin = *app
	if env.Bin == "" {
		env.logf("building the e2e app...")
		if env.Bin, err = buildApp(env.Root, tmp); err != nil {
			fmt.Fprintln(os.Stderr, "e2e:", err)
			return 2
		}
	}

	var metrics []Metric
	for _, sc := range scenarios {
		if env.Only != nil && !env.Only.MatchString(sc.Name) {
			continue
		}
		env.logf("scenario %s", sc.Name)
		t0 := time.Now()
		ms, err := sc.Run(env)
		if err != nil {
			env.logf("scenario %s failed to run: %v", sc.Name, err)
			ms = append(ms, Metric{Scenario: sc.Name, Status: Fail, Note: "did not run: " + firstLine(err.Error())})
		}
		for i := range ms {
			ms[i].Judge()
		}
		metrics = append(metrics, ms...)
		env.logf("scenario %s done in %s", sc.Name, time.Since(t0).Round(time.Millisecond))
	}

	fmt.Println()
	PrintReport(os.Stdout, metrics)
	path := *out
	if path == "" {
		path = filepath.Join(env.Root, ".perf", "e2e", machineName()+".json")
	}
	if err := WriteJSON(path, metrics, machineName()); err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
	} else {
		fmt.Println("results:", path)
	}
	if Failed(metrics) && !*noFail {
		return 1
	}
	return 0
}

func machineName() string { return machine.Detect().Key }

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

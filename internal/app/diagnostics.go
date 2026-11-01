package app

import (
	"arbor/internal/machine"
	"arbor/internal/perfreport"
	"arbor/internal/perfstore"
	"arbor/perf"
	"fmt"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// Performance diagnostics (ARB-5): one switch, off by default, turns on both
// the log of operations and the Performance window. While it is off nothing is
// recorded and no sink is installed. Data only leaves this computer when the
// user exports a report and sends the file themselves: there is no upload code.

const diagnosticsIntro = "Arbor will record how long operations take, on this computer only.\n\n" +
	"Recorded: operation and phase names, durations, whether they succeeded, a repository size bucket and the app version.\n\n" +
	"Never recorded: paths, repository, branch or file names, commit messages, code or Git's error messages.\n\n" +
	"Nothing is sent anywhere. You can export a report and choose to share that file."

// diagOff is what the user chose when turning diagnostics off.
type diagOff int

const (
	diagOffCancel diagOff = iota
	diagOffKeep
	diagOffDelete
)

// diagPrompts asks the user. Tests answer with a stand-in. Callbacks run on the
// UI thread.
type diagPrompts interface {
	ConfirmEnable(done func(ok bool))
	ConfirmDisable(done func(choice diagOff))
	SavePath(name string, done func(path string))
}

// diagnostics is the process-wide state of the switch, owned by the UI thread.
type diagnostics struct {
	settings *settingsStore
	userData string
	version  string
	prompts  diagPrompts
	// newWindow creates the Performance window; menuChanged rebuilds the menu.
	newWindow   func(view func(c *ui.Context)) hostWindow
	menuChanged func()

	store  *perfstore.Store
	window *perfView
}

// buildVersion is what the log files a build's operations under: the packaged
// app's version, else the commit it was built from, so each round of work on the
// code is its own point on the charts. "" when the build has neither, as with go
// run, which does not stamp the commit.
func buildVersion(packaged string) string {
	if packaged != "" {
		return packaged
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return commitVersion(info.Settings)
}

func commitVersion(settings []debug.BuildSetting) string {
	var commit string
	var dirty bool
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			commit = s.Value[:min(len(s.Value), 7)]
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if commit != "" && dirty {
		return commit + "+dirty"
	}
	return commit
}

func newDiagnostics(settings *settingsStore, userData, version string, prompts diagPrompts) *diagnostics {
	if version == "" {
		version = "dev"
	}
	return &diagnostics{settings: settings, userData: userData, version: version, prompts: prompts}
}

func (d *diagnostics) logDir() string { return perfstore.LogDir(d.userData) }

// Enabled reports the switch.
func (d *diagnostics) Enabled() bool { return d.settings.Get().PerformanceDiagnostics }

// Start resumes recording at launch when the user has turned it on. It does not
// touch the sink otherwise: the switch being off is the state it starts in.
func (d *diagnostics) Start() {
	if d.Enabled() {
		d.install()
	}
}

func (d *diagnostics) install() {
	if d.store != nil || d.userData == "" {
		return
	}
	d.store = perfstore.Open(d.logDir(), perfstore.Options{Version: d.version})
	perf.SetSink(d.store.Sink())
}

func (d *diagnostics) uninstall() {
	perf.SetSink(nil)
	if d.store != nil {
		d.store.Close()
		d.store = nil
	}
}

// Close writes what is waiting, at quit.
func (d *diagnostics) Close() { d.uninstall() }

// Toggle is the menu switch: it asks first, in both directions.
func (d *diagnostics) Toggle() {
	if !d.Enabled() {
		d.prompts.ConfirmEnable(func(ok bool) {
			if ok {
				_ = d.settings.Update(func(s *Settings) { s.PerformanceDiagnostics = true })
				d.install()
			}
			d.changed()
		})
		return
	}
	d.prompts.ConfirmDisable(func(choice diagOff) {
		if choice != diagOffCancel {
			_ = d.settings.Update(func(s *Settings) { s.PerformanceDiagnostics = false })
			d.uninstall()
			if d.window != nil {
				d.window.close()
			}
			if choice == diagOffDelete {
				_ = perfstore.Delete(d.logDir())
			}
		}
		d.changed()
	})
}

func (d *diagnostics) changed() {
	if d.menuChanged != nil {
		d.menuChanged()
	}
}

// OpenWindow shows the Performance window. It is only there when the switch is on.
func (d *diagnostics) OpenWindow() *perfView {
	if !d.Enabled() || d.newWindow == nil {
		return nil
	}
	if d.window != nil {
		d.window.win.Show()
		return d.window
	}
	p := newPerfView(d.userData)
	p.flush = func() {
		if d.store != nil {
			d.store.Flush()
		}
	}
	p.exportReport = d.export
	p.saveExport = d.prompts.SavePath
	d.window = p
	p.win = d.newWindow(p.view)
	p.win.OnFocus(p.reload)
	p.win.OnClosed(func() {
		p.closed = true
		if d.window == p {
			d.window = nil
		}
	})
	p.reload()
	return p
}

// export builds the report a user may share. It reads files, so it runs off the
// UI thread.
func (d *diagnostics) export() ([]byte, *perfreport.Export, error) {
	if d.store != nil {
		d.store.Flush()
	}
	m := machine.Detect()
	x, err := perfreport.BuildExport(d.logDir(), runtime.GOOS+"/"+runtime.GOARCH, m.CPU, time.Now())
	if err != nil {
		return nil, nil, err
	}
	data, err := x.Marshal()
	return data, x, err
}

// menuItems are the items of the View menu.
func (d *diagnostics) menuItems() []*mygo.MenuItem {
	items := []*mygo.MenuItem{{
		ID: "view.performanceDiagnostics", Label: "Performance Diagnostics", Type: mygo.MenuItemCheckbox,
		Checked: d.Enabled(), ToolTip: "Record how long operations take, on this computer only",
		Click: func(*mygo.MenuItem, *mygo.Window) { d.Toggle() },
	}}
	if d.Enabled() {
		items = append(items,
			&mygo.MenuItem{ID: "view.performance", Label: "Performance Window", Accelerator: "CmdOrCtrl+Shift+P",
				Click: func(*mygo.MenuItem, *mygo.Window) { d.OpenWindow() }},
			&mygo.MenuItem{ID: "view.exportPerformance", Label: "Export Performance Report…",
				Click: func(*mygo.MenuItem, *mygo.Window) {
					if p := d.OpenWindow(); p != nil {
						p.beginExport()
					}
				}},
		)
	}
	return items
}

// nativePrompts asks with the system's dialogs, which block, off the UI thread.
type nativePrompts struct{}

func (nativePrompts) ConfirmEnable(done func(bool)) {
	go func() {
		res, err := mygo.Dialog.Message(mygo.MessageOptions{
			Title: "Performance Diagnostics", Message: "Turn on performance diagnostics?", Detail: diagnosticsIntro,
			Buttons: []string{"Turn On", "Cancel"}, DefaultButton: 0, CancelButton: 1,
		})
		mygo.RunOnMain(func() { done(err == nil && res.Button == 0) })
	}()
}

func (nativePrompts) ConfirmDisable(done func(diagOff)) {
	go func() {
		res, err := mygo.Dialog.Message(mygo.MessageOptions{
			Title: "Performance Diagnostics", Message: "Turn off performance diagnostics?",
			Detail:  "Recording stops. You can keep what was recorded on this computer or delete it.",
			Buttons: []string{"Keep Data", "Delete Data", "Cancel"}, DefaultButton: 0, CancelButton: 2,
		})
		choice := diagOffCancel
		if err == nil {
			switch res.Button {
			case 0:
				choice = diagOffKeep
			case 1:
				choice = diagOffDelete
			}
		}
		mygo.RunOnMain(func() { done(choice) })
	}()
}

func (nativePrompts) SavePath(name string, done func(string)) {
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: "Export Performance Report", DefaultPath: name,
			Filters: []mygo.FileFilter{{Name: "JSON", Extensions: []string{"json"}}}})
		if err != nil {
			path = ""
		}
		mygo.RunOnMain(func() { done(path) })
	}()
}

// newPerfHostWindow creates the real window.
func newPerfHostWindow(view func(c *ui.Context)) hostWindow {
	return nativeWindow{mygo.NewWindow(mygo.WindowOptions{
		Title: "Performance", Width: 1120, Height: 780, MinWidth: 620, MinHeight: 420,
		StateKey: "arbor-performance", Content: ui.View(view),
	})}
}

func exportFileName(now time.Time) string {
	return fmt.Sprintf("arbor-performance-%s.json", now.Format("2006-01-02"))
}

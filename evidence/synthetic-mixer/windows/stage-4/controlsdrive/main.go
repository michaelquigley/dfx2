//go:build windows

// Command controlsdrive auditions the stage 4 synthetic mixer, examples/mixer
// -controls, with real synthesized mouse and keyboard input: the Windows
// counterpart of evidence/synthetic-mixer/linux/controls_drive.py. It runs the
// fixture, drives it through SendInput and its console, judges the fixture's
// edit and command logs, captures the screen, and writes mixer.jsonl,
// mixer.stderr.log, PNGs and summary.json. It extends the stage 3 driver of the
// same name, which stays as it ran. A separate module, so the root module never
// builds it.
//
// It needs an interactive desktop: a locked session, a disconnected or minimized
// Remote Desktop client, or a target behind an elevated window fails here rather
// than giving a result. Hands off the mouse and keyboard while it runs.
//
//	controlsdrive -fixture mixer.exe -out DIR
//	controlsdrive -release   // sends one left-button-up, after a killed run
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32                         = syscall.NewLazyDLL("user32.dll")
	pSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	pEnumWindows                   = user32.NewProc("EnumWindows")
	pGetWindowThreadProcessId      = user32.NewProc("GetWindowThreadProcessId")
	pGetWindowTextW                = user32.NewProc("GetWindowTextW")
	pSetWindowPos                  = user32.NewProc("SetWindowPos")
	pGetWindowRect                 = user32.NewProc("GetWindowRect")
	pClientToScreen                = user32.NewProc("ClientToScreen")
	pGetDpiForWindow               = user32.NewProc("GetDpiForWindow")
	pSetForegroundWindow           = user32.NewProc("SetForegroundWindow")
	pGetForegroundWindow           = user32.NewProc("GetForegroundWindow")
	pSetCursorPos                  = user32.NewProc("SetCursorPos")
	pGetCursorPos                  = user32.NewProc("GetCursorPos")
	pSendInput                     = user32.NewProc("SendInput")
)

const (
	inputMouse, inputKeyboard = 0, 1
	mouseLeftDown             = 0x0002
	mouseLeftUp               = 0x0004
	keyEventKeyUp             = 0x0002
	swpNoSize                 = 0x0001
	swpNoZOrder               = 0x0004

	vkTab, vkShift, vkSpace        = 0x09, 0x10, 0x20
	vkPrior, vkNext, vkEnd, vkHome = 0x21, 0x22, 0x23, 0x24
	vkLeft, vkUp, vkRight, vkDown  = 0x25, 0x26, 0x27, 0x28
	vkLetterA                      = 0x41
)

// Logical layout of -controls: strips 110 wide and 12 apart, each a fader and
// then a checkbox.
const (
	stripX, stripStep = 16.0, 122.0
	faderX, faderY    = 18.0, 16.0
	faderH, thumb     = 160.0, 12.0
	checkX, checkY    = 24.0, 195.0
	travel            = faderH - thumb
)

type rect struct{ left, top, right, bottom int32 }
type point struct{ x, y int32 }

// mouseInput is an INPUT holding a MOUSEINPUT on Windows x64: 40 bytes.
type mouseInput struct {
	kind, _                uint32
	dx, dy                 int32
	data, flags, timestamp uint32
	extra                  uintptr
}

// keyInput is an INPUT holding a KEYBDINPUT on Windows x64, padded to 40 bytes.
type keyInput struct {
	kind, _     uint32
	vk, scan    uint16
	flags, time uint32
	_           uint32
	extra       uintptr
	_           [8]byte
}

var _ [40]byte = [unsafe.Sizeof(keyInput{})]byte{}
var _ [40]byte = [unsafe.Sizeof(mouseInput{})]byte{}

type event map[string]any

func (e event) name() string            { s, _ := e["event"].(string); return s }
func (e event) text(k string) string    { s, _ := e[k].(string); return s }
func (e event) number(k string) float64 { f, _ := e[k].(float64); return f }

type driver struct {
	cmd     *exec.Cmd
	stdin   *bufio.Writer
	pid     uint32
	mu      sync.Mutex
	all     []event
	cond    *sync.Cond
	eof     bool
	cursor  int
	windows map[string]uintptr
	out     string
	detail  map[string]any // the running case's observations, for the summary
}

func main() { os.Exit(run()) }

func run() int {
	fixture := flag.String("fixture", "", "path to the mixer executable")
	out := flag.String("out", ".", "directory for logs and the summary")
	release := flag.Bool("release", false, "send one left-button-up and exit")
	text := flag.Bool("text", true, "run the separate text-cache auditions after the controls cases")
	flag.Parse()
	if *release {
		_ = button(mouseLeftUp)
		return 0
	}
	if *fixture == "" {
		fmt.Fprintln(os.Stderr, "controlsdrive: -fixture is required")
		return 2
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	pSetProcessDpiAwarenessContext.Call(^uintptr(3)) // physical coordinates, as the fixture uses
	d, err := start(*fixture, *out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	summary := map[string]any{"cases": []any{}}
	failed := d.audition(summary)
	d.finish(summary)
	if failed != nil {
		summary["failure"] = failed.Error()
	}
	passed := failed == nil && summary["clean"] == true
	if passed && *text {
		for _, budget := range []string{"small", "default"} {
			result := textAudition(*fixture, *out, budget)
			summary["text"] = append(summaryList(summary["text"]), result)
			if result["passed"] != true {
				passed = false
			}
		}
	}
	summary["passed"] = passed
	data, _ := json.MarshalIndent(summary, "", "  ")
	if err := os.WriteFile(filepath.Join(*out, "summary.json"), append(data, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	fmt.Println(string(data))
	if !passed {
		return 1
	}
	return 0
}

func start(fixture, out string, extra ...string) (*driver, error) {
	cmd := exec.Command(fixture, append([]string{"-controls", "-paused", "-snapshots", out}, extra...)...)
	stderr, err := os.Create(filepath.Join(out, "mixer.stderr.log"))
	if err != nil {
		return nil, err
	}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	log, err := os.Create(filepath.Join(out, "mixer.jsonl"))
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	d := &driver{cmd: cmd, stdin: bufio.NewWriter(stdin), pid: uint32(cmd.Process.Pid), windows: map[string]uintptr{}, out: out}
	d.cond = sync.NewCond(&d.mu)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			log.Write(append(append([]byte(nil), line...), '\n'))
			var e event
			if json.Unmarshal(line, &e) != nil {
				continue
			}
			d.mu.Lock()
			d.all = append(d.all, e)
			d.cond.Broadcast()
			d.mu.Unlock()
		}
		d.mu.Lock()
		d.eof = true
		d.cond.Broadcast()
		d.mu.Unlock()
	}()
	return d, nil
}

func (d *driver) command(line string) {
	d.stdin.WriteString(line + "\n")
	d.stdin.Flush()
}

// expect waits for the next matching event after those already consumed.
func (d *driver) expect(name string, timeout time.Duration, match func(event) bool) (event, error) {
	deadline := time.Now().Add(timeout)
	timer := time.AfterFunc(timeout, func() { d.mu.Lock(); d.cond.Broadcast(); d.mu.Unlock() })
	defer timer.Stop()
	d.mu.Lock()
	defer d.mu.Unlock()
	for {
		for d.cursor < len(d.all) {
			e := d.all[d.cursor]
			d.cursor++
			if e.name() == name && (match == nil || match(e)) {
				return e, nil
			}
		}
		if d.eof {
			return nil, fmt.Errorf("fixture exited waiting for %s", name)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for %s", name)
		}
		d.cond.Wait()
	}
}

func (d *driver) status() (event, error) {
	d.command("status")
	return d.expect("status", 15*time.Second, nil)
}

func (d *driver) external(line string) error {
	d.command(line)
	_, err := d.expect("external", 15*time.Second, func(e event) bool { return e.text("command") == line })
	return err
}

func (d *driver) edits() []event {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []event
	for _, e := range d.all {
		if e.name() == "edit" {
			out = append(out, e)
		}
	}
	return out
}

// settle waits until the fixture's input queue is empty, and every accepted
// command resolved, twice in a row. Windows delivers synthesized input through
// the system queue, so a short pause guards what no counter can show yet; the
// timing only bounds a hang.
func (d *driver) settle() error { return d.settleWith(true, 10*time.Second) }

func (d *driver) settleWith(commands bool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	quiet := 0
	for quiet < 2 {
		if time.Now().After(deadline) {
			if commands {
				return errors.New("input queue never drained or commands stayed outstanding")
			}
			return errors.New("input queue never drained")
		}
		time.Sleep(50 * time.Millisecond)
		s, err := d.status()
		if err != nil {
			return err
		}
		if s.number("input_queued") == 0 && (!commands || s.number("commands_outstanding") == 0) {
			quiet++
		} else {
			quiet = 0
		}
	}
	time.Sleep(100 * time.Millisecond)
	return nil
}

func (d *driver) find(name string) (uintptr, error) {
	if hwnd := d.windows[name]; hwnd != 0 {
		return hwnd, nil
	}
	prefix := "dfx2 mixer " + name
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var found uintptr
		callback := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
			var pid uint32
			pGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
			if pid != d.pid {
				return 1
			}
			buffer := make([]uint16, 256)
			n, _, _ := pGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buffer[0])), 256)
			if strings.HasPrefix(syscall.UTF16ToString(buffer[:n]), prefix) {
				found = hwnd
				return 0
			}
			return 1
		})
		pEnumWindows.Call(callback, 0)
		if found != 0 {
			d.windows[name] = found
			return found, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0, fmt.Errorf("no window titled %q", prefix)
}

func windowRect(hwnd uintptr) rect {
	var r rect
	pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r
}

func scaleOf(hwnd uintptr) float64 {
	dpi, _, _ := pGetDpiForWindow.Call(hwnd)
	if dpi == 0 {
		return 1
	}
	return float64(dpi) / 96
}

// at converts a logical client coordinate to the screen.
func at(hwnd uintptr, x, y float64) point {
	var origin point
	pClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&origin)))
	s := scaleOf(hwnd)
	return point{origin.x + int32(math.Round(x*s)), origin.y + int32(math.Round(y*s))}
}

func fader(hwnd uintptr, strip int, value float64) point {
	return at(hwnd, stripX+float64(strip)*stripStep+faderX, faderY+thumb/2+(1-value)*travel)
}

func checkbox(hwnd uintptr, strip int) point {
	return at(hwnd, stripX+float64(strip)*stripStep+checkX, checkY)
}

func button(flags uint32) error {
	input := mouseInput{kind: inputMouse, flags: flags}
	n, _, err := pSendInput.Call(1, uintptr(unsafe.Pointer(&input)), unsafe.Sizeof(input))
	if n != 1 {
		return fmt.Errorf("SendInput: %w (a locked or disconnected session cannot inject input)", err)
	}
	return nil
}

func key(vk uint16, up bool) error {
	input := keyInput{kind: inputKeyboard, vk: vk}
	if up {
		input.flags = keyEventKeyUp
	}
	n, _, err := pSendInput.Call(1, uintptr(unsafe.Pointer(&input)), unsafe.Sizeof(input))
	if n != 1 {
		return fmt.Errorf("SendInput: %w", err)
	}
	return nil
}

func tap(vks ...uint16) error {
	for _, vk := range vks {
		if err := key(vk, false); err != nil {
			return err
		}
		if err := key(vk, true); err != nil {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

func moveTo(p point) error {
	if ok, _, err := pSetCursorPos.Call(uintptr(p.x), uintptr(p.y)); ok == 0 {
		return fmt.Errorf("SetCursorPos: %w", err)
	}
	time.Sleep(15 * time.Millisecond)
	return nil
}

func press(p point) error {
	if err := moveTo(p); err != nil {
		return err
	}
	return button(mouseLeftDown)
}

func release() error { return button(mouseLeftUp) }

func click(p point) error {
	if err := press(p); err != nil {
		return err
	}
	return release()
}

// activate makes the window foreground, which a process that just sent input may
// do. A window that does not become foreground fails the case, rather than
// sending keys to whatever is focused.
func (d *driver) activate(name string) error {
	hwnd, err := d.find(name)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pSetForegroundWindow.Call(hwnd)
		time.Sleep(50 * time.Millisecond)
		if now, _, _ := pGetForegroundWindow.Call(); now == hwnd {
			return d.settle()
		}
		// Windows grants foreground changes to the process that sent the last
		// input, and a harmless key is the smallest input to send.
		tap(vkShift)
	}
	return fmt.Errorf("window %s never became foreground", name)
}

func editOf(e event) string {
	return fmt.Sprintf("%s/%s/%s=%v", e.text("window"), e.text("channel"), e.text("control"), e["value"])
}

func (d *driver) since(mark int) []event { return d.edits()[mark:] }

func (d *driver) mark() (int, error) {
	if err := d.settle(); err != nil {
		return 0, err
	}
	return len(d.edits()), nil
}

func require(ok bool, format string, args ...any) error {
	if !ok {
		return fmt.Errorf(format, args...)
	}
	return nil
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.02 }

func (d *driver) audition(summary map[string]any) error {
	if _, err := d.expect("opened", 60*time.Second, func(e event) bool { return e.text("window") == "A" }); err != nil {
		return err
	}
	if _, err := d.expect("opened", 15*time.Second, func(e event) bool { return e.text("window") == "B" }); err != nil {
		return err
	}
	a, err := d.find("A")
	if err != nil {
		return err
	}
	b, err := d.find("B")
	if err != nil {
		return err
	}
	pSetWindowPos.Call(a, 0, 40, 60, 0, 0, swpNoSize|swpNoZOrder)
	r := windowRect(a)
	pSetWindowPos.Call(b, 0, uintptr(r.right+40), 60, 0, 0, swpNoSize|swpNoZOrder)
	if err := d.settle(); err != nil {
		return err
	}
	first, err := d.status()
	if err != nil {
		return err
	}
	summary["first_surface"] = first["first_surface"]
	var cases []any
	defer func() { summary["cases"] = cases }()
	for _, c := range []struct {
		name string
		run  func(a, b uintptr) error
	}{
		{"drag_outside_window", d.dragOutside}, {"checkbox", d.checkboxCase}, {"keyboard", d.keyboard},
		{"independent_focus", d.independentFocus}, {"external_during_drag", d.externalDuringDrag},
		{"reorder_during_drag", d.reorderDuringDrag}, {"remove_during_drag", d.removeDuringDrag},
		{"focus_loss_during_drag", d.focusLossDuringDrag}, {"hover_is_idle", d.hoverIsIdle},
		{"delayed_commands", d.delayedCommands}, {"rejected_commands", d.rejectedCommands},
		{"overloaded_commands", d.overloadedCommands}, {"external_while_pending", d.externalWhilePending},
		{"timing", d.timing}, {"close_first_window", d.closeFirstWindow},
		{"close_during_drag", d.closeDuringDrag}, {"shutdown_outstanding", d.shutdownOutstanding},
	} {
		began := time.Now()
		d.detail = nil
		err := c.run(a, b)
		record := map[string]any{"case": c.name, "ok": err == nil, "seconds": time.Since(began).Seconds()}
		for k, v := range d.detail {
			record[k] = v
		}
		if err != nil {
			record["error"] = err.Error()
			cases = append(cases, record)
			release()
			return fmt.Errorf("%s: %w", c.name, err)
		}
		cases = append(cases, record)
	}
	return nil
}

func (d *driver) finish(summary map[string]any) {
	release()
	drained, err := d.drained()
	if drained == nil {
		d.command("quit")
		drained, err = d.expect("drained", 30*time.Second, nil)
	}
	code := -1
	if err == nil {
		waited := make(chan error, 1)
		go func() { waited <- d.cmd.Wait() }()
		select {
		case waitErr := <-waited:
			code = 0
			var exit *exec.ExitError
			if errors.As(waitErr, &exit) {
				code = exit.ExitCode()
			}
		case <-time.After(20 * time.Second):
			err = errors.New("fixture did not exit")
		}
	}
	if err != nil {
		d.cmd.Process.Kill()
		summary["finish_error"] = err.Error()
		summary["clean"] = false
		return
	}
	d.mu.Lock()
	var returned string
	conserved := false
	for _, e := range d.all {
		if e.name() == "return" {
			returned = e.text("error")
			conserved, _ = e["commands_conserved"].(bool)
			summary["commands"] = e["commands"]
		}
	}
	summary["edits"] = 0
	for _, e := range d.all {
		if e.name() == "edit" {
			summary["edits"] = summary["edits"].(int) + 1
		}
	}
	d.mu.Unlock()
	problems := []string{}
	for _, field := range []string{"windows", "tasks", "native_completions", "queued", "input_queued", "native_windows", "native_retired", "dispatch_targets", "paint_failures"} {
		if drained.number(field) != 0 {
			problems = append(problems, field)
		}
	}
	stderr, _ := os.ReadFile(filepath.Join(d.out, "mixer.stderr.log"))
	summary["exit"], summary["returned"], summary["ownership_problems"] = code, returned, problems
	summary["commands_conserved"], summary["stderr"] = conserved, string(stderr)
	summary["clean"] = code == 0 && returned == "<nil>" && len(problems) == 0 && conserved && len(stderr) == 0
}

// drained returns the drained record if the fixture already quit, as the
// shutdown case does.
func (d *driver) drained() (event, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, e := range d.all {
		if e.name() == "drained" {
			return e, nil
		}
	}
	return nil, nil
}

func (d *driver) dragOutside(a, _ uintptr) error {
	if err := d.activate("A"); err != nil {
		return err
	}
	if err := d.external("gain a 0.5"); err != nil {
		return err
	}
	mark, err := d.mark()
	if err != nil {
		return err
	}
	if err := press(fader(a, 0, 0.5)); err != nil {
		return err
	}
	defer release()
	if err := d.settle(); err != nil {
		return err
	}
	if edits := d.since(mark); len(edits) != 0 {
		return fmt.Errorf("a press at the current value edited: %v", edits)
	}
	for _, value := range []float64{0.6, 0.7, 0.8} {
		if err := moveTo(fader(a, 0, value)); err != nil {
			return err
		}
		if err := d.settle(); err != nil {
			return err
		}
	}
	edits := d.since(mark)
	if err := require(len(edits) >= 3 && near(edits[len(edits)-1].number("value"), 0.8), "drag did not follow the pointer: %v", edits); err != nil {
		return err
	}
	r := windowRect(a)
	if err := moveTo(point{r.right + 100, max(r.top-40, 0)}); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	edits = d.since(mark)
	if err := require(edits[len(edits)-1].number("value") == 1, "capture did not track above and outside the window: %v", edits); err != nil {
		return err
	}
	if err := moveTo(point{r.right + 100, r.bottom + 100}); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	edits = d.since(mark)
	if err := require(edits[len(edits)-1].number("value") == 0, "capture did not clamp below the window: %v", edits); err != nil {
		return err
	}
	count := len(edits)
	if err := release(); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	if edits = d.since(mark); len(edits) != count {
		return fmt.Errorf("a release outside at the last value added %v", edits[count:])
	}
	mark = len(d.edits())
	if err := moveTo(fader(a, 0, 0.9)); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	return require(len(d.since(mark)) == 0, "motion after an outside release edited")
}

func (d *driver) checkboxCase(a, _ uintptr) error {
	if err := d.activate("A"); err != nil {
		return err
	}
	if err := d.external("mute a off"); err != nil {
		return err
	}
	mark, err := d.mark()
	if err != nil {
		return err
	}
	point0 := checkbox(a, 0)
	if err := press(point0); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	if err := require(len(d.since(mark)) == 0, "a checkbox activated on press"); err != nil {
		return err
	}
	if err := release(); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	edits := d.since(mark)
	if err := require(len(edits) == 1 && edits[0]["value"] == true, "release inside: %v", edits); err != nil {
		return err
	}
	mark = len(d.edits())
	if err := press(point0); err != nil {
		return err
	}
	if err := moveTo(point{point0.x + 800, point0.y + 300}); err != nil {
		return err
	}
	if err := release(); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	return require(len(d.since(mark)) == 0, "a release outside activated: %v", d.since(mark))
}

func (d *driver) keyboard(a, _ uintptr) error {
	if err := d.activate("A"); err != nil {
		return err
	}
	for _, line := range []string{"gain a 0.5", "mute a off"} {
		if err := d.external(line); err != nil {
			return err
		}
	}
	d.command("snapshot controls-before")
	if _, err := d.expect("snapshot", 15*time.Second, nil); err != nil {
		return err
	}
	if err := click(fader(a, 0, 0.5)); err != nil {
		return err
	}
	mark, err := d.mark()
	if err != nil {
		return err
	}
	for _, vk := range []uint16{vkUp, vkUp, vkRight, vkDown, vkLeft, vkPrior, vkNext, vkHome, vkHome, vkDown, vkEnd} {
		if err := tap(vk); err != nil {
			return err
		}
		if err := d.settle(); err != nil {
			return err
		}
	}
	var values []float64
	for _, e := range d.since(mark) {
		values = append(values, e.number("value"))
	}
	want := []float64{0.51, 0.52, 0.53, 0.52, 0.51, 0.61, 0.51, 0, 1}
	if fmt.Sprint(values) != fmt.Sprint(want) {
		return fmt.Errorf("fader keys gave %v, want %v", values, want)
	}
	mark = len(d.edits())
	if err := tap(vkTab); err != nil {
		return err
	}
	if err := key(vkSpace, false); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	if err := require(len(d.since(mark)) == 0, "Space activated on press"); err != nil {
		return err
	}
	if err := key(vkSpace, true); err != nil {
		return err
	}
	if err := tap(vkTab, vkUp); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	edits := d.since(mark)
	if err := require(len(edits) == 2 && edits[0].text("control") == "mute" && edits[0].text("channel") == "a" &&
		edits[1].text("control") == "gain" && edits[1].text("channel") == "b", "Tab traversal: %v", edits); err != nil {
		return err
	}
	mark = len(d.edits())
	if err := key(vkShift, false); err != nil {
		return err
	}
	if err := tap(vkTab); err != nil {
		return err
	}
	if err := key(vkShift, true); err != nil {
		return err
	}
	if err := tap(vkSpace); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	edits = d.since(mark)
	if err := require(len(edits) == 1 && edits[0].text("channel") == "a" && edits[0].text("control") == "mute", "Shift+Tab: %v", edits); err != nil {
		return err
	}
	mark = len(d.edits())
	if err := tap(vkLetterA, vkLetterA, vkLetterA); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	if err := require(len(d.since(mark)) == 0, "an unused key edited"); err != nil {
		return err
	}
	d.command("snapshot controls-focused")
	_, err = d.expect("snapshot", 15*time.Second, nil)
	return err
}

func (d *driver) independentFocus(a, b uintptr) error {
	if err := d.activate("A"); err != nil {
		return err
	}
	if err := click(fader(a, 0, 0.5)); err != nil {
		return err
	}
	if err := d.activate("B"); err != nil {
		return err
	}
	if err := click(fader(b, 2, 0.5)); err != nil {
		return err
	}
	mark, err := d.mark()
	if err != nil {
		return err
	}
	if err := tap(vkUp); err != nil {
		return err
	}
	if err := d.activate("A"); err != nil {
		return err
	}
	if err := tap(vkUp); err != nil {
		return err
	}
	if err := d.activate("B"); err != nil {
		return err
	}
	if err := tap(vkUp); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	var seen []string
	for _, e := range d.since(mark) {
		seen = append(seen, e.text("window")+"/"+e.text("channel"))
	}
	if want := []string{"B/c", "A/a", "B/c"}; fmt.Sprint(seen) != fmt.Sprint(want) {
		return fmt.Errorf("focus leaked between windows: %v, want %v", seen, want)
	}
	return nil
}

func (d *driver) externalDuringDrag(a, _ uintptr) error {
	if err := d.activate("A"); err != nil {
		return err
	}
	if err := d.external("gain b 0.5"); err != nil {
		return err
	}
	mark, err := d.mark()
	if err != nil {
		return err
	}
	for _, line := range []string{"gain b 0.9", "mute b on"} {
		if err := d.external(line); err != nil {
			return err
		}
	}
	if err := d.settle(); err != nil {
		return err
	}
	if edits := d.since(mark); len(edits) != 0 {
		return fmt.Errorf("an external change echoed: %v", edits)
	}
	if err := press(fader(a, 1, 0.9)); err != nil {
		return err
	}
	defer release()
	if err := d.settle(); err != nil {
		return err
	}
	base := len(d.since(mark))
	for _, line := range []string{"gain b 0.1", "gain b 0.3"} {
		if err := d.external(line); err != nil {
			return err
		}
	}
	if err := d.settle(); err != nil {
		return err
	}
	if edits := d.since(mark); len(edits) != base {
		return fmt.Errorf("an external change during a drag echoed: %v", edits)
	}
	if err := moveTo(fader(a, 1, 0.6)); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	edits := d.since(mark)
	return require(len(edits) == base+1 && near(edits[len(edits)-1].number("value"), 0.6), "the gesture lost its value: %v", edits)
}

func (d *driver) reorderDuringDrag(a, _ uintptr) error {
	if err := d.activate("A"); err != nil {
		return err
	}
	if err := d.external("gain a 0.5"); err != nil {
		return err
	}
	mark, err := d.mark()
	if err != nil {
		return err
	}
	if err := press(fader(a, 0, 0.8)); err != nil {
		return err
	}
	defer release()
	if err := d.settle(); err != nil {
		return err
	}
	if err := d.external("reorder"); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	if err := moveTo(fader(a, 0, 0.3)); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	if err := release(); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	edits := d.since(mark)
	for _, e := range edits {
		if e.text("channel") != "a" {
			return fmt.Errorf("a reorder retargeted the gesture: %v", edits)
		}
	}
	if len(edits) == 0 {
		return errors.New("no edit")
	}
	if err := d.external("reorder"); err != nil {
		return err
	}
	mark, err = d.mark()
	if err != nil {
		return err
	}
	if err := click(fader(a, 0, 0.2)); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	edits = d.since(mark)
	return require(len(edits) > 0 && edits[0].text("channel") == "a", "press after reordering back: %v", edits)
}

func (d *driver) removeDuringDrag(a, _ uintptr) error {
	if err := d.activate("A"); err != nil {
		return err
	}
	if err := d.external("gain a 0.5"); err != nil {
		return err
	}
	mark, err := d.mark()
	if err != nil {
		return err
	}
	if err := press(fader(a, 0, 0.7)); err != nil {
		return err
	}
	defer release()
	if err := d.settle(); err != nil {
		return err
	}
	count := len(d.since(mark))
	if err := d.external("remove a"); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	if err := moveTo(fader(a, 0, 0.2)); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	if err := release(); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	if edits := d.since(mark); len(edits) != count {
		return fmt.Errorf("a removed target edited: %v", edits)
	}
	if err := d.external("insert a"); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	mark = len(d.edits())
	if err := moveTo(fader(a, 5, 0.2)); err != nil { // back as the last of six strips
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	if err := require(len(d.since(mark)) == 0, "the reinserted fader inherited the gesture"); err != nil {
		return err
	}
	if err := click(fader(a, 5, 0.2)); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	edits := d.since(mark)
	if err := require(len(edits) > 0 && edits[0].text("channel") == "a", "the reinserted fader is not interactive: %v", edits); err != nil {
		return err
	}
	return d.external("order a b c d e f")
}

func (d *driver) focusLossDuringDrag(a, _ uintptr) error {
	if err := d.activate("A"); err != nil {
		return err
	}
	if err := d.external("gain b 0.5"); err != nil {
		return err
	}
	mark, err := d.mark()
	if err != nil {
		return err
	}
	if err := press(fader(a, 1, 0.8)); err != nil {
		return err
	}
	defer release()
	if err := d.settle(); err != nil {
		return err
	}
	count := len(d.since(mark))
	if err := d.activate("B"); err != nil {
		return err
	}
	if err := moveTo(fader(a, 1, 0.1)); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	if err := release(); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	if edits := d.since(mark); len(edits) != count {
		return fmt.Errorf("a drag survived focus loss: %v", edits)
	}
	if err := d.activate("A"); err != nil {
		return err
	}
	mark = len(d.edits())
	if err := click(fader(a, 1, 0.2)); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	return require(len(d.since(mark)) > 0, "no press after focus loss")
}

func (d *driver) hoverIsIdle(a, _ uintptr) error {
	if err := d.activate("A"); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	before, err := d.status()
	if err != nil {
		return err
	}
	for _, p := range [][2]float64{{4, 4}, {60, 60}, {120, 60}, {200, 40}, {300, 90}} {
		if err := moveTo(at(a, p[0], p[1])); err != nil {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := tap(vkLetterA); err != nil {
		return err
	}
	time.Sleep(500 * time.Millisecond)
	after, err := d.status()
	if err != nil {
		return err
	}
	if after.number("frames") != before.number("frames") {
		return fmt.Errorf("hover or an unused key caused %v frames", after.number("frames")-before.number("frames"))
	}
	time.Sleep(3 * time.Second)
	quiet, err := d.status()
	if err != nil {
		return err
	}
	for _, field := range []string{"frames", "frame_timers", "presents", "evaluations"} {
		if quiet.number(field) != after.number(field) {
			return fmt.Errorf("settled controls did not go idle: %s %v -> %v", field, after.number(field), quiet.number(field))
		}
	}
	return nil
}

// closeDuringDrag closes window B mid-drag by name; window C, opened by the
// previous case, keeps responding.
func (d *driver) closeDuringDrag(_, b uintptr) error {
	if err := d.activate("B"); err != nil {
		return err
	}
	if err := press(fader(b, 2, 0.5)); err != nil {
		return err
	}
	defer release()
	if err := d.settle(); err != nil {
		return err
	}
	d.command("close B")
	if _, err := d.expect("closed", 15*time.Second, func(e event) bool { return e.text("window") == "B" }); err != nil {
		return err
	}
	if err := release(); err != nil {
		return err
	}
	c, err := d.find("C")
	if err != nil {
		return err
	}
	if err := d.activate("C"); err != nil {
		return err
	}
	mark, err := d.mark()
	if err != nil {
		return err
	}
	if err := click(fader(c, 2, 0.9)); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	return require(len(d.since(mark)) > 0, "the surviving window stopped responding")
}

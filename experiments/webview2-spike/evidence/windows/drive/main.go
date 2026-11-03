//go:build windows

// Command drive is the spike's scripted audition driver. It launches the
// real executable, drives its real window through Win32 (WM_CLOSE,
// SetWindowPos, minimize/restore), captures the screen, inspects the
// process's sockets, and times exits. It reads the host's log; it never
// reaches inside the process.
//
// What it records is automated evidence. It cannot type, tab or scroll
// inside the webview (those need a person), and it judges no pixels: the
// screenshots are for a person to look at.
//
//	go build -o drive.exe ./evidence/windows/drive
//	.\drive.exe -exe .\dfx2-webview2-spike.exe -out evidence\windows\run
package main

import (
	"bufio"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")

	pEnumWindows              = user32.NewProc("EnumWindows")
	pGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	pGetClassNameW            = user32.NewProc("GetClassNameW")
	pPostMessageW             = user32.NewProc("PostMessageW")
	pSetWindowPos             = user32.NewProc("SetWindowPos")
	pShowWindow               = user32.NewProc("ShowWindow")
	pGetWindowRect            = user32.NewProc("GetWindowRect")
	pSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	pGetDC                    = user32.NewProc("GetDC")
	pReleaseDC                = user32.NewProc("ReleaseDC")
	pIsWindowVisible          = user32.NewProc("IsWindowVisible")
	pCreateCompatibleDC       = gdi32.NewProc("CreateCompatibleDC")
	pCreateCompatibleBitmap   = gdi32.NewProc("CreateCompatibleBitmap")
	pSelectObject             = gdi32.NewProc("SelectObject")
	pBitBlt                   = gdi32.NewProc("BitBlt")
	pGetDIBits                = gdi32.NewProc("GetDIBits")
	pDeleteObject             = gdi32.NewProc("DeleteObject")
	pDeleteDC                 = gdi32.NewProc("DeleteDC")
	pVirtualAlloc             = syscall.NewLazyDLL("kernel32.dll").NewProc("VirtualAlloc")
	pVirtualFree              = syscall.NewLazyDLL("kernel32.dll").NewProc("VirtualFree")
)

const (
	wmClose      = 0x0010
	swMinimize   = 6
	swRestore    = 9
	swpNoMove    = 0x0002
	swpNoZOrder  = 0x0004
	srcCopy      = 0x00CC0020
	captureBlt   = 0x40000000
	dibRGBColors = 0
	windowClass  = "dfx2WebView2Spike"
	defaultWait  = 20 * time.Second
)

type rect struct{ left, top, right, bottom int32 }

var (
	exe     = flag.String("exe", "dfx2-webview2-spike.exe", "the spike executable")
	out     = flag.String("out", "evidence/windows/run", "directory for logs, captures and summary")
	modes   = flag.String("modes", "loopback,inprocess", "modes to drive")
	only    = flag.String("scenarios", "", "comma-separated scenarios (default: all)")
	repeats = flag.Int("repeat", 5, "launch/close repetitions for the repeat scenario")
)

// run is one launch of the executable.
type run struct {
	name  string
	cmd   *exec.Cmd
	start time.Time
	mu    sync.Mutex
	lines []string
	cond  *sync.Cond
	done  chan struct{}
	exit  int
	hwnd  uintptr
}

var summary []string

func note(format string, args ...any) {
	s := fmt.Sprintf(format, args...)
	fmt.Println(s)
	summary = append(summary, s)
}

func launch(name string, args ...string) *run {
	r := &run{name: name, done: make(chan struct{})}
	r.cond = sync.NewCond(&r.mu)
	r.cmd = exec.Command(*exe, args...)
	// Its own process group: a Ctrl+C in the driver's console never reaches
	// the host, and consoleBreak can signal the host alone.
	r.cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	stderr, err := r.cmd.StderrPipe()
	if err != nil {
		panic(err)
	}
	logf, err := os.Create(filepath.Join(*out, name+".log"))
	if err != nil {
		panic(err)
	}
	fmt.Fprintf(logf, "# %s %s\n", filepath.Base(*exe), strings.Join(args, " "))
	r.start = time.Now()
	if err := r.cmd.Start(); err != nil {
		panic(err)
	}
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			fmt.Fprintln(logf, sc.Text())
			r.mu.Lock()
			r.lines = append(r.lines, sc.Text())
			r.cond.Broadcast()
			r.mu.Unlock()
		}
		err := r.cmd.Wait()
		r.mu.Lock()
		r.exit = 0
		if ee, ok := err.(*exec.ExitError); ok {
			r.exit = ee.ExitCode()
		}
		r.mu.Unlock()
		logf.Close()
		close(r.done)
		r.mu.Lock()
		r.cond.Broadcast()
		r.mu.Unlock()
	}()
	return r
}

// waitLine waits for a log line matching re and returns it.
func (r *run) waitLine(re string, timeout time.Duration) (string, bool) {
	rx := regexp.MustCompile(re)
	deadline := time.Now().Add(timeout)
	go func() { time.Sleep(timeout); r.mu.Lock(); r.cond.Broadcast(); r.mu.Unlock() }()
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := 0
	for {
		for ; seen < len(r.lines); seen++ {
			if rx.MatchString(r.lines[seen]) {
				return r.lines[seen], true
			}
		}
		select {
		case <-r.done:
			return "", false
		default:
		}
		if time.Now().After(deadline) {
			return "", false
		}
		r.cond.Wait()
	}
}

func (r *run) grep(re string) []string {
	rx := regexp.MustCompile(re)
	r.mu.Lock()
	defer r.mu.Unlock()
	var m []string
	for _, l := range r.lines {
		if rx.MatchString(l) {
			m = append(m, l)
		}
	}
	return m
}

// window finds the run's top-level window, polling every millisecond.
func (r *run) window(timeout time.Duration) uintptr {
	deadline := time.Now().Add(timeout)
	pid := uint32(r.cmd.Process.Pid)
	for time.Now().Before(deadline) {
		if h := findWindow(pid); h != 0 {
			r.hwnd = h
			return h
		}
		time.Sleep(time.Millisecond)
	}
	return 0
}

var (
	enumOnce sync.Once
	enumCB   uintptr
	enumPID  uint32
	enumHit  uintptr
)

func findWindow(pid uint32) uintptr {
	enumOnce.Do(func() {
		enumCB = syscall.NewCallback(func(h, _ uintptr) uintptr {
			var p uint32
			pGetWindowThreadProcessId.Call(h, uintptr(unsafe.Pointer(&p)))
			if p != enumPID {
				return 1
			}
			var buf [64]uint16
			pGetClassNameW.Call(h, uintptr(unsafe.Pointer(&buf[0])), 64)
			if syscall.UTF16ToString(buf[:]) == windowClass {
				enumHit = h
				return 0
			}
			return 1
		})
	})
	enumPID, enumHit = pid, 0
	pEnumWindows.Call(enumCB, 0)
	return enumHit
}

// close posts WM_CLOSE and returns how long the process took to exit.
func (r *run) close(timeout time.Duration) (time.Duration, bool) {
	t := time.Now()
	pPostMessageW.Call(r.hwnd, wmClose, 0, 0)
	select {
	case <-r.done:
		return time.Since(t), true
	case <-time.After(timeout):
		r.cmd.Process.Kill()
		<-r.done
		return time.Since(t), false
	}
}

func (r *run) exitCode() int {
	<-r.done
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.exit
}

func capture(hwnd uintptr, name string) {
	pSetForegroundWindow.Call(hwnd)
	time.Sleep(400 * time.Millisecond)
	var wr rect
	pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
	w, h := int(wr.right-wr.left), int(wr.bottom-wr.top)
	if w <= 0 || h <= 0 {
		note("  capture %s: window has no area", name)
		return
	}
	screen, _, _ := pGetDC.Call(0)
	defer pReleaseDC.Call(0, screen)
	mem, _, _ := pCreateCompatibleDC.Call(screen)
	defer pDeleteDC.Call(mem)
	bmp, _, _ := pCreateCompatibleBitmap.Call(screen, uintptr(w), uintptr(h))
	defer pDeleteObject.Call(bmp)
	old, _, _ := pSelectObject.Call(mem, bmp)
	pBitBlt.Call(mem, 0, 0, uintptr(w), uintptr(h), screen, uintptr(wr.left), uintptr(wr.top), srcCopy|captureBlt)
	pSelectObject.Call(mem, old)
	type bmi struct {
		size                   uint32
		width, height          int32
		planes, bitCount       uint16
		compression, sizeImage uint32
		x, y                   int32
		used, important        uint32
		colors                 [4]uint32
	}
	info := bmi{width: int32(w), height: -int32(h), planes: 1, bitCount: 32}
	info.size = 40
	// The pixels come from VirtualAlloc, not the Go heap. With a 3.2 MB Go
	// slice this driver's captures came back all black; with VirtualAlloc
	// they don't. That matches the text spike's GDI/Go-heap-arena finding,
	// but the cause wasn't isolated here.
	buf, _, _ := pVirtualAlloc.Call(0, uintptr(w*h*4), 0x3000, 0x04)
	defer pVirtualFree.Call(buf, 0, 0x8000)
	px := unsafe.Slice((*byte)(unsafe.Add(unsafe.Pointer(nil), buf)), w*h*4)
	if n, _, err := pGetDIBits.Call(mem, bmp, 0, uintptr(h), buf, uintptr(unsafe.Pointer(&info)), dibRGBColors); n == 0 {
		note("  capture %s: GetDIBits failed: %v", name, err)
		return
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		img.Pix[i*4+0], img.Pix[i*4+1], img.Pix[i*4+2], img.Pix[i*4+3] = px[i*4+2], px[i*4+1], px[i*4+0], 255
	}
	f, err := os.Create(filepath.Join(*out, name+".png"))
	if err != nil {
		panic(err)
	}
	png.Encode(f, img)
	f.Close()
	note("  capture: %s.png (%dx%d)", name, w, h)
}

func powershell(cmd string) string {
	b, _ := exec.Command("powershell", "-NoProfile", "-Command", cmd).CombinedOutput()
	return strings.TrimSpace(string(b))
}

// sockets lists the process's own TCP endpoints (browser processes are
// separate and not counted).
func sockets(pid int) string {
	s := powershell(fmt.Sprintf("Get-NetTCPConnection -OwningProcess %d -ErrorAction SilentlyContinue | ForEach-Object { '{0} {1}:{2} -> {3}:{4}' -f $_.State,$_.LocalAddress,$_.LocalPort,$_.RemoteAddress,$_.RemotePort }", pid))
	if s == "" {
		return "none"
	}
	return strings.ReplaceAll(s, "\r\n", "; ")
}

func scenario(name string) bool {
	if *only == "" {
		return true
	}
	for _, s := range strings.Split(*only, ",") {
		if s == name {
			return true
		}
	}
	return false
}

func main() {
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		panic(err)
	}
	note("# drive %s, %s", time.Now().Format(time.RFC3339), *exe)
	for _, mode := range strings.Split(*modes, ",") {
		note("\n## %s", mode)
		if scenario("checks") {
			checks(mode)
		}
		if scenario("resize") {
			resize(mode)
		}
		if scenario("close-idle") {
			closeIdle(mode)
		}
		if scenario("close-busy") {
			closeBusy(mode)
		}
		if scenario("reload") {
			reload(mode)
		}
		if scenario("close-init") {
			closeInit(mode)
			closeHook(mode)
		}
		if scenario("repeat") {
			repeat(mode)
		}
		if scenario("console-break") {
			consoleBreak(mode)
		}
		if scenario("server-failure") && mode == "loopback" {
			serverFailure()
		}
	}
	if scenario("prereq") {
		note("\n## prerequisites")
		prereq()
	}
	os.WriteFile(filepath.Join(*out, "summary.txt"), []byte(strings.Join(summary, "\n")+"\n"), 0o644)
}

func checks(mode string) {
	r := launch(mode+"-checks", "-mode", mode, "-autorun", "checks")
	hw := r.window(defaultWait)
	sum, ok := r.waitLine(`report: .*checks (PASS|FAIL)`, 40*time.Second)
	note("checks: %s", tail(sum, ok))
	for _, l := range r.grep(`report: .*(FAIL|INFO) `) {
		note("  %s", after(l, "report: "))
	}
	surv, ok := r.waitLine(`still on the fixture`, 5*time.Second)
	note("  outside navigation: %s", tail(surv, ok))
	for _, l := range r.grep(`^\S+ origin: `) {
		note("  host: %s", after(l, " "))
	}
	for _, l := range r.grep(`SHCreateMemStream stream|runtime:|loader:|transport:`) {
		note("  host: %s", after(l, " "))
	}
	note("  sockets owned by pid %d: %s", r.cmd.Process.Pid, sockets(r.cmd.Process.Pid))
	if mode == "loopback" {
		if l, ok := r.waitLine(`listening on (http://\S+)`, time.Second); ok {
			addr := regexp.MustCompile(`http://\S+`).FindString(l)
			ordinaryClient(addr)
		}
	}
	capture(hw, mode+"-checks")
	d, ok := r.close(10 * time.Second)
	note("  close after checks: exited %v in %v, code %d; %s", ok, d.Round(time.Millisecond), r.exitCode(), exitLine(r))
}

func ordinaryClient(addr string) {
	resp, err := http.Get(addr + "/api/query?client=go-net-http&text=caf%C3%A9")
	if err != nil {
		note("  ordinary client (Go net/http): %v", err)
		return
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	note("  ordinary client (Go net/http) GET %s/api/query: %d %s %s", addr, resp.StatusCode, resp.Header.Get("Content-Type"), b)
	if curl, err := exec.LookPath("curl.exe"); err == nil {
		o, _ := exec.Command(curl, "-sS", "-i", addr+"/api/headers").CombinedOutput()
		note("  curl.exe -i %s/api/headers:\n    %s", addr, strings.ReplaceAll(strings.TrimSpace(string(o)), "\r\n", "\n    "))
	}
}

func resize(mode string) {
	r := launch(mode+"-resize", "-mode", mode, "-autorun", "slow")
	hw := r.window(defaultWait)
	if _, ok := r.waitLine(`req \d+ start GET /api/slow`, defaultWait); !ok {
		note("resize: slow request never started")
	}
	sizes := [][2]int{{600, 400}, {1400, 900}, {420, 700}, {1000, 760}}
	t := time.Now()
	for _, s := range sizes {
		pSetWindowPos.Call(hw, 0, 0, 0, uintptr(s[0]), uintptr(s[1]), swpNoMove|swpNoZOrder)
		time.Sleep(150 * time.Millisecond)
	}
	note("resize: %d SetWindowPos calls in %v while /api/slow was pending", len(sizes), time.Since(t).Round(time.Millisecond))
	capture(hw, mode+"-resized-during-slow")
	pShowWindow.Call(hw, swMinimize)
	time.Sleep(1500 * time.Millisecond)
	pShowWindow.Call(hw, swRestore)
	time.Sleep(500 * time.Millisecond)
	capture(hw, mode+"-restored")
	l, ok := r.waitLine(`req \d+ (handler done 200|response installed: 200).*(3\.\d+s|3s)`, 6*time.Second)
	note("  slow request during resize/minimize: %s", tail(l, ok))
	d, ok := r.close(10 * time.Second)
	note("  close: exited %v in %v, code %d; %s", ok, d.Round(time.Millisecond), r.exitCode(), exitLine(r))
}

func closeIdle(mode string) {
	r := launch(mode+"-close-idle", "-mode", mode)
	r.window(defaultWait)
	r.waitLine(`navigation \d+ completed: success true`, defaultWait)
	time.Sleep(500 * time.Millisecond)
	d, ok := r.close(10 * time.Second)
	note("close idle: exited %v in %v, code %d; %s", ok, d.Round(time.Millisecond), r.exitCode(), exitLine(r))
}

func closeBusy(mode string) {
	r := launch(mode+"-close-busy", "-mode", mode, "-autorun", "slow")
	r.window(defaultWait)
	r.waitLine(`req \d+ start GET /api/slow`, defaultWait)
	time.Sleep(300 * time.Millisecond)
	d, ok := r.close(10 * time.Second)
	note("close busy (slow request 300 ms in): exited %v in %v, code %d", ok, d.Round(time.Millisecond), r.exitCode())
	for _, l := range r.grep(`close:|saw cancellation|handler canceled|after close|settled on close|exit:`) {
		note("  %s", after(l, " "))
	}
}

func reload(mode string) {
	r := launch(mode+"-reload", "-mode", mode, "-autorun", "reload")
	r.window(defaultWait)
	l, ok := r.waitLine(`report: .*reload: page reloaded`, defaultWait)
	note("reload during slow: %s", tail(l, ok))
	// Wait past the 3 s handler, to see what happens to the abandoned request.
	time.Sleep(4 * time.Second)
	for _, l := range r.grep(`tag=reload|saw cancellation|navigation \d+`) {
		note("  %s", after(l, " "))
	}
	for _, l := range r.grep(`req \d+ (handler|response)`) {
		if strings.Contains(l, "3s") || strings.Contains(l, "3.0") || strings.Contains(l, "canceled") {
			note("  %s", after(l, " "))
		}
	}
	d, ok := r.close(10 * time.Second)
	note("  close: exited %v in %v, code %d; %s", ok, d.Round(time.Millisecond), r.exitCode(), exitLine(r))
}

// closeInit closes at several delays after the window appears, to land in
// environment creation, controller creation, and just after.
func closeInit(mode string) {
	for _, delay := range []time.Duration{0, 50 * time.Millisecond, 150 * time.Millisecond, 300 * time.Millisecond, 600 * time.Millisecond} {
		r := launch(fmt.Sprintf("%s-close-init-%dms", mode, delay.Milliseconds()), "-mode", mode)
		if r.window(defaultWait) == 0 {
			note("close init +%v: no window", delay)
			continue
		}
		time.Sleep(delay)
		d, ok := r.close(15 * time.Second)
		state := "?"
		if l := r.grep(`close: WM_CLOSE in state`); len(l) > 0 {
			state = after(l[0], "close: ")
		}
		note("close init +%v: exited %v in %v, code %d; %s", delay, ok, d.Round(time.Millisecond), r.exitCode(), state)
		for _, l := range r.grep(`webview: late|waiting up to|no environment callback|test: `) {
			note("  %s", after(l, " "))
		}
		note("  %s", exitLine(r))
	}
}

// closeHook has the host post WM_CLOSE to itself just before each creation
// call, so the close is queued however fast creation is. Where it lands
// shows whether anything pumps messages during that call.
func closeHook(mode string) {
	for _, at := range []string{"env", "controller"} {
		r := launch(fmt.Sprintf("%s-close-during-%s", mode, at), "-mode", mode, "-close-during", at)
		select {
		case <-r.done:
		case <-time.After(15 * time.Second):
			r.cmd.Process.Kill()
		}
		note("close-during %s (WM_CLOSE posted just before the call): exit %d after %v", at, r.exitCode(), time.Since(r.start).Round(time.Millisecond))
		for _, l := range r.grep(`test: |webview: environment|close: WM_CLOSE|webview: late|waiting up to|no environment callback|exit: `) {
			note("  %s", after(l, " "))
		}
	}
}

func repeat(mode string) {
	var times []string
	clean := 0
	for i := 0; i < *repeats; i++ {
		r := launch(fmt.Sprintf("%s-repeat-%d", mode, i+1), "-mode", mode)
		r.window(defaultWait)
		r.waitLine(`navigation \d+ completed: success true`, defaultWait)
		d, ok := r.close(10 * time.Second)
		if ok && r.exitCode() == 0 && strings.Contains(exitLine(r), "still referenced 0") {
			clean++
		}
		times = append(times, d.Round(time.Millisecond).String())
	}
	note("repeat launch/close x%d: %d clean (exit 0, no live callback objects); close-to-exit %s", *repeats, clean, strings.Join(times, ", "))
}

func serverFailure() {
	r := launch("loopback-server-failure", "-mode", "loopback", "-break-listener", "3s")
	r.window(defaultWait)
	select {
	case <-r.done:
	case <-time.After(15 * time.Second):
		r.cmd.Process.Kill()
	}
	note("server failure (-break-listener 3s): exit code %d after %v", r.exitCode(), time.Since(r.start).Round(time.Millisecond))
	for _, l := range r.grep(`break-listener|error:|close: failure`) {
		note("  %s", after(l, " "))
	}
}

func prereq() {
	tmp, _ := os.MkdirTemp("", "dfx2-wv2-prereq")
	defer os.RemoveAll(tmp)
	r := launch("prereq-missing-loader", "-mode", "inprocess", "-loader", filepath.Join(tmp, "WebView2Loader.dll"))
	code := r.exitCode()
	note("missing loader (-loader <empty dir>): exit %d, window %v", code, findWindow(uint32(r.cmd.Process.Pid)) != 0)
	for _, l := range r.grep(`error:|^\s+`) {
		note("  %s", strings.TrimSpace(after(l, " ")))
	}
	// A copy of the executable alone in a folder: the default lookup.
	alone := filepath.Join(tmp, "alone.exe")
	if b, err := os.ReadFile(*exe); err == nil && os.WriteFile(alone, b, 0o755) == nil {
		save := *exe
		*exe = alone
		r = launch("prereq-exe-without-loader", "-mode", "loopback")
		note("executable copied alone (no loader beside it): exit %d", r.exitCode())
		for _, l := range r.grep(`error:`) {
			note("  %s", after(l, " "))
		}
		*exe = save
	}
	empty := filepath.Join(tmp, "runtime")
	os.Mkdir(empty, 0o755)
	r = launch("prereq-missing-runtime-simulated", "-mode", "inprocess", "-runtime-dir", empty)
	note("missing Runtime, simulated (-runtime-dir <empty folder>): exit %d", r.exitCode())
	for _, l := range r.grep(`error:|^\s+`) {
		note("  %s", strings.TrimSpace(after(l, " ")))
	}
}

func exitLine(r *run) string {
	if l := r.grep(`exit: `); len(l) > 0 {
		parts := make([]string, len(l))
		for i, s := range l {
			parts[i] = after(s, "exit: ")
		}
		return strings.Join(parts, "; ")
	}
	return "(no exit line)"
}

func after(s, sep string) string {
	if i := strings.Index(s, sep); i >= 0 {
		return s[i+len(sep):]
	}
	return s
}

func tail(s string, ok bool) string {
	if !ok {
		return "NOT SEEN"
	}
	return after(s, "report: ")
}

// consoleBreak sends Ctrl+Break to the host's process group while a slow
// request is pending. The host should take its ordinary close path (Go
// reports Ctrl+Break as os.Interrupt), not die with 0xC000013A.
func consoleBreak(mode string) {
	r := launch(mode+"-console-break", "-mode", mode, "-autorun", "slow")
	r.window(defaultWait)
	r.waitLine(`req \d+ start GET /api/slow`, defaultWait)
	time.Sleep(300 * time.Millisecond)
	gen := syscall.NewLazyDLL("kernel32.dll").NewProc("GenerateConsoleCtrlEvent")
	t := time.Now()
	ok, _, err := gen.Call(1, uintptr(r.cmd.Process.Pid)) // CTRL_BREAK_EVENT
	if ok == 0 {
		note("console break: GenerateConsoleCtrlEvent failed: %v", err)
		r.cmd.Process.Kill()
		return
	}
	select {
	case <-r.done:
	case <-time.After(10 * time.Second):
		r.cmd.Process.Kill()
	}
	note("console break (Ctrl+Break, slow request pending): exit code %d in %v", r.exitCode(), time.Since(t).Round(time.Millisecond))
	for _, l := range r.grep(`console signal|close: WM_CLOSE|saw cancellation|settled on close|handler canceled|exit: `) {
		note("  %s", after(l, " "))
	}
}

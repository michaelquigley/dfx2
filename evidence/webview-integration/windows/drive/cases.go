//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const attachPage = "<!doctype html><title>attached service</title><p>attached service</p>"

func handlerCount(w event, key string) int { return int(w.sub("handler").num(key)) }

func kindsAfter(list []event, after uint64, kinds ...string) (out []event) {
	for _, e := range list {
		if uint64(e.num("seq")) <= after {
			continue
		}
		for _, k := range kinds {
			if e.str("kind") == k {
				out = append(out, e)
			}
		}
	}
	return out
}

// Both owned transports serve the page, assets and API; only the loopback
// endpoint listens; a client without the credential is refused; loopback
// streams; closing one window leaves the other.
func ownedBasics(m *matrix) {
	h := m.host("host", true, nil)
	starts := map[string]string{}
	for _, lm := range [][2]string{{"P", "inprocess"}, {"L", "loopback"}} {
		label, mode := lm[0], lm[1]
		start := h.open(label, mode)
		starts[mode] = start
		r := h.run(label, start, "checks", nil)["result"]
		res := event(r.(map[string]any))
		m.check(mode+" page, assets, query, POST echo, 404, no visible cookie",
			res.boolean("css") && res.boolean("img") && res.str("query") == "é&ü" && res.sub("echo").num("length") == 1005 &&
				res.sub("echo").str("method") == "POST" && res.num("missing") == 404 && res.str("cookie") == "", "result", res)
		m.detail(mode+"_origin", res.str("origin"))
	}
	m.check("in-process origin", starts["inprocess"] == "https://dfx2.invalid/", "start", starts["inprocess"])
	u, _ := url.Parse(starts["loopback"])
	ports := listeners(h.pid)
	m.check("only the loopback endpoint listens", len(ports) == 1 && ports[0] == "127.0.0.1:"+u.Port(), "listeners", ports)
	addr := strings.TrimSuffix(starts["loopback"], "/")
	statuses := map[string]int{
		"none":                        httpGet(addr, "/api/query?x=external", nil),
		"wrong credential":            httpGet(addr, "/api/query?x=external", map[string]string{"Cookie": "dfx2_endpoint=wrong"}),
		"wrong credential and Origin": httpGet(addr, "/api/query?x=external", map[string]string{"Cookie": "dfx2_endpoint=wrong", "Origin": "http://127.0.0.1:1"}),
	}
	m.check("uncredentialed loopback clients refused with 403", statuses["none"] == 403 && statuses["wrong credential"] == 403 && statuses["wrong credential and Origin"] == 403, "statuses", statuses)
	st := h.window("L")
	m.check("refusals never reached the handler", handlerCount(st, "GET /api/query") == 1, "handler", st["handler"], "endpoint", st.str("endpoint"))
	stream := event(h.run("L", starts["loopback"], "stream", nil)["result"].(map[string]any))
	chunks := stream.list("chunks")
	_ = chunks
	raw, _ := stream["chunks"].([]any)
	m.check("loopback streams the first chunk before the handler returns", len(raw) >= 2 && raw[0] == "chunk-1\n" && raw[1] == "chunk-2 after ack\n", "stream", stream)
	h.close("P")
	m.check("closing the in-process window leaves the loopback window listening", len(listeners(h.pid)) == 1, "listeners", listeners(h.pid))
	m.note(h, h.status())
	if hw := findWindow("dfx2 webview L (loopback)"); hw != 0 {
		err := capture(hw, filepath.Join(m.outDir, "screen-loopback.png"))
		m.detail("screen_capture", map[string]any{"file": "screen-loopback.png", "error": errString(err), "session_state": sessionState()})
	}
	h.close("L")
	m.done(h, "", false)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// In-process operation with no application listener at all.
func inProcessAlone(m *matrix) {
	h := m.host("host", true, nil)
	start := h.open("P", "inprocess")
	h.run("P", start, "checks", nil)
	ports := listeners(h.pid)
	m.check("in-process host listens on nothing", len(ports) == 0, "listeners", ports)
	m.done(h, "", false)
}

// Owned windows keep subresources and navigation on their origin, in both
// transports, with native interception plus the framework policy together.
func confinement(m *matrix) {
	collector := newRecorder(nil)
	defer collector.stop()
	h := m.host("host", true, nil)
	lStart := h.open("L", "loopback")
	oStart := h.open("O", "loopback")
	pStart := h.open("P", "inprocess")
	other := strings.TrimSuffix(oStart, "/")
	before := handlerCount(h.window("O"), "GET /api/query")
	seen := map[string]any{}
	seen["loopback"] = h.run("L", lStart, "attacks", map[string]string{"collector": collector.origin(), "other": other})["result"]
	seen["inprocess"] = h.run("P", pStart, "attacks", map[string]string{"collector": collector.origin(), "other": other})["result"]
	time.Sleep(time.Second)
	hits := collector.hits("")
	m.check("subresources, fetches, sockets, frames, workers and redirects reached no collector", len(hits) == 0, "hits", hits)
	m.check("no page reached another window's endpoint", handlerCount(h.window("O"), "GET /api/query") == before, "other_handler", h.window("O")["handler"])
	m.detail("page_views", seen)
	refusals := map[string]any{}
	for _, ls := range [][2]string{{"L", lStart}, {"P", pStart}} {
		label, start := ls[0], ls[1]
		for _, how := range []string{"location", "redirect", "popup", "form"} {
			after := h.seq(label)
			h.run(label, start, "armed", map[string]string{"then": "navigate", "how": how, "collector": collector.origin()})
			time.Sleep(1500 * time.Millisecond)
			list := h.browserEvents(label)
			refusals[label+"-"+how] = kindsAfter(list, after, "navigation-refused", "popup-refused", "blocked")
			uri := h.window(label).str("uri")
			m.check(label+" "+how+" stays on its origin", strings.HasPrefix(uri, start), "uri", uri)
		}
	}
	time.Sleep(time.Second)
	hits = collector.hits("")
	m.check("navigation attempts reached no collector: interception is the network boundary", len(hits) == 0, "hits", hits)
	m.detail("refusals", refusals)
	m.done(h, "", false)
}

// Sessions are per window: cookies and storage, across navigation, gone with
// the window, fresh on reopening.
func storage(m *matrix) {
	h := m.host("host", true, nil)
	a := h.open("A", "inprocess")
	b := h.open("B", "inprocess")
	res := func(e event) event { return event(e["result"].(map[string]any)) }
	setA := res(h.run("A", a, "storageSet", map[string]string{"tag": "a"}))
	readB := res(h.run("B", b, "storageRead", nil))
	rereadA := res(h.run("A", a, "storageRead", nil))
	m.check("A stored cookie, local and session storage", strings.Contains(setA.str("cookie"), "vis=a") && setA.str("local") == "a" && setA.str("session") == "a", "result", setA)
	m.check("B sees none of A's storage", !strings.Contains(readB.str("cookie"), "vis=") && readB["local"] == nil && readB["session"] == nil, "result", readB)
	m.check("A keeps its session across navigation", strings.Contains(rereadA.str("cookie"), "vis=a") && rereadA.str("local") == "a" && rereadA.str("session") == "a", "result", rereadA)
	// Cookies ignore ports: two loopback windows share nothing only because
	// their profiles are separate.
	l1 := h.open("L1", "loopback")
	l2 := h.open("L2", "loopback")
	setL1 := res(h.run("L1", l1, "storageSet", map[string]string{"tag": "l1"}))
	rereadL1 := res(h.run("L1", l1, "storageRead", nil))
	readL2 := res(h.run("L2", l2, "storageRead", nil))
	m.check("loopback page cookie kept across navigation", strings.Contains(setL1.str("cookie"), "vis=l1") && strings.Contains(rereadL1.str("cookie"), "vis=l1"), "set", setL1, "reread", rereadL1)
	m.check("loopback windows share no cookies across ports", !strings.Contains(readL2.str("cookie"), "vis="), "result", readL2)
	// Cache: recorded, not asserted. Each window has its own handler, so the
	// X-Count header says whether a repeat was served from cache.
	cache := map[string]any{}
	cache["A-first"] = res(h.run("A", a, "cached", nil))
	cache["A-second"] = res(h.run("A", a, "cached", nil))
	cache["B-first"] = res(h.run("B", b, "cached", nil))
	cache["A-handler"] = h.window("A").sub("handler")["GET /api/cached"]
	m.detail("cache_observation", cache)
	for _, label := range []string{"A", "B", "L1", "L2"} {
		h.close(label)
	}
	a = h.open("A", "inprocess")
	fresh := res(h.run("A", a, "storageRead", nil))
	m.check("a reopened window starts empty", fresh["local"] == nil && !strings.Contains(fresh.str("cookie"), "vis="), "result", fresh)
	m.done(h, "", false)
}

// Load failures over loopback: the framework page, Retry only for a known
// GET, redirects judged by the final method, HTTP errors shown normally.
func errorPagesLoopback(m *matrix) {
	h := m.host("host", true, nil)
	start := h.open("L", "loopback")
	h.run("L", start, "armed", nil)
	failure := func(trigger func()) event {
		after := h.seq("L")
		trigger()
		e, _ := h.waitBrowserEvent("L", "error-page", after, nil)
		return e
	}
	counts := func() event { return h.window("L").sub("handler") }
	get := failure(func() { h.send("go L " + start + "api/fail") })
	m.check("GET failure offers Retry", get.str("method") == "GET" && get.boolean("retry"), "event", get)
	retry := failure(func() { h.send("action L retry") })
	m.check("Retry repeated the failed GET", retry.str("uri") == start+"api/fail", "event", retry)
	after := h.seq("L")
	h.send("action L start")
	h.waitBrowserEvent("L", "error-action", after, func(e event) bool { return e.str("detail") == "start" })
	h.waitTitle("L", "dfx2 webview audition")
	post := failure(func() { h.run("L", start, "armed", map[string]string{"then": "submit", "action": "api/fail"}) })
	m.check("POST failure withholds Retry", post.str("method") == "POST" && !post.boolean("retry"), "event", post)
	posts := int(counts().num("POST /api/fail"))
	after = h.seq("L")
	h.send("action L retry")
	h.waitBrowserEvent("L", "error-action-refused", after, nil)
	h.send("action L start")
	h.waitBrowserEvent("L", "navigation-allowed", after, func(e event) bool { return e.str("uri") == start })
	h.waitTitle("L", "dfx2 webview audition")
	m.check("no error-page action replayed the POST", int(counts().num("POST /api/fail")) == posts, "posts_before", posts, "handler", counts())
	// 303 after a POST: the failure is the GET, which Retry may repeat.
	see := failure(func() { h.run("L", start, "armed", map[string]string{"then": "submit", "action": "api/see-other"}) })
	m.check("303 target is a retryable GET", see.str("uri") == start+"api/fail" && see.str("method") == "GET" && see.boolean("retry"), "event", see)
	posts = int(counts().num("POST /api/see-other"))
	failure(func() { h.send("action L retry") })
	m.check("Retry after a 303 resubmitted no POST", int(counts().num("POST /api/see-other")) == posts, "handler", counts())
	// 307 keeps the POST: no Retry. The transport may deliver it more than
	// once by itself; that is recorded, not asserted.
	temp := failure(func() { h.run("L", start, "armed", map[string]string{"then": "submit", "action": "api/temporary"}) })
	m.check("307 keeps the POST and withholds Retry", temp.str("method") == "POST" && !temp.boolean("retry"), "event", temp)
	m.detail("transport_redelivery", map[string]any{"POST /api/temporary": counts()["POST /api/temporary"], "GET /api/fail": counts()["GET /api/fail"], "POST /api/fail": counts()["POST /api/fail"]})
	after = h.seq("L")
	h.send("action L start")
	h.waitTitle("L", "dfx2 webview audition")
	m.check("Return to start after a 307 resubmitted no POST", int(counts().num("POST /api/temporary")) == int(event(m.current.Detail["transport_redelivery"].(map[string]any)).num("POST /api/temporary")), "handler", counts())
	for _, code := range []int{404, 500} {
		after = h.seq("L")
		h.send(fmt.Sprintf("go L %sapi/status?code=%d", start, code))
		h.waitTitle("L", fmt.Sprintf("status %d", code))
		m.check(fmt.Sprintf("%d page shown as an ordinary page", code), len(kindsAfter(h.browserEvents("L"), after, "error-page")) == 0)
	}
	if hw := findWindow("dfx2 webview L (loopback)"); hw != 0 {
		after = h.seq("L")
		h.send("go L " + start + "api/fail")
		h.waitBrowserEvent("L", "error-page", after, nil)
		time.Sleep(500 * time.Millisecond)
		err := capture(hw, filepath.Join(m.outDir, "screen-error-page.png"))
		m.detail("screen_capture", map[string]any{"file": "screen-error-page.png", "error": errString(err), "session_state": sessionState()})
	}
	m.detail("handler", counts())
	m.done(h, "", false)
}

// A successful new load of the failed URI retires the old error actions.
func errorActionsSameURI(m *matrix) {
	service := newRecorder(map[string]route{"/": {body: attachPage}, "/recover": {drop: true}})
	defer service.stop()
	h := m.host("host", true, nil)
	h.open("X", "attach", service.origin()+"/", service.origin())
	target := service.origin() + "/recover"
	after := h.seq("X")
	h.send("go X " + target)
	failed, _ := h.waitBrowserEvent("X", "error-page", after, nil)
	m.check("initial failure offers Retry", failed.boolean("retry"), "event", failed)
	service.set("/recover", route{body: "<!doctype html><title>recovered same URI</title>"})
	h.send("go X " + target)
	h.waitTitle("X", "recovered same URI")
	for _, action := range []string{"retry", "start"} {
		command := "action X " + action
		rejected := h.expect("error", h.send(command), 5*time.Second, "command", command)
		m.check("old "+action+" is refused after the same-URI recovery", rejected.str("error") == "no error page is showing", "event", rejected)
	}
	m.done(h, "", false)
}

// An attached service: navigation policy, error actions, and survival.
func attachment(m *matrix) {
	collector := newRecorder(nil)
	defer collector.stop()
	service := newRecorder(map[string]route{"/": {body: attachPage}, "/fail": {drop: true}, "/fail-post": {drop: true},
		"/elsewhere": {status: 302, location: collector.origin() + "/redirected"}})
	defer service.stop()
	h := m.host("host", true, nil)
	h.open("X", "attach", service.origin()+"/", service.origin())
	h.waitTitle("X", "attached service")
	after := h.seq("X")
	h.send("go X " + collector.origin() + "/outside")
	outside, _ := h.waitBrowserEvent("X", "navigation-refused", after, nil)
	after = h.seq("X")
	h.send("go X " + service.origin() + "/elsewhere")
	redirect, _ := h.waitBrowserEvent("X", "navigation-refused", after, func(e event) bool { return strings.HasSuffix(e.str("uri"), "/redirected") })
	time.Sleep(time.Second)
	uri := h.window("X").str("uri")
	m.check("refused navigations leave the window on its service", strings.HasPrefix(uri, service.origin()), "uri", uri, "outside", outside, "redirect", redirect)
	// WebView2 may still send a canceled GET navigation's request: an
	// attachment has no interception and no credential, so this is recorded.
	m.detail("collector_hits_after_refused_navigations", collector.hits(""))
	after = h.seq("X")
	h.send("go X " + service.origin() + "/fail")
	get, _ := h.waitBrowserEvent("X", "error-page", after, nil)
	m.check("attached GET failure offers Retry", get.boolean("retry"), "event", get)
	after = h.seq("X")
	h.send("action X retry")
	h.waitBrowserEvent("X", "error-page", after, nil)
	posts := service.count("POST", "/fail-post")
	after = h.seq("X")
	h.send("go X " + service.origin() + "/post?target=/fail-post")
	post, _ := h.waitBrowserEvent("X", "error-page", after, func(e event) bool { return strings.HasSuffix(e.str("uri"), "/fail-post") })
	m.check("attached POST failure withholds Retry", post.str("method") == "POST" && !post.boolean("retry"), "event", post)
	posted := service.count("POST", "/fail-post")
	h.send("action X start")
	h.waitBrowserEvent("X", "error-action", after, func(e event) bool { return e.str("detail") == "start" })
	time.Sleep(time.Second)
	m.check("Return to start resubmitted no POST", service.count("POST", "/fail-post") == posted, "posts_delivered_by_transport", posted-posts)
	h.close("X")
	m.done(h, "", false)
	m.check("the service is still answering after its window closed", httpGet(service.origin(), "/", nil) == 200)
	m.detail("service_requests", len(service.hits("")))
}

// A held older response for a URL, released after a newer navigation to the
// same URL failed, cannot suppress its error page or change its actions. The
// fixture orders the steps: the document request is dropped, the page's
// fetch of the same URL is held until the error page is showing.
func responseRace(m *matrix) {
	service := newRecorder(map[string]route{"/api/race": {dropDocs: true}}) // "/" is the audition page itself
	defer service.stop()
	service.static = *static
	h := m.host("host", true, nil)
	start := h.open("X", "attach", service.origin()+"/", service.origin())
	// The audition page comes from the service, so its reports land there too.
	service.runAttached(h, "X", start, "armed", nil)
	service.runAttached(h, "X", start, "fetchURL", map[string]string{"url": "/api/race"})
	deadline := time.Now().Add(*timeout)
	for time.Now().Before(deadline) && service.count("GET", "/api/race") == 0 {
		time.Sleep(100 * time.Millisecond)
	}
	m.check("the page's fetch of the race URL is held at the service", service.count("GET", "/api/race") == 1, "hits", service.hits("/api/race"))
	after := h.seq("X")
	h.send("go X " + service.origin() + "/api/race")
	failed, idx := h.waitBrowserEvent("X", "error-page", after, nil)
	m.check("the navigation to the race URL failed with Retry", failed.boolean("retry") && failed.str("method") == "GET", "event", failed)
	service.release()
	time.Sleep(2 * time.Second)
	later := kindsAfter(h.browserEvents("X"), idx, "resource-response", "load", "navigation-allowed")
	m.check("the released older response produced no resource response or load while the error page was pending", len(later) == 0, "events_after_error_page", later)
	after = h.seq("X")
	h.send("action X retry")
	action, _ := h.waitBrowserEvent("X", "error-action", after, func(e event) bool { return e.str("detail") == "retry" })
	m.check("the error page's Retry is still the current one", strings.HasSuffix(action.str("uri"), "/api/race"), "event", action)
	h.waitBrowserEvent("X", "error-page", after, nil)
	m.detail("race_hits", service.hits("/api/race"))
	m.done(h, "", false)
}

// Close while initializing, at each asynchronous stage; while handlers ignore
// cancellation; while a request body is being read.
func closeCases(m *matrix) {
	// A stage the host reports already passed when Open returned completed
	// synchronously inside the creation call (the Runtime does that for the
	// environment); it is recorded as unreachable from here, not failed. The
	// modeled tests cover a close at every stage.
	stages := map[string][]string{"loopback": {"environment", "controller", "cookie", "navigation"}, "inprocess": {"environment", "controller", "navigation"}}
	reachable := map[string][]string{}
	for _, mode := range []string{"loopback", "inprocess"} {
		for _, stage := range stages[mode] {
			h := m.host("init-"+mode+"-"+stage, true, nil)
			opened := h.openClose("A", mode, stage)
			record := map[string]any{"opened": opened}
			if e := opened.str("close_at_error"); e != "" {
				record["unreachable"] = e
				h.close("A")
			} else {
				reachable[mode] = append(reachable[mode], stage)
				closed := h.expect("closed", 0, 60*time.Second, "window", "A")
				record["closed"] = closed
			}
			// A second window shows the host and the loader survive the early close.
			start := h.open("B", mode)
			h.run("B", start, "checks", nil)
			record["second_window_events"] = len(h.browserEvents("B"))
			m.detail(mode+"-"+stage, record)
			h.close("B")
			m.done(h, "", false)
		}
	}
	m.check("close during credential confirmation and before navigation were reachable natively",
		strings.Contains(fmt.Sprint(reachable["loopback"]), "cookie") && strings.Contains(fmt.Sprint(reachable["loopback"]), "navigation") &&
			strings.Contains(fmt.Sprint(reachable["inprocess"]), "navigation"), "reachable", reachable)
	m.detail("reachable_stages", reachable)
	for _, mode := range []string{"inprocess", "loopback"} {
		h := m.host("handler-"+mode, true, nil)
		start := h.open("A", mode)
		h.run("A", start, "hold", nil)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) && handlerCount(h.window("A"), "GET /api/hold") == 0 {
			time.Sleep(100 * time.Millisecond)
		}
		m.check(mode+": the held handler was entered", handlerCount(h.window("A"), "GET /api/hold") == 1)
		mark := h.send("close A")
		h.quiet("closed", mark, 2*time.Second, "window", "A")
		st := h.window("A")
		m.check(mode+": the window stays pending while its handler runs", st != nil && (strings.Contains(st.str("endpoint"), "handlers=1") || strings.Contains(st.str("endpoint"), "native=")), "status", st)
		h.send("release A")
		h.expect("closed", mark, *timeout, "window", "A")
		m.done(h, "", false)
	}
	h := m.host("reading", true, nil)
	start := h.open("A", "inprocess")
	h.run("A", start, "upload", nil)
	h.close("A")
	m.done(h, "", false)
}

// The body reader, natively: an awaited upload larger than the read chunk is
// echoed intact; the in-process content getter's result is recorded for an
// empty POST and a ReadableStream upload; closing during an upload drains.
// The page's report and the host's request-content event are kept side by
// side: what the getter returned, and what the page saw.
func bodyReader(m *matrix) {
	h := m.host("body", true, nil)
	start := h.open("A", "inprocess")
	const size = 900000
	r := event(h.run("A", start, "uploadEcho", map[string]string{"size": fmt.Sprint(size)})["result"].(map[string]any))
	sum := sha256.Sum256([]byte(strings.Repeat("u", size)))
	m.check("a 900000-byte upload is echoed intact", r.num("status") == 200 && r.sub("echo").num("length") == size &&
		r.sub("echo").str("sha256") == hex.EncodeToString(sum[:]), "result", r)
	m.check("no body reader remains after the upload", h.window("A").sub("browser").num("Reads") == 0, "browser", h.window("A")["browser"])
	pages := map[string]event{}
	for _, run := range []string{"emptyPost", "streamUpload"} {
		after := h.seq("A")
		pages[run] = event(h.run("A", start, run, nil)["result"].(map[string]any))
		// The run's events: the host records the getter's result only, so
		// they are matched by sequence, after the mark, not by URL or method.
		var content any = "no event observed"
		if list := kindsAfter(h.browserEvents("A"), after, "request-content"); len(list) > 0 {
			content = list
		}
		m.detail(run, map[string]any{"page": pages[run], "request_content": content, "handler": h.window("A")["handler"]})
	}
	m.check("an empty POST is answered as an empty body", pages["emptyPost"].num("status") == 200 &&
		pages["emptyPost"].sub("echo").num("length") == 0, "page", pages["emptyPost"])
	h.close("A")
	m.done(h, "", false)
}

// closeObserved is the WebView2 close event's record, carried in the closed
// reply: how many body reads the close stopped while claimed, a Read in
// flight at the close boundary.
var closeObserved = regexp.MustCompile(`^claimed-reads=(0|[1-9][0-9]*)$`)

// bodyClose closes windows during a 900 KB upload, a bounded number of
// times at staggered delays, each in a fresh host. Native reads are fast, so
// a close landing on a claimed read is observed, not forced: the modeled
// tests force it. Every attempt must close, record a well-formed observation
// and drain cleanly, or the case fails. Only then does it pass, if some close
// stopped a claimed read, or report untested, if every close recorded zero.
func bodyClose(m *matrix) {
	claimed := 0
	var attempts []map[string]any
	for i, delay := range []time.Duration{0, 5, 10, 20, 40, 80} {
		delay *= time.Millisecond
		h := m.host(fmt.Sprintf("attempt-%d", i), true, nil)
		start := h.open("A", "inprocess")
		h.run("A", start, "upload", nil)
		time.Sleep(delay)
		closed := h.close("A")
		observed := closed.str("close")
		match := closeObserved.FindStringSubmatch(observed)
		attempt := map[string]any{"delay_ms": delay.Milliseconds(), "close": observed}
		attempts = append(attempts, attempt)
		m.detail("attempts", attempts)
		require(match != nil, "attempt %d: close observation missing or malformed: %q", i, observed)
		count, _ := strconv.Atoi(match[1])
		claimed += count
		m.done(h, "", false) // fails the case on any unclean exit or drain
	}
	m.detail("claimed_at_close", claimed)
	if claimed == 0 {
		// Every attempt closed, recorded a valid zero and drained: reached
		// here only after all of that succeeded.
		m.untested(fmt.Sprintf("no close landed on a claimed body read in %d attempts", len(attempts)))
		return
	}
	m.check("a close stopped a claimed body read, and every attempt drained", true, "claimed_at_close", claimed)
}

// Several windows: closing one leaves the others and the application; the
// last window ends a window application.
func closeOneKeepOne(m *matrix) {
	h := m.host("host", false, nil, "-open", "native")
	a := h.open("A", "inprocess")
	b := h.open("B", "loopback")
	_ = a
	st := h.status()
	m.note(h, st)
	pidA := uint32(st.sub("windows").sub("A").num("browser_pid"))
	h.close("A")
	time.Sleep(2 * time.Second)
	m.check("the closed window's browser process exited while the host continues", !processAlive(pidA), "pid", pidA)
	r := event(h.run("B", b, "checks", nil)["result"].(map[string]any))
	m.check("B kept working", r.boolean("css") && r.sub("echo").num("length") == 1005, "result", r)
	m.check("the native window remains", h.status().sub("windows").has("N"))
	h.close("B")
	h.close("N") // the last window: the application exits by itself
	h.expect("return", 0, 60*time.Second)
	m.done(h, "", false)
}

// Native-only startup loads no browser library and extracts nothing; the
// first browser window does both; the last close unloads and removes.
func lazyLoading(m *matrix) {
	h := m.host("host", true, nil)
	h.send("native N")
	time.Sleep(2 * time.Second)
	before := map[string]any{"loader_mapped": mapped(h.pid, "WebView2Loader"), "client_mapped": mapped(h.pid, "EmbeddedBrowserWebView"), "temp": tempEntries(h.tmp)}
	m.check("native-only startup maps no WebView2 module and extracts nothing", before["loader_mapped"] == false && before["client_mapped"] == false && len(before["temp"].([]string)) == 0, "before", before)
	h.open("A", "inprocess")
	after := map[string]any{"loader_mapped": mapped(h.pid, "WebView2Loader"), "client_mapped": mapped(h.pid, "EmbeddedBrowserWebView"), "temp": tempEntries(h.tmp)}
	loaderDirs := 0
	for _, e := range tempEntries(h.tmp) {
		if strings.HasPrefix(e, "dfx2-webview2-loader-") {
			loaderDirs++
		}
	}
	m.check("the first browser window extracts and maps the loader and the runtime client", after["loader_mapped"] == true && after["client_mapped"] == true && loaderDirs == 1, "after", after)
	m.detail("modules_with_browser", modules(h.pid))
	h.close("A")
	time.Sleep(time.Second)
	closed := map[string]any{"loader_mapped": mapped(h.pid, "WebView2Loader"), "client_mapped": mapped(h.pid, "EmbeddedBrowserWebView"), "temp": tempEntries(h.tmp)}
	m.check("the last close unloads the loader and removes its directory", closed["loader_mapped"] == false && len(closed["temp"].([]string)) == 0, "closed", closed)
	m.detail("runtime_client_still_mapped_after_last_close", closed["client_mapped"])
	m.done(h, "", false)
}

// A window's profile exists only while it lives; its browser process exits
// before the profile is deleted.
func profileLifecycle(m *matrix) {
	h := m.host("host", true, nil)
	h.open("L", "loopback")
	st := h.status()
	m.note(h, st)
	w := st.sub("windows").sub("L")
	profile, pid := w.str("profile"), uint32(w.num("browser_pid"))
	entries := tempEntries(h.tmp)
	m.check("the profile is a fresh framework directory under the host's TEMP", strings.HasPrefix(filepath.Base(profile), "dfx2-webview2-profile-") &&
		strings.HasPrefix(strings.ToLower(profile), strings.ToLower(h.tmp)) && len(entries) == 2, "profile", profile, "temp", entries)
	_, err := os.Stat(profile)
	m.check("the profile directory exists while the window lives", err == nil)
	closedAt := time.Now()
	h.close("L")
	exited := !processAlive(pid)
	m.check("the browser process has exited when the window reports closed", exited, "pid", pid)
	events := h.events("closed", 0, "window", "L")
	m.detail("close_took", time.Since(closedAt).Round(time.Millisecond).String())
	m.detail("closed_event", events)
	_, err = os.Stat(profile)
	m.check("the profile directory is gone after close", os.IsNotExist(err), "err", errString(err))
	m.check("nothing framework-owned remains in TEMP", len(tempEntries(h.tmp)) == 0, "temp", tempEntries(h.tmp))
	m.done(h, "", false)
}

// A native drawn window and a browser window coexist while the browser shell
// runs a Win32 size/move loop: the shell's caption is dragged with the
// injected pointer, a page request completes during the drag, and the shell
// ends up moved.
func sizeMove(m *matrix) {
	h := m.host("host", true, nil, "-open", "native")
	start := h.open("A", "inprocess")
	hwnd := findWindow("dfx2 webview A (inprocess)")
	m.check("the browser shell was found by title", hwnd != 0)
	if hwnd == 0 {
		m.done(h, "", false)
		return
	}
	var before rect
	pGetWindowRect.Call(hwnd, uintptrOf(&before))
	loopsBefore := h.status().sub("native_counters").num("ModalLoops")
	finished := make(chan error, 1)
	go func() { finished <- captionDrag(hwnd, 160, 4*time.Second) }()
	time.Sleep(1200 * time.Millisecond)
	report := h.run("A", start, "checks", nil)
	var dragErr error
	select {
	case dragErr = <-finished:
	case <-time.After(20 * time.Second):
		dragErr = fmt.Errorf("the drag did not finish within 20 s")
	}
	var after rect
	pGetWindowRect.Call(hwnd, uintptrOf(&after))
	counters := h.status().sub("native_counters")
	if dragErr != nil && counters.num("ModalLoops") == loopsBefore {
		// Pointer injection needs a connected, unlocked session: the case
		// did not run.
		m.untested(fmt.Sprintf("no size/move loop could be started (%v; session %s): needs a connected, unlocked desktop", dragErr, sessionState()))
		m.detail("counters", counters)
	} else {
		m.check("the caption drag was injected", dragErr == nil, "error", fmt.Sprint(dragErr))
		m.check("the size/move loop was established", counters.num("ModalLoops") > loopsBefore, "counters", counters)
		m.check("the page's request completed while the loop ran", event(report["result"].(map[string]any)).num("missing") == 404 && counters.num("ModalTurns") > 0, "report", report, "counters", counters)
		m.check("the shell moved", after.left > before.left, "before", before.left, "after", after.left)
		m.check("the loop is no longer active", !counters.boolean("ModalActive"))
	}
	h.close("A")
	h.close("N")
	m.done(h, "", false)
}

// A browser-process failure cancels and drains the application.
func browserFailure(m *matrix) {
	h := m.host("host", true, nil)
	h.send("native N")
	start := h.open("A", "inprocess")
	h.run("A", start, "checks", nil)
	st := h.status()
	m.note(h, st)
	pid := uint32(st.sub("windows").sub("A").num("browser_pid"))
	m.check("the browser process is known", pid != 0 && processAlive(pid), "pid", pid)
	if err := killProcess(pid); err != nil {
		fail("killing the browser process: %v", err)
	}
	ret := h.expect("return", 0, 60*time.Second)
	m.check("the application failed and drained", strings.Contains(ret.str("error"), "WebView2") && (strings.Contains(ret.str("error"), "browser process") || strings.Contains(ret.str("error"), "exited")), "return", ret)
	m.done(h, "browser process", false)
}

// Without a WebView2 Runtime, native windows work and the first browser
// window is a fatal, actionable initialization failure.
func missingRuntime(m *matrix) {
	empty := filepath.Join(m.scratch, "no-runtime")
	os.MkdirAll(empty, 0o755)
	env := []string{"WEBVIEW2_BROWSER_EXECUTABLE_FOLDER=" + empty}
	h := m.host("native-only", true, env)
	h.send("native N")
	time.Sleep(time.Second)
	m.check("a native-only application runs without the runtime", h.running() && h.status().sub("windows").has("N"))
	h.close("N")
	m.done(h, "", false)
	h = m.host("browser", true, env)
	mark := h.send("open A inprocess")
	errEvent := h.expect("error", mark, *timeout)
	m.check("Open fails naming the missing runtime", strings.Contains(errEvent.str("error"), "Runtime not found"), "error", errEvent)
	ret := h.expect("return", h.last, 60*time.Second)
	m.check("the failure is fatal and reaches Run", strings.Contains(ret.str("error"), "Runtime not found"), "return", ret)
	m.done(h, "Runtime not found", false)
}

// The application executable alone, copied away from the repository and its
// SDK files, hosts a browser window: the loader comes from inside it.
func exeOnly(m *matrix) {
	dir := filepath.Join(m.scratch, "exe-only")
	os.MkdirAll(dir, 0o755)
	copied := filepath.Join(dir, "app.exe")
	b, err := os.ReadFile(*exe)
	if err != nil {
		fail("reading the executable: %v", err)
	}
	if err := os.WriteFile(copied, b, 0o755); err != nil {
		fail("copying the executable: %v", err)
	}
	saved := *exe
	*exe = copied
	defer func() { *exe = saved }()
	h := m.host("host", true, nil)
	entries, _ := os.ReadDir(dir)
	m.check("nothing but the executable is beside it", len(entries) == 1, "entries", len(entries))
	start := h.open("L", "loopback")
	r := event(h.run("L", start, "checks", nil)["result"].(map[string]any))
	m.check("the copied executable hosts a loopback window", r.boolean("css") && r.num("missing") == 404, "result", r)
	var loaderPath string
	for _, mod := range modulePaths(h.pid) {
		if strings.Contains(strings.ToLower(mod), "webview2loader") {
			loaderPath = mod
		}
	}
	m.check("the loader was loaded from the extracted copy under TEMP", strings.Contains(strings.ToLower(loaderPath), "dfx2-webview2-loader-"), "loader", loaderPath)
	h.close("L")
	m.done(h, "", false)
	// The directory beside the executable is untouched.
	entries, _ = os.ReadDir(dir)
	m.check("no file appeared beside the executable", len(entries) == 1)
}

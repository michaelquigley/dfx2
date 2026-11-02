package main

import (
	"fmt"
	"image"
	"log"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"dfx2textspike/text"
)

// The -meter fixture stands in for sessionmixer's meter sampler: a worker
// goroutine publishes the latest level about 30 times a second and wakes the
// UI thread, which takes the latest state and repaints. Everything the
// worker shares is in meterFeed; everything drawn is in meterView, owned by
// the UI thread.

// meterPeriod is the synthetic sampler's tick, about 30 Hz.
const meterPeriod = time.Second / 30

// levelAt is the synthetic level, linear amplitude in (0, 1], at tick n:
// two slow sines and a hashed jitter, so the same tick always gives the same
// level and the bar moves visibly without being periodic to the eye.
func levelAt(n uint64) float32 {
	t := float64(n) / 30
	v := 0.5 + 0.3*math.Sin(2*math.Pi*0.23*t) + 0.12*math.Sin(2*math.Pi*1.7*t)
	v += 0.15 * (float64(splitmix(n)>>11)/(1<<53) - 0.5)
	return float32(min(max(v, 0.002), 1))
}

func splitmix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ x>>30) * 0xbf58476d1ce4e5b9
	x = (x ^ x>>27) * 0x94d049bb133111eb
	return x ^ x>>31
}

// meterFeed is the worker and the state it publishes. The worker calls wake
// at most once per update the UI has not yet taken: while a wakeup is
// pending, newer levels overwrite the published one and no further wakeup is
// posted, so a slow UI shows the latest level instead of a backlog.
type meterFeed struct {
	period time.Duration
	// wake asks the UI thread to call take. It must not block or carry
	// pointers into Go memory; it reports whether the request was queued.
	wake func() bool

	mu    sync.Mutex
	level float32
	tick  uint64
	seq   uint64 // published updates, ever

	pending atomic.Bool // a wakeup is queued and take has not run since

	ctl      chan bool // run (true) or pause (false)
	quit     chan struct{}
	done     chan struct{}
	stopOnce sync.Once

	ticks, posts, coalesced, failed atomic.Uint64
}

func newMeterFeed(period time.Duration, wake func() bool) *meterFeed {
	return &meterFeed{
		period: period,
		wake:   wake,
		level:  levelAt(0),
		ctl:    make(chan bool),
		quit:   make(chan struct{}),
		done:   make(chan struct{}),
	}
}

// start launches the worker, ticking or paused.
func (m *meterFeed) start(running bool) { go m.run(running) }

func (m *meterFeed) run(running bool) {
	defer close(m.done)
	var tk *time.Ticker
	var tc <-chan time.Time
	set := func(on bool) {
		switch {
		case on && tk == nil:
			tk = time.NewTicker(m.period)
			tc = tk.C
		case !on && tk != nil:
			tk.Stop()
			tk, tc = nil, nil
		}
	}
	set(running)
	defer set(false)
	next := uint64(1)
	for {
		select {
		case <-m.quit:
			return
		case on := <-m.ctl:
			set(on)
		case <-tc:
			m.publish(next)
			next++
		}
	}
}

func (m *meterFeed) publish(n uint64) {
	m.mu.Lock()
	m.level, m.tick = levelAt(n), n
	m.seq++
	m.mu.Unlock()
	m.ticks.Add(1)
	if !m.pending.CompareAndSwap(false, true) {
		m.coalesced.Add(1)
		return
	}
	if m.wake() {
		m.posts.Add(1)
		return
	}
	// Not queued (the message queue is full, say): let the next tick try.
	m.pending.Store(false)
	m.failed.Add(1)
}

// setRunning pauses or resumes the worker's timer. It returns at once if
// the worker has stopped.
func (m *meterFeed) setRunning(on bool) {
	select {
	case m.ctl <- on:
	case <-m.done:
	}
}

// take is the UI side of a wakeup: it clears the pending flag before
// reading, so an update published after the read posts a new wakeup.
func (m *meterFeed) take() (level float32, tick, seq uint64) {
	m.pending.Store(false)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.level, m.tick, m.seq
}

// stop ends the worker and waits for it. Once it returns, wake is never
// called again. It may be called more than once.
func (m *meterFeed) stop() {
	m.stopOnce.Do(func() { close(m.quit) })
	<-m.done
}

// meterView is the meter as the UI thread draws it.
type meterView struct {
	level  float32
	tick   uint64
	paused bool

	readoutText   string
	readout       *text.Paragraph
	readoutShapes int // readouts made: each is shaped once, by design
	pause, resume *text.Paragraph

	button image.Rectangle // the pause control, in device pixels, as last drawn
}

func newMeterView() *meterView {
	return &meterView{
		level:  levelAt(0),
		pause:  text.NewParagraph("Pause"),
		resume: text.NewParagraph("Resume"),
	}
}

func dbOf(level float32) float64 { return 20 * math.Log10(float64(level)) }

// meterFloor is the bottom of the bar's scale, in dB.
const meterFloor = -60

// draw lays the meter row out at (x, y), w device pixels wide, and returns
// the y below it.
func (m *meterView) draw(a *app, x, y, w int, scale float32) int {
	sz := func(dip float32) int { return int(math.Round(float64(dip * scale))) }
	labelSize := 14 * scale
	rowH := sz(28)

	m.button = image.Rect(x, y, x+sz(84), y+rowH)
	fillRect(&a.frame, m.button, mix(a.bg, a.fg, 0.18))
	label := m.pause
	if m.paused {
		label = m.resume
	}
	textTop := y + (rowH-int(labelSize*1.25))/2
	a.sys.Draw(&a.frame, label, labelSize, x+sz(12), textTop, m.button.Dx()-sz(12), a.fg)

	rx := m.button.Max.X + sz(12)
	if s := fmt.Sprintf("%.1f dB", dbOf(m.level)); s != m.readoutText {
		m.readoutText, m.readout = s, text.NewParagraph(s)
		m.readoutShapes++
	}
	a.sys.Draw(&a.frame, m.readout, labelSize, rx, textTop, sz(80), a.fg)

	bx := rx + sz(84)
	bar := image.Rect(bx, y+(rowH-sz(12))/2, max(x+w, bx+1), y+(rowH+sz(12))/2)
	fillRect(&a.frame, bar, mix(a.bg, a.fg, 0.1))
	frac := float32(min(max((dbOf(m.level)-meterFloor)/-meterFloor, 0), 1))
	zones := []struct {
		to float64 // dB
		c  text.RGB
	}{
		{-12, text.RGB{R: 0x4c, G: 0xaf, B: 0x50}},
		{-3, text.RGB{R: 0xff, G: 0xb3, B: 0x00}},
		{0, text.RGB{R: 0xe5, G: 0x39, B: 0x35}},
	}
	at := func(f float32) int { return bar.Min.X + int(f*float32(bar.Dx())) }
	from := float32(0)
	for _, z := range zones {
		to := float32((z.to - meterFloor) / -meterFloor)
		if from >= frac {
			break
		}
		fillRect(&a.frame, image.Rect(at(from), bar.Min.Y, at(min(to, frac)), bar.Max.Y), z.c)
		from = to
	}
	return y + rowH + sz(12)
}

// fillRect fills r, clipped to the frame, with c.
func fillRect(f *text.Frame, r image.Rectangle, c text.RGB) {
	r = r.Intersect(image.Rect(0, 0, f.W, f.H))
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := f.Pix[(y*f.W+r.Min.X)*4 : (y*f.W+r.Max.X)*4]
		for i := 0; i < len(row); i += 4 {
			row[i], row[i+1], row[i+2], row[i+3] = c.B, c.G, c.R, 255
		}
	}
}

func mix(a, b text.RGB, t float32) text.RGB {
	l := func(x, y uint8) uint8 { return uint8(float32(x) + (float32(y)-float32(x))*t + 0.5) }
	return text.RGB{R: l(a.R, b.R), G: l(a.G, b.G), B: l(a.B, b.B)}
}

// perf counts what the window does, for the -meter fixture's summaries and
// the counters printed on s and at exit. Only the UI thread touches it.
type perf struct {
	wakes, paints, renders uint64
	renderTotal, renderMax time.Duration

	mark time.Time // start of the current summary interval
	at   perf0     // counters at mark
}

type perf0 struct {
	wakes, paints, renders uint64
	renderTotal            time.Duration
	ticks, posts, coal     uint64
	shapes, wraps, raster  int
	readouts               int
}

// ms formats d in milliseconds, in ASCII for Windows consoles.
func ms(d time.Duration) string { return fmt.Sprintf("%.3fms", d.Seconds()*1000) }

func (p *perf) rendered(d time.Duration) {
	p.renders++
	p.renderTotal += d
	p.renderMax = max(p.renderMax, d)
}

// summary logs the counters since the last summary, and their totals.
func (p *perf) summary(why string, a *app, feed *meterFeed) {
	now := time.Now()
	cur := perf0{wakes: p.wakes, paints: p.paints, renders: p.renders, renderTotal: p.renderTotal,
		shapes: a.sys.Stats.Shapes, wraps: a.sys.Stats.Wraps, raster: a.sys.Stats.Rasterized}
	if feed != nil {
		cur.ticks, cur.posts, cur.coal = feed.ticks.Load(), feed.posts.Load(), feed.coalesced.Load()
	}
	if a.meter != nil {
		cur.readouts = a.meter.readoutShapes
	}
	if p.mark.IsZero() {
		p.mark = now
	}
	secs := now.Sub(p.mark).Seconds()
	d := func(c, b uint64) string { return fmt.Sprintf("%d (%.1f/s)", c-b, float64(c-b)/max(secs, 1e-9)) }
	avg := time.Duration(0)
	if n := cur.renders - p.at.renders; n > 0 {
		avg = (cur.renderTotal - p.at.renderTotal) / time.Duration(n)
	}
	line := fmt.Sprintf("stats [%s] last %.1fs: paints %s, renders %s, render avg %s",
		why, secs, d(cur.paints, p.at.paints), d(cur.renders, p.at.renders), ms(avg))
	if feed != nil {
		line += fmt.Sprintf("; meter ticks %s, posts %d, coalesced %d, wakes %d",
			d(cur.ticks, p.at.ticks), cur.posts-p.at.posts, cur.coal-p.at.coal, cur.wakes-p.at.wakes)
	}
	line += fmt.Sprintf("; text shaped %d (readouts %d), wrapped %d, rasterized %d",
		cur.shapes-p.at.shapes, cur.readouts-p.at.readouts, cur.wraps-p.at.wraps, cur.raster-p.at.raster)
	line += fmt.Sprintf(" | totals: paints %d, renders %d, render max %s, shaped %d, rasterized %d",
		cur.paints, cur.renders, ms(p.renderMax), cur.shapes, cur.raster)
	if feed != nil {
		line += fmt.Sprintf(", ticks %d, failed posts %d", cur.ticks, feed.failed.Load())
	}
	log.Print(line)
	p.mark, p.at = now, cur
}

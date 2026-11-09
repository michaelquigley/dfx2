package gtk_test

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/michaelquigley/dfx2/internal/appcore"
	"github.com/michaelquigley/dfx2/internal/platform"
	"github.com/michaelquigley/dfx2/internal/platform/gtk"
)

type paintContent struct{ released int }

func (*paintContent) Frame(appcore.FrameRequest) (appcore.FrameResult, error) {
	return appcore.FrameResult{Image: platform.Image{Width: 2, Height: 3, Stride: 8, Scale: 1, Pix: make([]byte, 24)}}, nil
}

func (c *paintContent) Release() { c.released++ }

func TestCairoPaintFailureCancelsDrainsAndReachesRun(t *testing.T) {
	for _, status := range []int32{0, 1, 7} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			h := gtk.NewPaintCoreHost(t, status)
			content := &paintContent{}
			var w *appcore.Window
			var canceled atomic.Bool
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := appcore.RunBackend(ctx, appcore.Options{Mode: appcore.WindowApplication, Logf: func(string, ...any) {}}, func(a *appcore.App) error {
				var err error
				w, err = appcore.OpenContent(a, appcore.WindowOptions{Width: 2, Height: 3}, content)
				if err != nil {
					return err
				}
				return a.Go("owned", func(ctx context.Context) error { <-ctx.Done(); canceled.Store(true); return nil })
			}, h)
			if ctx.Err() != nil {
				t.Fatal("Run reached the hang guard")
			}
			if status == 0 {
				if err != nil {
					t.Fatalf("successful paint failed: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("dfx2 backend pump: GTK paint: window 1, 2x3 image: cairo status %d", status)) {
				t.Fatalf("Run lost Cairo paint failure: %v", err)
			}
			select {
			case <-w.Done():
			default:
				t.Error("window did not finish")
			}
			if !canceled.Load() || content.released != 1 || !h.Released || h.Unrefs != 2 || h.ImagesRemaining() != 0 {
				t.Fatalf("cleanup: canceled=%t content=%d context=%t unrefs=%d images=%d", canceled.Load(), content.released, h.Released, h.Unrefs, h.ImagesRemaining())
			}
			c := h.Counters()
			if c.Windows != 0 || c.Retired != 0 || c.DispatchTargets != 0 {
				t.Fatalf("owned native work remains: %+v", c)
			}
			want := uint64(0)
			if status != 0 {
				want = 1
			}
			if c.PaintFailures != want {
				t.Fatalf("paint failures %d, want %d", c.PaintFailures, want)
			}
		})
	}
}

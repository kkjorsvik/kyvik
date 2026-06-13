package browser

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

type chromedpEngine struct {
	allowInsecure bool
	mu            sync.Mutex
	allocCtx      context.Context
	allocCancel   context.CancelFunc
}

func newChromedpEngine(allowInsecure bool) (browserEngine, error) {
	e := &chromedpEngine{allowInsecure: allowInsecure}
	if _, err := e.ensureAllocator(); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *chromedpEngine) allocatorOpts() []chromedp.ExecAllocatorOption {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Headless,
		chromedp.NoSandbox,
		chromedp.DisableGPU,
	)
	if e.allowInsecure {
		opts = append(opts, chromedp.Flag("ignore-certificate-errors", true))
	}
	return opts
}

// ensureAllocator returns the current allocator context, creating a new one if
// it is nil or has already been cancelled (i.e. the browser crashed).
func (e *chromedpEngine) ensureAllocator() (context.Context, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.allocCtx != nil && e.allocCtx.Err() == nil {
		return e.allocCtx, nil
	}
	if e.allocCancel != nil {
		e.allocCancel()
	}
	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), e.allocatorOpts()...)
	e.allocCtx = allocCtx
	e.allocCancel = cancel
	return allocCtx, nil
}

// resetAllocator cancels the current allocator and creates a fresh one,
// used after detecting a browser crash.
func (e *chromedpEngine) resetAllocator() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.allocCancel != nil {
		e.allocCancel()
	}
	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), e.allocatorOpts()...)
	e.allocCtx = allocCtx
	e.allocCancel = cancel
}

func (e *chromedpEngine) LoadHTML(ctx context.Context, url string, viewportWidth, viewportHeight int, timeout, settle time.Duration) (string, error) {
	for attempt := range 2 {
		allocCtx, err := e.ensureAllocator()
		if err != nil {
			return "", err
		}
		result, err := e.runLoadHTML(allocCtx, url, viewportWidth, viewportHeight, timeout, settle)
		if err != nil && attempt == 0 && isBrowserDisconnected(err) {
			e.resetAllocator()
			continue
		}
		return result, err
	}
	return "", fmt.Errorf("browser unavailable after restart")
}

func (e *chromedpEngine) runLoadHTML(allocCtx context.Context, url string, viewportWidth, viewportHeight int, timeout, settle time.Duration) (string, error) {
	cctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	cctx, cancelTimeout := context.WithTimeout(cctx, timeout)
	defer cancelTimeout()

	var html string
	actions := []chromedp.Action{
		chromedp.EmulateViewport(int64(viewportWidth), int64(viewportHeight)),
		chromedp.Navigate(url),
		chromedp.WaitReady("body", chromedp.ByQuery),
	}
	if settle > 0 {
		actions = append(actions, chromedp.Sleep(settle))
	}
	actions = append(actions, chromedp.OuterHTML("html", &html, chromedp.ByQuery))
	if err := chromedp.Run(cctx, actions...); err != nil {
		return "", err
	}
	return html, nil
}

func (e *chromedpEngine) Screenshot(ctx context.Context, url string, viewportWidth, viewportHeight int, timeout, settle time.Duration, fullPage bool, maxWidth, maxHeight int) ([]byte, bool, error) {
	for attempt := range 2 {
		allocCtx, err := e.ensureAllocator()
		if err != nil {
			return nil, false, err
		}
		img, truncated, err := e.runScreenshot(allocCtx, url, viewportWidth, viewportHeight, timeout, settle, fullPage, maxWidth, maxHeight)
		if err != nil && attempt == 0 && isBrowserDisconnected(err) {
			e.resetAllocator()
			continue
		}
		return img, truncated, err
	}
	return nil, false, fmt.Errorf("browser unavailable after restart")
}

func (e *chromedpEngine) runScreenshot(allocCtx context.Context, url string, viewportWidth, viewportHeight int, timeout, settle time.Duration, fullPage bool, maxWidth, maxHeight int) ([]byte, bool, error) {
	cctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	cctx, cancelTimeout := context.WithTimeout(cctx, timeout)
	defer cancelTimeout()

	var dims struct {
		W int `json:"w"`
		H int `json:"h"`
	}
	actions := []chromedp.Action{
		chromedp.EmulateViewport(int64(viewportWidth), int64(viewportHeight)),
		chromedp.Navigate(url),
		chromedp.WaitReady("body", chromedp.ByQuery),
	}
	if settle > 0 {
		actions = append(actions, chromedp.Sleep(settle))
	}
	actions = append(actions, chromedp.Evaluate(`({w: Math.max(document.body.scrollWidth, document.documentElement.scrollWidth),
h: Math.max(document.body.scrollHeight, document.documentElement.scrollHeight)})`, &dims))

	if err := chromedp.Run(cctx, actions...); err != nil {
		return nil, false, err
	}

	truncated := false
	if fullPage {
		if dims.H > maxHeight || dims.W > maxWidth {
			truncated = true
			fullPage = false
		}
	}

	var img []byte
	var err error
	if fullPage {
		img, err = page.CaptureScreenshot().
			WithFormat(page.CaptureScreenshotFormatPng).
			WithCaptureBeyondViewport(true).
			Do(cctx)
	} else {
		img, err = page.CaptureScreenshot().
			WithFormat(page.CaptureScreenshotFormatPng).
			WithCaptureBeyondViewport(false).
			Do(cctx)
	}
	if err != nil {
		return nil, false, fmt.Errorf("capture screenshot: %w", err)
	}

	return img, truncated, nil
}

func (e *chromedpEngine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.allocCancel != nil {
		e.allocCancel()
		e.allocCancel = nil
		e.allocCtx = nil
	}
	return nil
}

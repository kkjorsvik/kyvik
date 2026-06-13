package browser

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

type rodEngine struct {
	allowInsecure bool
	mu            sync.Mutex
	browser       *rod.Browser
}

func newRodEngine(allowInsecure bool) (browserEngine, error) {
	engine := &rodEngine{allowInsecure: allowInsecure}
	if _, err := engine.ensureBrowser(); err != nil {
		return nil, err
	}
	return engine, nil
}

func (e *rodEngine) ensureBrowser() (*rod.Browser, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.browser != nil {
		return e.browser, nil
	}

	launch := launcher.New().Headless(true)
	u, err := launch.Launch()
	if err != nil {
		return nil, err
	}
	browser := rod.New().ControlURL(u)
	if err := browser.Connect(); err != nil {
		return nil, err
	}
	if e.allowInsecure {
		_ = browser.IgnoreCertErrors(true)
	}
	e.browser = browser
	return browser, nil
}

// resetBrowser closes and clears the cached browser so the next call to
// ensureBrowser will launch a fresh instance.
func (e *rodEngine) resetBrowser() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.browser != nil {
		_ = e.browser.Close()
		e.browser = nil
	}
}

func (e *rodEngine) LoadHTML(ctx context.Context, url string, viewportWidth, viewportHeight int, timeout, settle time.Duration) (string, error) {
	var html string
	err := e.withPage(ctx, timeout, func(page *rod.Page) error {
		if err := page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
			Width:             viewportWidth,
			Height:            viewportHeight,
			DeviceScaleFactor: 1,
			Mobile:            false,
		}); err != nil {
			return err
		}
		if err := page.Navigate(url); err != nil {
			return err
		}
		if err := page.WaitLoad(); err != nil {
			return err
		}
		if settle > 0 {
			time.Sleep(settle)
		}
		var err error
		html, err = page.HTML()
		return err
	})
	return html, err
}

func (e *rodEngine) Screenshot(ctx context.Context, url string, viewportWidth, viewportHeight int, timeout, settle time.Duration, fullPage bool, maxWidth, maxHeight int) ([]byte, bool, error) {
	var img []byte
	var truncated bool
	err := e.withPage(ctx, timeout, func(page *rod.Page) error {
		if err := page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
			Width:             viewportWidth,
			Height:            viewportHeight,
			DeviceScaleFactor: 1,
			Mobile:            false,
		}); err != nil {
			return err
		}
		if err := page.Navigate(url); err != nil {
			return err
		}
		if err := page.WaitLoad(); err != nil {
			return err
		}
		if settle > 0 {
			time.Sleep(settle)
		}

		if fullPage {
			scrollHeight, scrollWidth, err := pageDimensions(page)
			if err == nil {
				if scrollHeight > maxHeight || scrollWidth > maxWidth {
					truncated = true
					fullPage = false
				}
			}
		}

		var err error
		img, err = page.Screenshot(fullPage, &proto.PageCaptureScreenshot{
			Format: proto.PageCaptureScreenshotFormatPng,
		})
		return err
	})
	return img, truncated, err
}

func (e *rodEngine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.browser == nil {
		return nil
	}
	err := e.browser.Close()
	e.browser = nil
	return err
}

// withPage executes fn with a fresh browser tab. If the browser has crashed
// (detected via isBrowserDisconnected), it resets the instance and retries once.
func (e *rodEngine) withPage(ctx context.Context, timeout time.Duration, fn func(page *rod.Page) error) error {
	for attempt := range 2 {
		err := e.tryWithPage(ctx, timeout, fn)
		if err != nil && attempt == 0 && isBrowserDisconnected(err) {
			e.resetBrowser()
			continue
		}
		return err
	}
	return fmt.Errorf("browser unavailable after restart")
}

// tryWithPage opens a single tab, applies the caller's context and timeout,
// runs fn, and closes the tab on return.
func (e *rodEngine) tryWithPage(ctx context.Context, timeout time.Duration, fn func(page *rod.Page) error) error {
	browser, err := e.ensureBrowser()
	if err != nil {
		return err
	}
	page, err := browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return err
	}
	defer page.Close()
	page = page.Context(ctx).Timeout(timeout)
	return fn(page)
}

func pageDimensions(page *rod.Page) (int, int, error) {
	res, err := page.Evaluate(rod.Eval(`() => ({
		h: Math.max(document.body.scrollHeight, document.documentElement.scrollHeight),
		w: Math.max(document.body.scrollWidth, document.documentElement.scrollWidth)
	})`))
	if err != nil {
		return 0, 0, err
	}
	var out struct {
		H int `json:"h"`
		W int `json:"w"`
	}
	if err := res.Value.Unmarshal(&out); err != nil {
		return 0, 0, err
	}
	return out.H, out.W, nil
}

func (e *rodEngine) String() string {
	return fmt.Sprintf("rodEngine(insecure=%v)", e.allowInsecure)
}

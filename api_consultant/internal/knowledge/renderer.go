package knowledge

import (
	"context"
	"fmt"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
)

const (
	renderTimeout     = 30 * time.Second
	renderStableDur   = 500 * time.Millisecond
	maxConcurrentTabs = 3
)

// blockedResourceTypes lists network resource types the renderer skips
// to save bandwidth, memory, and speed up page loads.
var blockedResourceTypes = []proto.NetworkResourceType{
	proto.NetworkResourceTypeImage,
	proto.NetworkResourceTypeFont,
	proto.NetworkResourceTypeStylesheet,
	proto.NetworkResourceTypeMedia,
}

// RodRenderer renders JavaScript-heavy pages via a headless Chromium instance
// managed by Rod. Create with NewRodRenderer; call Close when done.
type RodRenderer struct {
	browser *rod.Browser
	tabSem  chan struct{}
}

// NewRodRenderer launches a headless Chromium process via Rod's launcher.
// Returns an error if Chrome/Chromium cannot be started.
func NewRodRenderer() (*RodRenderer, error) {
	u, err := launcher.New().
		Headless(true).
		Set("disable-gpu").
		Set("no-sandbox").
		Set("disable-dev-shm-usage").
		Launch()
	if err != nil {
		return nil, fmt.Errorf("launch headless browser: %w", err)
	}

	browser := rod.New().ControlURL(u)
	if err := browser.Connect(); err != nil {
		return nil, fmt.Errorf("connect to headless browser: %w", err)
	}

	return &RodRenderer{
		browser: browser,
		tabSem:  make(chan struct{}, maxConcurrentTabs),
	}, nil
}

// Render navigates to pageURL, waits for JS to execute and the DOM to
// stabilize, then returns the rendered HTML.
func (r *RodRenderer) Render(ctx context.Context, pageURL string) (string, error) {
	select {
	case r.tabSem <- struct{}{}:
		defer func() { <-r.tabSem }()
	case <-ctx.Done():
		return "", ctx.Err()
	}

	page, err := stealth.Page(r.browser)
	if err != nil {
		return "", fmt.Errorf("create tab: %w", err)
	}
	defer page.Close()

	renderCtx, cancel := context.WithTimeout(ctx, renderTimeout)
	defer cancel()
	page = page.Context(renderCtx)

	// Block unnecessary resources (images, fonts, CSS, media)
	router := page.HijackRequests()
	for _, rt := range blockedResourceTypes {
		rt := rt
		_ = router.Add("*", rt, func(h *rod.Hijack) {
			h.Response.Fail(proto.NetworkErrorReasonBlockedByClient)
		})
	}
	go router.Run()
	defer router.MustStop()

	if err := page.Navigate(pageURL); err != nil {
		return "", fmt.Errorf("navigate to %s: %w", pageURL, err)
	}

	// WaitStable waits until the page DOM stops changing for the given
	// duration — replaces the blind 2s sleep used by the old chromedp renderer.
	_ = page.WaitStable(renderStableDur)

	html, err := page.HTML()
	if err != nil {
		return "", fmt.Errorf("get HTML from %s: %w", pageURL, err)
	}

	return html, nil
}

// Close shuts down the headless browser process.
func (r *RodRenderer) Close() {
	_ = r.browser.Close()
}

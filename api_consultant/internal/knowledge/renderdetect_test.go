package knowledge

import "testing"

func TestNeedsRenderingReactApp(t *testing.T) {
	page := []byte(`<!DOCTYPE html><html><head></head><body>
		<div id="root"></div>
		<noscript>You need to enable JavaScript to run this app.</noscript>
		<script src="/static/js/main.abc123.js"></script>
	</body></html>`)
	if !needsRendering(page) {
		t.Fatal("expected React SPA to need rendering")
	}
}

func TestNeedsRenderingNextJS(t *testing.T) {
	page := []byte(`<!DOCTYPE html><html><head>
		<meta name="generator" content="Next.js">
	</head><body><div id="__next"></div>
		<script src="/_next/static/chunks/main.js"></script>
	</body></html>`)
	if !needsRendering(page) {
		t.Fatal("expected Next.js app to need rendering")
	}
}

func TestNeedsRenderingStaticPage(t *testing.T) {
	page := []byte(`<!DOCTYPE html><html><head><title>Docs</title></head><body>
		<h1>Welcome to the documentation</h1>
		<p>This is a static page with enough content to read. It contains
		multiple paragraphs of real text that a human would find useful.
		The crawler should not waste Chrome resources on this page because
		all the content is already present in the HTML source code.</p>
		<p>Additional paragraph for word count threshold.</p>
	</body></html>`)
	if needsRendering(page) {
		t.Fatal("static page with real content should not need rendering")
	}
}

func TestNeedsRenderingEmptyData(t *testing.T) {
	if needsRendering(nil) {
		t.Fatal("nil data should not need rendering")
	}
	if needsRendering([]byte{}) {
		t.Fatal("empty data should not need rendering")
	}
}

func TestNeedsRenderingVueApp(t *testing.T) {
	page := []byte(`<!DOCTYPE html><html><head></head><body>
		<div id="app" data-v-abc123></div>
		<noscript>JavaScript is required.</noscript>
		<script src="/js/app.js"></script>
	</body></html>`)
	if !needsRendering(page) {
		t.Fatal("expected Vue SPA to need rendering")
	}
}

func TestNeedsRenderingAngularApp(t *testing.T) {
	page := []byte(`<!DOCTYPE html><html ng-app="myApp"><head></head><body>
		<div id="root"></div>
		<noscript>Enable JS</noscript>
	</body></html>`)
	if !needsRendering(page) {
		t.Fatal("expected Angular app to need rendering")
	}
}

package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestNewHandlerRendersTemplateIndex(t *testing.T) {
	t.Parallel()

	uiFS := fstest.MapFS{
		"templates/index.gohtml":          {Data: []byte(`{{define "index"}}<html><body>{{template "shell" .}}</body></html>{{end}}`)},
		"templates/partials/shell.gohtml": {Data: []byte(`{{define "shell"}}rendered shell{{end}}`)},
		"static/css/app.css":              {Data: []byte("body { color: red; }")},
		"logo.png":                        {Data: []byte("png")},
	}

	h := NewHandler(nil, nil, nil, uiFS)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Fatalf("content-type = %q, want text/html", got)
	}
	if !strings.Contains(rr.Body.String(), "rendered shell") {
		t.Fatalf("body = %q, want rendered template content", rr.Body.String())
	}
}

func TestNewHandlerServesStaticAssetsAndLogo(t *testing.T) {
	t.Parallel()

	uiFS := fstest.MapFS{
		"templates/index.gohtml":          {Data: []byte(`{{define "index"}}ok{{end}}`)},
		"templates/partials/shell.gohtml": {Data: []byte(`{{define "shell"}}{{end}}`)},
		"static/js/app.js":                {Data: []byte("console.log('ok');")},
		"logo.png":                        {Data: []byte("png")},
	}

	h := NewHandler(nil, nil, nil, uiFS)

	assetReq := httptest.NewRequest(http.MethodGet, "/static/js/app.js", nil)
	assetRR := httptest.NewRecorder()
	h.ServeHTTP(assetRR, assetReq)
	if assetRR.Code != http.StatusOK {
		t.Fatalf("static asset status = %d, want %d", assetRR.Code, http.StatusOK)
	}
	if !strings.Contains(assetRR.Body.String(), "console.log") {
		t.Fatalf("static asset body = %q", assetRR.Body.String())
	}

	logoReq := httptest.NewRequest(http.MethodGet, "/logo.png", nil)
	logoRR := httptest.NewRecorder()
	h.ServeHTTP(logoRR, logoReq)
	if logoRR.Code != http.StatusOK {
		t.Fatalf("logo status = %d, want %d", logoRR.Code, http.StatusOK)
	}
	if logoRR.Body.String() != "png" {
		t.Fatalf("logo body = %q, want png", logoRR.Body.String())
	}
}

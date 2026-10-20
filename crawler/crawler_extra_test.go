package crawler_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"code/crawler"
)

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func htmlResp(r *http.Request, status int, ctype string, body io.ReadCloser) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Status:        http.StatusText(status),
		Header:        http.Header{"Content-Type": {ctype}},
		Body:          body,
		ContentLength: -1,
		Request:       r,
	}
}

// Эталонный отчёт из задания (шаг 10): сравнение без учёта форматирования.
func TestGoldenReport(t *testing.T) {
	s := newSite(map[string]spec{
		"https://example.com": page(`<html><head><title>Example title</title>
			<meta name="description" content="Example description"></head>
			<body><h1>Hi</h1><a href="/missing">m</a><img src="/static/logo.png"></body></html>`),
		"https://example.com/static/logo.png": {status: 200, ctype: "image/png", header: http.Header{"Content-Length": {"12345"}}},
	})
	data, err := crawler.Analyze(context.Background(), crawler.Options{
		URL: "https://example.com", Depth: 1, HTTPClient: &http.Client{Transport: s},
	})
	if err != nil {
		t.Fatal(err)
	}

	var got crawler.Report
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2024, 6, 1, 12, 34, 56, 0, time.UTC)
	got.GeneratedAt = fixed
	got.Pages[0].DiscoveredAt = fixed
	gotJSON, _ := json.Marshal(got)

	const golden = `{
  "root_url": "https://example.com",
  "depth": 1,
  "generated_at": "2024-06-01T12:34:56Z",
  "pages": [{
    "url": "https://example.com",
    "depth": 0,
    "http_status": 200,
    "status": "ok",
    "seo": {
      "has_title": true,
      "title": "Example title",
      "has_description": true,
      "description": "Example description",
      "has_h1": true
    },
    "broken_links": [{"url": "https://example.com/missing", "status_code": 404, "error": "Not Found"}],
    "assets": [{"url": "https://example.com/static/logo.png", "type": "image", "status_code": 200, "size_bytes": 12345}],
    "discovered_at": "2024-06-01T12:34:56Z"
  }]
}`
	var a, b any
	_ = json.Unmarshal(gotJSON, &a)
	if err := json.Unmarshal([]byte(golden), &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("report differs from golden:\n%s", gotJSON)
	}
}

// nil HTTPClient и нулевые/отрицательные опции → значения по умолчанию.
func TestDefaultsWithRealLocalServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<title>Real</title>")
	}))
	defer srv.Close()

	data, err := crawler.Analyze(context.Background(), crawler.Options{
		URL: srv.URL, Depth: -1, Retries: -1, Concurrency: -1, Timeout: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	var rep crawler.Report
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Depth != 10 || len(rep.Pages) != 1 || rep.Pages[0].SEO.Title != "Real" {
		t.Fatalf("unexpected report: %+v", rep)
	}
}

func TestUserAgentHeader(t *testing.T) {
	var mu sync.Mutex
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = r.Header.Get("User-Agent")
		mu.Unlock()
		w.Header().Set("Content-Type", "text/html")
	}))
	defer srv.Close()

	if _, err := crawler.Analyze(context.Background(), crawler.Options{URL: srv.URL, Depth: 1, UserAgent: "my-bot"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got != "my-bot" {
		t.Errorf("User-Agent = %q, want my-bot", got)
	}
}

func TestInvalidURLWithSpaceInHost(t *testing.T) {
	if _, err := crawler.Analyze(context.Background(), crawler.Options{URL: "http://a b/"}); err == nil {
		t.Fatal("expected error")
	}
}

// Любой успешный ответ разбирается как HTML (например, feed.xml с <title>).
func TestXMLBodyIsParsedForSEO(t *testing.T) {
	s := newSite(map[string]spec{"http://site.test": {status: 200, ctype: "application/xml",
		body: `<rss><channel><title>Feed</title></channel></rss>`}})
	p := analyze(t, s, crawler.Options{URL: "http://site.test", Depth: 1}).Pages[0]
	if p.Status != "ok" || !p.SEO.HasTitle || p.SEO.Title != "Feed" || p.SEO.HasH1 {
		t.Fatalf("unexpected page: %+v", p)
	}
}

// Страница, которую не удалось скачать: assets и broken_links = null, error есть.
func TestFailedPageHasNullArrays(t *testing.T) {
	s := newSite(map[string]spec{"http://site.test": {err: errors.New("no such host")}})
	data, err := crawler.Analyze(context.Background(), crawler.Options{URL: "http://site.test", Depth: 1, HTTPClient: &http.Client{Transport: s}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"assets":null`, `"broken_links":null`, `"error":"Get`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %s in %s", want, data)
		}
	}
}

// Успешная страница: нет ключа error, массивы пустые, а не null.
func TestOKPageOmitsEmptyError(t *testing.T) {
	s := newSite(map[string]spec{"http://site.test": page("<p>x</p>")})
	data, _ := crawler.Analyze(context.Background(), crawler.Options{URL: "http://site.test", Depth: 1, HTTPClient: &http.Client{Transport: s}})
	if strings.Contains(string(data), `"error"`) {
		t.Errorf("empty error must be omitted: %s", data)
	}
}

// Порядок: страницы по (depth, url); ассеты по типу image, script, style.
func TestDeterministicOrder(t *testing.T) {
	s := newSite(map[string]spec{
		"http://site.test": page(`<link rel="stylesheet" href="/s.css"><script src="/a.js"></script><img src="/i.png">
			<a href="/z">z</a><a href="/b">b</a>`),
		"http://site.test/z":     page("z"),
		"http://site.test/b":     page("b"),
		"http://site.test/s.css": {status: 200, ctype: "text/css"},
		"http://site.test/a.js":  {status: 200, ctype: "text/javascript"},
		"http://site.test/i.png": {status: 200, ctype: "image/png"},
	})
	rep := analyze(t, s, crawler.Options{URL: "http://site.test", Depth: 2})

	var pages []string
	for _, p := range rep.Pages {
		pages = append(pages, p.URL)
	}
	if strings.Join(pages, ",") != "http://site.test,http://site.test/b,http://site.test/z" {
		t.Errorf("pages order: %v", pages)
	}
	var types []string
	for _, a := range rep.Pages[0].Assets {
		types = append(types, a.Type)
	}
	if strings.Join(types, ",") != "image,script,style" {
		t.Errorf("assets order: %v", types)
	}
}

func TestUnknownStatusTextFallback(t *testing.T) {
	s := newSite(map[string]spec{
		"http://site.test":       page(`<a href="/weird">w</a>`),
		"http://site.test/weird": {status: 499},
	})
	bl := analyze(t, s, crawler.Options{URL: "http://site.test", Depth: 1}).Pages[0].BrokenLinks
	if len(bl) != 1 || bl[0].StatusCode != 499 || bl[0].Error != "HTTP 499" {
		t.Fatalf("broken = %+v", bl)
	}
}

func TestPageBodyReadError(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return htmlResp(r, 200, "text/html", io.NopCloser(errReader{})), nil
	})
	data, err := crawler.Analyze(context.Background(), crawler.Options{URL: "http://site.test", Depth: 1, HTTPClient: &http.Client{Transport: rt}})
	if err != nil {
		t.Fatal(err)
	}
	var rep crawler.Report
	_ = json.Unmarshal(data, &rep)
	if len(rep.Pages) != 1 || rep.Pages[0].Status != "error" || !strings.Contains(rep.Pages[0].Error, "boom") {
		t.Fatalf("unexpected: %+v", rep.Pages)
	}
}

func TestAssetSizeEdgeCases(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/a.js": // размер известен только из resp.ContentLength
			resp := htmlResp(r, 200, "text/javascript", io.NopCloser(strings.NewReader("")))
			resp.ContentLength = 7
			return resp, nil
		case "/bad.png": // размер определить нельзя: ошибка чтения тела
			return htmlResp(r, 200, "image/png", io.NopCloser(errReader{})), nil
		default:
			return htmlResp(r, 200, "text/html", io.NopCloser(strings.NewReader(
				`<script src="/a.js"></script><img src="/bad.png">`))), nil
		}
	})
	data, err := crawler.Analyze(context.Background(), crawler.Options{URL: "http://site.test", Depth: 1, HTTPClient: &http.Client{Transport: rt}})
	if err != nil {
		t.Fatal(err)
	}
	var rep crawler.Report
	_ = json.Unmarshal(data, &rep)

	assets := map[string]crawler.Asset{}
	for _, a := range rep.Pages[0].Assets {
		assets[a.URL] = a
	}
	if a := assets["http://site.test/a.js"]; a.SizeBytes != 7 || a.Error != "" {
		t.Errorf("a.js: %+v", a)
	}
	if a := assets["http://site.test/bad.png"]; a.SizeBytes != 0 || !strings.Contains(a.Error, "boom") {
		t.Errorf("bad.png: %+v", a)
	}
}

func TestCancelDuringRetryBackoff(t *testing.T) {
	s := newSite(map[string]spec{"http://site.test": {ctype: "text/html", seq: []int{500}}})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	start := time.Now()
	data, err := crawler.Analyze(ctx, crawler.Options{
		URL: "http://site.test", Depth: 1, Retries: 5, RetryBackoff: time.Hour,
		HTTPClient: &http.Client{Transport: s},
	})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("retry backoff ignores cancel")
	}
	if !json.Valid(data) {
		t.Fatal("invalid JSON after cancel")
	}
	if n := s.hitCount("http://site.test"); n != 1 {
		t.Errorf("hits = %d, want 1 (no more attempts after cancel)", n)
	}
}

// RPS перекрывает Delay: при Delay=1h обход закончился бы только через часы.
func TestRPSOverridesDelay(t *testing.T) {
	done := make(chan crawler.Report, 1)
	go func() {
		done <- analyze(t, depthSite(), crawler.Options{URL: "http://site.test", Depth: 2, Delay: time.Hour, RPS: 200})
	}()
	select {
	case rep := <-done:
		if len(rep.Pages) != 3 {
			t.Errorf("pages = %d, want 3", len(rep.Pages))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RPS did not override Delay")
	}
}

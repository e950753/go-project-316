package crawler_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"code/crawler"
)

// --- фейковый сервер: подменяет http.Transport, сети нет ---

type spec struct {
	status int
	ctype  string
	body   string
	header http.Header
	err    error
	seq    []int // статусы по номеру обращения (для retry-тестов)
}

type site struct {
	mu    sync.Mutex
	pages map[string]spec
	hits  map[string]int
	times []time.Time
}

func newSite(pages map[string]spec) *site {
	return &site{pages: pages, hits: map[string]int{}}
}

func page(body string) spec { return spec{status: 200, ctype: "text/html; charset=utf-8", body: body} }

func (s *site) RoundTrip(r *http.Request) (*http.Response, error) {
	key := r.URL.String()

	s.mu.Lock()
	s.hits[key]++
	hit := s.hits[key]
	s.times = append(s.times, time.Now())
	sp, ok := s.pages[key]
	s.mu.Unlock()

	if !ok {
		sp = spec{status: 404}
	}
	if sp.err != nil {
		return nil, sp.err
	}
	status := sp.status
	if len(sp.seq) > 0 {
		status = sp.seq[min(hit-1, len(sp.seq)-1)]
	}
	h := http.Header{}
	for k, v := range sp.header {
		h[k] = v
	}
	if sp.ctype != "" {
		h.Set("Content-Type", sp.ctype)
	}
	return &http.Response{
		StatusCode:    status,
		Status:        http.StatusText(status),
		Header:        h,
		Body:          io.NopCloser(strings.NewReader(sp.body)),
		ContentLength: -1,
		Request:       r,
	}, nil
}

func (s *site) hitCount(u string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[u]
}

func analyze(t *testing.T, s *site, opts crawler.Options) crawler.Report {
	t.Helper()
	opts.HTTPClient = &http.Client{Transport: s}
	if opts.RetryBackoff == 0 {
		opts.RetryBackoff = time.Millisecond
	}
	data, err := crawler.Analyze(context.Background(), opts)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	var rep crawler.Report
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, data)
	}
	return rep
}

// --- HTTP: успех, ошибки, сеть, retry ---

func TestSuccess(t *testing.T) {
	s := newSite(map[string]spec{"http://site.test": page("<html><title>T</title></html>")})
	rep := analyze(t, s, crawler.Options{URL: "http://site.test", Depth: 1})

	if len(rep.Pages) != 1 {
		t.Fatalf("pages = %d, want 1", len(rep.Pages))
	}
	p := rep.Pages[0]
	if p.HTTPStatus != 200 || p.Status != "ok" || p.Error != "" || p.Depth != 0 {
		t.Fatalf("unexpected page: %+v", p)
	}
}

func TestHTTPErrorsAndNetworkFailure(t *testing.T) {
	cases := map[string]spec{
		"404":     {status: 404},
		"500":     {status: 500},
		"network": {err: errors.New("network is unreachable")},
	}
	for name, sp := range cases {
		t.Run(name, func(t *testing.T) {
			s := newSite(map[string]spec{"http://site.test": sp})
			rep := analyze(t, s, crawler.Options{URL: "http://site.test", Depth: 1})
			if len(rep.Pages) != 1 {
				t.Fatalf("pages = %d", len(rep.Pages))
			}
			p := rep.Pages[0]
			if p.Status != "error" || p.Error == "" {
				t.Fatalf("expected error page, got %+v", p)
			}
			if name != "network" && p.HTTPStatus != sp.status {
				t.Fatalf("http_status = %d, want %d", p.HTTPStatus, sp.status)
			}
		})
	}
}

func TestTimeout(t *testing.T) {
	slow := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	data, err := crawler.Analyze(context.Background(), crawler.Options{
		URL: "http://site.test", Depth: 1, Timeout: 30 * time.Millisecond,
		HTTPClient: &http.Client{Transport: slow},
	})
	if err != nil {
		t.Fatal(err)
	}
	var rep crawler.Report
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Pages) != 1 || rep.Pages[0].Status != "error" {
		t.Fatalf("expected one error page, got %+v", rep.Pages)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestInvalidURL(t *testing.T) {
	for _, u := range []string{"", "example.com", "ftp://example.com", "http://"} {
		if _, err := crawler.Analyze(context.Background(), crawler.Options{URL: u}); err == nil {
			t.Errorf("expected error for %q", u)
		}
	}
}

func TestRetries(t *testing.T) {
	tests := []struct {
		name       string
		retries    int
		seq        []int
		wantStatus string
		wantHits   int
	}{
		{"error then success", 2, []int{500, 200}, "ok", 2},
		{"retries exhausted", 1, []int{500, 500, 200}, "error", 2},
		{"always 503", 2, []int{503}, "error", 3},
		{"429 is retried", 1, []int{429, 200}, "ok", 2},
		{"404 is not retried", 3, []int{404}, "error", 1},
		{"no retries", 0, []int{500}, "error", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSite(map[string]spec{"http://site.test": {ctype: "text/html", seq: tt.seq}})
			rep := analyze(t, s, crawler.Options{URL: "http://site.test", Depth: 1, Retries: tt.retries})
			if got := rep.Pages[0].Status; got != tt.wantStatus {
				t.Errorf("status = %s, want %s", got, tt.wantStatus)
			}
			if got := s.hitCount("http://site.test"); got != tt.wantHits || got > tt.retries+1 {
				t.Errorf("hits = %d, want %d (max %d)", got, tt.wantHits, tt.retries+1)
			}
		})
	}
}

// --- битые ссылки ---

func TestBrokenLinks(t *testing.T) {
	s := newSite(map[string]spec{
		"http://site.test": page(`<a href="/ok">ok</a><a href="/missing">m</a>
			<a href="mailto:a@b.c">x</a><a href="">e</a><a href="ftp://x/y">f</a>
			<a href="#top">t</a><a href="http://down.test/x">d</a>`),
		"http://site.test/ok":      page("ok"),
		"http://down.test/x":       {err: errors.New("dial tcp: lookup down.test: no such host")},
		"http://site.test/missing": {status: 404},
	})
	rep := analyze(t, s, crawler.Options{URL: "http://site.test", Depth: 1})

	got := rep.Pages[0].BrokenLinks
	if len(got) != 2 {
		t.Fatalf("broken = %+v, want 2 entries", got)
	}
	if got[0].URL != "http://site.test/missing" || got[0].StatusCode != 404 || got[0].Error != "Not Found" {
		t.Errorf("bad first entry: %+v", got[0])
	}
	if got[1].URL != "http://down.test/x" || got[1].StatusCode != 0 || !strings.Contains(got[1].Error, "no such host") {
		t.Errorf("bad second entry: %+v", got[1])
	}
}

// --- crawler.SEO ---

func TestSEOPresent(t *testing.T) {
	s := newSite(map[string]spec{"http://site.test": page(
		`<html><head><title> Tom &amp; Jerry </title>
		<meta name="description" content="Cats &amp; mice"></head><body><h1>Hi</h1></body></html>`)})
	seo := analyze(t, s, crawler.Options{URL: "http://site.test", Depth: 1}).Pages[0].SEO

	want := crawler.SEO{HasTitle: true, Title: "Tom & Jerry", HasDescription: true, Description: "Cats & mice", HasH1: true}
	if seo != want {
		t.Fatalf("seo = %+v, want %+v", seo, want)
	}
}

func TestSEOAbsent(t *testing.T) {
	s := newSite(map[string]spec{"http://site.test": page(`<html><body><p>nothing</p></body></html>`)})
	seo := analyze(t, s, crawler.Options{URL: "http://site.test", Depth: 1}).Pages[0].SEO
	if seo != (crawler.SEO{}) {
		t.Fatalf("seo = %+v, want zero value", seo)
	}
}

// --- глубина ---

func depthSite() *site {
	return newSite(map[string]spec{
		"http://site.test":    page(`<a href="/a">a</a><a href="/b">b</a><a href="/a#x">dup</a><a href="http://other.test/x">ext</a>`),
		"http://site.test/a":  page(`<a href="/">root</a><a href="/b">b</a>`),
		"http://site.test/b":  page(`<a href="/c">c</a>`),
		"http://site.test/c":  page(`end`),
		"http://other.test/x": page(`external`),
	})
}

func urls(r crawler.Report) []string {
	var out []string
	for _, p := range r.Pages {
		out = append(out, p.URL)
	}
	sort.Strings(out)
	return out
}

func TestDepth(t *testing.T) {
	tests := []struct {
		depth int
		want  []string
	}{
		{1, []string{"http://site.test"}},
		{2, []string{"http://site.test", "http://site.test/a", "http://site.test/b"}},
		{3, []string{"http://site.test", "http://site.test/a", "http://site.test/b", "http://site.test/c"}},
	}
	for _, tt := range tests {
		rep := analyze(t, depthSite(), crawler.Options{URL: "http://site.test", Depth: tt.depth})
		got := urls(rep)
		if strings.Join(got, ",") != strings.Join(tt.want, ",") {
			t.Errorf("depth %d: pages = %v, want %v", tt.depth, got, tt.want)
		}
	}
}

func TestPageDepthField(t *testing.T) {
	rep := analyze(t, depthSite(), crawler.Options{URL: "http://site.test", Depth: 3})
	want := map[string]int{"http://site.test": 0, "http://site.test/a": 1, "http://site.test/b": 1, "http://site.test/c": 2}
	for _, p := range rep.Pages {
		if p.Depth != want[p.URL] {
			t.Errorf("%s depth = %d, want %d", p.URL, p.Depth, want[p.URL])
		}
	}
}

// --- ассеты ---

func TestAssets(t *testing.T) {
	s := newSite(map[string]spec{
		"http://site.test": page(`<link rel="stylesheet" href="/s.css"><a href="/a">a</a>
			<img src="/logo.png"><img src="/logo.png"><script src="/app.js"></script>`),
		"http://site.test/a":        page(`<img src="/logo.png">`),
		"http://site.test/logo.png": {status: 200, ctype: "image/png", body: "abcde"}, // без Content-Length
		"http://site.test/app.js":   {status: 200, ctype: "text/javascript", body: "x", header: http.Header{"Content-Length": {"999"}}},
		"http://site.test/s.css":    {status: 404},
	})
	rep := analyze(t, s, crawler.Options{URL: "http://site.test", Depth: 2})

	if n := s.hitCount("http://site.test/logo.png"); n != 1 {
		t.Errorf("logo.png requested %d times, want 1", n)
	}

	assets := map[string]crawler.Asset{}
	for _, a := range rep.Pages[0].Assets {
		assets[a.URL] = a
	}
	if len(rep.Pages[0].Assets) != 3 {
		t.Fatalf("assets = %+v, want 3 unique", rep.Pages[0].Assets)
	}
	if a := assets["http://site.test/logo.png"]; a.Type != "image" || a.SizeBytes != 5 || a.StatusCode != 200 || a.Error != "" {
		t.Errorf("logo: %+v", a)
	}
	if a := assets["http://site.test/app.js"]; a.Type != "script" || a.SizeBytes != 999 {
		t.Errorf("app.js: %+v", a)
	}
	if a := assets["http://site.test/s.css"]; a.Type != "style" || a.StatusCode != 404 || a.Error == "" || a.SizeBytes != 0 {
		t.Errorf("s.css: %+v", a)
	}

	// тот же ассет на другой странице — те же данные
	for _, p := range rep.Pages {
		if p.URL == "http://site.test/a" && (len(p.Assets) != 1 || p.Assets[0] != assets["http://site.test/logo.png"]) {
			t.Errorf("asset on /a differs: %+v", p.Assets)
		}
	}
}

// --- скорость ---

func TestDelayBetweenRequests(t *testing.T) {
	s := depthSite()
	const delay = 50 * time.Millisecond
	analyze(t, s, crawler.Options{URL: "http://site.test", Depth: 2, Delay: delay, Concurrency: 4})

	times := append([]time.Time(nil), s.times...)
	sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
	if len(times) < 4 {
		t.Fatalf("too few requests: %d", len(times))
	}
	for i := 1; i < len(times); i++ {
		if gap := times[i].Sub(times[i-1]); gap < delay-15*time.Millisecond {
			t.Errorf("gap %d = %v, want >= ~%v", i, gap, delay)
		}
	}
}

func TestCancelStopsWaiting(t *testing.T) {
	s := depthSite()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)

	start := time.Now()
	data, err := crawler.Analyze(ctx, crawler.Options{
		URL: "http://site.test", Depth: 3, Delay: time.Hour,
		HTTPClient: &http.Client{Transport: s},
	})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("Analyze hangs after cancel")
	}
	var rep crawler.Report
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatalf("report must stay valid JSON: %v", err)
	}
}

// --- формат JSON ---

func TestJSONFormat(t *testing.T) {
	s := newSite(map[string]spec{
		"http://site.test":       page(`<title>T</title><a href="/missing">m</a><img src="/l.png">`),
		"http://site.test/l.png": {status: 200, ctype: "image/png", body: "ab"},
	})
	opts := crawler.Options{URL: "http://site.test", Depth: 1, HTTPClient: &http.Client{Transport: s}}

	compact, err := crawler.Analyze(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.IndentJSON = true
	indented, err := crawler.Analyze(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasSuffix(string(compact), "\n") || !strings.HasSuffix(string(indented), "\n") {
		t.Error("output must end with newline")
	}
	if strings.Contains(strings.TrimSpace(string(compact)), "\n") {
		t.Error("compact JSON must be a single line")
	}
	if !strings.Contains(string(indented), "\n  \"pages\"") {
		t.Error("indented JSON expected")
	}

	var a, b crawler.Report
	_ = json.Unmarshal(compact, &a)
	_ = json.Unmarshal(indented, &b)
	a.GeneratedAt, b.GeneratedAt = time.Time{}, time.Time{}
	a.Pages[0].DiscoveredAt, b.Pages[0].DiscoveredAt = time.Time{}, time.Time{}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Errorf("content differs:\n%s\n%s", ja, jb)
	}

	// все ключи обязательны
	var raw map[string]any
	if err := json.Unmarshal(compact, &raw); err != nil {
		t.Fatal(err)
	}
	mustKeys(t, raw, "root_url", "depth", "generated_at", "pages")
	pg := raw["pages"].([]any)[0].(map[string]any)
	mustKeys(t, pg, "url", "depth", "http_status", "status", "seo", "broken_links", "assets", "discovered_at")
	mustKeys(t, pg["seo"].(map[string]any), "has_title", "title", "has_description", "description", "has_h1")
	mustKeys(t, pg["broken_links"].([]any)[0].(map[string]any), "url", "status_code", "error")
	mustKeys(t, pg["assets"].([]any)[0].(map[string]any), "url", "type", "status_code", "size_bytes")
}

func TestEmptyArraysAreNotNull(t *testing.T) {
	s := newSite(map[string]spec{"http://site.test": page("<p>x</p>")})
	data, _ := crawler.Analyze(context.Background(), crawler.Options{URL: "http://site.test", Depth: 1, HTTPClient: &http.Client{Transport: s}})
	if !strings.Contains(string(data), `"broken_links":[]`) || !strings.Contains(string(data), `"assets":[]`) {
		t.Errorf("empty arrays expected: %s", data)
	}
}

func mustKeys(t *testing.T, m map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
}

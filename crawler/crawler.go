// Package crawler реализует консольный краулер: обходит сайт, проверяет ссылки
// и ассеты, собирает SEO-метрики и формирует JSON-отчёт.
package crawler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultDepth        = 10
	defaultWorkers      = 4
	defaultTimeout      = 15 * time.Second
	defaultRetryBackoff = 100 * time.Millisecond
	maxBodyBytes        = 10 << 20
)

// Analyze — тонкая точка входа: обходит сайт и возвращает JSON-отчёт.
// Сетевые ошибки попадают в отчёт; ошибкой возвращается только невалидный
// вход. При отмене ctx возвращается валидный отчёт с уже собранными страницами.
func Analyze(ctx context.Context, opts Options) ([]byte, error) {
	c, err := newCrawler(opts)
	if err != nil {
		return nil, err
	}
	return encode(c.run(ctx), c.opts.IndentJSON)
}

func encode(report Report, indent bool) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(report); err != nil { // Encode добавляет завершающий \n
		return nil, fmt.Errorf("encode report: %w", err)
	}
	return buf.Bytes(), nil
}

// crawler хранит состояние обхода.
type crawler struct {
	opts    Options
	client  *http.Client
	root    *url.URL
	limiter *limiter
	sem     chan struct{} // ограничивает параллельные проверки ссылок/ассетов

	links  *cache[linkResult]
	assets *cache[Asset]
}

type task struct {
	url   string
	depth int
}

type pageResult struct {
	page  Page
	links []string // внутренние ссылки для следующего уровня
	ok    bool
}

type linkResult struct {
	Status int
	Error  string
}

func newCrawler(opts Options) (*crawler, error) {
	root, err := url.Parse(strings.TrimSpace(opts.URL))
	if err != nil {
		return nil, fmt.Errorf("invalid url %q: %w", opts.URL, err)
	}
	if (root.Scheme != "http" && root.Scheme != "https") || root.Host == "" {
		return nil, fmt.Errorf("invalid url %q: expected absolute http(s) URL", opts.URL)
	}
	root.Fragment = ""
	opts.URL = root.String()

	if opts.Depth <= 0 {
		opts.Depth = defaultDepth
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = defaultWorkers
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	if opts.Retries < 0 {
		opts.Retries = 0
	}
	if opts.RetryBackoff <= 0 {
		opts.RetryBackoff = defaultRetryBackoff
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{}
	}

	return &crawler{
		opts:    opts,
		client:  client,
		root:    root,
		limiter: newLimiter(opts.Delay, opts.RPS),
		sem:     make(chan struct{}, opts.Concurrency),
		links:   newCache[linkResult](),
		assets:  newCache[Asset](),
	}, nil
}

// run обходит сайт в ширину, уровень за уровнем.
func (c *crawler) run(ctx context.Context) Report {
	report := Report{RootURL: c.opts.URL, Depth: c.opts.Depth, Pages: []Page{}}

	visited := map[string]bool{visitKey(c.opts.URL): true}
	level := []task{{url: c.opts.URL, depth: 0}}

	for len(level) > 0 && ctx.Err() == nil {
		results := c.processLevel(ctx, level)

		var next []task
		for i, r := range results {
			if !r.ok {
				continue
			}
			report.Pages = append(report.Pages, r.page)

			childDepth := level[i].depth + 1
			if childDepth >= c.opts.Depth {
				continue
			}
			for _, l := range r.links {
				if k := visitKey(l); !visited[k] {
					visited[k] = true
					next = append(next, task{url: l, depth: childDepth})
				}
			}
		}
		level = next
	}

	sort.SliceStable(report.Pages, func(i, j int) bool {
		if report.Pages[i].Depth != report.Pages[j].Depth {
			return report.Pages[i].Depth < report.Pages[j].Depth
		}
		return report.Pages[i].URL < report.Pages[j].URL
	})

	report.GeneratedAt = time.Now()
	return report
}

// processLevel обрабатывает страницы одного уровня пулом воркеров.
func (c *crawler) processLevel(ctx context.Context, level []task) []pageResult {
	results := make([]pageResult, len(level))
	idx := make(chan int)

	var wg sync.WaitGroup
	for w := 0; w < c.opts.Concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				results[i] = c.processPage(ctx, level[i])
			}
		}()
	}

feed:
	for i := range level {
		select {
		case idx <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(idx)
	wg.Wait()
	return results
}

func (c *crawler) processPage(ctx context.Context, t task) pageResult {
	page := Page{
		URL:          t.url,
		Depth:        t.depth,
		DiscoveredAt: time.Now(),
	}
	base, err := url.Parse(t.url)
	if err != nil {
		return pageResult{}
	}

	var parsed *parsedPage
	status, err := c.fetch(ctx, t.url, func(resp *http.Response) error {
		if resp.StatusCode >= 400 {
			return nil
		}
		p, perr := parseHTML(io.LimitReader(resp.Body, maxBodyBytes), base)
		if perr != nil {
			return perr
		}
		parsed = &p
		return nil
	})
	if ctx.Err() != nil {
		return pageResult{} // отменено — страницу в отчёт не включаем
	}

	res := newLinkResult(status, err)
	page.HTTPStatus = status
	page.Error = res.Error
	if res.Error == "" {
		page.Status = "ok"
	} else {
		page.Status = "error"
	}
	// Уже скачанная страница не должна проверяться как ссылка повторно.
	c.links.do(visitKey(t.url), func() linkResult { return res })

	var internal []string
	if parsed != nil {
		page.SEO = parsed.seo
		page.BrokenLinks = c.checkLinks(ctx, parsed.links)
		page.Assets = c.checkAssets(ctx, parsed.assets)
		for _, l := range parsed.links {
			if u, perr := url.Parse(l); perr == nil && strings.EqualFold(u.Host, c.root.Host) {
				internal = append(internal, l)
			}
		}
	}
	if ctx.Err() != nil {
		return pageResult{}
	}
	return pageResult{page: page, links: internal, ok: true}
}

// visitKey приводит URL к ключу для дедупликации: пустой путь равен "/",
// поэтому http://site.test и http://site.test/ считаются одной страницей.
func visitKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}

// parallel запускает fn для 0..n-1, ограничивая параллелизм семафором.
func (c *crawler) parallel(n int, fn func(i int)) {
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		c.sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-c.sem }()
			fn(i)
		}(i)
	}
	wg.Wait()
}

func (c *crawler) checkLinks(ctx context.Context, links []string) []BrokenLink {
	results := make([]linkResult, len(links))
	c.parallel(len(links), func(i int) {
		results[i] = c.links.do(visitKey(links[i]), func() linkResult {
			status, err := c.checkURL(ctx, links[i])
			return newLinkResult(status, err)
		})
	})

	broken := []BrokenLink{}
	for i, r := range results {
		if r.Error != "" {
			broken = append(broken, BrokenLink{URL: links[i], StatusCode: r.Status, Error: r.Error})
		}
	}
	return broken
}

func (c *crawler) checkAssets(ctx context.Context, refs []assetRef) []Asset {
	assets := make([]Asset, len(refs))
	c.parallel(len(refs), func(i int) {
		assets[i] = c.assets.do(refs[i].url, func() Asset { return c.fetchAsset(ctx, refs[i]) })
	})
	// Стабильный порядок: image, script, style; внутри типа — порядок в HTML.
	sort.SliceStable(assets, func(i, j int) bool { return assetRank(assets[i].Type) < assetRank(assets[j].Type) })
	return assets
}

func assetRank(typ string) int {
	switch typ {
	case "image":
		return 0
	case "script":
		return 1
	case "style":
		return 2
	}
	return 3
}

func (c *crawler) fetchAsset(ctx context.Context, ref assetRef) Asset {
	a := Asset{URL: ref.url, Type: ref.typ}

	var size int64
	status, err := c.fetch(ctx, ref.url, func(resp *http.Response) error {
		if resp.StatusCode >= 400 {
			return nil
		}
		if n, ok := declaredSize(resp); ok {
			size = n
			return nil
		}
		// Content-Length нет — считаем по фактическому телу.
		n, err := io.Copy(io.Discard, resp.Body)
		size = n
		return err
	})

	res := newLinkResult(status, err)
	a.StatusCode = status
	a.Error = res.Error
	if res.Error == "" {
		a.SizeBytes = size
	}
	return a
}

func declaredSize(resp *http.Response) (int64, bool) {
	if h := resp.Header.Get("Content-Length"); h != "" {
		if n, err := strconv.ParseInt(h, 10, 64); err == nil && n >= 0 {
			return n, true
		}
	}
	if resp.ContentLength > 0 {
		return resp.ContentLength, true
	}
	return 0, false
}

func drain(resp *http.Response) error {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return nil
}

// newLinkResult сводит (статус, ошибка) к паре «код + сообщение об ошибке».
// Пустой Error означает, что ресурс доступен.
func newLinkResult(status int, err error) linkResult {
	switch {
	case err != nil:
		return linkResult{Status: status, Error: err.Error()}
	case status >= 400:
		msg := http.StatusText(status)
		if msg == "" {
			msg = "HTTP " + strconv.Itoa(status)
		}
		return linkResult{Status: status, Error: msg}
	}
	return linkResult{Status: status}
}

// cache гарантирует, что fn для одного ключа выполнится ровно один раз,
// даже при конкурентных обращениях.
type cache[T any] struct {
	mu sync.Mutex
	m  map[string]*entry[T]
}

type entry[T any] struct {
	once sync.Once
	val  T
}

func newCache[T any]() *cache[T] {
	return &cache[T]{m: make(map[string]*entry[T])}
}

func (c *cache[T]) do(key string, fn func() T) T {
	c.mu.Lock()
	e, ok := c.m[key]
	if !ok {
		e = &entry[T]{}
		c.m[key] = e
	}
	c.mu.Unlock()

	e.once.Do(func() { e.val = fn() })
	return e.val
}

package crawler

import (
	"net/http"
	"time"
)

// Options — параметры запуска краулера.
type Options struct {
	URL         string        // стартовый URL (обязателен)
	Depth       int           // страницы с depth < Depth обходятся; <=0 → 10
	Retries     int           // число повторов при временных ошибках (429/5xx/сеть)
	Delay       time.Duration // пауза между запросами для всего процесса
	Timeout     time.Duration // таймаут одного запроса; <=0 → 15s
	UserAgent   string        // заголовок User-Agent
	Concurrency int           // число воркеров; <=0 → 4
	RPS         float64       // запросов в секунду; если >0, перекрывает Delay
	IndentJSON  bool          // форматировать JSON с отступами

	// RetryBackoff — базовая пауза между повторами (растёт линейно). <=0 → 100ms.
	RetryBackoff time.Duration

	// HTTPClient — клиент для всех запросов. nil → &http.Client{}.
	HTTPClient *http.Client
}

type Report struct {
	RootURL     string    `json:"root_url"`
	Depth       int       `json:"depth"`
	GeneratedAt time.Time `json:"generated_at"`
	Pages       []Page    `json:"pages"`
}

type Page struct {
	URL          string       `json:"url"`
	Depth        int          `json:"depth"`
	HTTPStatus   int          `json:"http_status"`
	Status       string       `json:"status"`
	Error        string       `json:"error"`
	SEO          SEO          `json:"seo"`
	BrokenLinks  []BrokenLink `json:"broken_links"`
	Assets       []Asset      `json:"assets"`
	DiscoveredAt time.Time    `json:"discovered_at"`
}

type SEO struct {
	HasTitle       bool   `json:"has_title"`
	Title          string `json:"title"`
	HasDescription bool   `json:"has_description"`
	Description    string `json:"description"`
	HasH1          bool   `json:"has_h1"`
}

type BrokenLink struct {
	URL        string `json:"url"`
	StatusCode int    `json:"status_code"`
	Error      string `json:"error"`
}

type Asset struct {
	URL        string `json:"url"`
	Type       string `json:"type"`
	StatusCode int    `json:"status_code"`
	SizeBytes  int64  `json:"size_bytes"`
	Error      string `json:"error"`
}

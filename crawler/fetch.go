package crawler

import (
	"context"
	"net/http"
	"time"
)

func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

// fetch выполняет GET с лимитером, таймаутом и повторами.
// onResp вызывается только для итогового ответа (внутри таймаута запроса).
// Возвращает последний полученный HTTP-статус (0 при сетевой ошибке) и ошибку.
func (c *crawler) fetch(ctx context.Context, rawURL string, onResp func(*http.Response) error) (int, error) {
	var status int
	for attempt := 0; ; attempt++ {
		if err := c.limiter.wait(ctx); err != nil {
			return status, err
		}
		final := attempt >= c.opts.Retries

		var (
			retry bool
			err   error
		)
		status, retry, err = c.attempt(ctx, rawURL, final, onResp)
		if !retry || final || ctx.Err() != nil {
			return status, err
		}
		pause := c.opts.RetryBackoff * time.Duration(attempt+1)
		if err := sleep(ctx, pause); err != nil {
			return status, err
		}
	}
}

func (c *crawler) attempt(ctx context.Context, rawURL string, final bool, onResp func(*http.Response) error) (int, bool, error) {
	rctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(rctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, false, err
	}
	if c.opts.UserAgent != "" {
		req.Header.Set("User-Agent", c.opts.UserAgent)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return 0, true, err
	}
	defer func() { _ = resp.Body.Close() }()

	if retryableStatus(resp.StatusCode) && !final {
		return resp.StatusCode, true, nil
	}
	if onResp != nil {
		err = onResp(resp)
	}
	return resp.StatusCode, false, err
}

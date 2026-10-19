package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func capture(t *testing.T, args ...string) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := newApp().Run(context.Background(), append([]string{"hexlet-go-crawler"}, args...))
	_ = w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return string(out), runErr
}

func testServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<title>CLI</title><h1>x</h1>")
	}))
}

func TestCLIPrintsJSONAsIs(t *testing.T) {
	srv := testServer()
	defer srv.Close()

	out, err := capture(t, "--depth", "1", "--workers", "2", "--rps", "0", "--delay", "1ms",
		"--timeout", "5s", "--retries", "0", "--user-agent", "test-bot", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "{") || !strings.HasSuffix(out, "}\n") {
		t.Errorf("output must be bare JSON with trailing newline, got %q", out)
	}
	var rep map[string]any
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if rep["root_url"] != srv.URL {
		t.Errorf("root_url = %v", rep["root_url"])
	}
}

func TestCLINetworkErrorIsNotFatal(t *testing.T) {
	out, err := capture(t, "--retries", "0", "--depth", "1", "http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("network errors must not fail the CLI: %v", err)
	}
	if !strings.Contains(out, `"status": "error"`) {
		t.Errorf("report must contain the error, got %s", out)
	}
}

func TestCLIRequiresURL(t *testing.T) {
	if _, err := capture(t); err == nil || !strings.Contains(err.Error(), "url is required") {
		t.Fatalf("err = %v", err)
	}
}

func TestCLIRejectsInvalidURL(t *testing.T) {
	if _, err := capture(t, "ftp://example.com"); err == nil {
		t.Fatal("expected error")
	}
}

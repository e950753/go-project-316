package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"

	"code/crawler"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	err := newApp().Run(ctx, os.Args)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newApp() *cli.Command {
	return &cli.Command{
		Name:      "hexlet-go-crawler",
		Usage:     "analyze a website structure",
		ArgsUsage: "<url>",
		Flags: []cli.Flag{
			&cli.IntFlag{Name: "depth", Value: 10, Usage: "crawl depth"},
			&cli.IntFlag{Name: "retries", Value: 1, Usage: "number of retries for failed requests"},
			&cli.DurationFlag{Name: "delay", Value: 0, Usage: "delay between requests (example: 200ms, 1s)"},
			&cli.DurationFlag{Name: "timeout", Value: 15 * time.Second, Usage: "per-request timeout"},
			&cli.Float64Flag{Name: "rps", Value: 0, Usage: "limit requests per second (overrides delay)"},
			&cli.StringFlag{Name: "user-agent", Usage: "custom user agent"},
			&cli.IntFlag{Name: "workers", Value: 4, Usage: "number of concurrent workers"},
		},
		Action: run,
	}
}

func run(ctx context.Context, cmd *cli.Command) error {
	target := cmd.Args().First()
	if target == "" {
		return fmt.Errorf("url is required: hexlet-go-crawler [global options] <url> (see --help)")
	}

	data, err := crawler.Analyze(ctx, crawler.Options{
		URL:         target,
		Depth:       int(cmd.Int("depth")),
		Retries:     int(cmd.Int("retries")),
		Delay:       cmd.Duration("delay"),
		Timeout:     cmd.Duration("timeout"),
		RPS:         cmd.Float("rps"),
		UserAgent:   cmd.String("user-agent"),
		Concurrency: int(cmd.Int("workers")),
		IndentJSON:  true,
		HTTPClient:  &http.Client{},
	})
	if err != nil {
		return err
	}

	_, err = os.Stdout.Write(data)
	return err
}

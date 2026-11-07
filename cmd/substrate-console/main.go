// Command substrate-console is a console for Agent Substrate, built as an MCP
// App. `substrate-console mcp` speaks MCP over stdio for Claude, ChatGPT,
// VS Code, Codex and other MCP hosts. `substrate-console serve` serves the
// same MCP endpoint over HTTP plus a local page that renders the console.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/alee792/substrate-console/internal/backend"
	"github.com/alee792/substrate-console/internal/backend/ate"
	"github.com/alee792/substrate-console/internal/backend/demo"
	"github.com/alee792/substrate-console/internal/mcpapp"
	"github.com/alee792/substrate-console/internal/metrics"
	"github.com/alee792/substrate-console/internal/shell"
	"github.com/alee792/substrate-console/web"
)

var version = "dev"

const usage = `substrate-console: a console for Agent Substrate, built as an MCP App.

Usage:
  substrate-console mcp   [flags]   MCP over stdio, for MCP hosts
  substrate-console serve [flags]   MCP over HTTP at /mcp, and the console page at /
  substrate-console version

Flags:
`

type flags struct {
	demo, readOnly                    bool
	kubeconfig, kubeContext, endpoint string
	tokenFile, prometheusURL, addr    string
	noFonts                           bool
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		newFlagSet("", &flags{}).PrintDefaults()
		os.Exit(2)
	}
	cmd := os.Args[1]
	if cmd == "version" || cmd == "--version" {
		fmt.Println(version)
		return
	}
	var f flags
	fs := newFlagSet(cmd, &f)
	if cmd != "mcp" && cmd != "serve" {
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		fs.PrintDefaults()
		os.Exit(2)
	}
	_ = fs.Parse(os.Args[2:])

	// stdout carries MCP in stdio mode, so all logs go to stderr.
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cmd, f, log); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("substrate-console stopped", "err", err)
		os.Exit(1)
	}
}

func newFlagSet(name string, f *flags) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.BoolVar(&f.demo, "demo", false, "serve an example cluster instead of connecting to one")
	fs.BoolVar(&f.readOnly, "read-only", false, "leave out the tools that change the cluster")
	fs.StringVar(&f.kubeconfig, "kubeconfig", "", "path to the kubeconfig (default: the usual kubectl rules)")
	fs.StringVar(&f.kubeContext, "context", "", "kubeconfig context to use")
	fs.StringVar(&f.endpoint, "endpoint", "", "ateapi address; default is a port-forward to the api Service, as kubectl ate does")
	fs.StringVar(&f.tokenFile, "token-file", "", "file holding an ateapi bearer token; default mints an ate-client ServiceAccount token")
	fs.StringVar(&f.prometheusURL, "prometheus-url", "", "Prometheus base URL for vitals and charts, e.g. http://localhost:9090")
	fs.StringVar(&f.addr, "addr", "127.0.0.1:7878", "serve: address to listen on (keep it on loopback; /mcp has no auth)")
	fs.BoolVar(&f.noFonts, "no-web-fonts", false, "use host and system fonts only, so the view's CSP allows no external origins")
	return fs
}

func run(ctx context.Context, cmd string, f flags, log *slog.Logger) error {
	var (
		b backend.Backend
		m backend.Metrics
	)
	if f.demo {
		d := demo.New(nil)
		b, m = d, d
		log.Info("serving the demo cluster")
	} else {
		live, err := ate.New(ctx, ate.Config{
			Kubeconfig: f.kubeconfig, Context: f.kubeContext, Endpoint: f.endpoint, TokenFile: f.tokenFile,
		})
		if err != nil {
			return fmt.Errorf("connect to Substrate: %w (use --demo to try the console without a cluster)", err)
		}
		defer live.Close()
		b = live
		if f.prometheusURL != "" {
			m = metrics.NewPrometheus(f.prometheusURL)
		}
	}
	fonts := []string{"https://fonts.googleapis.com", "https://fonts.gstatic.com"}
	if f.noFonts {
		fonts = nil
	}
	app := mcpapp.New(mcpapp.Options{
		Backend: b, Metrics: m, ViewHTML: web.ConsoleHTML, Version: version,
		ReadOnly: f.readOnly, FontDomains: fonts,
	})

	switch cmd {
	case "mcp":
		return app.MCP().Run(ctx, &mcp.StdioTransport{})
	case "serve":
		host, _, err := net.SplitHostPort(f.addr)
		if err == nil {
			if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
				log.Warn("listening beyond loopback: /mcp has no authentication in this version", "addr", f.addr)
			}
		}
		srv := &http.Server{Addr: f.addr, Handler: shell.Handler(app.MCP(), web.ShellHTML), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			<-ctx.Done()
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(sctx)
		}()
		log.Info("console ready", "url", "http://"+f.addr+"/", "mcp", "http://"+f.addr+"/mcp")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
	return nil
}

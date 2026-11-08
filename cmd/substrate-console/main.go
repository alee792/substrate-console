// Command substrate-console is a console for Agent Substrate, built as an MCP
// App. `substrate-console mcp` speaks MCP over stdio for Claude, ChatGPT,
// VS Code, Codex and other MCP hosts. `substrate-console serve` serves the
// same MCP endpoint over HTTP plus a local page that renders the console.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	substratefake "github.com/alee792/substrate-fake"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/alee792/substrate-console/internal/backend"
	"github.com/alee792/substrate-console/internal/backend/ate"
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
	noFonts, auth                     bool
	authTokenFile                     string
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
	fs.StringVar(&f.addr, "addr", "127.0.0.1:7878", "serve: address to listen on")
	fs.BoolVar(&f.auth, "auth", false, "serve: require a token on /mcp (on by default beyond loopback); generates one unless --auth-token-file or $SUBSTRATE_CONSOLE_TOKEN sets it")
	fs.StringVar(&f.authTokenFile, "auth-token-file", "", "serve: file holding the token /mcp requires; implies --auth")
	fs.BoolVar(&f.noFonts, "no-web-fonts", false, "use host and system fonts only, so the view's CSP allows no external origins")
	return fs
}

func run(ctx context.Context, cmd string, f flags, log *slog.Logger) error {
	var (
		b backend.Backend
		m backend.Metrics
	)
	if f.demo {
		// The demo is a fake cluster served on loopback. The console talks
		// to it exactly as it would to a real one.
		c, err := substratefake.Start(ctx, substratefake.Options{Scenario: substratefake.Demo})
		if err != nil {
			return fmt.Errorf("start the demo cluster: %w", err)
		}
		defer c.Close()
		f.kubeconfig, f.kubeContext, f.endpoint, f.tokenFile = c.Kubeconfig, c.Context, c.APIEndpoint, ""
		f.prometheusURL = c.PrometheusURL
		log.Info("serving the demo cluster", "kubeconfig", c.Kubeconfig, "ateapi", c.APIEndpoint, "prometheus", c.PrometheusURL)
	}
	{
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
		token, generated, err := serveToken(f)
		if err != nil {
			return err
		}
		srv := &http.Server{Addr: f.addr, Handler: shell.Handler(app.MCP(), web.ShellHTML, token), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			<-ctx.Done()
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(sctx)
		}()
		page := "http://" + f.addr + "/"
		switch {
		case token == "":
			log.Info("console ready", "url", page, "mcp", page+"mcp")
		case generated:
			// The generated token goes to the terminal only, never through
			// the logger, so it can't land in a log collector. In the link it
			// sits in the fragment, which browsers never send to a server.
			log.Info("console ready; /mcp needs the token printed below", "url", page, "mcp", page+"mcp")
			fmt.Fprintf(os.Stderr, "\n  Open %s#token=%s\n  Token for MCP hosts: %s\n\n", page, token, token)
		default:
			log.Info("console ready; /mcp needs your token", "url", page, "mcp", page+"mcp")
		}
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
	return nil
}

// serveToken decides whether /mcp needs a token and returns it. Auth is on
// when asked for, when a token is supplied, or when the address is not
// loopback. A tunnel to loopback looks local to the server, so use --auth
// for that.
func serveToken(f flags) (token string, generated bool, err error) {
	if f.authTokenFile != "" {
		b, err := os.ReadFile(f.authTokenFile)
		if err != nil {
			return "", false, fmt.Errorf("read --auth-token-file: %w", err)
		}
		token = strings.TrimSpace(string(b))
	} else {
		token = strings.TrimSpace(os.Getenv("SUBSTRATE_CONSOLE_TOKEN"))
	}
	if token != "" {
		if len(token) < 16 {
			return "", false, errors.New("the /mcp token must be at least 16 characters")
		}
		return token, false, nil
	}
	if !f.auth && isLoopback(f.addr) {
		return "", false, nil
	}
	// A generated token is only shown on the terminal. Without one there
	// would be no safe place to print it.
	if fi, err := os.Stderr.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return "", false, errors.New("/mcp needs a token, and stderr isn't a terminal to show a generated one on: pass --auth-token-file or set SUBSTRATE_CONSOLE_TOKEN")
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", false, err
	}
	return hex.EncodeToString(b), true, nil
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

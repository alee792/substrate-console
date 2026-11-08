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
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	substratefake "github.com/alee792/substrate-fake"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	ateapi "go.ate.dev/substrate/api/ateapi/v1"

	"github.com/alee792/substrate-console/internal/agents"
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
	tokenFile, tokenCommand, sa       string
	prometheusURL, addr, atespaces    string
	noFonts, auth, forward            bool
	authTokenFile, publicURL          string
	canopyURL, canopyKeyFile          string
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
	fs.StringVar(&f.sa, "service-account", os.Getenv("ATE_SERVICE_ACCOUNT"), "namespace/name of your own ServiceAccount; the console uses your kubeconfig to get short-lived ateapi tokens for it (default $ATE_SERVICE_ACCOUNT, else the shared ate-client owner account)")
	fs.StringVar(&f.tokenFile, "token-file", "", "file holding the console's ateapi bearer token, reread on every call")
	fs.StringVar(&f.tokenCommand, "token-command", "", "command that prints the console's ateapi token, rerun when it expires, e.g. 'gcloud auth print-identity-token'")
	fs.StringVar(&f.atespaces, "atespaces", "", "comma-separated atespaces to show people who can't list atespaces")
	fs.StringVar(&f.prometheusURL, "prometheus-url", "", "Prometheus base URL for vitals and charts, e.g. http://localhost:9090")
	fs.StringVar(&f.addr, "addr", "127.0.0.1:7878", "serve: address to listen on")
	fs.BoolVar(&f.auth, "auth", false, "serve: require a token on /mcp (on by default beyond loopback); generates one unless --auth-token-file or $SUBSTRATE_CONSOLE_TOKEN sets it")
	fs.StringVar(&f.authTokenFile, "auth-token-file", "", "serve: file holding the token /mcp requires; implies --auth")
	fs.StringVar(&f.publicURL, "public-url", "", "serve: the https URL a tunnel or proxy serves the console at, e.g. https://console.example.com; /mcp then accepts that host and requires a token")
	fs.BoolVar(&f.forward, "forward-token", false, "serve: act as each caller: /mcp requires the caller's own ateapi token and sends it to ateapi, so Substrate's access policies decide what they see")
	fs.StringVar(&f.canopyURL, "canopy-url", os.Getenv("CANOPY_URL"), "Canopy to show agents from, e.g. http://localhost:8000 (default $CANOPY_URL); its API key comes from $CANOPY_API_KEY or --canopy-key-file")
	fs.StringVar(&f.canopyKeyFile, "canopy-key-file", "", "file holding the Canopy API key, instead of $CANOPY_API_KEY")
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
		f.kubeconfig, f.kubeContext, f.endpoint, f.tokenFile, f.tokenCommand = c.Kubeconfig, c.Context, c.APIEndpoint, "", ""
		f.prometheusURL = c.PrometheusURL
		log.Info("serving the demo cluster", "kubeconfig", c.Kubeconfig, "ateapi", c.APIEndpoint, "prometheus", c.PrometheusURL)
		if f.atespaces == "" {
			// Name the demo's atespaces, as an operator would name a team's,
			// so people who can't list them still find theirs.
			if r, err := c.Client().ListAtespaces(ctx, &ateapi.ListAtespacesRequest{}); err == nil {
				for _, a := range r.GetAtespaces() {
					f.atespaces += a.GetMetadata().GetName() + ","
				}
			}
		}
		if f.forward && cmd == "serve" {
			// Demo tokens go to the terminal only, like a generated /mcp
			// token. They're for a throwaway cluster, but still not for logs.
			fmt.Fprintf(os.Stderr, "\n  Tokens to try /mcp with, valid for a day:\n")
			for _, p := range []struct{ who, principal string }{
				{"owner (ate-client)", substratefake.ClientPrincipal},
				{"global viewer", substratefake.DemoOnCall},
				{"triage editor", substratefake.DemoTriageDev},
			} {
				fmt.Fprintf(os.Stderr, "  %-19s %s\n", p.who+":", c.World.IssueToken(p.principal, substratefake.TokenOptions{TTL: 24 * time.Hour}))
			}
			fmt.Fprintln(os.Stderr)
		}
	}
	if err := checkIdentity(cmd, f); err != nil {
		return err
	}
	{
		live, err := ate.New(ctx, ate.Config{
			Kubeconfig: f.kubeconfig, Context: f.kubeContext, Endpoint: f.endpoint,
			ServiceAccount: f.sa, TokenFile: f.tokenFile, TokenCommand: f.tokenCommand, Forward: f.forward,
			Atespaces: splitList(f.atespaces),
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
	ag, err := canopyClient(f)
	if err != nil {
		return err
	}
	if ag != nil {
		log.Info("showing agents from Canopy", "canopy", ag.Host())
	}
	fonts := []string{"https://fonts.googleapis.com", "https://fonts.gstatic.com"}
	if f.noFonts {
		fonts = nil
	}
	app := mcpapp.New(mcpapp.Options{
		Backend: b, Metrics: m, ViewHTML: web.ConsoleHTML, Version: version,
		ReadOnly: f.readOnly, FontDomains: fonts, Agents: ag,
	})

	switch cmd {
	case "mcp":
		return app.MCP().Run(ctx, &mcp.StdioTransport{})
	case "serve":
		var token string
		var generated bool
		publicHost, err := publicHost(f.publicURL)
		if err != nil {
			return err
		}
		if publicHost != "" && !f.forward {
			// A tunnel connects from loopback, so without this the server
			// would think everyone reaching it through the tunnel is you.
			f.auth = true
		}
		if !f.forward {
			if token, generated, err = serveToken(f); err != nil {
				return err
			}
		}
		srv := &http.Server{Addr: f.addr, Handler: shell.Handler(app.MCP(), web.ShellHTML, shell.Auth{Token: token, Forward: f.forward, PublicHost: publicHost}), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			<-ctx.Done()
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(sctx)
		}()
		page := "http://" + f.addr + "/"
		switch {
		case f.forward:
			log.Info("console ready; /mcp acts as each caller and needs their own ateapi token", "url", page, "mcp", page+"mcp")
		case token == "":
			log.Info("console ready", "url", page, "mcp", page+"mcp")
		case generated:
			// The generated token goes to the terminal only, never through
			// the logger, so it can't land in a log collector. In the link it
			// sits in the fragment, which browsers never send to a server.
			log.Info("console ready; /mcp needs the token printed below", "url", page, "mcp", page+"mcp")
			fmt.Fprintf(os.Stderr, "\n  Open %s#token=%s\n  Token for MCP hosts: %s\n", page, token, token)
			if publicHost != "" {
				// The header keeps the token out of the tunnel's access logs;
				// the query form is for hosts that take only a URL.
				mcpURL := strings.TrimSuffix(f.publicURL, "/") + "/mcp"
				fmt.Fprintf(os.Stderr, "  Connector URL:    %s\n  with header:      Authorization: Bearer %s\n  or, URL only:     %s?key=%s\n", mcpURL, token, mcpURL, token)
			}
			fmt.Fprintln(os.Stderr)
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

// canopyClient connects the agents section, when a Canopy is configured.
func canopyClient(f flags) (*agents.Client, error) {
	if f.canopyURL == "" {
		if f.canopyKeyFile != "" {
			return nil, errors.New("--canopy-key-file needs --canopy-url")
		}
		return nil, nil
	}
	if f.forward {
		// Canopy has one API key for the console, and checks no caller of
		// its own yet, so every caller would act as the console.
		return nil, errors.New("--canopy-url can't be combined with --forward-token: the console has one Canopy API key, so every caller would act as it")
	}
	key := strings.TrimSpace(os.Getenv("CANOPY_API_KEY"))
	if f.canopyKeyFile != "" {
		b, err := os.ReadFile(f.canopyKeyFile)
		if err != nil {
			return nil, fmt.Errorf("read --canopy-key-file: %w", err)
		}
		key = strings.TrimSpace(string(b))
	}
	if key == "" {
		return nil, errors.New("--canopy-url needs an API key: set CANOPY_API_KEY or pass --canopy-key-file")
	}
	return agents.New(agents.Config{URL: f.canopyURL, APIKey: key, HTTPClient: &http.Client{Timeout: 2 * time.Minute}})
}

// checkIdentity rejects flag combinations that disagree about whose token
// the console sends.
func checkIdentity(cmd string, f flags) error {
	own := 0
	for _, set := range []bool{f.sa != "", f.tokenFile != "", f.tokenCommand != ""} {
		if set {
			own++
		}
	}
	if own > 1 {
		return errors.New("pick one of --service-account, --token-file and --token-command")
	}
	if f.sa != "" {
		if ns, name, ok := strings.Cut(f.sa, "/"); !ok || ns == "" || name == "" || strings.Contains(name, "/") {
			return fmt.Errorf("--service-account %q: want namespace/name, e.g. ate-users/alice", f.sa)
		}
	}
	if cmd == "serve" && f.publicURL != "" && !f.demo && !f.forward && own == 0 {
		// Without an identity flag the console acts as the shared
		// ate-client owner. Behind a public URL that would hand cluster
		// ownership to anyone with the link, so fail closed.
		return errors.New("--public-url would expose the shared ate-client owner account: pass --service-account namespace/name (or --token-file, --token-command or --forward-token) so the console acts as someone with only the access they need")
	}
	if cmd == "serve" && f.publicURL != "" && f.canopyURL != "" && !f.readOnly {
		// The console holds one Canopy API key and Canopy checks no caller
		// of its own, so behind a public URL anyone with the link could
		// start sessions and approve tool calls as the console.
		return errors.New("--canopy-url with --public-url would let anyone with the link start agent sessions and approve tool calls on the console's Canopy key: add --read-only, or leave out --canopy-url")
	}
	if !f.forward {
		return nil
	}
	if cmd != "serve" {
		return errors.New("--forward-token is for serve; over stdio the console has one user, so pass --service-account, --token-file or --token-command")
	}
	if own > 0 {
		return errors.New("--forward-token never uses the console's own token, so leave out --service-account, --token-file and --token-command")
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
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

// publicHost checks --public-url and returns its host name.
func publicHost(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return "", fmt.Errorf("--public-url %q: want an https origin such as https://console.example.com", raw)
	}
	return u.Hostname(), nil
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

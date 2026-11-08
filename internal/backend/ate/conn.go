// Copyright 2026 The Agent Substrate Authors
// Copyright 2026 The substrate-console Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// This file adapts kubectl-ate's connection code from
// go.ate.dev/substrate (internal/ateclient, internal/portforward and
// internal/clustertrustbundle at v1.0.0). Those packages are internal to
// Substrate's module, so an outside client can't import them. It keeps
// kubectl-ate's behavior: port-forward to the api Service in ate-system,
// verify its servicedns certificate against the live ClusterTrustBundle,
// and authenticate with a minted ate-client ServiceAccount token unless
// given another. Forwarding each caller's token is the console's own.
// Delete this file once Substrate publishes a client module.

package ate

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	authv1 "k8s.io/api/authentication/v1"
	certsv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"

	"github.com/alee792/substrate-console/internal/backend"
)

const (
	systemNamespace      = "ate-system"
	apiServiceName       = "api"
	clientServiceAccount = "ate-client"
	serviceDNSSigner     = "servicedns.podcert.ate.dev/identity"
	liveBundleSelector   = "podcert.ate.dev/canarying=live"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func namespace() string     { return envOr("ATE_NAMESPACE", systemNamespace) }
func apiService() string    { return envOr("ATE_API_SERVICE_NAME", apiServiceName) }
func clientAccount() string { return envOr("ATE_CLIENT_SERVICE_ACCOUNT", clientServiceAccount) }
func apiServerName() string { return apiService() + "." + namespace() + ".svc" }

func loadKubeConfig(path, kubeContext string) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = path
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: kubeContext}).ClientConfig()
	if err != nil {
		return nil, err
	}
	cfg.WarningHandlerWithContext = &rest.NoWarnings{}
	return cfg, nil
}

// dial connects to ateapi. With an empty endpoint it port-forwards to the
// api Service. It returns the connection, its credentials and a func that
// tears it down.
func dial(ctx context.Context, rc *rest.Config, cs *kubernetes.Clientset, cfg Config) (*grpc.ClientConn, callerCreds, func(), error) {
	stop := func() {}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		port, s, err := servicePortForward(ctx, rc, cs, namespace(), apiService(), 443)
		if err != nil {
			return nil, callerCreds{}, nil, fmt.Errorf("port-forward to %s/%s: %w", namespace(), apiService(), err)
		}
		endpoint, stop = fmt.Sprintf("127.0.0.1:%d", port), s
	}
	tlsCfg, err := serverTLSConfig(ctx, cs, apiServerName())
	if err != nil {
		stop()
		return nil, callerCreds{}, nil, err
	}
	own, err := ownToken(ctx, cs, cfg)
	if err != nil {
		stop()
		return nil, callerCreds{}, nil, err
	}
	creds := callerCreds{own: own}
	conn, err := grpc.NewClient(endpoint,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		grpc.WithPerRPCCredentials(creds),
		grpc.WithDefaultServiceConfig(`{"loadBalancingConfig": [{"round_robin":{}}]}`),
	)
	if err != nil {
		stop()
		return nil, callerCreds{}, nil, fmt.Errorf("dial ateapi: %w", err)
	}
	return conn, creds, func() { conn.Close(); stop() }, nil
}

// serverTLSConfig trusts the live servicedns ClusterTrustBundle. It tries
// the v1 API, then v1beta1 for older clusters.
func serverTLSConfig(ctx context.Context, cs *kubernetes.Clientset, serverName string) (*tls.Config, error) {
	opts := metav1.ListOptions{LabelSelector: liveBundleSelector}
	var bundles []certsv1.ClusterTrustBundle
	list, err := cs.CertificatesV1().ClusterTrustBundles().List(ctx, opts)
	if apierrors.IsNotFound(err) {
		beta, berr := cs.CertificatesV1beta1().ClusterTrustBundles().List(ctx, opts)
		if berr != nil {
			return nil, fmt.Errorf("list ClusterTrustBundles: %w", berr)
		}
		for _, b := range beta.Items {
			bundles = append(bundles, certsv1.ClusterTrustBundle{ObjectMeta: b.ObjectMeta, Spec: certsv1.ClusterTrustBundleSpec(b.Spec)})
		}
	} else if err != nil {
		return nil, fmt.Errorf("list ClusterTrustBundles: %w", err)
	} else {
		bundles = list.Items
	}
	pool := x509.NewCertPool()
	found := false
	for _, b := range bundles {
		if b.Spec.SignerName != serviceDNSSigner {
			continue
		}
		if !pool.AppendCertsFromPEM([]byte(b.Spec.TrustBundle)) {
			return nil, fmt.Errorf("ClusterTrustBundle %q has no valid certificates", b.Name)
		}
		found = true
	}
	if !found {
		return nil, fmt.Errorf("no live ClusterTrustBundle for signer %q; is Substrate installed in this cluster?", serviceDNSSigner)
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool, ServerName: serverName}, nil
}

// ownToken returns the console's own ateapi token: tokenCommand's output,
// tokenFile ("-" for stdin), or a minted ate-client ServiceAccount token, as
// kubectl-ate does. The minted token authenticates as that shared
// ServiceAccount, not as the person running the console. Forward mode has
// no own token: every call carries the caller's.
func ownToken(ctx context.Context, cs *kubernetes.Clientset, cfg Config) (func(context.Context) (string, error), error) {
	switch {
	case cfg.Forward:
		return nil, nil
	case cfg.TokenCommand != "":
		return (&cachedToken{fetch: func(ctx context.Context) (string, error) { return runTokenCommand(ctx, cfg.TokenCommand) }}).get, nil
	case cfg.TokenFile == "-":
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, err
		}
		tok := strings.TrimSpace(string(b))
		return func(context.Context) (string, error) { return tok, nil }, nil
	case cfg.TokenFile != "":
		// Reread on every call, as kubectl-ate does, so a refreshed file
		// takes effect.
		return func(context.Context) (string, error) {
			b, err := os.ReadFile(cfg.TokenFile)
			if err != nil {
				return "", fmt.Errorf("read token file: %w", err)
			}
			return strings.TrimSpace(string(b)), nil
		}, nil
	}
	mint := &cachedToken{fetch: func(ctx context.Context) (string, error) {
		exp := int64(3600)
		tok, err := cs.CoreV1().ServiceAccounts(namespace()).CreateToken(ctx, clientAccount(), &authv1.TokenRequest{
			Spec: authv1.TokenRequestSpec{Audiences: []string{apiServerName()}, ExpirationSeconds: &exp},
		}, metav1.CreateOptions{})
		if err != nil {
			return "", fmt.Errorf("request ateapi token: %w", err)
		}
		if tok.Status.Token == "" {
			return "", fmt.Errorf("request ateapi token: empty response")
		}
		return tok.Status.Token, nil
	}}
	// Mint once up front so a cluster without Substrate fails at start.
	if _, err := mint.get(ctx); err != nil {
		return nil, err
	}
	return mint.get, nil
}

// runTokenCommand runs a command such as `gcloud auth print-identity-token`
// and returns what it prints.
func runTokenCommand(ctx context.Context, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("token command: %w", err)
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", fmt.Errorf("token command printed nothing")
	}
	return tok, nil
}

// cachedToken keeps a fetched token until a minute before it expires, or
// for five minutes when it isn't a JWT and says nothing of its expiry.
type cachedToken struct {
	fetch func(context.Context) (string, error)
	mu    sync.Mutex
	tok   string
	until time.Time
}

func (c *cachedToken) get(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tok != "" && time.Now().Before(c.until) {
		return c.tok, nil
	}
	tok, err := c.fetch(ctx)
	if err != nil {
		return "", err
	}
	c.tok, c.until = tok, time.Now().Add(5*time.Minute)
	if _, exp := Claims(tok); !exp.IsZero() {
		c.until = exp.Add(-time.Minute)
	}
	return tok, nil
}

// Claims reads a JWT's sub and expiry without checking its signature.
// ateapi checks the token; the console only uses these to label who it
// acts as and to know when to refresh. Zero values mean it isn't a JWT.
func Claims(tok string) (sub string, exp time.Time) {
	parsed, err := jwt.ParseSigned(tok, []jose.SignatureAlgorithm{jose.RS256, jose.RS384, jose.RS512, jose.ES256, jose.ES384, jose.ES512, jose.PS256, jose.EdDSA})
	if err != nil {
		return "", time.Time{}
	}
	var c jwt.Claims
	if err := parsed.UnsafeClaimsWithoutVerification(&c); err != nil {
		return "", time.Time{}
	}
	if c.Expiry != nil {
		exp = c.Expiry.Time()
	}
	return c.Subject, exp
}

// ErrNoCaller is returned in forward mode for a call with no caller token.
var ErrNoCaller = status.Error(codes.Unauthenticated, "no caller token: in forward mode every request needs the caller's own ateapi token")

// callerCreds sends the caller's token when the context has one, and the
// console's own otherwise.
type callerCreds struct {
	own func(context.Context) (string, error)
}

func (c callerCreds) token(ctx context.Context) (string, error) {
	if caller, ok := backend.CallerFrom(ctx); ok {
		return caller.Token, nil
	}
	if c.own == nil {
		return "", ErrNoCaller
	}
	return c.own(ctx)
}

func (c callerCreds) GetRequestMetadata(ctx context.Context, _ ...string) (map[string]string, error) {
	tok, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	if tok == "" {
		return nil, fmt.Errorf("bearer token is empty")
	}
	return map[string]string{"authorization": "Bearer " + tok}, nil
}

func (callerCreds) RequireTransportSecurity() bool { return true }

// servicePortForward forwards a random local port to servicePort of a
// Service by picking one ready backing pod, as `kubectl port-forward svc/x`
// does. The tunnel does not fail over if that pod dies.
func servicePortForward(ctx context.Context, cfg *rest.Config, cs kubernetes.Interface, ns, service string, servicePort int32) (int, func(), error) {
	svc, err := cs.CoreV1().Services(ns).Get(ctx, service, metav1.GetOptions{})
	if err != nil {
		return 0, nil, fmt.Errorf("get service: %w", err)
	}
	if len(svc.Spec.Selector) == 0 {
		return 0, nil, fmt.Errorf("service %s has no selector", service)
	}
	pods, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(svc.Spec.Selector).String()})
	if err != nil {
		return 0, nil, fmt.Errorf("list pods: %w", err)
	}
	var pod *corev1.Pod
	for i := range pods.Items {
		if podReady(&pods.Items[i]) {
			pod = &pods.Items[i]
			break
		}
	}
	if pod == nil {
		return 0, nil, fmt.Errorf("no ready %s pods in %s", service, ns)
	}
	target, err := targetPort(svc, pod, servicePort)
	if err != nil {
		return 0, nil, err
	}
	req := cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(ns).Name(pod.Name).SubResource("portforward")
	dialer, err := portforward.NewSPDYOverWebsocketDialer(req.URL(), cfg)
	if err != nil {
		return 0, nil, fmt.Errorf("websocket dialer: %w", err)
	}
	stopCh, readyCh := make(chan struct{}), make(chan struct{})
	fw, err := portforward.New(dialer, []string{fmt.Sprintf("0:%d", target)}, stopCh, readyCh, io.Discard, io.Discard)
	if err != nil {
		return 0, nil, err
	}
	errCh := make(chan error, 1)
	go func() {
		if err := fw.ForwardPorts(); err != nil {
			errCh <- err
		}
	}()
	select {
	case <-readyCh:
	case err := <-errCh:
		return 0, nil, err
	case <-ctx.Done():
		close(stopCh)
		return 0, nil, ctx.Err()
	}
	ports, err := fw.GetPorts()
	if err != nil || len(ports) == 0 {
		close(stopCh)
		return 0, nil, fmt.Errorf("get forwarded ports: %w", err)
	}
	return int(ports[0].Local), func() { close(stopCh) }, nil
}

func targetPort(svc *corev1.Service, pod *corev1.Pod, port int32) (int32, error) {
	for _, sp := range svc.Spec.Ports {
		if sp.Port != port {
			continue
		}
		if sp.TargetPort.Type == intstr.String {
			for _, c := range pod.Spec.Containers {
				for _, cp := range c.Ports {
					if cp.Name == sp.TargetPort.StrVal {
						return cp.ContainerPort, nil
					}
				}
			}
			return 0, fmt.Errorf("named targetPort %q not found on pod %s", sp.TargetPort.StrVal, pod.Name)
		}
		if sp.TargetPort.IntVal == 0 {
			return sp.Port, nil
		}
		return sp.TargetPort.IntVal, nil
	}
	return 0, fmt.Errorf("service %s has no port %d", svc.Name, port)
}

func podReady(p *corev1.Pod) bool {
	if p.Status.Phase != corev1.PodRunning || p.DeletionTimestamp != nil {
		return false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

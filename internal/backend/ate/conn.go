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
// and authenticate with a minted ate-client ServiceAccount token.
// Delete this file once Substrate publishes a client module.

package ate

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
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
// api Service. It returns the connection and a func that tears it down.
func dial(ctx context.Context, cfg *rest.Config, cs *kubernetes.Clientset, endpoint, tokenFile string) (*grpc.ClientConn, func(), error) {
	stop := func() {}
	if endpoint == "" {
		port, s, err := servicePortForward(ctx, cfg, cs, namespace(), apiService(), 443)
		if err != nil {
			return nil, nil, fmt.Errorf("port-forward to %s/%s: %w", namespace(), apiService(), err)
		}
		endpoint, stop = fmt.Sprintf("127.0.0.1:%d", port), s
	}
	tlsCfg, err := serverTLSConfig(ctx, cs, apiServerName())
	if err != nil {
		stop()
		return nil, nil, err
	}
	tok, err := bearerToken(ctx, cs, tokenFile)
	if err != nil {
		stop()
		return nil, nil, err
	}
	conn, err := grpc.NewClient(endpoint,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		grpc.WithPerRPCCredentials(tok),
		grpc.WithDefaultServiceConfig(`{"loadBalancingConfig": [{"round_robin":{}}]}`),
	)
	if err != nil {
		stop()
		return nil, nil, fmt.Errorf("dial ateapi: %w", err)
	}
	return conn, func() { conn.Close(); stop() }, nil
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

// bearerToken reads tokenFile ("-" for stdin) or mints a one-hour
// ate-client ServiceAccount token, as kubectl-ate does. The minted token
// authenticates as that ServiceAccount, not as the person running the
// console.
func bearerToken(ctx context.Context, cs *kubernetes.Clientset, tokenFile string) (credentials.PerRPCCredentials, error) {
	switch tokenFile {
	case "":
		exp := int64(3600)
		tok, err := cs.CoreV1().ServiceAccounts(namespace()).CreateToken(ctx, clientAccount(), &authv1.TokenRequest{
			Spec: authv1.TokenRequestSpec{Audiences: []string{apiServerName()}, ExpirationSeconds: &exp},
		}, metav1.CreateOptions{})
		if err != nil {
			return nil, fmt.Errorf("request ateapi token: %w", err)
		}
		if tok.Status.Token == "" {
			return nil, fmt.Errorf("request ateapi token: empty response")
		}
		return staticToken(tok.Status.Token), nil
	case "-":
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, err
		}
		return staticToken(strings.TrimSpace(string(b))), nil
	}
	return fileToken(tokenFile), nil
}

type staticToken string

func (t staticToken) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	if t == "" {
		return nil, fmt.Errorf("bearer token is empty")
	}
	return map[string]string{"authorization": "Bearer " + string(t)}, nil
}
func (staticToken) RequireTransportSecurity() bool { return true }

type fileToken string

func (f fileToken) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	b, err := os.ReadFile(string(f))
	if err != nil {
		return nil, fmt.Errorf("read token file: %w", err)
	}
	return staticToken(strings.TrimSpace(string(b))).GetRequestMetadata(context.Background())
}
func (fileToken) RequireTransportSecurity() bool { return true }

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

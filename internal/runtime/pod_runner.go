package runtime

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"gopkg.in/yaml.v3"
)

type PodRunner struct {
	cfg       RunnerConfig
	mu        sync.RWMutex
	token     string
	namespace string
	caPool    *x509.CertPool
	apiHost   string
	httpC     *http.Client
}

const (
	saTokenPath     = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	saCACertPath    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	saNamespacePath = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
)

func newPodRunner(cfg RunnerConfig) (*PodRunner, error) {
	r := &PodRunner{cfg: cfg}
	if _, err := os.Stat(saTokenPath); err == nil {
		if err := r.discover(); err != nil {
			return nil, fmt.Errorf("PodRunner: in-cluster discovery: %w", err)
		}
		return r, nil
	}
	if cfg.KubeconfigPath != "" {
		if err := r.discoverFromKubeconfig(cfg.KubeconfigPath); err != nil {
			return nil, fmt.Errorf("PodRunner: kubeconfig discovery: %w", err)
		}
		return r, nil
	}
	return nil, errors.New("runtime.NewRunner: PodRunner requires SA mount or KubeconfigPath")
}

func (r *PodRunner) discover() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tokenB, err := os.ReadFile(saTokenPath)
	if err != nil {
		return err
	}
	r.token = strings.TrimSpace(string(tokenB))
	nsB, err := os.ReadFile(saNamespacePath)
	if err != nil {
		return err
	}
	r.namespace = strings.TrimSpace(string(nsB))
	caB, err := os.ReadFile(saCACertPath)
	if err != nil {
		return err
	}
	r.caPool = x509.NewCertPool()
	if !r.caPool.AppendCertsFromPEM(caB) {
		return errors.New("CA invalid PEM")
	}
	r.apiHost = "https://kubernetes.default.svc"
	r.httpC = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: r.caPool}}}
	return nil
}

// kubeconfigDoc captures the minimal kubeconfig YAML shape D1.7 needs.
type kubeconfigDoc struct {
	CurrentContext string `yaml:"current-context"`
	Contexts       []struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster   string `yaml:"cluster"`
			User      string `yaml:"user"`
			Namespace string `yaml:"namespace"`
		} `yaml:"context"`
	} `yaml:"contexts"`
	Clusters []struct {
		Name    string `yaml:"name"`
		Cluster struct {
			Server                   string `yaml:"server"`
			CertificateAuthorityData string `yaml:"certificate-authority-data"`
			CertificateAuthority     string `yaml:"certificate-authority"`
			InsecureSkipTLSVerify    bool   `yaml:"insecure-skip-tls-verify"`
		} `yaml:"cluster"`
	} `yaml:"clusters"`
	Users []struct {
		Name string `yaml:"name"`
		User struct {
			Token                 string `yaml:"token"`
			TokenFile             string `yaml:"tokenFile"`
			ClientCertificateData string `yaml:"client-certificate-data"`
			ClientKeyData         string `yaml:"client-key-data"`
		} `yaml:"user"`
	} `yaml:"users"`
}

// discoverFromKubeconfig parses a kubeconfig file (D1.7) and populates
// the PodRunner's token, namespace, CA, and apiHost so out-of-cluster
// operators can drive Spawn/Get/Stop/AttachIO against a remote cluster.
func (r *PodRunner) discoverFromKubeconfig(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read kubeconfig %q: %w", path, err)
	}
	var doc kubeconfigDoc
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("parse kubeconfig %q: %w", path, err)
	}
	if doc.CurrentContext == "" {
		return errors.New("kubeconfig: missing current-context")
	}
	var ctxClusterName, ctxUserName, ctxNamespace string
	for _, c := range doc.Contexts {
		if c.Name == doc.CurrentContext {
			ctxClusterName = c.Context.Cluster
			ctxUserName = c.Context.User
			ctxNamespace = c.Context.Namespace
			break
		}
	}
	if ctxClusterName == "" {
		return fmt.Errorf("kubeconfig: context %q not found", doc.CurrentContext)
	}
	if ctxNamespace == "" {
		ctxNamespace = "default"
	}
	var server, caData string
	var insecureSkipVerify bool
	for _, c := range doc.Clusters {
		if c.Name == ctxClusterName {
			server = c.Cluster.Server
			insecureSkipVerify = c.Cluster.InsecureSkipTLSVerify
			if c.Cluster.CertificateAuthorityData != "" {
				decoded, err := base64.StdEncoding.DecodeString(c.Cluster.CertificateAuthorityData)
				if err != nil {
					return fmt.Errorf("decode CA: %w", err)
				}
				caData = string(decoded)
			} else if c.Cluster.CertificateAuthority != "" {
				ca, err := os.ReadFile(c.Cluster.CertificateAuthority)
				if err != nil {
					return fmt.Errorf("read CA file %q: %w", c.Cluster.CertificateAuthority, err)
				}
				caData = string(ca)
			}
			break
		}
	}
	if server == "" {
		return fmt.Errorf("kubeconfig: cluster %q not found", ctxClusterName)
	}
	var token string
	for _, u := range doc.Users {
		if u.Name == ctxUserName {
			if u.User.TokenFile != "" {
				tb, err := os.ReadFile(u.User.TokenFile)
				if err != nil {
					return fmt.Errorf("read tokenFile: %w", err)
				}
				token = strings.TrimSpace(string(tb))
			} else if u.User.Token != "" {
				token = strings.TrimSpace(u.User.Token)
			}
			break
		}
	}
	if token == "" {
		return fmt.Errorf("kubeconfig: user %q has no token / tokenFile (client-cert auth not yet supported in D1.7)", ctxUserName)
	}
	r.token = token
	r.namespace = ctxNamespace
	r.apiHost = strings.TrimRight(server, "/")
	tlsCfg := &tls.Config{InsecureSkipVerify: insecureSkipVerify}
	if !insecureSkipVerify && caData != "" {
		r.caPool = x509.NewCertPool()
		if !r.caPool.AppendCertsFromPEM([]byte(caData)) {
			return errors.New("kubeconfig: CA data is not valid PEM")
		}
		tlsCfg.RootCAs = r.caPool
	}
	r.httpC = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	return nil
}

func (r *PodRunner) Spawn(ctx context.Context, spec SpawnSpec) (*SessionInfo, error) {
	r.mu.RLock()
	ns, token := r.namespace, r.token
	r.mu.RUnlock()
	if token == "" {
		return nil, errors.New("PodRunner.Spawn: no auth (run via in-cluster SA mount OR --kubeconfig)")
	}
	if spec.Name == "" {
		return nil, errors.New("Spawn: empty Name")
	}
	if ns == "" {
		return nil, errors.New("Spawn: empty namespace")
	}
	body, err := json.Marshal(buildPodManifest(ns, spec))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.apiHost+"/api/v1/namespaces/"+ns+"/pods", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.signRequest(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.httpC.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("POST pod HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return &SessionInfo{Name: spec.Name, AgentSlug: spec.AgentSlug, Team: spec.Team, Role: spec.Role, Cwd: spec.Cwd, CreatedAt: time.Now().UTC()}, nil
}

func (r *PodRunner) Get(ctx context.Context, sessionID string) (*SessionInfo, error) {
	r.mu.RLock()
	ns, token := r.namespace, r.token
	r.mu.RUnlock()
	if token == "" {
		return nil, errors.New("PodRunner.Get: no auth")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.apiHost+"/api/v1/namespaces/"+ns+"/pods/"+sessionID, nil)
	if err != nil {
		return nil, err
	}
	r.signRequest(req)
	resp, err := r.httpC.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrSessionNotFound
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET pod HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var pod struct {
		Metadata struct {
			Name              string    `json:"name"`
			CreationTimestamp time.Time `json:"creationTimestamp"`
			Labels            map[string]string
		} `json:"metadata"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pod); err != nil {
		return nil, err
	}
	return &SessionInfo{
		Name: pod.Metadata.Name, AgentSlug: pod.Metadata.Labels["chepherd.io/agent-slug"],
		Team: pod.Metadata.Labels["chepherd.io/team"], Role: Role(pod.Metadata.Labels["chepherd.io/role"]),
		CreatedAt: pod.Metadata.CreationTimestamp,
	}, nil
}

func (r *PodRunner) Stop(ctx context.Context, sessionID string) error {
	r.mu.RLock()
	ns, token := r.namespace, r.token
	r.mu.RUnlock()
	if token == "" {
		return errors.New("PodRunner.Stop: no auth")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, r.apiHost+"/api/v1/namespaces/"+ns+"/pods/"+sessionID, nil)
	if err != nil {
		return err
	}
	r.signRequest(req)
	resp, err := r.httpC.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("DELETE pod HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// List (D1.3) — labelSelector for chepherd-managed Pods.
func (r *PodRunner) List(ctx context.Context) ([]*SessionInfo, error) {
	r.mu.RLock()
	ns, token := r.namespace, r.token
	r.mu.RUnlock()
	if token == "" {
		return nil, errors.New("PodRunner.List: no auth")
	}
	endpoint := r.apiHost + "/api/v1/namespaces/" + ns + "/pods?labelSelector=" + url.QueryEscape("chepherd.io/managed-by=chepherd")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	r.signRequest(req)
	resp, err := r.httpC.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET pods HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name              string            `json:"name"`
				CreationTimestamp time.Time         `json:"creationTimestamp"`
				Labels            map[string]string `json:"labels"`
				Annotations       map[string]string `json:"annotations"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	out := make([]*SessionInfo, 0, len(list.Items))
	for _, item := range list.Items {
		paused := item.Metadata.Annotations["chepherd.io/paused"] == "true"
		out = append(out, &SessionInfo{
			Name:      item.Metadata.Name,
			AgentSlug: item.Metadata.Labels["chepherd.io/agent-slug"],
			Team:      item.Metadata.Labels["chepherd.io/team"],
			Role:      Role(item.Metadata.Labels["chepherd.io/role"]),
			CreatedAt: item.Metadata.CreationTimestamp,
			Paused:    paused,
		})
	}
	return out, nil
}

// Pause (D1.4) — sets/clears chepherd.io/paused annotation on the Pod.
// K8s has no native per-Pod pause; the annotation is a chepherd-side
// flag observed by the agent's stdin reader (it ignores writes when
// paused). True pause/resume implemented via the agent container's
// own logic — chepherd only persists the intent.
func (r *PodRunner) Pause(ctx context.Context, sessionID string, paused bool) error {
	r.mu.RLock()
	ns, token := r.namespace, r.token
	r.mu.RUnlock()
	if token == "" {
		return errors.New("PodRunner.Pause: no auth")
	}
	patch := map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]string{
				"chepherd.io/paused": fmt.Sprintf("%t", paused),
			},
		},
	}
	body, _ := json.Marshal(patch)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		r.apiHost+"/api/v1/namespaces/"+ns+"/pods/"+sessionID, bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.signRequest(req)
	req.Header.Set("Content-Type", "application/strategic-merge-patch+json")
	resp, err := r.httpC.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrSessionNotFound
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("PATCH pod HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// Restart (D1.5) — Stop + re-Spawn with the same SessionInfo. Labels
// preserved across the bounce because the pod manifest is rebuilt
// from Get'd SessionInfo. Returns a fresh SessionInfo with new
// CreatedAt + same identity (name, agent slug, team, role).
func (r *PodRunner) Restart(ctx context.Context, sessionID string) error {
	info, err := r.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if err := r.Stop(ctx, sessionID); err != nil {
		return err
	}
	// Wait briefly for the apiserver to flush the deletion before re-create.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := r.Get(ctx, sessionID); err == ErrSessionNotFound {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	_, err = r.Spawn(ctx, SpawnSpec{
		Name: info.Name, AgentSlug: info.AgentSlug, Team: info.Team,
		Role: info.Role, Cwd: info.Cwd,
	})
	return err
}

// Rename (D1.6) — k8s pods can't be renamed in place. Approach:
//   1. Get the existing pod manifest via Get (loses cwd; chepherd labels carry the rest)
//   2. Stop the old pod
//   3. Spawn under newName with the same SessionInfo (labels + slug preserved)
// Net effect: a new Pod under newName with the old's identity tags;
// any in-flight PTY connection drops. Dashboard re-attach is the
// operator's responsibility.
func (r *PodRunner) Rename(ctx context.Context, sessionID, newName string) error {
	if newName == "" {
		return errors.New("Rename: empty newName")
	}
	if sessionID == newName {
		return nil
	}
	info, err := r.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if err := r.Stop(ctx, sessionID); err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := r.Get(ctx, sessionID); err == ErrSessionNotFound {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	_, err = r.Spawn(ctx, SpawnSpec{
		Name: newName, AgentSlug: info.AgentSlug, Team: info.Team,
		Role: info.Role, Cwd: info.Cwd,
	})
	return err
}

// AttachIO (D1.2) — opens a WebSocket against the k8s exec
// subresource for the agent container and returns an
// io.ReadWriteCloser that multiplexes stdin (writes) + stdout (reads).
// The k8s exec wire protocol prefixes each frame with a 1-byte
// channel: 0=stdin, 1=stdout, 2=stderr, 3=error, 4=resize.
func (r *PodRunner) AttachIO(ctx context.Context, sessionID string) (io.ReadWriteCloser, error) {
	r.mu.RLock()
	ns, token, host, caPool := r.namespace, r.token, r.apiHost, r.caPool
	r.mu.RUnlock()
	if token == "" {
		return nil, errors.New("PodRunner.AttachIO: no auth")
	}
	// Reach the agent container by slug — the sidecar shares the Pod
	// network namespace but is a separate exec target.
	info, err := r.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	container := info.AgentSlug
	if container == "" {
		container = "claude-code"
	}
	q := url.Values{}
	q.Set("stdin", "true")
	q.Set("stdout", "true")
	q.Set("stderr", "true")
	q.Set("tty", "true")
	q.Set("container", container)
	q["command"] = []string{"/bin/sh"}
	wsURL := strings.Replace(host, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL += "/api/v1/namespaces/" + ns + "/pods/" + sessionID + "/exec?" + q.Encode()
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		Subprotocols:     []string{"v4.channel.k8s.io"},
	}
	if caPool != nil {
		dialer.TLSClientConfig = &tls.Config{RootCAs: caPool}
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	conn, _, err := dialer.DialContext(ctx, wsURL, header)
	if err != nil {
		return nil, fmt.Errorf("exec dial: %w", err)
	}
	return newExecStream(conn), nil
}

// execStream wraps a k8s exec WebSocket as an io.ReadWriteCloser.
// Reads return stdout (channel 1) bytes; writes prefix with channel 0
// (stdin). Stderr (channel 2) is discarded — operator dashboards
// surface it via Pod log streaming separately.
type execStream struct {
	conn *websocket.Conn
	mu   sync.Mutex
	buf  bytes.Buffer
}

func newExecStream(conn *websocket.Conn) *execStream {
	return &execStream{conn: conn}
}

func (s *execStream) Read(p []byte) (int, error) {
	s.mu.Lock()
	if s.buf.Len() > 0 {
		n, _ := s.buf.Read(p)
		s.mu.Unlock()
		return n, nil
	}
	s.mu.Unlock()
	for {
		_, msg, err := s.conn.ReadMessage()
		if err != nil {
			return 0, err
		}
		if len(msg) < 1 {
			continue
		}
		channel, payload := msg[0], msg[1:]
		if channel == 1 { // stdout
			if len(payload) == 0 {
				continue
			}
			s.mu.Lock()
			s.buf.Write(payload)
			n, _ := s.buf.Read(p)
			s.mu.Unlock()
			return n, nil
		}
		// stderr (2), error (3), resize (4) — ignore for D1.2 minimal viable.
	}
}

func (s *execStream) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	frame := make([]byte, 1+len(p))
	frame[0] = 0 // stdin channel
	copy(frame[1:], p)
	if err := s.conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *execStream) Close() error { return s.conn.Close() }

func (r *PodRunner) signRequest(req *http.Request) {
	r.mu.RLock()
	tok := r.token
	r.mu.RUnlock()
	req.Header.Set("Authorization", "Bearer "+tok)
}

func buildPodManifest(ns string, spec SpawnSpec) map[string]any {
	return map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{
			"name": spec.Name, "namespace": ns,
			"labels": map[string]string{
				"chepherd.io/agent-slug": spec.AgentSlug, "chepherd.io/team": spec.Team,
				"chepherd.io/role": string(spec.Role), "chepherd.io/managed-by": "chepherd",
			},
		},
		"spec": map[string]any{
			"restartPolicy": "Never",
			"containers": []map[string]any{
				agentContainer(spec),
				mcpSidecarContainer(),
			},
		},
	}
}

func agentContainer(spec SpawnSpec) map[string]any {
	return map[string]any{
		"name": spec.AgentSlug, "image": agentImageFor(spec.AgentSlug),
		"imagePullPolicy": "IfNotPresent", "workingDir": spec.Cwd,
		"tty": true, "stdin": true, "stdinOnce": false,
		"env": []map[string]any{
			{"name": "CHEPHERD_MCP_LISTEN", "value": "127.0.0.1:9090"},
		},
	}
}

func mcpSidecarContainer() map[string]any {
	return map[string]any{
		"name": "chepherd-mcp", "image": "ghcr.io/chepherd/chepherd:0.9.3",
		"imagePullPolicy": "IfNotPresent",
		"args":            []string{"mcp", "--listen", "127.0.0.1:9090"},
		"env": []map[string]any{
			{"name": "CHEPHERD_CONTROL_PLANE_URL", "value": "http://chepherd.chepherd.svc.cluster.local:80"},
			{"name": "CHEPHERD_POD_NAME", "valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "metadata.name"}}},
		},
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "50m", "memory": "64Mi"},
			"limits":   map[string]any{"cpu": "200m", "memory": "256Mi"},
		},
	}
}

func agentImageFor(slug string) string {
	if slug == "" {
		slug = "claude-code"
	}
	return "ghcr.io/chepherd/chepherd-agent:" + slug
}

var _ Runner = (*PodRunner)(nil)

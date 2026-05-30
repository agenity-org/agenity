package runtime

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
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

func (r *PodRunner) Spawn(ctx context.Context, spec SpawnSpec) (*SessionInfo, error) {
	r.mu.RLock()
	ns, token := r.namespace, r.token
	r.mu.RUnlock()
	if token == "" {
		return nil, errScaffoldPending("PodRunner.Spawn (D1.7)")
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
		return nil, errScaffoldPending("PodRunner.Get (D1.7)")
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
		return errScaffoldPending("PodRunner.Stop (D1.7)")
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

func (r *PodRunner) List(ctx context.Context) ([]*SessionInfo, error) {
	return nil, errScaffoldPending("PodRunner.List (D1.3)")
}
func (r *PodRunner) Pause(ctx context.Context, sessionID string, paused bool) error {
	return errScaffoldPending("PodRunner.Pause (D1.4)")
}
func (r *PodRunner) Restart(ctx context.Context, sessionID string) error {
	return errScaffoldPending("PodRunner.Restart (D1.5)")
}
func (r *PodRunner) Rename(ctx context.Context, sessionID, newName string) error {
	return errScaffoldPending("PodRunner.Rename (D1.6)")
}
func (r *PodRunner) AttachIO(ctx context.Context, sessionID string) (io.ReadWriteCloser, error) {
	return nil, errScaffoldPending("PodRunner.AttachIO (D1.2)")
}

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
				// #316 D6 — chepherd-mcp sidecar in every agent Pod. Shares
				// the Pod's network namespace; reaches the control plane via
				// the in-cluster Service DNS name. The agent container's
				// process talks to the sidecar over localhost MCP socket.
				mcpSidecarContainer(),
			},
		},
	}
}

// agentContainer returns the agent's own container manifest section.
// Split out so D6's sidecar addition keeps buildPodManifest readable.
func agentContainer(spec SpawnSpec) map[string]any {
	return map[string]any{
		"name":            spec.AgentSlug,
		"image":           agentImageFor(spec.AgentSlug),
		"imagePullPolicy": "IfNotPresent",
		"workingDir":      spec.Cwd,
		"tty":             true,
		"stdin":           true,
		"stdinOnce":       false,
		"env": []map[string]any{
			{"name": "CHEPHERD_MCP_LISTEN", "value": "127.0.0.1:9090"},
		},
	}
}

// mcpSidecarContainer returns the chepherd-mcp sidecar manifest. Every
// agent Pod gets it so the agent has access to chepherd's MCP tool
// surface (spawn peer, send_to_session, set_scorecard, etc.) via a
// localhost socket. The sidecar dials the chepherd control plane's
// in-cluster Service for upstream relay.
//
// Refs #316 D6.
func mcpSidecarContainer() map[string]any {
	return map[string]any{
		"name":            "chepherd-mcp",
		"image":           "ghcr.io/chepherd/chepherd:0.9.3",
		"imagePullPolicy": "IfNotPresent",
		"args":            []string{"mcp", "--listen", "127.0.0.1:9090"},
		"env": []map[string]any{
			{"name": "CHEPHERD_CONTROL_PLANE_URL",
				"value": "http://chepherd.chepherd.svc.cluster.local:80"},
			{"name": "CHEPHERD_POD_NAME",
				"valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "metadata.name"}}},
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

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

// PodRunner implements Runner for K8s CRI Pods. Uses pure net/http
// against the kube-apiserver via the in-cluster ServiceAccount mount
// — no client-go transitive dep tree.
//
// Refs #312 (D1) #208.
type PodRunner struct {
	cfg RunnerConfig

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
	if inClusterEnvProbe() {
		if err := r.discover(); err != nil {
			return nil, fmt.Errorf("PodRunner: in-cluster discovery: %w", err)
		}
		return r, nil
	}
	if cfg.KubeconfigPath != "" {
		return r, nil
	}
	return nil, errors.New("runtime.NewRunner: PodRunner requires either in-cluster ServiceAccount mount OR cfg.KubeconfigPath")
}

func inClusterEnvProbe() bool {
	_, err := os.Stat(saTokenPath)
	return err == nil
}

func (r *PodRunner) discover() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tokenB, err := os.ReadFile(saTokenPath)
	if err != nil {
		return fmt.Errorf("read SA token: %w", err)
	}
	r.token = strings.TrimSpace(string(tokenB))
	nsB, err := os.ReadFile(saNamespacePath)
	if err != nil {
		return fmt.Errorf("read SA namespace: %w", err)
	}
	r.namespace = strings.TrimSpace(string(nsB))
	caB, err := os.ReadFile(saCACertPath)
	if err != nil {
		return fmt.Errorf("read SA CA: %w", err)
	}
	r.caPool = x509.NewCertPool()
	if !r.caPool.AppendCertsFromPEM(caB) {
		return errors.New("SA CA is not valid PEM")
	}
	r.apiHost = "https://kubernetes.default.svc"
	r.httpC = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: r.caPool},
		},
	}
	return nil
}

func (r *PodRunner) Spawn(ctx context.Context, spec SpawnSpec) (*SessionInfo, error) {
	r.mu.RLock()
	ns := r.namespace
	token := r.token
	r.mu.RUnlock()
	if token == "" {
		return nil, errScaffoldPending("PodRunner.Spawn (kubeconfig-path mode pending D1.7)")
	}
	if spec.Name == "" {
		return nil, errors.New("PodRunner.Spawn: empty spec.Name")
	}
	if ns == "" {
		return nil, errors.New("PodRunner.Spawn: namespace empty")
	}
	manifest := buildPodManifest(ns, spec)
	body, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	endpoint := r.apiHost + "/api/v1/namespaces/" + ns + "/pods"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.signRequest(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.httpC.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST pod: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("kube-apiserver POST pod HTTP %d: %s",
			resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return &SessionInfo{
		Name:      spec.Name,
		AgentSlug: spec.AgentSlug,
		Team:      spec.Team,
		Role:      spec.Role,
		Cwd:       spec.Cwd,
		CreatedAt: time.Now().UTC(),
	}, nil
}

func (r *PodRunner) Get(ctx context.Context, sessionID string) (*SessionInfo, error) {
	r.mu.RLock()
	ns := r.namespace
	token := r.token
	r.mu.RUnlock()
	if token == "" {
		return nil, errScaffoldPending("PodRunner.Get (kubeconfig-path mode pending D1.7)")
	}
	endpoint := r.apiHost + "/api/v1/namespaces/" + ns + "/pods/" + sessionID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	r.signRequest(req)
	resp, err := r.httpC.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET pod: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrSessionNotFound
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("kube-apiserver GET pod HTTP %d: %s",
			resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var pod struct {
		Metadata struct {
			Name              string    `json:"name"`
			CreationTimestamp time.Time `json:"creationTimestamp"`
			Labels            map[string]string
		} `json:"metadata"`
		Status struct {
			Phase string `json:"phase"`
		} `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pod); err != nil {
		return nil, fmt.Errorf("decode pod: %w", err)
	}
	return &SessionInfo{
		Name:      pod.Metadata.Name,
		AgentSlug: pod.Metadata.Labels["chepherd.io/agent-slug"],
		Team:     pod.Metadata.Labels["chepherd.io/team"],
		Role:     Role(pod.Metadata.Labels["chepherd.io/role"]),
		CreatedAt: pod.Metadata.CreationTimestamp,
	}, nil
}

func (r *PodRunner) Stop(ctx context.Context, sessionID string) error {
	r.mu.RLock()
	ns := r.namespace
	token := r.token
	r.mu.RUnlock()
	if token == "" {
		return errScaffoldPending("PodRunner.Stop (kubeconfig-path mode pending D1.7)")
	}
	endpoint := r.apiHost + "/api/v1/namespaces/" + ns + "/pods/" + sessionID
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	r.signRequest(req)
	resp, err := r.httpC.Do(req)
	if err != nil {
		return fmt.Errorf("DELETE pod: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("kube-apiserver DELETE pod HTTP %d: %s",
			resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

func (r *PodRunner) List(ctx context.Context) ([]*SessionInfo, error) {
	return nil, errScaffoldPending("PodRunner.List (D1.3)")
}
func (r *PodRunner) Pause(ctx context.Context, sessionID string, paused bool) error {
	return errScaffoldPending("PodRunner.Pause (D1.4 — no per-pod pause primitive)")
}
func (r *PodRunner) Restart(ctx context.Context, sessionID string) error {
	return errScaffoldPending("PodRunner.Restart (D1.5 — delete+respawn)")
}
func (r *PodRunner) Rename(ctx context.Context, sessionID, newName string) error {
	return errScaffoldPending("PodRunner.Rename (D1.6 — pods can't be renamed)")
}
func (r *PodRunner) AttachIO(ctx context.Context, sessionID string) (io.ReadWriteCloser, error) {
	return nil, errScaffoldPending("PodRunner.AttachIO (D1.2 — WebSocket SPDY upgrade)")
}

func (r *PodRunner) signRequest(req *http.Request) {
	r.mu.RLock()
	token := r.token
	r.mu.RUnlock()
	req.Header.Set("Authorization", "Bearer "+token)
}

func buildPodManifest(ns string, spec SpawnSpec) map[string]any {
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":      spec.Name,
			"namespace": ns,
			"labels": map[string]string{
				"chepherd.io/agent-slug": spec.AgentSlug,
				"chepherd.io/team":       spec.Team,
				"chepherd.io/role":       string(spec.Role),
				"chepherd.io/managed-by": "chepherd",
			},
		},
		"spec": map[string]any{
			"restartPolicy": "Never",
			"containers": []map[string]any{
				{
					"name":            spec.AgentSlug,
					"image":           agentImageFor(spec.AgentSlug),
					"imagePullPolicy": "IfNotPresent",
					"workingDir":      spec.Cwd,
					"tty":             true,
					"stdin":           true,
					"stdinOnce":       false,
				},
			},
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

// internal/federation/mtls.go implements V0.9.2-ARCHITECTURE.md
// §15.1 + §22 cross-org daemon-to-daemon mTLS termination
// (#487 Wave T3). Each daemon holds an ECDSA P-256 leaf cert
// signed by its org's root CA; peer daemons' org-root CAs are
// pinned via the federation trust store.
//
// The TLS configs produced here go on:
//
//   - Federation.HTTPClient.Transport (outbound — cross-org delivers,
//     agent-card fetches, registry sync)
//   - The daemon's federation-side HTTP server (inbound — peer
//     daemons calling /jsonrpc, fetching agent cards)
//
// Dev/test mode: when no cert is configured, BuildClientTLSConfig /
// BuildServerTLSConfig return nil — callers fall through to plain
// HTTP via the existing transport (existing code path). Operators
// flip mTLS on via `--federation-mtls true` in cmd/run.go.
//
// Single cert per org for T3 (no rotation); the persistence purposes
// + KeyStore-mirroring pattern leave room for the rotation seam to
// land in a follow-up Wave without breaking the wire shape.
//
// Refs #487 V0.9.2-ARCHITECTURE.md §15.1 §22.
package federation

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
)

const (
	// FederationCertPurpose is the AuthSecret row that stores the
	// daemon's own federation leaf-cert PEM (cert + key concatenated).
	FederationCertPurpose = "federation-mtls-cert"

	// FederationPinnedCAsPurpose is the AuthSecret row that stores
	// the PEM bundle of trusted peer org-root CAs. Each line in the
	// bundle is one CA cert.
	FederationPinnedCAsPurpose = "federation-pinned-cas"

	// FederationCertValidity is how long a freshly-issued cert is
	// valid. v0.9.4 uses 1 year; rotation lands with a follow-up
	// Wave when production scale demands shorter windows.
	FederationCertValidity = 365 * 24 * time.Hour
)

// MTLSConfig wraps the loaded cert + pinned CA pool so the cmd/
// run.go wiring can pass a single object into Federation /
// FederatedDeliverer / the server-side TLS builder.
type MTLSConfig struct {
	Certificate tls.Certificate
	PinnedCAs   *x509.CertPool
	// OrgID is the X.509 Common Name on the leaf cert — also the
	// stable identifier other daemons use to look up their
	// corresponding pinned-CA entry.
	OrgID string
}

// LoadOrCreateMTLS returns the daemon's federation mTLS material.
// On first call when no cert exists in the AuthSecret store, mints
// a fresh self-signed P-256 cert (the daemon IS its own org root
// in this T3 cut — multi-tier CA chains arrive with cross-org
// federation rollout). orgID becomes the CN.
//
// Pinned-CAs row is OPTIONAL: when absent the returned CertPool is
// empty + RequireAndVerifyClientCert at the server side will reject
// every inbound peer. Operators populate it via grant-flow
// out-of-band cert exchange (D3 GrantStore CRUD can later carry the
// peer cert in a follow-up Wave); for now LoadOrCreateMTLS just
// surfaces whatever's been Save()d to the row.
func LoadOrCreateMTLS(ctx context.Context, repo persistence.AuthSecretRepository, orgID string) (*MTLSConfig, error) {
	if repo == nil {
		return nil, errors.New("LoadOrCreateMTLS: nil repository")
	}
	if orgID == "" {
		return nil, errors.New("LoadOrCreateMTLS: empty orgID")
	}
	certPEM, keyPEM, err := loadOrMintCert(ctx, repo, orgID)
	if err != nil {
		return nil, fmt.Errorf("LoadOrCreateMTLS: cert: %w", err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("LoadOrCreateMTLS: parse pair: %w", err)
	}
	pool, err := loadPinnedCAs(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("LoadOrCreateMTLS: pinned CAs: %w", err)
	}
	return &MTLSConfig{Certificate: cert, PinnedCAs: pool, OrgID: orgID}, nil
}

// BuildClientTLSConfig returns a *tls.Config suitable for an
// outbound federation HTTP client. The local cert is presented on
// every TLS handshake; peer certs are verified against the pinned
// CAs. When pinnedCAs is empty (no peers configured) the config
// still authenticates the local cert outbound but trusts no peer
// — every dial fails with x509: certificate signed by unknown
// authority. That's the conservative default; operators add peer
// CAs as grants are accepted.
func BuildClientTLSConfig(m *MTLSConfig) *tls.Config {
	if m == nil {
		return nil
	}
	return &tls.Config{
		Certificates: []tls.Certificate{m.Certificate},
		RootCAs:      m.PinnedCAs,
		MinVersion:   tls.VersionTLS13,
	}
}

// BuildServerTLSConfig returns a *tls.Config suitable for the
// daemon's federation-facing HTTP server. RequireAndVerifyClientCert
// ensures every inbound cross-org caller presents a cert that
// chains to a pinned CA — unauthenticated callers get an immediate
// handshake failure, not a JSON-RPC error envelope. The dashboard +
// the runner-facing HTTP listener stay on the existing TLS-without-
// client-cert path so browsers + runners aren't broken.
func BuildServerTLSConfig(m *MTLSConfig) *tls.Config {
	if m == nil {
		return nil
	}
	return &tls.Config{
		Certificates: []tls.Certificate{m.Certificate},
		ClientCAs:    m.PinnedCAs,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
}

// AddPinnedCA persists `caPEM` into the federation-pinned-cas row
// alongside any existing CAs. Called by the grant-acceptance flow
// when an operator approves a cross-org peer. The PEM is the peer
// daemon's org-root CA cert — the trust anchor the peer's leaf
// cert chains to.
//
// The persistence shape is a concatenation of PEM blocks (one per
// line group); LoadPinnedCAs parses every block on read.
func AddPinnedCA(ctx context.Context, repo persistence.AuthSecretRepository, caPEM []byte) error {
	if repo == nil {
		return errors.New("AddPinnedCA: nil repository")
	}
	if len(caPEM) == 0 || !strings.Contains(string(caPEM), "-----BEGIN CERTIFICATE-----") {
		return errors.New("AddPinnedCA: caPEM is not a PEM-encoded certificate")
	}
	existing, _ := repo.Get(ctx, FederationPinnedCAsPurpose)
	var bundle []byte
	if existing != nil {
		bundle = append(bundle, existing.Key...)
		if len(bundle) > 0 && bundle[len(bundle)-1] != '\n' {
			bundle = append(bundle, '\n')
		}
	}
	bundle = append(bundle, caPEM...)
	return repo.Save(ctx, FederationPinnedCAsPurpose, bundle, "x509-pem-bundle")
}

// CertPEMOf returns the PEM-encoded leaf cert (no key). Used by
// the grant-issuance flow to ship this daemon's cert to a peer for
// pinning on their end. Pairs with AddPinnedCA on the receiver.
func CertPEMOf(m *MTLSConfig) ([]byte, error) {
	if m == nil || len(m.Certificate.Certificate) == 0 {
		return nil, errors.New("CertPEMOf: empty certificate")
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: m.Certificate.Certificate[0],
	}), nil
}

func loadOrMintCert(ctx context.Context, repo persistence.AuthSecretRepository, orgID string) ([]byte, []byte, error) {
	if sec, err := repo.Get(ctx, FederationCertPurpose); err == nil && sec != nil && len(sec.Key) > 0 {
		certPEM, keyPEM, ok := splitCertAndKey(sec.Key)
		if ok {
			return certPEM, keyPEM, nil
		}
		// Stored bundle malformed — mint a fresh one + overwrite.
	} else if err != nil && !strings.Contains(err.Error(), "not found") {
		return nil, nil, fmt.Errorf("get cert row: %w", err)
	}
	// Mint a fresh leaf cert. Self-signed for v0.9.4; multi-tier CA
	// chain support arrives with cross-org production rollout.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: orgID},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(FederationCertValidity),
		KeyUsage: x509.KeyUsageDigitalSignature |
			x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
		BasicConstraintsValid: true,
		IsCA:                  true,
		// SANs: include orgID-as-DNS, plus localhost + 127.0.0.1 so
		// test + single-host dev deployments connect without
		// extra config. Production cmd/run.go will extend via the
		// follow-up Hostnames config (or operators issue cert via
		// their own CA + skip the LoadOrCreate mint path entirely).
		DNSNames:    []string{orgID, "localhost"},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, fmt.Errorf("create cert: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	bundle := append(append([]byte(nil), certPEM...), keyPEM...)
	if err := repo.Save(ctx, FederationCertPurpose, bundle, "x509-cert-key-pem"); err != nil {
		return nil, nil, fmt.Errorf("persist cert: %w", err)
	}
	return certPEM, keyPEM, nil
}

func splitCertAndKey(bundle []byte) (certPEM, keyPEM []byte, ok bool) {
	rest := bundle
	for {
		block, remaining := pem.Decode(rest)
		if block == nil {
			break
		}
		blockPEM := pem.EncodeToMemory(block)
		switch block.Type {
		case "CERTIFICATE":
			certPEM = append(certPEM, blockPEM...)
		case "EC PRIVATE KEY", "PRIVATE KEY":
			keyPEM = append(keyPEM, blockPEM...)
		}
		rest = remaining
	}
	return certPEM, keyPEM, len(certPEM) > 0 && len(keyPEM) > 0
}

func loadPinnedCAs(ctx context.Context, repo persistence.AuthSecretRepository) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	sec, err := repo.Get(ctx, FederationPinnedCAsPurpose)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return pool, nil
		}
		return nil, err
	}
	if sec == nil || len(sec.Key) == 0 {
		return pool, nil
	}
	if !pool.AppendCertsFromPEM(sec.Key) {
		return nil, errors.New("pinned-CAs row is not a valid PEM bundle")
	}
	return pool, nil
}

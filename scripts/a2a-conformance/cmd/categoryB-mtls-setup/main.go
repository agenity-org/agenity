// scripts/a2a-conformance/cmd/categoryB-mtls-setup/main.go is a
// one-shot helper for the v0.9.4 QA Category B mTLS walk. It opens
// each daemon's sqlite DB (after the daemon has been booted once
// + minted its federation cert), exports the LEAF cert + private
// key to PEM files, then cross-pins each peer's leaf as a trusted
// CA on the other.
//
// Symmetrical pattern to internal/e2e/p0_527_two_daemon_mtls_test.go
// but as a standalone CLI so the QA bash walker can drive the same
// material via raw curl / openssl s_client.
//
// Usage:
//
//	categoryB-mtls-setup \
//	  --a-state-dir /tmp/.../state-X --a-org-id alice.example \
//	  --b-state-dir /tmp/.../state-Y --b-org-id bob.example   \
//	  --out-dir /tmp/.../certs
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/agenity-org/agenity/internal/federation"
	"github.com/agenity-org/agenity/internal/persistence/sqlite"
)

func main() {
	aDir := flag.String("a-state-dir", "", "")
	aOrg := flag.String("a-org-id", "", "")
	bDir := flag.String("b-state-dir", "", "")
	bOrg := flag.String("b-org-id", "", "")
	outDir := flag.String("out-dir", "", "")
	flag.Parse()

	if *aDir == "" || *bDir == "" || *aOrg == "" || *bOrg == "" || *outDir == "" {
		log.Fatalf("all flags required")
	}
	if err := os.MkdirAll(*outDir, 0o700); err != nil {
		log.Fatalf("mkdir %s: %v", *outDir, err)
	}

	aCertPEM, aKeyPEM := loadAndExport(*aDir, *aOrg, *outDir, "a")
	bCertPEM, bKeyPEM := loadAndExport(*bDir, *bOrg, *outDir, "b")
	_, _ = aKeyPEM, bKeyPEM

	// Cross-pin: B trusts A's leaf cert as CA + vice versa.
	pinInto(*bDir, aCertPEM, "A->B")
	pinInto(*aDir, bCertPEM, "B->A")

	fmt.Printf("OK cross-pinned + exported certs to %s\n", *outDir)
}

func loadAndExport(stateDir, orgID, outDir, label string) (certPEM, keyPEM []byte) {
	ctx := context.Background()
	store, err := sqlite.NewStore(ctx, filepath.Join(stateDir, "chepherd.db"))
	if err != nil {
		log.Fatalf("open %s store: %v", label, err)
	}
	defer store.Close()

	mtls, err := federation.LoadOrCreateMTLS(ctx, store.AuthSecrets(), orgID)
	if err != nil {
		log.Fatalf("LoadOrCreateMTLS %s: %v", label, err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: mtls.Certificate.Certificate[0],
	})
	priv, ok := mtls.Certificate.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		log.Fatalf("%s key is not *ecdsa.PrivateKey", label)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		log.Fatalf("marshal %s key: %v", label, err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.WriteFile(filepath.Join(outDir, label+".cert.pem"), certPEM, 0o644); err != nil {
		log.Fatalf("write %s cert: %v", label, err)
	}
	if err := os.WriteFile(filepath.Join(outDir, label+".key.pem"), keyPEM, 0o600); err != nil {
		log.Fatalf("write %s key: %v", label, err)
	}
	return certPEM, keyPEM
}

func pinInto(stateDir string, caPEM []byte, label string) {
	ctx := context.Background()
	store, err := sqlite.NewStore(ctx, filepath.Join(stateDir, "chepherd.db"))
	if err != nil {
		log.Fatalf("open %s store: %v", label, err)
	}
	defer store.Close()
	if err := federation.AddPinnedCA(ctx, store.AuthSecrets(), caPEM); err != nil {
		log.Fatalf("AddPinnedCA %s: %v", label, err)
	}
}

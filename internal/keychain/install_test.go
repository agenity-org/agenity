// internal/keychain/install_test.go — pins #322 H6.1.
package keychain

import (
	"testing"
)

func TestInstall_OverridesActiveChain(t *testing.T) {
	t.Parallel()
	bao := &OpenBaoBackend{Addr: "http://x", Token: "tok"}
	Install(bao)
	defer Install(nil)
	got := Active()
	if got == nil {
		t.Fatal("Active() = nil after Install")
	}
	if got.Name() != "openbao" {
		t.Errorf("Active().Name = %q, want openbao", got.Name())
	}
}

func TestInstall_NilRestoresAuto(t *testing.T) {
	t.Parallel()
	Install(&OpenBaoBackend{Addr: "http://x", Token: "tok"})
	Install(nil)
	// Active should fall through to platform chain — name will be
	// macos / secret-tool / file depending on host; just assert it's
	// not 'openbao'.
	got := Active()
	if got == nil {
		t.Fatal("Active() = nil after Install(nil)")
	}
	if got.Name() == "openbao" {
		t.Error("Active().Name = openbao after Install(nil); expected platform fallback")
	}
}

func TestNewOpenBaoBackendFromFlags_RequiresAddrAndTokenFile(t *testing.T) {
	t.Parallel()
	if _, err := NewOpenBaoBackendFromFlags("", "/tmp/tok", "secret"); err != ErrConfigIncomplete {
		t.Errorf("empty addr: err = %v, want ErrConfigIncomplete", err)
	}
	if _, err := NewOpenBaoBackendFromFlags("http://x", "", "secret"); err != ErrConfigIncomplete {
		t.Errorf("empty token file: err = %v, want ErrConfigIncomplete", err)
	}
}

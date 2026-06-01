// internal/runtimehttp/p2_639_cross_org_fail_closed_test.go — pins
// #639: crossOrgGrantAdapter.Check must return a non-nil error (deny)
// when store == nil + check == nil, not nil (allow-all).
//
// Regression: before the fix, store=nil returned nil (allow-all),
// meaning a misconfigured production deploy without a wired grant
// store would pass every cross-org mTLS caller.
package runtimehttp

import (
	"context"
	"testing"
)

func TestP2_639_CrossOrgGrantAdapter_FailClosedWhenStoreNil(t *testing.T) {
	t.Parallel()
	adapter := &crossOrgGrantAdapter{store: nil, check: nil}
	err := adapter.Check(context.Background(), "alice.example", "message/send")
	if err == nil {
		t.Error("Check returned nil (allow) with store=nil — want non-nil error (fail-closed)")
	}
}

func TestP2_639_CrossOrgGrantAdapter_ExplicitCheckOverridesStoreNil(t *testing.T) {
	t.Parallel()
	called := false
	adapter := &crossOrgGrantAdapter{
		store: nil,
		check: func(callerOrg, scope string) error {
			called = true
			return nil // explicit check allows
		},
	}
	err := adapter.Check(context.Background(), "alice.example", "message/send")
	if err != nil {
		t.Errorf("explicit check set → Check returned %v, want nil", err)
	}
	if !called {
		t.Error("explicit check function was not called")
	}
}

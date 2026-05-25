package catalog

import (
	"testing"
)

func TestLoadAllTemplates(t *testing.T) {
	templates := []string{"solo", "solo-supervised", "pair", "council", "multi-team"}
	for _, n := range templates {
		p, err := Load("/home/openova/repos/chepherd/catalog/" + n + ".yaml")
		if err != nil {
			t.Errorf("%s: %v", n, err)
			continue
		}
		t.Logf("%s: topology=%s members=%d", p.Name, p.Topology, len(p.Members))
		for _, m := range p.Members {
			t.Logf("  - %s (%s, %s, model=%s)", m.Name, m.Agent, m.Role, m.StatSheet.ModelTier)
		}
	}
}

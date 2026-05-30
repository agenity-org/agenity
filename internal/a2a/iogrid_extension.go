package a2a

// IOgridExtension is the chepherd-defined Agent Card extension
// advertising the iogrid integration surface — the URL where this
// chepherd accepts iogrid recipes via the A2A wire, and the recipe
// schema versions it understands.
//
// Vanilla A2A clients ignore unknown extensions (per A2A spec); a
// chepherd-aware iogrid catalogue can discover a peer that accepts
// recipes by reading this block from /.well-known/agent-card.json.
//
// Refs #318 (#225 row E1) + #208.
type IOgridExtension struct {
	// Version of the chepherd-iogrid extension schema (semver).
	Version string `json:"version"`

	// Endpoint is the URL where this chepherd accepts iogrid recipe
	// dispatches. Empty when --iogrid-endpoint is unset → vanilla
	// A2A surface only.
	Endpoint string `json:"endpoint,omitempty"`

	// SupportedRecipeVersions lists the semver versions of the
	// iogrid recipe schema this chepherd accepts. v0.9.3 ships with
	// "1" only (matching the iogrid_v claim emitted by E2's SignRecipe).
	SupportedRecipeVersions []string `json:"supportedRecipeVersions,omitempty"`

	// PublisherTrustList lists publisher identities whose recipes
	// this chepherd accepts. Empty means "any verified signature is
	// accepted" — operator policy decision.
	PublisherTrustList []string `json:"publisherTrustList,omitempty"`
}

// DefaultIOgridExtension returns the v0.9.3-scaffold iogrid extension
// every chepherd advertises by default when --iogrid-endpoint is set.
// Endpoint is populated by the caller; everything else takes defaults.
func DefaultIOgridExtension() *IOgridExtension {
	return &IOgridExtension{
		Version:                 "0.9.3",
		SupportedRecipeVersions: []string{"1"},
	}
}

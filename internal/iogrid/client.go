package iogrid

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is a minimal HTTP client for fetching + verifying iogrid
// recipes from a publisher's catalogue. Usage:
//
//	c := iogrid.NewClient("https://iogrid.example.com")
//	recipe, err := c.FetchRecipe(ctx, "alibaba.qwen-code/v1.2.3", publisherPubKey)
//
// The publisher's public key is supplied by the caller — chepherd's
// operator policy decides which publishers it trusts (via the
// AgentCard XIOgrid.PublisherTrustList field). Future hardening:
// fetch the publisher's key from a well-known endpoint on the
// catalogue server itself (chain-of-trust delegation).
//
// Refs #318 (#225 row E1) + #304 (E2 substrate).
type Client struct {
	// BaseURL is the iogrid catalogue root. Recipes are fetched from
	// BaseURL + "/recipes/" + recipeID.
	BaseURL string

	// HTTPClient is the underlying http.Client. Defaults to a fresh
	// client with a 10s timeout if nil.
	HTTPClient *http.Client
}

// NewClient constructs a Client with sensible defaults.
func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL:    baseURL,
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// FetchRecipe GETs the recipe by ID from the catalogue, decodes the
// YAML+JWS document, verifies the signature against the supplied
// public key, and returns the verified Recipe. Returns an error if
// the HTTP call fails, the body isn't valid YAML, the signature
// doesn't verify, or the recipe body was tampered post-signing.
func (c *Client) FetchRecipe(ctx context.Context, recipeID string, publisherPub *ecdsa.PublicKey) (*Recipe, error) {
	if c.BaseURL == "" {
		return nil, errors.New("iogrid Client: empty BaseURL")
	}
	if recipeID == "" {
		return nil, errors.New("iogrid Client: empty recipeID")
	}
	if publisherPub == nil {
		return nil, errors.New("iogrid Client: nil publisher public key")
	}
	url := c.BaseURL + "/recipes/" + recipeID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("iogrid Client: build request: %w", err)
	}
	req.Header.Set("Accept", "application/yaml, application/x-yaml")
	hc := c.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("iogrid Client: GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("iogrid Client: GET %s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("iogrid Client: read body: %w", err)
	}
	return VerifyRecipe(body, publisherPub)
}

// FetchCatalogueIndex GETs the catalogue index from BaseURL + "/index"
// and returns the list of recipe IDs the catalogue advertises. Returns
// the IDs in the order the catalogue published them (typically newest
// first). Useful for catalogue browsing in the chepherd dashboard.
func (c *Client) FetchCatalogueIndex(ctx context.Context) ([]string, error) {
	if c.BaseURL == "" {
		return nil, errors.New("iogrid Client: empty BaseURL")
	}
	url := c.BaseURL + "/index"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("iogrid Client: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	hc := c.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("iogrid Client: GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("iogrid Client: GET %s: HTTP %d", url, resp.StatusCode)
	}
	var body struct {
		Recipes []string `json:"recipes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("iogrid Client: decode index: %w", err)
	}
	return body.Recipes, nil
}

package verifier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

var ErrInvalidToken = errors.New("invalid token")

type Claims struct {
	TenantID string
	BridgeID string
}

type TokenVerifier interface {
	Verify(ctx context.Context, token string) (Claims, error)
}

type Config struct {
	URL      string
	Client   *http.Client
	CacheTTL time.Duration
	Now      func() time.Time
}

type Client struct {
	url      string
	client   *http.Client
	cacheTTL time.Duration
	now      func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	claims    Claims
	expiresAt time.Time
}

type verifyResponse struct {
	TenantID string `json:"tenantId"`
	BridgeID string `json:"bridgeId"`
}

func NewClient(cfg Config) (*Client, error) {
	if cfg.URL == "" {
		return nil, errors.New("missing verify token url")
	}

	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}

	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	return &Client{
		url:      strings.TrimRight(cfg.URL, "/"),
		client:   client,
		cacheTTL: cfg.CacheTTL,
		now:      now,
		cache:    map[string]cacheEntry{},
	}, nil
}

func (client *Client) Verify(ctx context.Context, token string) (Claims, error) {
	if token == "" {
		return Claims{}, ErrInvalidToken
	}

	if claims, ok := client.loadCached(token); ok {
		return claims, nil
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.url, nil)
	if err != nil {
		return Claims{}, fmt.Errorf("create verifier request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)

	response, err := client.client.Do(request)
	if err != nil {
		return Claims{}, fmt.Errorf("verify token request failed: %w", err)
	}
	defer response.Body.Close()

	switch response.StatusCode {
	case http.StatusOK:
		var payload verifyResponse
		if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
			return Claims{}, fmt.Errorf("decode verifier response: %w", err)
		}
		if payload.TenantID == "" || payload.BridgeID == "" {
			return Claims{}, errors.New("verifier response missing tenantId or bridgeId")
		}

		claims := Claims{
			TenantID: payload.TenantID,
			BridgeID: payload.BridgeID,
		}
		client.storeCached(token, claims)
		return claims, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return Claims{}, ErrInvalidToken
	default:
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4*1024))
		return Claims{}, fmt.Errorf("verify token unexpected status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
}

func (client *Client) loadCached(token string) (Claims, bool) {
	if client.cacheTTL <= 0 {
		return Claims{}, false
	}

	client.mu.Lock()
	defer client.mu.Unlock()

	entry, ok := client.cache[token]
	if !ok {
		return Claims{}, false
	}
	if client.now().After(entry.expiresAt) {
		delete(client.cache, token)
		return Claims{}, false
	}

	return entry.claims, true
}

func (client *Client) storeCached(token string, claims Claims) {
	if client.cacheTTL <= 0 {
		return
	}

	client.mu.Lock()
	defer client.mu.Unlock()

	client.cache[token] = cacheEntry{
		claims:    claims,
		expiresAt: client.now().Add(client.cacheTTL),
	}
}

package verifier

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientVerifySuccessAndCache(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	now := time.Unix(1_700_000_000, 0).UTC()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits.Add(1)
		if request.Header.Get("Authorization") != "Bearer token-123" {
			t.Fatalf("unexpected authorization header: %s", request.Header.Get("Authorization"))
		}
		if request.Header.Get(secretHeader) != "verifier-secret" {
			t.Fatalf("unexpected secret header: %s", request.Header.Get(secretHeader))
		}

		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"tenantId":"tenant_123","bridgeId":"bridge_123"}`))
	}))
	defer func() { server.Close() }()

	client, err := NewClient(Config{
		URL:      server.URL,
		Secret:   "verifier-secret",
		CacheTTL: time.Minute,
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new verifier client: %v", err)
	}

	claims, err := client.Verify(context.Background(), "token-123")
	if err != nil {
		t.Fatalf("verify token: %v", err)
	}
	if claims.BridgeID != "bridge_123" {
		t.Fatalf("unexpected bridge id: %s", claims.BridgeID)
	}

	claims, err = client.Verify(context.Background(), "token-123")
	if err != nil {
		t.Fatalf("verify token from cache: %v", err)
	}
	if claims.TenantID != "tenant_123" {
		t.Fatalf("unexpected tenant id: %s", claims.TenantID)
	}
	if hits.Load() != 1 {
		t.Fatalf("unexpected verifier hits: %d", hits.Load())
	}
}

func TestClientVerifyRejectsInvalidToken(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "invalid token", http.StatusUnauthorized)
	}))
	defer func() { server.Close() }()

	client, err := NewClient(Config{URL: server.URL, Secret: "verifier-secret"})
	if err != nil {
		t.Fatalf("new verifier client: %v", err)
	}

	_, err = client.Verify(context.Background(), "bad-token")
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected invalid token error, got %v", err)
	}
}

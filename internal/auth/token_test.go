package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestSignAndParseBridgeToken(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	token, err := SignBridgeToken([]byte("secret"), BridgeClaims{
		TenantID:         "tenant_123",
		BridgeID:         "bridge_123",
		RegisteredClaims: RegisteredClaims(now),
	})
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	claims, err := ParseBridgeToken([]byte("secret"), token, now)
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}

	if claims.TenantID != "tenant_123" {
		t.Fatalf("unexpected tenant id: %s", claims.TenantID)
	}
	if claims.BridgeID != "bridge_123" {
		t.Fatalf("unexpected bridge id: %s", claims.BridgeID)
	}
}

func TestParseBridgeTokenRejectsWrongSecret(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	token, err := SignBridgeToken([]byte("secret"), BridgeClaims{
		TenantID:         "tenant_123",
		BridgeID:         "bridge_123",
		RegisteredClaims: RegisteredClaims(now),
	})
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	if _, err := ParseBridgeToken([]byte("different"), token, now); err == nil {
		t.Fatal("expected parse to fail")
	}
}

func RegisteredClaims(now time.Time) jwt.RegisteredClaims {
	return jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
	}
}

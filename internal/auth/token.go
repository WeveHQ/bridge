package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type BridgeClaims struct {
	TenantID string `json:"tenantId"`
	BridgeID string `json:"bridgeId"`
	jwt.RegisteredClaims
}

func SignBridgeToken(secret []byte, claims BridgeClaims) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("missing token secret")
	}
	if claims.BridgeID == "" {
		return "", errors.New("missing bridge id")
	}
	if claims.TenantID == "" {
		return "", errors.New("missing tenant id")
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}

func ParseBridgeToken(secret []byte, token string, now time.Time) (BridgeClaims, error) {
	if len(secret) == 0 {
		return BridgeClaims{}, errors.New("missing token secret")
	}
	if token == "" {
		return BridgeClaims{}, errors.New("missing token")
	}

	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Name}),
		jwt.WithTimeFunc(func() time.Time { return now }),
	)

	parsed, err := parser.ParseWithClaims(token, &BridgeClaims{}, func(incoming *jwt.Token) (any, error) {
		return secret, nil
	})
	if err != nil {
		return BridgeClaims{}, fmt.Errorf("parse bridge token: %w", err)
	}

	claims, ok := parsed.Claims.(*BridgeClaims)
	if !ok {
		return BridgeClaims{}, errors.New("unexpected bridge claims type")
	}
	if claims.BridgeID == "" {
		return BridgeClaims{}, errors.New("missing bridgeId claim")
	}
	if claims.TenantID == "" {
		return BridgeClaims{}, errors.New("missing tenantId claim")
	}

	return *claims, nil
}

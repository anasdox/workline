package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/golang-jwt/jwt/v5"

	"proofline/internal/repo"
)

type AuthConfig struct {
	JWTSecret string
	Logger    *log.Logger
}

type Principal struct {
	ActorID     string
	OrgID       string
	Roles       []string
	Permissions []string
	Source      string
}

type principalKey struct{}

func (c AuthConfig) logger() *log.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return log.Default()
}

func withPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func principalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

func actorIDFromContext(ctx context.Context) (string, huma.StatusError) {
	p, err := principalFromRequest(ctx)
	if err != nil {
		return "", err
	}
	return p.ActorID, nil
}

func principalFromRequest(ctx context.Context) (Principal, huma.StatusError) {
	if p, ok := principalFromContext(ctx); ok && p.ActorID != "" && p.OrgID != "" {
		return p, nil
	}
	return Principal{}, newAPIError(http.StatusUnauthorized, "unauthorized", "authentication required", nil)
}

type jwtClaims struct {
	jwt.RegisteredClaims
	Org         string   `json:"org,omitempty"`
	Roles       []string `json:"roles,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
}

func authenticateJWT(token string, secret string) (Principal, error) {
	if strings.TrimSpace(secret) == "" {
		return Principal{}, errors.New("jwt secret not configured")
	}
	parser := jwt.NewParser(jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	claims := &jwtClaims{}
	parsed, err := parser.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		return []byte(secret), nil
	})
	if err != nil {
		return Principal{}, err
	}
	if !parsed.Valid {
		return Principal{}, errors.New("invalid token")
	}
	if claims.Subject == "" {
		return Principal{}, errors.New("subject claim required")
	}
	if claims.Org == "" {
		return Principal{}, errors.New("org claim required")
	}
	return Principal{
		ActorID:     claims.Subject,
		OrgID:       claims.Org,
		Roles:       claims.Roles,
		Permissions: claims.Permissions,
		Source:      "jwt",
	}, nil
}

func authenticateAPIKey(ctx context.Context, r repo.Repo, key string) (Principal, error) {
	if strings.TrimSpace(key) == "" {
		return Principal{}, errors.New("api key required")
	}
	hash := repo.HashAPIKey(key)
	apiKey, err := r.GetAPIKeyByHash(ctx, hash)
	if err != nil {
		return Principal{}, err
	}
	if apiKey.ActorID == "" {
		return Principal{}, errors.New("api key missing actor")
	}
	return Principal{
		ActorID: apiKey.ActorID,
		OrgID:   apiKey.OrgID,
		Source:  "api_key",
	}, nil
}

func bearerToken(authz string) (string, bool) {
	parts := strings.Fields(authz)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", false
	}
	return parts[1], true
}

func newAuthMiddleware(basePath string, cfg AuthConfig, r repo.Repo) func(http.Handler) http.Handler {
	healthPath := path.Join(basePath, "health")
	openapiPath := path.Join(basePath, "openapi.json")
	devLoginPath := path.Join(basePath, "auth/dev/login")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			// Only enforce for API base path.
			if basePath != "" && !strings.HasPrefix(req.URL.Path, basePath) {
				next.ServeHTTP(w, req)
				return
			}
			if req.URL.Path == healthPath {
				next.ServeHTTP(w, req)
				return
			}
			if req.URL.Path == openapiPath {
				next.ServeHTTP(w, req)
				return
			}
			if req.URL.Path == devLoginPath {
				next.ServeHTTP(w, req)
				return
			}

			authz := strings.TrimSpace(req.Header.Get("Authorization"))
			apiKeyHeader := strings.TrimSpace(req.Header.Get("X-Api-Key"))

			if authz != "" {
				token, ok := bearerToken(authz)
				if !ok {
					respondStatusError(w, newAPIError(http.StatusUnauthorized, "invalid_credentials", "invalid credentials", nil))
					return
				}
				principal, err := authenticateJWT(token, cfg.JWTSecret)
				if err != nil {
					respondStatusError(w, newAPIError(http.StatusUnauthorized, "invalid_credentials", "invalid credentials", nil))
					return
				}
				ctx := withPrincipal(req.Context(), principal)
				next.ServeHTTP(w, req.WithContext(ctx))
				return
			}

			if apiKeyHeader != "" {
				principal, err := authenticateAPIKey(req.Context(), r, apiKeyHeader)
				if err != nil {
					respondStatusError(w, newAPIError(http.StatusUnauthorized, "invalid_credentials", "invalid credentials", nil))
					return
				}
				ctx := withPrincipal(req.Context(), principal)
				next.ServeHTTP(w, req.WithContext(ctx))
				return
			}

			respondStatusError(w, newAPIError(http.StatusUnauthorized, "unauthorized", "authentication required", nil))
		})
	}
}

func respondStatusError(w http.ResponseWriter, err huma.StatusError) {
	status := http.StatusInternalServerError
	if e, ok := err.(interface{ GetStatus() int }); ok {
		status = e.GetStatus()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(err)
}

func signDevToken(secret, actorID, orgID string, roles, scopes []string) (string, error) {
	if secret == "" {
		return "", errors.New("jwt secret not configured")
	}
	now := jwt.NewNumericDate(time.Now())
	claims := jwtClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   actorID,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  now,
			NotBefore: now,
		},
		Org:         orgID,
		Roles:       roles,
		Permissions: scopes,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

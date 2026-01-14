package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"workline/internal/config"
	"workline/internal/db"
	"workline/internal/engine"
	"workline/internal/migrate"
	"workline/internal/server"
)

func main() {
	workspace := "/tmp/workline-check5"
	if _, err := db.EnsureWorkspace(workspace); err != nil {
		panic(err)
	}
	conn, err := db.Open(db.Config{Workspace: workspace})
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	if err := migrate.Migrate(conn); err != nil {
		panic(err)
	}
	cfg := config.Default("workline")
	e := engine.New(conn, cfg)
	if _, err := e.InitProject(context.Background(), cfg.Project.ID, "default-org", "", "tester"); err != nil {
		panic(err)
	}
	if err := e.Repo.UpsertProjectConfig(context.Background(), cfg.Project.ID, cfg); err != nil {
		panic(err)
	}
	jwtSecret := "test-secret"
	h, err := server.New(server.Config{Engine: e, BasePath: "/v0", Auth: server.AuthConfig{JWTSecret: jwtSecret}})
	if err != nil {
		panic(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()
	token := signToken(jwtSecret, "tester", "default-org", time.Now().Add(time.Hour))

	body := map[string]any{
		"title": "Needs validation",
		"type":  "feature",
		"policy": map[string]any{"preset": "done.standard"},
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v0/projects/workline/tasks", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer res.Body.Close()
	var resp any
	_ = json.NewDecoder(res.Body).Decode(&resp)
	fmt.Printf("status=%d resp=%v\n", res.StatusCode, resp)
}

func signToken(secret, actorID, orgID string, expiresAt time.Time) string {
	// minimal copy of server_test helper
	claims := map[string]any{
		"sub": actorID,
		"org": orgID,
		"exp": expiresAt.Unix(),
		"nbf": time.Now().Unix(),
		"iat": time.Now().Unix(),
	}
	header := map[string]any{"alg": "HS256", "typ": "JWT"}
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64RawURLEncode(b)
	}
	sig := hmacSHA256(enc(header) + "." + enc(claims), secret)
	return enc(header) + "." + enc(claims) + "." + sig
}

func base64RawURLEncode(b []byte) string {
	const enc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	out := make([]byte, 0, (len(b)*4+2)/3)
	var val uint
	var valb int
	for _, c := range b {
		val = (val << 8) | uint(c)
		valb += 8
		for valb >= 6 {
			out = append(out, enc[(val>>(valb-6))&0x3f])
			valb -= 6
		}
	}
	if valb > -6 {
		out = append(out, enc[((val<<8)>>(valb+8))&0x3f])
	}
	return string(out)
}

func hmacSHA256(data, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	_, _ = h.Write([]byte(data))
	return base64RawURLEncode(h.Sum(nil))
}

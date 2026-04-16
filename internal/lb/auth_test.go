package lb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoadAuthExtractsClaims(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	token := testJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-123",
		},
		"https://api.openai.com/profile": map[string]any{
			"email": "alice@example.com",
		},
	})

	auth := map[string]any{
		"tokens": map[string]any{
			"access_token": token,
		},
	}
	b, _ := json.Marshal(auth)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), b, 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}

	info, err := LoadAuth(home)
	if err != nil {
		t.Fatalf("LoadAuth: %v", err)
	}
	if info.ChatGPTAccountID != "acct-123" {
		t.Fatalf("expected account id acct-123, got %q", info.ChatGPTAccountID)
	}
	if info.UserEmail != "alice@example.com" {
		t.Fatalf("expected email alice@example.com, got %q", info.UserEmail)
	}
}

func TestLoadAuthPrefersExplicitAccountID(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	token := testJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-from-claim",
		},
	})

	auth := map[string]any{
		"tokens": map[string]any{
			"access_token": token,
			"account_id":   "acct-explicit",
		},
	}
	b, _ := json.Marshal(auth)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), b, 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}

	info, err := LoadAuth(home)
	if err != nil {
		t.Fatalf("LoadAuth: %v", err)
	}
	if info.ChatGPTAccountID != "acct-explicit" {
		t.Fatalf("expected explicit account id, got %q", info.ChatGPTAccountID)
	}
}

func TestRefreshAuthPersistsRotatedTokens(t *testing.T) {
	home := t.TempDir()
	oldAccess := testJWT(map[string]any{
		"https://api.openai.com/auth":    map[string]any{"chatgpt_account_id": "acct-old"},
		"https://api.openai.com/profile": map[string]any{"email": "old@example.com"},
	})
	newAccess := testJWT(map[string]any{
		"https://api.openai.com/auth":    map[string]any{"chatgpt_account_id": "acct-new"},
		"https://api.openai.com/profile": map[string]any{"email": "new@example.com"},
	})
	newID := testJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-new"},
	})

	auth := map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"access_token":  oldAccess,
			"refresh_token": "refresh-old",
			"id_token":      "id-old",
			"account_id":    "acct-old",
		},
	}
	b, _ := json.Marshal(auth)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), b, 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if got := r.Header.Get("Content-Type"); !strings.Contains(got, "application/x-www-form-urlencoded") {
			t.Fatalf("unexpected content type: %q", got)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.Form.Get("grant_type"); got != "refresh_token" {
			t.Fatalf("grant_type = %q", got)
		}
		if got := r.Form.Get("client_id"); got != "client-123" {
			t.Fatalf("client_id = %q", got)
		}
		if got := r.Form.Get("refresh_token"); got != "refresh-old" {
			t.Fatalf("refresh_token = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  newAccess,
			"refresh_token": "refresh-new",
			"id_token":      newID,
		})
	}))
	defer tokenSrv.Close()

	info, err := RefreshAuth(context.Background(), tokenSrv.Client(), home, tokenSrv.URL, "client-123", oldAccess)
	if err != nil {
		t.Fatalf("RefreshAuth: %v", err)
	}
	if info.AccessToken != newAccess {
		t.Fatalf("unexpected access token after refresh")
	}
	if info.RefreshToken != "refresh-new" {
		t.Fatalf("unexpected refresh token after refresh: %q", info.RefreshToken)
	}
	if info.ChatGPTAccountID != "acct-new" {
		t.Fatalf("unexpected account id after refresh: %q", info.ChatGPTAccountID)
	}
	if info.UserEmail != "new@example.com" {
		t.Fatalf("unexpected email after refresh: %q", info.UserEmail)
	}

	raw, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		t.Fatalf("read persisted auth.json: %v", err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("parse persisted auth.json: %v", err)
	}
	if got := stringField(persisted["last_refresh"]); got == "" {
		t.Fatalf("expected last_refresh to be persisted")
	}
	tokens, _ := persisted["tokens"].(map[string]any)
	if got := stringField(tokens["refresh_token"]); got != "refresh-new" {
		t.Fatalf("persisted refresh token = %q", got)
	}
}

func TestRefreshAuthSerializesConcurrentRefreshesPerAccount(t *testing.T) {
	home := t.TempDir()
	oldAccess := testJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-old"},
	})
	newAccess := testJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-new"},
	})

	auth := map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"access_token":  oldAccess,
			"refresh_token": "refresh-old",
			"id_token":      "id-old",
			"account_id":    "acct-old",
		},
	}
	b, _ := json.Marshal(auth)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), b, 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}

	var calls atomic.Int32
	var seenRefresh sync.Map
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		refreshToken := r.Form.Get("refresh_token")
		if refreshToken != "refresh-old" {
			t.Fatalf("unexpected refresh token: %q", refreshToken)
		}
		if _, loaded := seenRefresh.LoadOrStore(refreshToken, true); loaded {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"refresh token already used","code":"refresh_token_reused"}}`)
			return
		}
		if call == 1 {
			time.Sleep(150 * time.Millisecond)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  newAccess,
			"refresh_token": "refresh-new",
			"id_token":      "id-new",
		})
	}))
	defer tokenSrv.Close()

	start := make(chan struct{})
	errs := make(chan error, 2)
	results := make(chan AuthInfo, 2)
	for range 2 {
		go func() {
			<-start
			info, err := RefreshAuth(context.Background(), tokenSrv.Client(), home, tokenSrv.URL, "client-123", oldAccess)
			if err != nil {
				errs <- err
				return
			}
			results <- info
		}()
	}
	close(start)

	for range 2 {
		select {
		case err := <-errs:
			t.Fatalf("RefreshAuth: %v", err)
		case info := <-results:
			if info.AccessToken != newAccess {
				t.Fatalf("unexpected access token after concurrent refresh: %q", info.AccessToken)
			}
			if info.RefreshToken != "refresh-new" {
				t.Fatalf("unexpected refresh token after concurrent refresh: %q", info.RefreshToken)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for concurrent refresh")
		}
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("expected one token refresh request, got %d", got)
	}
}

func testJWT(payload map[string]any) string {
	head := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	b, _ := json.Marshal(payload)
	body := base64.RawURLEncoding.EncodeToString(b)
	return head + "." + body + ".signature"
}

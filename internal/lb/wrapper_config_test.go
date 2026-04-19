package lb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveCodexInvocationUsesRunProxyURLFromConfig(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	custom := `
[proxy]
listen = "127.0.0.1:8765"

proxy_url = "http://127.0.0.1:19000"

[run]
inherit_shell = false
`
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte(custom), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}

	store, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	_, _, _, env, inheritShell := resolveCodexInvocation(store, "", "", "", nil)
	if env["OPENAI_BASE_URL"] != "http://127.0.0.1:19000" {
		t.Fatalf("OPENAI_BASE_URL = %q, want %q", env["OPENAI_BASE_URL"], "http://127.0.0.1:19000")
	}
	if inheritShell {
		t.Fatalf("inheritShell = true, want false")
	}
}

func TestSeedRuntimeAuthIfMissingCreatesProxyOnlyAuthWithoutAccounts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	runtimeHome := filepath.Join(root, "runtime-proxy-only")
	if err := os.MkdirAll(runtimeHome, 0o700); err != nil {
		t.Fatalf("mkdir runtime home: %v", err)
	}
	if err := seedRuntimeAuthIfMissing(store, runtimeHome, ""); err != nil {
		t.Fatalf("seedRuntimeAuthIfMissing: %v", err)
	}

	if _, err := os.Stat(filepath.Join(runtimeHome, "auth.json")); err != nil {
		t.Fatalf("expected runtime auth.json: %v", err)
	}
	auth, err := LoadAuth(runtimeHome)
	if err != nil {
		t.Fatalf("LoadAuth(runtime): %v", err)
	}
	if auth.AccessToken == "" {
		t.Fatalf("expected runtime access token")
	}
	if auth.ChatGPTAccountID != "proxy-only" {
		t.Fatalf("expected proxy-only account id, got %q", auth.ChatGPTAccountID)
	}
	raw, err := os.ReadFile(filepath.Join(runtimeHome, "auth.json"))
	if err != nil {
		t.Fatalf("read runtime auth.json: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal runtime auth: %v", err)
	}
	tokens, _ := parsed["tokens"].(map[string]any)
	idToken, _ := tokens["id_token"].(string)
	if idToken == "" {
		t.Fatalf("expected proxy-only id_token in runtime auth")
	}
	refreshToken, _ := tokens["refresh_token"].(string)
	if refreshToken == "" {
		t.Fatalf("expected proxy-only refresh_token in runtime auth")
	}
}

func TestSeedRuntimeAuthIfMissingRepairsInvalidRuntimeAuth(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	runtimeHome := filepath.Join(root, "runtime-repair")
	if err := os.MkdirAll(runtimeHome, 0o700); err != nil {
		t.Fatalf("mkdir runtime home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runtimeHome, "auth.json"), []byte(`{"tokens":{"access_token":""}}`), 0o600); err != nil {
		t.Fatalf("write invalid runtime auth: %v", err)
	}

	if err := seedRuntimeAuthIfMissing(store, runtimeHome, ""); err != nil {
		t.Fatalf("seedRuntimeAuthIfMissing: %v", err)
	}
	auth, err := LoadAuth(runtimeHome)
	if err != nil {
		t.Fatalf("LoadAuth(runtime): %v", err)
	}
	if auth.AccessToken == "" {
		t.Fatalf("expected repaired runtime access token")
	}
}

func TestSeedRuntimeAuthIfMissingRefreshesExistingRuntimeAuthFromSelectedAccount(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	homeA := filepath.Join(root, "acc-a")
	homeB := filepath.Join(root, "acc-b")
	if err := os.MkdirAll(homeA, 0o700); err != nil {
		t.Fatalf("mkdir homeA: %v", err)
	}
	if err := os.MkdirAll(homeB, 0o700); err != nil {
		t.Fatalf("mkdir homeB: %v", err)
	}
	writeAuthForTest(t, homeA, "acct-a", "a@example.com")
	writeAuthForTest(t, homeB, "acct-b", "b@example.com")

	if err := store.Update(func(sf *StoreFile) error {
		sf.Accounts = []Account{
			{Alias: "a", ID: "openai:a", HomeDir: homeA, Enabled: true},
			{Alias: "b", ID: "openai:b", HomeDir: homeB, Enabled: true},
		}
		sf.State.ActiveIndex = 0
		sf.State.PinnedAccountID = "openai:b"
		return nil
	}); err != nil {
		t.Fatalf("store update: %v", err)
	}

	runtimeHome := filepath.Join(root, "runtime-refresh")
	if err := os.MkdirAll(runtimeHome, 0o700); err != nil {
		t.Fatalf("mkdir runtime home: %v", err)
	}
	writeAuthForTest(t, runtimeHome, "acct-a", "a@example.com")

	if err := seedRuntimeAuthIfMissing(store, runtimeHome, ""); err != nil {
		t.Fatalf("seedRuntimeAuthIfMissing: %v", err)
	}
	auth, err := LoadAuth(runtimeHome)
	if err != nil {
		t.Fatalf("LoadAuth(runtime): %v", err)
	}
	if auth.ChatGPTAccountID != "proxy-only" {
		t.Fatalf("expected refreshed runtime account proxy-only, got %q", auth.ChatGPTAccountID)
	}
}

func TestSeedRuntimeAuthIfMissingFetchesFromRemoteProxy(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/admin/runtime-auth" {
			http.NotFound(w, r)
			return
		}
		auth, err := proxyOnlyRuntimeAuthPayload(proxyOnlyRuntimeProfile{})
		if err != nil {
			t.Fatalf("proxyOnlyRuntimeAuthPayload: %v", err)
		}
		_ = json.NewEncoder(w).Encode(AdminRuntimeAuthResponse{
			Auth: json.RawMessage(auth),
		})
	}))
	defer server.Close()

	runtimeHome := filepath.Join(root, "runtime-remote")
	if err := os.MkdirAll(runtimeHome, 0o700); err != nil {
		t.Fatalf("mkdir runtime home: %v", err)
	}
	if err := seedRuntimeAuthIfMissing(store, runtimeHome, server.URL); err != nil {
		t.Fatalf("seedRuntimeAuthIfMissing: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(runtimeHome, "auth.json"))
	if err != nil {
		t.Fatalf("read runtime auth.json: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal runtime auth payload: %v", err)
	}
	tokens, _ := payload["tokens"].(map[string]any)
	accessToken, _ := tokens["access_token"].(string)
	if strings.TrimSpace(accessToken) == "" {
		t.Fatalf("expected proxy-only access_token")
	}
	if got, _ := tokens["id_token"].(string); got != accessToken {
		t.Fatalf("expected proxy-only id_token to match access_token, got %q want %q", got, accessToken)
	}
	if got, _ := tokens["account_id"].(string); got != "proxy-only" {
		t.Fatalf("unexpected account_id: %q", got)
	}
	if got, _ := tokens["refresh_token"].(string); got != accessToken {
		t.Fatalf("expected refresh_token to match access_token, got %q want %q", got, accessToken)
	}
}

func TestSeedRuntimeAuthIfMissingCopiesRemoteRuntimeConfig(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	wantConfig := "model = \"gpt-5.2-codex\"\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/admin/runtime-auth" {
			http.NotFound(w, r)
			return
		}
		auth, err := proxyOnlyRuntimeAuthPayload(proxyOnlyRuntimeProfile{})
		if err != nil {
			t.Fatalf("proxyOnlyRuntimeAuthPayload: %v", err)
		}
		_ = json.NewEncoder(w).Encode(AdminRuntimeAuthResponse{
			Auth:        json.RawMessage(auth),
			Config:      wantConfig,
			SourceAlias: "remote-a",
		})
	}))
	defer server.Close()

	runtimeHome := filepath.Join(root, "runtime-remote-config")
	if err := os.MkdirAll(runtimeHome, 0o700); err != nil {
		t.Fatalf("mkdir runtime home: %v", err)
	}
	if err := seedRuntimeAuthIfMissing(store, runtimeHome, server.URL); err != nil {
		t.Fatalf("seedRuntimeAuthIfMissing: %v", err)
	}

	gotConfig, err := os.ReadFile(filepath.Join(runtimeHome, "config.toml"))
	if err != nil {
		t.Fatalf("read runtime config.toml: %v", err)
	}
	if string(gotConfig) != wantConfig {
		t.Fatalf("runtime config.toml = %q, want %q", string(gotConfig), wantConfig)
	}
}

func TestSeedRuntimeAuthIfMissingCopiesUserConfigWhenAccountConfigMissing(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("CODEX_HOME", "")

	userCodexHome := filepath.Join(root, ".codex")
	if err := os.MkdirAll(userCodexHome, 0o700); err != nil {
		t.Fatalf("mkdir user codex home: %v", err)
	}
	wantConfig := []byte("model = \"gpt-5.4\"\n")
	if err := os.WriteFile(filepath.Join(userCodexHome, "config.toml"), wantConfig, 0o600); err != nil {
		t.Fatalf("write user config.toml: %v", err)
	}

	store, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	accountHome := filepath.Join(root, "acc-a")
	if err := os.MkdirAll(accountHome, 0o700); err != nil {
		t.Fatalf("mkdir account home: %v", err)
	}
	writeAuthForTest(t, accountHome, "acct-a", "a@example.com")

	if err := store.Update(func(sf *StoreFile) error {
		sf.Accounts = []Account{
			{Alias: "a", ID: "openai:a", HomeDir: accountHome, Enabled: true},
		}
		sf.State.ActiveIndex = 0
		return nil
	}); err != nil {
		t.Fatalf("store update: %v", err)
	}

	runtimeHome := filepath.Join(root, "runtime-user-config")
	if err := os.MkdirAll(runtimeHome, 0o700); err != nil {
		t.Fatalf("mkdir runtime home: %v", err)
	}
	if err := seedRuntimeAuthIfMissing(store, runtimeHome, ""); err != nil {
		t.Fatalf("seedRuntimeAuthIfMissing: %v", err)
	}

	gotConfig, err := os.ReadFile(filepath.Join(runtimeHome, "config.toml"))
	if err != nil {
		t.Fatalf("read runtime config.toml: %v", err)
	}
	if string(gotConfig) != string(wantConfig) {
		t.Fatalf("runtime config.toml = %q, want %q", string(gotConfig), string(wantConfig))
	}
}

func TestSeedRuntimeAuthIfMissingPrefersAccountConfigOverUserConfig(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("CODEX_HOME", "")

	userCodexHome := filepath.Join(root, ".codex")
	if err := os.MkdirAll(userCodexHome, 0o700); err != nil {
		t.Fatalf("mkdir user codex home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(userCodexHome, "config.toml"), []byte("model = \"gpt-5.4\"\n"), 0o600); err != nil {
		t.Fatalf("write user config.toml: %v", err)
	}

	store, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	accountHome := filepath.Join(root, "acc-a")
	if err := os.MkdirAll(accountHome, 0o700); err != nil {
		t.Fatalf("mkdir account home: %v", err)
	}
	writeAuthForTest(t, accountHome, "acct-a", "a@example.com")
	wantConfig := []byte("model = \"gpt-5.2-codex\"\n")
	if err := os.WriteFile(filepath.Join(accountHome, "config.toml"), wantConfig, 0o600); err != nil {
		t.Fatalf("write account config.toml: %v", err)
	}

	if err := store.Update(func(sf *StoreFile) error {
		sf.Accounts = []Account{
			{Alias: "a", ID: "openai:a", HomeDir: accountHome, Enabled: true},
		}
		sf.State.ActiveIndex = 0
		return nil
	}); err != nil {
		t.Fatalf("store update: %v", err)
	}

	runtimeHome := filepath.Join(root, "runtime-account-config")
	if err := os.MkdirAll(runtimeHome, 0o700); err != nil {
		t.Fatalf("mkdir runtime home: %v", err)
	}
	if err := seedRuntimeAuthIfMissing(store, runtimeHome, ""); err != nil {
		t.Fatalf("seedRuntimeAuthIfMissing: %v", err)
	}

	gotConfig, err := os.ReadFile(filepath.Join(runtimeHome, "config.toml"))
	if err != nil {
		t.Fatalf("read runtime config.toml: %v", err)
	}
	if string(gotConfig) != string(wantConfig) {
		t.Fatalf("runtime config.toml = %q, want %q", string(gotConfig), string(wantConfig))
	}
}

func TestSeedRuntimeAuthIfMissingDoesNotBorrowDifferentAccountConfig(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("CODEX_HOME", "")

	userCodexHome := filepath.Join(root, ".codex")
	if err := os.MkdirAll(userCodexHome, 0o700); err != nil {
		t.Fatalf("mkdir user codex home: %v", err)
	}
	fallbackConfig := []byte("model = \"gpt-5.4\"\n")
	if err := os.WriteFile(filepath.Join(userCodexHome, "config.toml"), fallbackConfig, 0o600); err != nil {
		t.Fatalf("write user config.toml: %v", err)
	}

	store, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	homeA := filepath.Join(root, "acc-a")
	homeB := filepath.Join(root, "acc-b")
	if err := os.MkdirAll(homeA, 0o700); err != nil {
		t.Fatalf("mkdir homeA: %v", err)
	}
	if err := os.MkdirAll(homeB, 0o700); err != nil {
		t.Fatalf("mkdir homeB: %v", err)
	}
	writeAuthForTest(t, homeA, "acct-a", "a@example.com")
	writeAuthForTest(t, homeB, "acct-b", "b@example.com")
	if err := os.WriteFile(filepath.Join(homeA, "config.toml"), []byte("model = \"wrong-account\"\n"), 0o600); err != nil {
		t.Fatalf("write account A config.toml: %v", err)
	}

	if err := store.Update(func(sf *StoreFile) error {
		sf.Accounts = []Account{
			{Alias: "a", ID: "openai:a", HomeDir: homeA, Enabled: true},
			{Alias: "b", ID: "openai:b", HomeDir: homeB, Enabled: true},
		}
		sf.State.ActiveIndex = 0
		sf.State.PinnedAccountID = "openai:b"
		return nil
	}); err != nil {
		t.Fatalf("store update: %v", err)
	}

	runtimeHome := filepath.Join(root, "runtime-no-borrow")
	if err := os.MkdirAll(runtimeHome, 0o700); err != nil {
		t.Fatalf("mkdir runtime home: %v", err)
	}
	if err := seedRuntimeAuthIfMissing(store, runtimeHome, ""); err != nil {
		t.Fatalf("seedRuntimeAuthIfMissing: %v", err)
	}

	auth, err := LoadAuth(runtimeHome)
	if err != nil {
		t.Fatalf("LoadAuth(runtime): %v", err)
	}
	if auth.ChatGPTAccountID != "proxy-only" {
		t.Fatalf("expected runtime account proxy-only, got %q", auth.ChatGPTAccountID)
	}

	gotConfig, err := os.ReadFile(filepath.Join(runtimeHome, "config.toml"))
	if err != nil {
		t.Fatalf("read runtime config.toml: %v", err)
	}
	if string(gotConfig) != string(fallbackConfig) {
		t.Fatalf("runtime config.toml = %q, want fallback %q", string(gotConfig), string(fallbackConfig))
	}
}

func writeAuthForTest(t *testing.T, home, accountID, email string) {
	t.Helper()
	token := testJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": accountID,
		},
		"https://api.openai.com/profile": map[string]any{
			"email": email,
		},
	})
	payload := map[string]any{
		"tokens": map[string]any{
			"access_token": token,
			"account_id":   accountID,
		},
	}
	b, _ := json.Marshal(payload)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), b, 0o600); err != nil {
		t.Fatalf("write auth.json for %s: %v", home, err)
	}
}

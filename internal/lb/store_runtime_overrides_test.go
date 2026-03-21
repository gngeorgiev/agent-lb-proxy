package lb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeSettingsOverridesAreEphemeral(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	baseUpstream := store.Snapshot().Settings.Proxy.UpstreamBaseURL
	if err := store.Update(func(sf *StoreFile) error {
		sf.Accounts = []Account{
			{ID: "a", Alias: "a", HomeDir: t.TempDir(), BaseURL: baseUpstream, Enabled: true},
			{ID: "b", Alias: "b", HomeDir: t.TempDir(), BaseURL: "https://custom.example/backend-api", Enabled: true},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed accounts: %v", err)
	}

	listen := "127.0.0.1:9999"
	name := "edge-env"
	upstream := "https://alt.example/backend-api"
	childProxyURLs := []string{"http://child-a.internal/", " http://child-b.internal/base "}
	overrides := RuntimeSettingsOverrides{
		ProxyName:       &name,
		Listen:          &listen,
		UpstreamBaseURL: &upstream,
		ChildProxyURLs:  &childProxyURLs,
	}
	store.SetRuntimeSettingsOverrides(overrides)

	snap := store.Snapshot()
	if snap.Settings.Proxy.Listen != listen {
		t.Fatalf("listen override not applied: got %q want %q", snap.Settings.Proxy.Listen, listen)
	}
	if snap.Settings.Proxy.Name != name {
		t.Fatalf("proxy name override not applied: got %q want %q", snap.Settings.Proxy.Name, name)
	}
	if snap.Settings.Proxy.UpstreamBaseURL != upstream {
		t.Fatalf("upstream override not applied: got %q want %q", snap.Settings.Proxy.UpstreamBaseURL, upstream)
	}
	if snap.Accounts[0].BaseURL != upstream {
		t.Fatalf("expected account base URL to follow upstream override: got %q", snap.Accounts[0].BaseURL)
	}
	if snap.Accounts[1].BaseURL != "https://custom.example/backend-api" {
		t.Fatalf("unexpected override for custom account base URL: got %q", snap.Accounts[1].BaseURL)
	}
	if got := snap.Settings.Proxy.ChildProxyURLs; len(got) != 2 || got[0] != "http://child-a.internal" || got[1] != "http://child-b.internal/base" {
		t.Fatalf("child proxy override not applied: %#v", got)
	}

	reopened, err := OpenStore(root)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	persisted := reopened.Snapshot()
	if persisted.Settings.Proxy.Listen == listen {
		t.Fatalf("listen override persisted unexpectedly: %q", persisted.Settings.Proxy.Listen)
	}
	if persisted.Settings.Proxy.Name == name {
		t.Fatalf("proxy name override persisted unexpectedly: %q", persisted.Settings.Proxy.Name)
	}
	if persisted.Settings.Proxy.UpstreamBaseURL == upstream {
		t.Fatalf("upstream override persisted unexpectedly: %q", persisted.Settings.Proxy.UpstreamBaseURL)
	}
	if len(persisted.Settings.Proxy.ChildProxyURLs) != 0 {
		t.Fatalf("child proxy override persisted unexpectedly: %#v", persisted.Settings.Proxy.ChildProxyURLs)
	}
}

func TestStoreJSONDoesNotPersistSettings(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	if err := store.Update(func(sf *StoreFile) error {
		sf.State.MessageCounter = 42
		return nil
	}); err != nil {
		t.Fatalf("update store: %v", err)
	}

	bytes, err := os.ReadFile(filepath.Join(root, "store.json"))
	if err != nil {
		t.Fatalf("read store.json: %v", err)
	}
	if strings.Contains(string(bytes), "\"settings\"") {
		t.Fatalf("settings should not be persisted in store.json: %s", string(bytes))
	}
}

func TestOpenStoreRecoversAccountsFromAccountsDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	alias := "recovered"
	home := filepath.Join(root, "accounts", alias)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir account home: %v", err)
	}
	auth := `{"tokens":{"access_token":"tok","account_id":"acc123"}}`
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(auth), 0o600); err != nil {
		t.Fatalf("write auth: %v", err)
	}

	if err := store.Update(func(sf *StoreFile) error {
		sf.Accounts = []Account{
			{
				ID:      "openai:ghost",
				Alias:   "ghost",
				HomeDir: filepath.Join(root, "accounts", "ghost"),
				Enabled: true,
			},
		}
		sf.State.ActiveIndex = 3
		sf.State.PinnedAccountID = "openai:ghost"
		return nil
	}); err != nil {
		t.Fatalf("clear accounts: %v", err)
	}

	reopened, err := OpenStore(root)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	accounts := ListAccounts(reopened)
	if len(accounts) != 1 {
		t.Fatalf("expected 1 reconciled account, got %d", len(accounts))
	}
	if accounts[0].Alias != alias {
		t.Fatalf("unexpected alias: %s", accounts[0].Alias)
	}
	if accounts[0].ChatGPTAccountID != "acc123" {
		t.Fatalf("unexpected account id: %s", accounts[0].ChatGPTAccountID)
	}
	snap := reopened.Snapshot()
	if snap.State.PinnedAccountID != "" {
		t.Fatalf("expected pinned account to be cleared, got %q", snap.State.PinnedAccountID)
	}
	if snap.State.ActiveIndex != 0 {
		t.Fatalf("expected active index to be clamped to 0, got %d", snap.State.ActiveIndex)
	}
}

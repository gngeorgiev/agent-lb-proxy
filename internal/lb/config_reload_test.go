package lb

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestReloadSettingsFromConfigUpdatesUpstreamAndTrackedAccountBaseURL(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	origListen := "127.0.0.1:8765"
	origName := "edge-a"
	oldUpstream := "https://old.example/backend-api"
	newUpstream := "https://new.example/backend-api"
	customUpstream := "https://custom.example/backend-api"

	if err := store.Update(func(sf *StoreFile) error {
		sf.Settings.Proxy.Name = origName
		sf.Settings.Proxy.Listen = origListen
		sf.Settings.Proxy.UpstreamBaseURL = oldUpstream
		sf.Accounts = []Account{
			{ID: "a", Alias: "a", BaseURL: oldUpstream, Enabled: true},
			{ID: "b", Alias: "b", BaseURL: customUpstream, Enabled: true},
		}
		return nil
	}); err != nil {
		t.Fatalf("store update: %v", err)
	}

	cfg := store.Snapshot().Settings
	cfg.Proxy.Name = "edge-b"
	cfg.Proxy.UpstreamBaseURL = newUpstream
	cfg.Proxy.Listen = "127.0.0.1:9999"
	cfg.Proxy.ChildProxyURLs = []string{"http://child-a.internal", "http://child-b.internal"}
	if err := WriteSettingsConfig(root, cfg); err != nil {
		t.Fatalf("WriteSettingsConfig: %v", err)
	}

	summary, err := store.ReloadSettingsFromConfig()
	if err != nil {
		t.Fatalf("ReloadSettingsFromConfig: %v", err)
	}
	if !summary.UpstreamChanged {
		t.Fatalf("expected upstream change to be detected")
	}
	if !summary.ListenChangeIgnored {
		t.Fatalf("expected listen change to be ignored at runtime")
	}
	if summary.UpdatedAccountBaseURL != 1 {
		t.Fatalf("expected 1 tracked account upstream update, got %d", summary.UpdatedAccountBaseURL)
	}

	snap := store.Snapshot()
	if snap.Settings.Proxy.Listen != origListen {
		t.Fatalf("expected listen to stay %s, got %s", origListen, snap.Settings.Proxy.Listen)
	}
	if snap.Settings.Proxy.Name != "edge-b" {
		t.Fatalf("expected proxy name edge-b, got %s", snap.Settings.Proxy.Name)
	}
	if snap.Settings.Proxy.UpstreamBaseURL != newUpstream {
		t.Fatalf("expected upstream %s, got %s", newUpstream, snap.Settings.Proxy.UpstreamBaseURL)
	}
	if got := snap.Settings.Proxy.ChildProxyURLs; len(got) != 2 || got[0] != "http://child-a.internal" || got[1] != "http://child-b.internal" {
		t.Fatalf("expected child proxy urls to reload, got %#v", got)
	}
	if got := snap.Accounts[0].BaseURL; got != newUpstream {
		t.Fatalf("expected tracked account base URL %s, got %s", newUpstream, got)
	}
	if got := snap.Accounts[1].BaseURL; got != customUpstream {
		t.Fatalf("expected custom account base URL to remain %s, got %s", customUpstream, got)
	}
}

func TestStartConfigReloaderReloadsPolicyMode(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if err := store.Update(func(sf *StoreFile) error {
		sf.Settings.Policy.Mode = PolicySticky
		return nil
	}); err != nil {
		t.Fatalf("store update: %v", err)
	}
	if err := store.PersistSettingsToConfig(); err != nil {
		t.Fatalf("PersistSettingsToConfig: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartConfigReloader(ctx, store, nil, nil, 20*time.Millisecond)

	cfg := store.Snapshot().Settings
	cfg.Policy.Mode = PolicyUsageBalanced
	if err := WriteSettingsConfig(root, cfg); err != nil {
		t.Fatalf("WriteSettingsConfig: %v", err)
	}

	if !waitFor(t, 2*time.Second, 20*time.Millisecond, func() bool {
		return store.Snapshot().Settings.Policy.Mode == PolicyUsageBalanced
	}) {
		t.Fatalf("timed out waiting for policy mode reload")
	}
}

func waitFor(t *testing.T, timeout, interval time.Duration, fn func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(interval)
	}
	return fn()
}

func TestConfigReloaderHandlesAtomicRenameWrites(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if err := store.PersistSettingsToConfig(); err != nil {
		t.Fatalf("PersistSettingsToConfig: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartConfigReloader(ctx, store, nil, nil, 20*time.Millisecond)

	cfg := store.Snapshot().Settings
	cfg.Policy.Mode = PolicyRoundRobin
	shadowRoot := t.TempDir()
	if err := WriteSettingsConfig(shadowRoot, cfg); err != nil {
		t.Fatalf("WriteSettingsConfig shadow: %v", err)
	}
	// Replace config atomically, as many editors do.
	if err := os.Rename(ConfigPath(shadowRoot), ConfigPath(root)); err != nil {
		t.Fatalf("rename config: %v", err)
	}

	if !waitFor(t, 2*time.Second, 20*time.Millisecond, func() bool {
		return store.Snapshot().Settings.Policy.Mode == PolicyRoundRobin
	}) {
		t.Fatalf("timed out waiting for policy mode reload after atomic rename")
	}
}

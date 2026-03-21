package lb

import (
	"testing"
	"time"
)

func TestBuildProxyStatusIncludesSelectionAndScores(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	sf := defaultStore()
	sf.Settings.Proxy.Name = "edge-a"
	sf.Settings.Policy.Mode = PolicyUsageBalanced
	sf.State.ActiveIndex = 0
	sf.Accounts = []Account{
		{
			Alias:   "alice",
			ID:      "openai:alice",
			Enabled: true,
			Quota: QuotaState{
				DailyLimit:  100,
				DailyUsed:   80,
				WeeklyLimit: 100,
				WeeklyUsed:  70,
				Source:      "openai_usage_api",
			},
		},
		{
			Alias:   "bob",
			ID:      "openai:bob",
			Enabled: true,
			Quota: QuotaState{
				DailyLimit:  100,
				DailyUsed:   20,
				WeeklyLimit: 100,
				WeeklyUsed:  30,
				Source:      "openai_usage_api",
			},
		},
	}

	status := BuildProxyStatus(sf, now)
	if status.ProxyName != "edge-a" {
		t.Fatalf("expected proxy name edge-a, got %q", status.ProxyName)
	}
	if status.SelectedAccountID != "openai:bob" {
		t.Fatalf("expected bob selected, got %q", status.SelectedAccountID)
	}
	if len(status.Accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(status.Accounts))
	}
	if status.Accounts[0].Alias != "alice" || !status.Accounts[0].Active {
		t.Fatalf("expected active account sorted first")
	}
	if status.Accounts[0].ProxyName != "edge-a" || status.Accounts[1].ProxyName != "edge-a" {
		t.Fatalf("expected account proxy names to be populated: %+v", status.Accounts)
	}
}

func TestBuildProxyStatusCooldownAndDisabled(t *testing.T) {
	t.Parallel()
	now := time.Now()
	sf := defaultStore()
	sf.Accounts = []Account{
		{Alias: "a", ID: "a", Enabled: true, CooldownUntilMS: now.Add(5 * time.Second).UnixMilli()},
		{Alias: "b", ID: "b", Enabled: false, DisabledReason: "http-401"},
	}
	status := BuildProxyStatus(sf, now)
	if status.Accounts[0].CooldownSeconds <= 0 && status.Accounts[1].CooldownSeconds <= 0 {
		t.Fatalf("expected cooldown seconds on one account")
	}
	foundDisabled := false
	for _, a := range status.Accounts {
		if a.DisabledReason == "http-401" {
			foundDisabled = true
		}
	}
	if !foundDisabled {
		t.Fatalf("expected disabled account in status")
	}
}

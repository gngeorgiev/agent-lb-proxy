package lb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProxyStatusEndpoint(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	nowMS := time.Now().UnixMilli()
	if err := store.Update(func(sf *StoreFile) error {
		sf.Settings.Policy.Mode = PolicyUsageBalanced
		sf.Accounts = []Account{
			{
				Alias:   "alice",
				ID:      "openai:alice",
				Enabled: true,
				Quota: QuotaState{
					DailyLimit:  100,
					DailyUsed:   60,
					WeeklyLimit: 100,
					WeeklyUsed:  70,
					LastSyncAt:  nowMS,
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
					LastSyncAt:  nowMS,
				},
			},
		}
		sf.State.ActiveIndex = 0
		return nil
	}); err != nil {
		t.Fatalf("store update: %v", err)
	}

	srv := httptest.NewServer(NewProxyServer(store, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var status ProxyStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.SelectedAccountID != "openai:bob" {
		t.Fatalf("expected selected openai:bob, got %q", status.SelectedAccountID)
	}
	if len(status.Accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(status.Accounts))
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gngeorgiev/openai-codex-lb/internal/lb"
)

func setLocalZone(t *testing.T, name string, offsetSeconds int) {
	t.Helper()
	prev := time.Local
	time.Local = time.FixedZone(name, offsetSeconds)
	t.Cleanup(func() {
		time.Local = prev
	})
}

func TestStatusCommandPrintsTable(t *testing.T) {
	setLocalZone(t, "EET", 2*60*60)
	now := time.Now()

	status := lb.ProxyStatus{
		ProxyName:         "main",
		GeneratedAt:       time.Now().UTC().Format(time.RFC3339),
		Policy:            lb.PolicyConfig{Mode: lb.PolicyUsageBalanced},
		SelectedAccountID: "openai:alice",
		State:             lb.RuntimeState{PinnedAccountID: "openai:alice"},
		SelectionReason:   "usage-stay",
		Accounts: []lb.AccountStatus{
			{ProxyName: "main", Alias: "alice", ID: "openai:alice", Email: "a@example.com", Active: true, Healthy: true, Enabled: true, DailyLeftPct: 80, DailyResetAt: now.Add(6 * time.Hour).Unix(), WeeklyLeftPct: 70, WeeklyResetAt: now.Add(4 * 24 * time.Hour).Unix(), Score: 0.75},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(status)
	}))
	defer server.Close()

	out, code := captureStdout(func() int {
		return run([]string{"status", "--root", t.TempDir(), "--proxy-url", server.URL})
	})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d output=%s", code, out)
	}
	if strings.Contains(out, "policy=usage_balanced") || strings.Contains(out, "proxy=main") || strings.Contains(out, "pinned=alice") {
		t.Fatalf("did not expect status header line in output: %s", out)
	}
	if !regexp.MustCompile(`(?m)^\*\s+P\s+main\s+alice\s+a@example\.com`).MatchString(out) {
		t.Fatalf("expected pinned marker in account row: %s", out)
	}
	if !strings.Contains(out, "PIN") {
		t.Fatalf("expected pin column when an account is pinned: %s", out)
	}
	if strings.Contains(out, "STATUS") {
		t.Fatalf("did not expect status column when all accounts are ready: %s", out)
	}
	if strings.Contains(out, "\tID\t") || strings.Contains(out, " ID ") {
		t.Fatalf("did not expect ID column in output: %s", out)
	}
	if !strings.Contains(out, "alice") {
		t.Fatalf("expected account row in output: %s", out)
	}
	if !strings.Contains(out, "in 6 hours") && !strings.Contains(out, "in 5 hours") && !strings.Contains(out, "in 7 hours") {
		t.Fatalf("expected relative reset timestamp in output: %s", out)
	}
	if !strings.Contains(out, "in 4 days") {
		t.Fatalf("expected relative weekly reset timestamp in output: %s", out)
	}
	if !regexp.MustCompile(`(?m)^(\s+)?80\.0%\s+\s+70\.0%`).MatchString(out) {
		t.Fatalf("expected aggregate usage line in output: %s", out)
	}
}

func TestFormatStatusGeneratedAtUsesLocalTimezone(t *testing.T) {
	setLocalZone(t, "EEST", 3*60*60)

	got := formatStatusGeneratedAt("2024-03-09T16:00:00Z")
	if got != "2024-03-09 19:00 EEST" {
		t.Fatalf("unexpected generated_at format: %q", got)
	}
}

func TestStatusCommandAggregateUsageLeftAveragesAccounts(t *testing.T) {
	status := lb.ProxyStatus{
		ProxyName:   "main",
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Policy:      lb.PolicyConfig{Mode: lb.PolicyUsageBalanced},
		Accounts: []lb.AccountStatus{
			{ProxyName: "main", Alias: "a", ID: "openai:a", Enabled: true, DailyLeftPct: 100, WeeklyLeftPct: 80},
			{ProxyName: "main", Alias: "b", ID: "openai:b", Enabled: true, DailyLeftPct: 70, WeeklyLeftPct: 50},
			{ProxyName: "main", Alias: "c", ID: "openai:c", Enabled: true, DailyLeftPct: 40, WeeklyLeftPct: 20},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(status)
	}))
	defer server.Close()

	out, code := captureStdout(func() int {
		return run([]string{"status", "--root", t.TempDir(), "--proxy-url", server.URL})
	})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d output=%s", code, out)
	}
	if !regexp.MustCompile(`(?m)^(\s+)?70\.0%\s+\s+50\.0%`).MatchString(out) {
		t.Fatalf("unexpected aggregate usage line: %s", out)
	}
}

func TestStatusCommandHidesExpiredTransientLastSwitchReason(t *testing.T) {
	status := lb.ProxyStatus{
		ProxyName:   "main",
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Policy:      lb.PolicyConfig{Mode: lb.PolicyUsageBalanced},
		Accounts: []lb.AccountStatus{
			{
				ProxyName:        "main",
				Alias:            "g99517399",
				ID:               "openai:g99517399",
				Email:            "g99517399@gmail.com",
				Enabled:          true,
				Healthy:          true,
				CooldownSeconds:  0,
				DailyLeftPct:     64,
				WeeklyLeftPct:    94,
				Score:            0.760,
				LastSwitchReason: "websocket-proxy-error",
				QuotaSource:      "openai_usage_api",
			},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(status)
	}))
	defer server.Close()

	out, code := captureStdout(func() int {
		return run([]string{"status", "--root", t.TempDir(), "--proxy-url", server.URL})
	})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d output=%s", code, out)
	}
	if strings.Contains(out, "STATUS") {
		t.Fatalf("did not expect status column in output: %s", out)
	}
	if strings.Contains(out, "websocket-proxy-error") {
		t.Fatalf("expected expired websocket-proxy-error to be hidden: %s", out)
	}
}

func TestStatusCommandHidesPinAndStatusColumnsWhenUnused(t *testing.T) {
	status := lb.ProxyStatus{
		ProxyName:   "main",
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Policy:      lb.PolicyConfig{Mode: lb.PolicyUsageBalanced},
		Accounts: []lb.AccountStatus{
			{ProxyName: "main", Alias: "a", ID: "openai:a", Email: "a@example.com", Enabled: true, Healthy: true, DailyLeftPct: 90, WeeklyLeftPct: 80, Score: 0.9},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(status)
	}))
	defer server.Close()

	out, code := captureStdout(func() int {
		return run([]string{"status", "--root", t.TempDir(), "--proxy-url", server.URL})
	})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d output=%s", code, out)
	}
	header := strings.Split(strings.TrimSpace(out), "\n")[0]
	if strings.Contains(header, "PIN") {
		t.Fatalf("did not expect PIN column in header: %s", out)
	}
	if strings.Contains(header, "STATUS") {
		t.Fatalf("did not expect STATUS column in header: %s", out)
	}
	if strings.Contains(header, "ID") {
		t.Fatalf("did not expect ID column in header: %s", out)
	}
}

func TestStatusCommandShowsStatusColumnWhenAccountNotReady(t *testing.T) {
	status := lb.ProxyStatus{
		ProxyName:   "main",
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Policy:      lb.PolicyConfig{Mode: lb.PolicyUsageBalanced},
		Accounts: []lb.AccountStatus{
			{ProxyName: "main", Alias: "a", ID: "openai:a", Email: "a@example.com", Enabled: false, DisabledReason: "refresh-token-reused", DailyLeftPct: 90, WeeklyLeftPct: 80, Score: 0.9},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(status)
	}))
	defer server.Close()

	out, code := captureStdout(func() int {
		return run([]string{"status", "--root", t.TempDir(), "--proxy-url", server.URL})
	})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d output=%s", code, out)
	}
	if !strings.Contains(out, "STATUS") {
		t.Fatalf("expected STATUS column in output: %s", out)
	}
	if !strings.Contains(out, "disabled(refresh-token-reused)") {
		t.Fatalf("expected disabled status in output: %s", out)
	}
	if strings.Contains(out, "QUOTA") || strings.Contains(out, "LAST_SWITCH") {
		t.Fatalf("did not expect quota/last switch columns in output: %s", out)
	}
}

func TestStatusCommandUsesCompactLayoutOnNarrowTerminals(t *testing.T) {
	t.Setenv("COLUMNS", "100")
	now := time.Now()
	status := lb.ProxyStatus{
		ProxyName:   "main",
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Policy:      lb.PolicyConfig{Mode: lb.PolicyUsageBalanced},
		Accounts: []lb.AccountStatus{
			{
				ProxyName:        "main",
				Alias:            "alice",
				ID:               "openai:alice",
				Email:            "a@example.com",
				Active:           true,
				Enabled:          true,
				Healthy:          true,
				DailyLeftPct:     80,
				DailyResetAt:     now.Add(6 * time.Hour).Unix(),
				WeeklyLeftPct:    70,
				WeeklyResetAt:    now.Add(4 * 24 * time.Hour).Unix(),
				Score:            0.75,
				LastSwitchReason: "usage-stay",
				QuotaSource:      "openai_usage_api",
			},
		},
		ChildProxies: []lb.ChildProxyStatus{
			{Name: "edge-vpn", URL: "http://mullvad-vpn:8766", Reachable: true, Healthy: true, Score: 0.6, SelectedTarget: "openai:alice", SelectionReason: "usage-stay"},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(status)
	}))
	defer server.Close()

	out, code := captureStdout(func() int {
		return run([]string{"status", "--root", t.TempDir(), "--proxy-url", server.URL})
	})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d output=%s", code, out)
	}
	if strings.Contains(out, "ACTIVE  PROXY  ALIAS") {
		t.Fatalf("did not expect wide table header in compact output: %s", out)
	}
	if !strings.Contains(out, "* alice @ main") {
		t.Fatalf("expected compact account header: %s", out)
	}
	if !strings.Contains(out, "daily 80.0%") || !strings.Contains(out, "weekly 70.0%") {
		t.Fatalf("expected compact usage line: %s", out)
	}
	if !strings.Contains(out, "----") || !strings.Contains(out, "daily 80.0%  weekly 70.0%") {
		t.Fatalf("expected compact aggregate block: %s", out)
	}
	if strings.Contains(out, "quota ") || strings.Contains(out, "note ") {
		t.Fatalf("did not expect quota/last-switch metadata in compact output: %s", out)
	}
	if !strings.Contains(out, "child proxies:") || !strings.Contains(out, "- edge-vpn  ready  score 0.600") {
		t.Fatalf("expected compact child proxy section: %s", out)
	}
}

func TestStatusCommandJSON(t *testing.T) {
	status := lb.ProxyStatus{GeneratedAt: time.Now().UTC().Format(time.RFC3339), Accounts: []lb.AccountStatus{{Alias: "a", ID: "id-a"}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(status)
	}))
	defer server.Close()

	out, code := captureStdout(func() int {
		return run([]string{"status", "--root", t.TempDir(), "--proxy-url", server.URL, "--json"})
	})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(out, `"accounts"`) {
		t.Fatalf("expected json output, got: %s", out)
	}
}

func TestStatusCommandShort(t *testing.T) {
	status := lb.ProxyStatus{
		ProxyName:       "main",
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
		Policy:          lb.PolicyConfig{Mode: lb.PolicyUsageBalanced},
		SelectionReason: "usage-stay",
		Accounts: []lb.AccountStatus{
			{ProxyName: "main", Alias: "alice", ID: "openai:alice", Active: true},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(status)
	}))
	defer server.Close()

	out, code := captureStdout(func() int {
		return run([]string{"status", "--root", t.TempDir(), "--proxy-url", server.URL, "--short"})
	})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d output=%s", code, out)
	}
	line := strings.TrimSpace(out)
	if line != "lb=alice reason=usage-stay mode=usage_balanced" {
		t.Fatalf("unexpected short status line: %q", line)
	}
}

func TestStatusCommandShortUsesActiveChildProxy(t *testing.T) {
	status := lb.ProxyStatus{
		ProxyName:         "main",
		GeneratedAt:       time.Now().UTC().Format(time.RFC3339),
		Policy:            lb.PolicyConfig{Mode: lb.PolicyUsageBalanced},
		SelectedProxyName: "child-b",
		SelectionReason:   "usage-stay",
		ChildProxies: []lb.ChildProxyStatus{
			{Name: "child-b", URL: "http://child-b.internal", Active: true, Healthy: true, Reachable: true, Score: 0.9},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(status)
	}))
	defer server.Close()

	out, code := captureStdout(func() int {
		return run([]string{"status", "--root", t.TempDir(), "--proxy-url", server.URL, "--short"})
	})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d output=%s", code, out)
	}
	line := strings.TrimSpace(out)
	if line != "lb=child-b reason=usage-stay mode=usage_balanced" {
		t.Fatalf("unexpected short status line: %q", line)
	}
}

func TestStatusCommandPrintsChildProxyTable(t *testing.T) {
	status := lb.ProxyStatus{
		ProxyName:         "main",
		GeneratedAt:       time.Now().UTC().Format(time.RFC3339),
		Policy:            lb.PolicyConfig{Mode: lb.PolicyUsageBalanced},
		SelectedProxyURL:  "http://child-b.internal",
		SelectedProxyName: "child-b",
		SelectionReason:   "usage-stay",
		Accounts: []lb.AccountStatus{
			{ProxyName: "child-b", Alias: "bob", ID: "openai:bob", Active: true, Healthy: true, Enabled: true, Score: 0.9},
		},
		ChildProxies: []lb.ChildProxyStatus{
			{
				Name:            "child-b",
				URL:             "http://child-b.internal",
				Active:          true,
				Healthy:         true,
				Reachable:       true,
				Score:           0.9,
				SelectedTarget:  "openai:bob",
				SelectionReason: "usage-stay",
			},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(status)
	}))
	defer server.Close()

	out, code := captureStdout(func() int {
		return run([]string{"status", "--root", t.TempDir(), "--proxy-url", server.URL})
	})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d output=%s", code, out)
	}
	if !strings.Contains(out, "child-b") {
		t.Fatalf("expected child proxy name in output: %s", out)
	}
	if !strings.Contains(out, "http://child-b.internal") {
		t.Fatalf("expected child proxy row in output: %s", out)
	}
	if !strings.Contains(out, "openai:bob") {
		t.Fatalf("expected child selected target in output: %s", out)
	}
}

func TestStatusCommandJSONAndShortMutuallyExclusive(t *testing.T) {
	errOut, code := captureStderr(func() int {
		return run([]string{"status", "--root", t.TempDir(), "--json", "--short"})
	})
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(errOut, "mutually exclusive") {
		t.Fatalf("expected mutual exclusion error, got: %s", errOut)
	}
}

func TestStatusCommandDefaultsToRunProxyURL(t *testing.T) {
	status := lb.ProxyStatus{GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(status)
	}))
	defer server.Close()

	root := t.TempDir()
	store, err := lb.OpenStore(root)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cfg := store.Snapshot().Settings
	cfg.ProxyURL = server.URL
	cfg.Proxy.Listen = "127.0.0.1:1"
	if err := lb.WriteSettingsConfig(root, cfg); err != nil {
		t.Fatalf("write settings config: %v", err)
	}

	_, code := captureStdout(func() int {
		return run([]string{"status", "--root", root})
	})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
}

func TestStatusCommandUsesCODEXLBProxyURL(t *testing.T) {
	status := lb.ProxyStatus{GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(status)
	}))
	defer server.Close()

	t.Setenv("CODEXLB_PROXY_URL", server.URL)

	_, code := captureStdout(func() int {
		return run([]string{"status", "--root", t.TempDir()})
	})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
}

func TestStatusCommandUsesCODEXLBRoot(t *testing.T) {
	status := lb.ProxyStatus{GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(status)
	}))
	defer server.Close()

	root := t.TempDir()
	store, err := lb.OpenStore(root)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cfg := store.Snapshot().Settings
	cfg.ProxyURL = server.URL
	cfg.Proxy.Listen = "127.0.0.1:1"
	if err := lb.WriteSettingsConfig(root, cfg); err != nil {
		t.Fatalf("write settings config: %v", err)
	}

	t.Setenv("CODEXLB_ROOT", root)

	_, code := captureStdout(func() int {
		return run([]string{"status"})
	})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
}

func TestParseProxyURLListEnv(t *testing.T) {
	got := parseProxyURLListEnv(" http://child-a.internal/,\nhttp://child-b.internal/base  http://child-a.internal/ ")
	if len(got) != 2 {
		t.Fatalf("expected 2 child proxy urls, got %#v", got)
	}
	if got[0] != "http://child-a.internal" || got[1] != "http://child-b.internal/base" {
		t.Fatalf("unexpected parsed urls: %#v", got)
	}
}

func captureStdout(fn func() int) (string, int) {
	orig := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	code := fn()
	_ = w.Close()
	os.Stdout = orig
	buf := &bytes.Buffer{}
	_, _ = io.Copy(buf, r)
	_ = r.Close()
	return buf.String(), code
}

func captureStderr(fn func() int) (string, int) {
	orig := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	code := fn()
	_ = w.Close()
	os.Stderr = orig
	buf := &bytes.Buffer{}
	_, _ = io.Copy(buf, r)
	_ = r.Close()
	return buf.String(), code
}

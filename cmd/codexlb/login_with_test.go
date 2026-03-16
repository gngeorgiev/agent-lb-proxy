package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoginWithImportsAuthFromDocker(t *testing.T) {
	root := t.TempDir()
	destHome := filepath.Join(root, "host-codex-home")
	dockerLog := filepath.Join(root, "docker.log")
	dockerBin := filepath.Join(root, "docker")
	writeFakeDocker(t, dockerBin)

	t.Setenv("FAKE_DOCKER_LOG", dockerLog)
	t.Setenv("FAKE_DOCKER_AUTH", `{"tokens":{"access_token":"`+testJWT(map[string]any{
		"https://api.openai.com/auth":    map[string]any{"chatgpt_account_id": "acct-docker"},
		"https://api.openai.com/profile": map[string]any{"email": "docker@example.com"},
	})+`","account_id":"acct-docker"}}`)

	out, code := captureStdout(func() int {
		return run([]string{
			"login-with",
			"work",
			"--root", root,
			"--username", "docker@example.com",
			"--password", "secret-value",
			"--docker-bin", dockerBin,
			"--docker-image", "codexlb-login:test",
			"--docker-network", "vpn_net",
			"--codex-home", destHome,
		})
	})
	if code != 0 {
		t.Fatalf("login-with failed: code=%d out=%s", code, out)
	}

	authPath := filepath.Join(destHome, "auth.json")
	rawAuth, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("read imported auth.json: %v", err)
	}
	if !strings.Contains(string(rawAuth), "acct-docker") {
		t.Fatalf("unexpected imported auth.json: %s", string(rawAuth))
	}
	accountAuth := filepath.Join(root, "accounts", "work", "auth.json")
	if _, err := os.Stat(accountAuth); err != nil {
		t.Fatalf("expected account auth.json: %v", err)
	}

	logData, err := os.ReadFile(dockerLog)
	if err != nil {
		t.Fatalf("read fake docker log: %v", err)
	}
	logText := string(logData)
	if strings.Contains(logText, "BUILD ") {
		t.Fatalf("did not expect a local image build: %s", logText)
	}
	if !strings.Contains(logText, "--network vpn_net") {
		t.Fatalf("missing docker network in log: %s", logText)
	}
	if !strings.Contains(logText, "codexlb-login:test") {
		t.Fatalf("missing docker image override in log: %s", logText)
	}
	if !strings.Contains(out, destHome) {
		t.Fatalf("expected destination home in stdout, got: %s", out)
	}
	if !strings.Contains(out, "registered account work") {
		t.Fatalf("expected alias in stdout, got: %s", out)
	}
}

func TestLoginWithReadsPasswordFromStdin(t *testing.T) {
	root := t.TempDir()
	destHome := filepath.Join(root, "host-codex-home")
	dockerLog := filepath.Join(root, "docker.log")
	dockerBin := filepath.Join(root, "docker")
	writeFakeDocker(t, dockerBin)

	t.Setenv("FAKE_DOCKER_LOG", dockerLog)
	t.Setenv("FAKE_DOCKER_AUTH", `{"tokens":{"access_token":"`+testJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-stdin"},
	})+`","account_id":"acct-stdin"}}`)

	origStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdin: %v", err)
	}
	if _, err := w.WriteString("secret-from-stdin\n"); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	_ = w.Close()
	os.Stdin = r
	defer func() {
		os.Stdin = origStdin
		_ = r.Close()
	}()

	if code := run([]string{
		"login-with",
		"stdin",
		"--root", root,
		"--username", "stdin@example.com",
		"--password-stdin",
		"--docker-bin", dockerBin,
		"--codex-home", destHome,
	}); code != 0 {
		t.Fatalf("login-with --password-stdin failed: %d", code)
	}

	if _, err := os.Stat(filepath.Join(destHome, "auth.json")); err != nil {
		t.Fatalf("expected imported auth.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "accounts", "stdin", "auth.json")); err != nil {
		t.Fatalf("expected imported account auth.json: %v", err)
	}
}

func TestLoginWithRejectsPasswordFlagConflicts(t *testing.T) {
	errOut, code := captureStderr(func() int {
		return run([]string{
			"login-with",
			"conflict",
			"--username", "conflict@example.com",
			"--password", "secret",
			"--password-stdin",
		})
	})
	if code != 2 {
		t.Fatalf("expected exit code 2, got %d", code)
	}
	if !strings.Contains(errOut, "exactly one of --password or --password-stdin") {
		t.Fatalf("unexpected stderr: %s", errOut)
	}
}

func writeFakeDocker(t *testing.T, path string) {
	t.Helper()
	script := `#!/usr/bin/env bash
set -euo pipefail
cmd="${1:-}"
shift || true
	case "$cmd" in
  run)
    echo "RUN $*" >> "${FAKE_DOCKER_LOG:?missing FAKE_DOCKER_LOG}"
    output_mount=""
    prev=""
    for arg in "$@"; do
      if [[ "$prev" == "-v" ]]; then
        if [[ "$arg" == *":/work/output" ]]; then
          output_mount="${arg%%:/work/output}"
        fi
      fi
      prev="$arg"
    done
    if [[ -z "$output_mount" ]]; then
      echo "missing output mount" >&2
      exit 1
    fi
    mkdir -p "$output_mount/codex-home"
    cat > "$output_mount/codex-home/auth.json" <<JSON
${FAKE_DOCKER_AUTH:?missing FAKE_DOCKER_AUTH}
JSON
    exit 0
    ;;
esac
echo "unexpected docker command: $cmd" >&2
exit 1
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker script: %v", err)
	}
}

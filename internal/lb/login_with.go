package lb

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type DockerLoginOptions struct {
	Username      string
	Password      string
	DockerBin     string
	DockerImage   string
	DockerNetwork string
	CodexHome     string
}

const DefaultLoginDockerImage = "ghcr.io/gngeorgiev/agent-lb-proxy-login:latest"

func DefaultCodexHome() (string, error) {
	if envHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); envHome != "" {
		return envHome, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".codex"), nil
}

func LoginWithDocker(ctx context.Context, opts DockerLoginOptions) error {
	if strings.TrimSpace(opts.Username) == "" {
		return fmt.Errorf("username is required")
	}
	if strings.TrimSpace(opts.Password) == "" {
		return fmt.Errorf("password is required")
	}
	if strings.TrimSpace(opts.DockerBin) == "" {
		opts.DockerBin = "docker"
	}
	if strings.TrimSpace(opts.DockerImage) == "" {
		opts.DockerImage = DefaultLoginDockerImage
	}
	if strings.TrimSpace(opts.DockerNetwork) == "" {
		opts.DockerNetwork = "bridge"
	}
	if strings.TrimSpace(opts.CodexHome) == "" {
		home, err := DefaultCodexHome()
		if err != nil {
			return err
		}
		opts.CodexHome = home
	}

	stageDir, err := os.MkdirTemp("", "codexlb-login-with-*")
	if err != nil {
		return fmt.Errorf("create temp workdir: %w", err)
	}
	defer os.RemoveAll(stageDir)

	requestDir := filepath.Join(stageDir, "request")
	outputDir := filepath.Join(stageDir, "output")
	outputHome := filepath.Join(outputDir, "codex-home")
	if err := os.MkdirAll(requestDir, 0o700); err != nil {
		return fmt.Errorf("create request dir: %w", err)
	}
	if err := os.MkdirAll(outputHome, 0o700); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	requestPath := filepath.Join(requestDir, "request.json")
	reqData := []byte(fmt.Sprintf("{\"username\":%q,\"password\":%q}", opts.Username, opts.Password))
	if err := os.WriteFile(requestPath, reqData, 0o600); err != nil {
		return fmt.Errorf("write login request: %w", err)
	}

	if err := runDockerLogin(ctx, opts.DockerBin, opts.DockerImage, opts.DockerNetwork, requestDir, outputDir); err != nil {
		return err
	}

	if _, err := LoadAuth(outputHome); err != nil {
		return fmt.Errorf("validate imported auth from container: %w", err)
	}

	if err := importDockerLoginArtifacts(outputHome, opts.CodexHome); err != nil {
		return err
	}
	return nil
}

func runDockerLogin(ctx context.Context, dockerBin, image, network, requestDir, outputDir string) error {
	args := []string{
		"run",
		"--rm",
		"--init",
		"--network", network,
		"-v", requestDir + ":/work/request:ro",
		"-v", outputDir + ":/work/output",
		image,
	}
	cmd := exec.CommandContext(ctx, dockerBin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run docker login container: %w", err)
	}
	return nil
}

func importDockerLoginArtifacts(fromHome, toHome string) error {
	if err := os.MkdirAll(toHome, 0o700); err != nil {
		return fmt.Errorf("create host CODEX_HOME: %w", err)
	}
	authPath := filepath.Join(fromHome, "auth.json")
	if _, err := os.Stat(authPath); err != nil {
		return fmt.Errorf("container login did not produce auth.json: %w", err)
	}
	if err := copyFile(authPath, filepath.Join(toHome, "auth.json"), 0o600); err != nil {
		return fmt.Errorf("import auth.json to host: %w", err)
	}
	configPath := filepath.Join(fromHome, "config.toml")
	if _, err := os.Stat(configPath); err == nil {
		if err := copyFile(configPath, filepath.Join(toHome, "config.toml"), 0o600); err != nil {
			return fmt.Errorf("import config.toml to host: %w", err)
		}
	}
	return nil
}

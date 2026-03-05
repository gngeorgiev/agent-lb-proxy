package lb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"time"
)

func StartConfigReloader(ctx context.Context, store *Store, logger *log.Logger, events *EventLogger, interval time.Duration) {
	if store == nil {
		return
	}
	if interval <= 0 {
		interval = time.Second
	}

	path := ConfigPath(store.RootDir())
	lastHash, _ := fileDigest(path)
	events.Log("config.reload_watcher_started", map[string]any{
		"path":        path,
		"interval_ms": interval.Milliseconds(),
	})

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		lastErr := ""

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				hash, err := fileDigest(path)
				if err != nil {
					msg := fmt.Sprintf("config digest: %v", err)
					if msg != lastErr {
						if logger != nil {
							logger.Printf("config reload: %s", msg)
						}
						events.Log("config.reload_failed", map[string]any{
							"path":  path,
							"error": msg,
						})
						lastErr = msg
					}
					continue
				}
				if hash == lastHash {
					continue
				}

				summary, err := store.ReloadSettingsFromConfig()
				if err != nil {
					msg := fmt.Sprintf("reload config.toml: %v", err)
					if msg != lastErr {
						if logger != nil {
							logger.Printf("config reload: %s", msg)
						}
						events.Log("config.reload_failed", map[string]any{
							"path":  path,
							"error": msg,
						})
						lastErr = msg
					}
					continue
				}

				lastHash = hash
				lastErr = ""
				fields := map[string]any{
					"path":                     path,
					"policy_mode":              summary.Current.Policy.Mode,
					"upstream_base_url":        summary.Current.Proxy.UpstreamBaseURL,
					"updated_account_base_url": summary.UpdatedAccountBaseURL,
				}
				if summary.ListenChangeIgnored {
					fields["listen_requires_restart"] = true
					fields["listen_running"] = summary.Previous.Proxy.Listen
				}
				events.Log("config.reloaded", fields)
				if logger != nil {
					logger.Printf("reloaded config.toml (policy=%s upstream=%s updated_accounts=%d listen_requires_restart=%t)",
						summary.Current.Policy.Mode, summary.Current.Proxy.UpstreamBaseURL, summary.UpdatedAccountBaseURL, summary.ListenChangeIgnored)
				}
			}
		}
	}()
}

func fileDigest(path string) (string, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	sum := sha256.Sum256(bytes)
	return hex.EncodeToString(sum[:]), nil
}

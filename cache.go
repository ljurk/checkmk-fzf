package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	cacheVersion  = 1
	cacheFreshFor = 30 * time.Second
	cacheLockFor  = 2 * time.Minute
)

type cacheEntry struct {
	Version   int       `json:"version"`
	Source    string    `json:"source"`
	FetchedAt time.Time `json:"fetched_at"`
	Hosts     []host    `json:"hosts"`
}

func cachePath() (string, error) {
	directory, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("find user cache directory: %w", err)
	}
	return filepath.Join(directory, programName(), "services.json"), nil
}

func cacheSource(cfg config) string {
	return cfg.Username + "@" + cfg.URL + "/" + cfg.Site
}

func readCache(cfg config) (cacheEntry, error) {
	path, err := cachePath()
	if err != nil {
		return cacheEntry{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return cacheEntry{}, err
	}
	defer file.Close()

	var entry cacheEntry
	if err := json.NewDecoder(file).Decode(&entry); err != nil {
		return cacheEntry{}, fmt.Errorf("decode cache: %w", err)
	}
	if entry.Version != cacheVersion || entry.Source != cacheSource(cfg) || entry.FetchedAt.IsZero() {
		return cacheEntry{}, errors.New("cache does not match the current configuration")
	}
	return entry, nil
}

func writeCache(cfg config, hosts []host) error {
	path, err := cachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}

	temporary, err := os.CreateTemp(filepath.Dir(path), ".services-*.json")
	if err != nil {
		return fmt.Errorf("create temporary cache: %w", err)
	}
	temporaryPath := temporary.Name()
	ok := false
	defer func() {
		temporary.Close()
		if !ok {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("set cache permissions: %w", err)
	}
	entry := cacheEntry{Version: cacheVersion, Source: cacheSource(cfg), FetchedAt: time.Now(), Hosts: hosts}
	encoder := json.NewEncoder(temporary)
	if err := encoder.Encode(entry); err != nil {
		return fmt.Errorf("encode cache: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync cache: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close cache: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace cache: %w", err)
	}
	ok = true
	return nil
}

func loadHosts(secrets secretStore) (*client, config, []host, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, config{}, nil, err
	}
	if entry, err := readCache(cfg); err == nil {
		if time.Since(entry.FetchedAt) >= cacheFreshFor {
			startBackgroundRefresh()
		}
		return clientForConfig(cfg, ""), cfg, entry.Hosts, nil
	}

	c, err := authenticatedClient(cfg, secrets)
	if err != nil {
		return nil, config{}, nil, err
	}
	hosts, err := fetchAllHosts(context.Background(), c)
	if err != nil {
		return nil, config{}, nil, err
	}
	// A cache failure should not hide successfully fetched Checkmk data.
	_ = writeCache(cfg, hosts)
	return c, cfg, hosts, nil
}

func fetchAllHosts(ctx context.Context, c *client) ([]host, error) {
	return c.hosts(ctx, map[string]any{"op": ">=", "left": "state", "right": "0"})
}

var startBackgroundRefresh = func() {
	executable, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(executable, "__refresh")
	if err := cmd.Start(); err == nil {
		_ = cmd.Process.Release()
	}
}

func executeRefresh(secrets secretStore) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	locked, unlock, err := acquireCacheLock()
	if err != nil || !locked {
		return err
	}
	defer unlock()

	c, err := authenticatedClient(cfg, secrets)
	if err != nil {
		return err
	}
	hosts, err := fetchAllHosts(context.Background(), c)
	if err != nil {
		return err
	}
	return writeCache(cfg, hosts)
}

func acquireCacheLock() (bool, func(), error) {
	path, err := cachePath()
	if err != nil {
		return false, func() {}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, func() {}, err
	}
	lockPath := path + ".lock"
	file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_ = file.Close()
		return true, func() { _ = os.Remove(lockPath) }, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return false, func() {}, fmt.Errorf("create cache lock: %w", err)
	}
	info, statErr := os.Stat(lockPath)
	if statErr == nil && time.Since(info.ModTime()) > cacheLockFor {
		if err := os.Remove(lockPath); err != nil {
			return false, func() {}, nil
		}
		return acquireCacheLock()
	}
	return false, func() {}, nil
}

package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type config struct {
	URL      string   `yaml:"url"`
	Site     string   `yaml:"site"`
	Username string   `yaml:"username"`
	Hosts    []string `yaml:"hosts,omitempty"`
}

func configPath() (string, error) {
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user config directory: %w", err)
	}
	return filepath.Join(directory, programName(), "config.yml"), nil
}

func loadConfig() (config, error) {
	path, err := configPath()
	if err != nil {
		return config{}, err
	}
	return loadConfigFile(path)
}

func loadConfigFile(path string) (config, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return config{}, fmt.Errorf("configuration not found at %s; create it from config.example.yml", path)
		}
		return config{}, fmt.Errorf("open configuration %s: %w", path, err)
	}
	defer file.Close()

	var cfg config
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return config{}, fmt.Errorf("decode configuration %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return config{}, fmt.Errorf("invalid configuration %s: %w", path, err)
	}
	return cfg, nil
}

func (cfg *config) validate() error {
	cfg.URL = strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	cfg.Site = strings.Trim(strings.TrimSpace(cfg.Site), "/")
	cfg.Username = strings.TrimSpace(cfg.Username)
	for index, host := range cfg.Hosts {
		cfg.Hosts[index] = strings.TrimSpace(host)
	}
	cfg.Hosts = compactStrings(cfg.Hosts)

	for name, value := range map[string]string{"url": cfg.URL, "site": cfg.Site, "username": cfg.Username} {
		if value == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	parsed, err := url.Parse(cfg.URL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return errors.New("url must be an absolute HTTP or HTTPS URL")
	}
	return nil
}

func compactStrings(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func newConfiguredClient(secrets secretStore) (*client, config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, config{}, err
	}
	secret, err := secrets.Get(keyringService, credentialAccount(cfg))
	if err != nil {
		if errors.Is(err, errSecretNotFound) {
			return nil, config{}, errors.New("no Checkmk credential is stored; run checkmk-fzf auth login")
		}
		return nil, config{}, fmt.Errorf("read credential from keyring: %w", err)
	}

	return &client{
		baseURL: cfg.URL, site: cfg.Site, username: cfg.Username, secret: secret,
		http: &http.Client{Timeout: 30 * time.Second},
	}, cfg, nil
}

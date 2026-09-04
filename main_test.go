package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestClientHosts(t *testing.T) {
	t.Parallel()

	var gotQuery map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/site/check_mk/api/1.0/domain-types/service/collections/all" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer user secret" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		if got := r.URL.Query()["columns"]; !reflect.DeepEqual(got, []string{"host_name", "host_alias", "description", "state", "plugin_output"}) {
			t.Errorf("columns = %#v", got)
		}
		if err := json.Unmarshal([]byte(r.URL.Query().Get("query")), &gotQuery); err != nil {
			t.Errorf("query: %v", err)
		}
		io.WriteString(w, `{"value":[
			{"extensions":{"host_name":"host-1","host_alias":"Alpha","description":"CPU","state":2,"plugin_output":"hot"}},
			{"extensions":{"host_name":"host-1","host_alias":"Alpha","description":"Disk","state":1}},
			{"extensions":{"host_name":"host-2","host_alias":"Beta","description":"Ping","state":0,"plugin_output":"up"}}
		]}`)
	}))
	defer server.Close()

	c := &client{
		baseURL: server.URL, site: "site", username: "user", secret: "secret",
		http: &http.Client{Timeout: time.Second},
	}
	wantQuery := map[string]any{"op": "!=", "left": "state", "right": "0"}
	hosts, err := c.hosts(context.Background(), wantQuery)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotQuery, wantQuery) {
		t.Errorf("query = %#v, want %#v", gotQuery, wantQuery)
	}
	if len(hosts) != 2 || hosts[0].Alias != "Alpha" || len(hosts[0].Services) != 2 || hosts[1].Alias != "Beta" {
		t.Fatalf("hosts = %#v", hosts)
	}
	if hosts[0].Services[1].Description != "" {
		t.Errorf("missing plugin_output = %q", hosts[0].Services[1].Description)
	}
}

func TestClientErrorIncludesResponse(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not authorized", http.StatusUnauthorized)
	}))
	defer server.Close()
	c := &client{baseURL: server.URL, site: "site", http: server.Client()}
	_, err := c.hosts(context.Background(), map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401: not authorized") {
		t.Fatalf("error = %v", err)
	}
}

func TestHostURL(t *testing.T) {
	t.Parallel()
	c := &client{baseURL: "https://checkmk.example", site: "main"}
	got := c.hostURL("web server/1")
	want := "https://checkmk.example/main/check_mk/view.py?host=web+server%2F1&view_name=host"
	if got != want {
		t.Fatalf("hostURL = %q, want %q", got, want)
	}
}

func TestFormatService(t *testing.T) {
	t.Parallel()
	description := strings.Repeat("ä", 61)
	got := formatService(service{Title: "CPU", State: 2, Description: description})
	if !strings.HasPrefix(got, "CPU                            critical   ") {
		t.Errorf("unexpected prefix: %q", got)
	}
	if suffix := strings.TrimPrefix(got, "CPU                            critical   "); len([]rune(suffix)) != 60 {
		t.Errorf("description has %d runes", len([]rune(suffix)))
	}
}

func TestLoadConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte("url: https://checkmk.example/\nsite: /main/\nusername: user\nhosts:\n  - ' Alpha '\n  - ''\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := config{URL: "https://checkmk.example", Site: "main", Username: "user", Hosts: []string{"Alpha"}}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("config = %#v, want %#v", cfg, want)
	}
}

func TestConfigRejectsSecretField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	content := "url: https://checkmk.example\nsite: main\nusername: user\nsecret: must-not-be-here\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfigFile(path); err == nil || !strings.Contains(err.Error(), "field secret not found") {
		t.Fatalf("error = %v", err)
	}
}

func TestOverviewOutputAndQuery(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		io.WriteString(w, `{"value":[
			{"extensions":{"host_name":"z","host_alias":"Zulu","description":"CPU","state":1,"plugin_output":"warn"}},
			{"extensions":{"host_name":"a","host_alias":"Alpha","description":"Disk","state":2,"plugin_output":"full"}},
			{"extensions":{"host_name":"a","host_alias":"Alpha","description":"CPU","state":1,"plugin_output":"hot"}}
		]}`)
	}))
	defer server.Close()
	setConfig(t, server.URL)
	secrets := &memoryKeyring{secret: "secret", exists: true}

	var stdout bytes.Buffer
	if err := executeOverview(false, &stdout, secrets); err != nil {
		t.Fatal(err)
	}
	want := "HOST                           CRIT WARN\n" +
		"Alpha                             1    1\n" +
		"Zulu                              0    1\n"
	if stdout.String() != want {
		t.Errorf("output:\n%s\nwant:\n%s", stdout.String(), want)
	}
	if gotQuery != `{"left":"state","op":"\u003e=","right":"0"}` {
		t.Errorf("query = %s", gotQuery)
	}
}

func TestLoadHostsUsesFreshCacheWithoutCredential(t *testing.T) {
	setConfig(t, "https://checkmk.example")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	want := []host{{Name: "host-1", Alias: "Alpha", Services: []service{{Title: "CPU", State: 2}}}}
	if err := writeCache(cfg, want); err != nil {
		t.Fatal(err)
	}

	_, _, got, err := loadHosts(&memoryKeyring{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hosts = %#v, want %#v", got, want)
	}
}

func TestLoadHostsStartsBackgroundRefreshForStaleCache(t *testing.T) {
	setConfig(t, "https://checkmk.example")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	path, err := cachePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	entry := cacheEntry{
		Version: cacheVersion, Source: cacheSource(cfg),
		FetchedAt: time.Now().Add(-cacheFreshFor - time.Second),
		Hosts:     []host{{Alias: "cached"}},
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	started := false
	original := startBackgroundRefresh
	startBackgroundRefresh = func() { started = true }
	t.Cleanup(func() { startBackgroundRefresh = original })
	_, _, hosts, err := loadHosts(&memoryKeyring{})
	if err != nil {
		t.Fatal(err)
	}
	if !started || len(hosts) != 1 || hosts[0].Alias != "cached" {
		t.Fatalf("started = %v, hosts = %#v", started, hosts)
	}
}

func TestShowOrdersByState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"value":[
			{"extensions":{"host_name":"a","host_alias":"Alpha","description":"OK","state":0,"plugin_output":"up"}},
			{"extensions":{"host_name":"a","host_alias":"Alpha","description":"Bad","state":2,"plugin_output":"down"}}
		]}`)
	}))
	defer server.Close()
	setConfig(t, server.URL)
	secrets := &memoryKeyring{secret: "secret", exists: true}

	var stdout bytes.Buffer
	if err := executeShow("Alpha", &stdout, secrets); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "Bad") || !strings.Contains(lines[1], "OK") {
		t.Fatalf("output = %q", stdout.String())
	}
}

func TestCobraCommandTree(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if err := run([]string{"fzf", "--help"}, strings.NewReader(""), &stdout, &stderr, &memoryKeyring{}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Browse hosts and service details interactively", "tui", "--crit", "--my"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("help output does not contain %q:\n%s", expected, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "__preview") || strings.Contains(stdout.String(), "__open") {
		t.Errorf("help exposed internal commands:\n%s", stdout.String())
	}

	stdout.Reset()
	if err := run([]string{"show", "one", "two"}, strings.NewReader(""), &stdout, &stderr, &memoryKeyring{}); err == nil {
		t.Fatal("show accepted too many arguments")
	}
}

func TestTUIFiltersAndSearch(t *testing.T) {
	model := tuiModel{
		cfg: config{Hosts: []string{"Alpha"}},
		allHosts: []host{
			{Name: "a", Alias: "Alpha", Services: []service{{Title: "CPU", State: 2}, {Title: "Disk", State: 1}}},
			{Name: "b", Alias: "Beta", Services: []service{{Title: "Ping", State: 0}}},
		},
	}
	model.rebuildHosts()
	if len(model.hosts) != 2 {
		t.Fatalf("hosts = %#v", model.hosts)
	}
	model.critOnly = true
	model.rebuildHosts()
	if len(model.hosts) != 1 || model.hosts[0].Alias != "Alpha" || len(model.hosts[0].Services) != 1 {
		t.Fatalf("critical hosts = %#v", model.hosts)
	}
	model.critOnly, model.myOnly = false, true
	model.rebuildHosts()
	if len(model.hosts) != 1 || model.hosts[0].Alias != "Alpha" {
		t.Fatalf("my hosts = %#v", model.hosts)
	}
	model.myOnly, model.query = false, "bet"
	model.rebuildHosts()
	if len(model.hosts) != 1 || model.hosts[0].Alias != "Beta" {
		t.Fatalf("searched hosts = %#v", model.hosts)
	}
}

func TestTUIRefreshPreservesSelection(t *testing.T) {
	model := tuiModel{
		allHosts: []host{{Name: "a", Alias: "Alpha", Services: []service{{State: 0}}}, {Name: "b", Alias: "Beta", Services: []service{{State: 1}}}},
		selected: 1, refreshing: true,
	}
	model.rebuildHosts()
	updated, _ := model.Update(tuiRefreshMsg{
		hosts:     []host{{Name: "b", Alias: "Beta renamed", Services: []service{{State: 2}}}, {Name: "c", Alias: "Charlie", Services: []service{{State: 0}}}},
		fetchedAt: time.Now(),
	})
	got := updated.(tuiModel)
	if got.refreshing || len(got.hosts) != 2 || got.hosts[got.selected].Name != "b" {
		t.Fatalf("model after refresh = %#v", got)
	}
}

func TestTUIViewContainsStatusAndControls(t *testing.T) {
	model := tuiModel{
		client: &client{}, width: 90, height: 20, fetchedAt: time.Now(),
		allHosts: []host{{Name: "a", Alias: "Alpha", Services: []service{{Title: "CPU", Description: "hot", State: 2}}}},
	}
	model.rebuildHosts()
	view := model.View()
	for _, expected := range []string{"Checkmk services", "Alpha", "CPU", "updated", shortcutKey, "search", "refresh"} {
		if !strings.Contains(view, expected) {
			t.Errorf("view does not contain %q:\n%s", expected, view)
		}
	}
}

func TestRenderShortcutsFitsWholeBadges(t *testing.T) {
	got := renderShortcuts(22, shortcut{"q", "quit"}, shortcut{"r", "refresh"}, shortcut{"x", "extra"})
	if !strings.Contains(got, "q") || !strings.Contains(got, "r") || strings.Contains(got, "overview") {
		t.Fatalf("shortcuts = %q", got)
	}
	if strings.Count(got, shortcutKey) != 2 || strings.Count(got, colorReset) != 4 {
		t.Fatalf("shortcuts contain incomplete styles: %q", got)
	}
}

func TestTUIOverviewGridColorsHostsByWorstState(t *testing.T) {
	model := tuiModel{
		width: 100, height: 24, overview: true, fetchedAt: time.Now(),
		allHosts: []host{
			{Name: "ok", Alias: "Healthy", Services: []service{{State: 0}}},
			{Name: "warn", Alias: "Degraded", Services: []service{{State: 0}, {State: 1}}},
			{Name: "crit", Alias: "Broken", Services: []service{{State: 1}, {State: 2}}},
			{Name: "unknown", Alias: "Missing", Services: []service{{State: 2}, {State: 3}}},
		},
	}
	model.rebuildHosts()
	view := model.View()
	for _, expected := range []string{"OVERVIEW", "Healthy", "Degraded", "Broken", "Missing", gridOK, gridWarning, gridCritical, gridUnknown} {
		if !strings.Contains(view, expected) {
			t.Errorf("overview does not contain %q:\n%s", expected, view)
		}
	}
	if got := worstState(model.allHosts[3].Services); got != 3 {
		t.Fatalf("worst state = %d, want 3", got)
	}
}

func TestTUIOverviewCriticalFilterKeepsFullHostState(t *testing.T) {
	model := tuiModel{
		overview: true,
		critOnly: true,
		allHosts: []host{
			{Name: "critical", Alias: "Critical", Services: []service{{State: 2}, {State: 1}}},
			{Name: "warning", Alias: "Warning", Services: []service{{State: 1}}},
		},
	}
	model.rebuildHosts()
	if len(model.hosts) != 1 || model.hosts[0].Alias != "Critical" {
		t.Fatalf("critical overview hosts = %#v", model.hosts)
	}
	critical, warning := stateCounts(model.hosts[0].Services)
	if critical != 1 || warning != 1 {
		t.Fatalf("service counts = %d critical, %d warning", critical, warning)
	}
}

func TestTUITabSwitchesViews(t *testing.T) {
	model := tuiModel{allHosts: []host{{Name: "a", Alias: "Alpha", Services: []service{{State: 0}}}}}
	model.rebuildHosts()
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyTab})
	overview := updated.(tuiModel)
	if !overview.overview {
		t.Fatal("Tab did not switch to the overview")
	}
	updated, _ = overview.Update(tea.KeyMsg{Type: tea.KeyTab})
	if updated.(tuiModel).overview {
		t.Fatal("second Tab did not switch back to services")
	}
}

func TestAuthCommands(t *testing.T) {
	setConfig(t, "https://checkmk.example")
	secrets := &memoryKeyring{}
	var stdout, stderr bytes.Buffer

	if err := run([]string{"auth", "login"}, strings.NewReader("top secret\n"), &stdout, &stderr, secrets); err != nil {
		t.Fatal(err)
	}
	if !secrets.exists || secrets.secret != "top secret" {
		t.Fatalf("stored secret = %q, exists = %v", secrets.secret, secrets.exists)
	}
	if secrets.service != keyringService || secrets.account != "user@https://checkmk.example/site" {
		t.Errorf("keyring identity = %q / %q", secrets.service, secrets.account)
	}
	if strings.Contains(stdout.String(), "top secret") || strings.Contains(stderr.String(), "top secret") {
		t.Fatal("secret was written to command output")
	}

	stdout.Reset()
	stderr.Reset()
	if err := run([]string{"auth", "status"}, strings.NewReader(""), &stdout, &stderr, secrets); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Credential is stored") {
		t.Errorf("status output = %q", stdout.String())
	}

	stdout.Reset()
	if err := run([]string{"auth", "logout"}, strings.NewReader(""), &stdout, &stderr, secrets); err != nil {
		t.Fatal(err)
	}
	if secrets.exists {
		t.Fatal("credential still exists after logout")
	}
}

type memoryKeyring struct {
	secret  string
	exists  bool
	service string
	account string
}

func (store *memoryKeyring) Get(service, account string) (string, error) {
	store.service, store.account = service, account
	if !store.exists {
		return "", errSecretNotFound
	}
	return store.secret, nil
}

func (store *memoryKeyring) Set(service, account, secret string) error {
	store.service, store.account, store.secret, store.exists = service, account, secret, true
	return nil
}

func (store *memoryKeyring) Delete(service, account string) error {
	store.service, store.account = service, account
	if !store.exists {
		return errSecretNotFound
	}
	store.secret, store.exists = "", false
	return nil
}

func setConfig(t *testing.T, baseURL string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	directory := filepath.Join(root, programName())
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	content := "url: " + baseURL + "\nsite: site\nusername: user\nhosts:\n  - Alpha\n"
	if err := os.WriteFile(filepath.Join(directory, "config.yml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

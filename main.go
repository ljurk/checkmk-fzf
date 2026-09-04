package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
)

const (
	colorReset   = "\x1b[0m"
	colorRed     = "\x1b[31m"
	colorGreen   = "\x1b[32m"
	colorYellow  = "\x1b[33m"
	colorMagenta = "\x1b[35m"
)

var stateNames = map[int]string{0: "ok", 1: "warning", 2: "critical", 3: "unknown"}

type service struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	State       int    `json:"state"`
}

type host struct {
	Name     string    `json:"name"`
	Alias    string    `json:"alias"`
	Services []service `json:"services"`
}

type client struct {
	baseURL  string
	site     string
	username string
	secret   string
	http     *http.Client
}

type apiResponse struct {
	Value []struct {
		Extensions struct {
			HostName     string `json:"host_name"`
			HostAlias    string `json:"host_alias"`
			Description  string `json:"description"`
			State        int    `json:"state"`
			PluginOutput string `json:"plugin_output"`
		} `json:"extensions"`
	} `json:"value"`
}

func (c *client) hostURL(hostName string) string {
	query := url.Values{"view_name": {"host"}, "host": {hostName}}
	return fmt.Sprintf("%s/%s/check_mk/view.py?%s", c.baseURL, c.site, query.Encode())
}

func (c *client) hosts(ctx context.Context, query any) ([]host, error) {
	queryJSON, err := json.Marshal(query)
	if err != nil {
		return nil, fmt.Errorf("encode query: %w", err)
	}

	endpoint := fmt.Sprintf("%s/%s/check_mk/api/1.0/domain-types/service/collections/all", c.baseURL, c.site)
	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("build request URL: %w", err)
	}
	params := requestURL.Query()
	params.Set("query", string(queryJSON))
	for _, column := range []string{"host_name", "host_alias", "description", "state", "plugin_output"} {
		params.Add("columns", column)
	}
	requestURL.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.username+" "+c.secret)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query Checkmk: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return nil, fmt.Errorf("Checkmk returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode Checkmk response: %w", err)
	}

	byAlias := make(map[string]*host)
	aliases := make([]string, 0)
	for _, item := range result.Value {
		ext := item.Extensions
		current, ok := byAlias[ext.HostAlias]
		if !ok {
			current = &host{Name: ext.HostName, Alias: ext.HostAlias}
			byAlias[ext.HostAlias] = current
			aliases = append(aliases, ext.HostAlias)
		}
		current.Services = append(current.Services, service{
			Title: ext.Description, Description: ext.PluginOutput, State: ext.State,
		})
	}

	hosts := make([]host, 0, len(aliases))
	for _, alias := range aliases {
		hosts = append(hosts, *byAlias[alias])
	}
	return hosts, nil
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, systemKeyring{}); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, secrets secretStore) error {
	root := newRootCommand(stdin, stdout, stderr, secrets)
	root.SetArgs(args)
	return root.Execute()
}

func newRootCommand(stdin io.Reader, stdout, stderr io.Writer, secrets secretStore) *cobra.Command {
	root := &cobra.Command{
		Use:           programName(),
		Short:         "Query service status from Checkmk",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return executeTUI(false, false, stdin, stdout, secrets)
		},
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetIn(stdin)

	var overviewMy bool
	overview := &cobra.Command{
		Use:   "overview",
		Short: "Show hosts with their CRIT and WARN service counts",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return executeOverview(overviewMy, stdout, secrets)
		},
	}
	overview.Flags().BoolVar(&overviewMy, "my", false, "only show aliases listed in config.yml")

	show := &cobra.Command{
		Use:   "show [HOST_ALIAS]",
		Short: "Show services for a host alias",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			hostAlias := "*"
			if len(args) == 1 {
				hostAlias = args[0]
			}
			return executeShow(hostAlias, stdout, secrets)
		},
	}

	var tuiCrit, tuiMy bool
	tuiCommand := &cobra.Command{
		Use:     "tui",
		Aliases: []string{"fzf"},
		Short:   "Browse hosts and service details interactively",
		Args:    cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return executeTUI(tuiCrit, tuiMy, stdin, stdout, secrets)
		},
	}
	tuiCommand.Flags().BoolVar(&tuiCrit, "crit", false, "start with only critical services")
	tuiCommand.Flags().BoolVar(&tuiMy, "my", false, "start with only aliases listed in config.yml")
	refresh := &cobra.Command{
		Use:    "__refresh",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return executeRefresh(secrets)
		},
	}

	root.AddCommand(overview, show, tuiCommand, newAuthCommand(stdin, stdout, stderr, secrets), refresh)
	return root
}

func executeOverview(my bool, stdout io.Writer, secrets secretStore) error {
	_, cfg, hosts, err := loadHosts(secrets)
	if err != nil {
		return err
	}

	allowed := map[string]bool(nil)
	if my {
		if len(cfg.Hosts) == 0 {
			return errors.New("config hosts does not contain any hosts")
		}
		allowed = make(map[string]bool, len(cfg.Hosts))
		for _, alias := range cfg.Hosts {
			allowed[alias] = true
		}
	}

	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Alias < hosts[j].Alias })

	color := writerIsTerminal(stdout)
	fmt.Fprintf(stdout, "%-30s %4s %4s\n", "HOST", "CRIT", "WARN")
	for _, current := range hosts {
		crit, warning := 0, 0
		for _, svc := range current.Services {
			if svc.State == 2 {
				crit++
			} else if svc.State == 1 {
				warning++
			}
		}
		if crit == 0 && warning == 0 || my && !allowed[current.Alias] {
			continue
		}
		line := fmt.Sprintf("%-30s %4d %4d", current.Alias, crit, warning)
		if color {
			if crit > 0 {
				line = colorize(line, colorRed)
			} else {
				line = colorize(line, colorYellow)
			}
		}
		fmt.Fprintln(stdout, line)
	}
	return nil
}

func executeShow(hostAlias string, stdout io.Writer, secrets secretStore) error {
	_, _, hosts, err := loadHosts(secrets)
	if err != nil {
		return err
	}

	type row struct {
		host host
		svc  service
	}
	rows := make([]row, 0)
	for _, current := range hosts {
		if hostAlias == "*" {
			services := current.Services[:0]
			for _, svc := range current.Services {
				if svc.State != 0 {
					services = append(services, svc)
				}
			}
			current.Services = services
		} else if current.Alias != hostAlias {
			continue
		}
		for _, svc := range current.Services {
			rows = append(rows, row{host: current, svc: svc})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].svc.State > rows[j].svc.State })
	color := writerIsTerminal(stdout)
	for _, item := range rows {
		line := fmt.Sprintf("%-45s %s", item.host.Alias, formatService(item.svc))
		if color {
			line = colorize(line, stateColor(item.svc.State))
		}
		fmt.Fprintln(stdout, line)
	}
	return nil
}

func formatService(svc service) string {
	description := truncateRunes(svc.Description, 60)
	return fmt.Sprintf("%-30s %-10s %s", svc.Title, stateNames[svc.State], description)
}

func truncateRunes(value string, maximum int) string {
	if utf8.RuneCountInString(value) <= maximum {
		return value
	}
	runes := []rune(value)
	return string(runes[:maximum])
}

func stateColor(state int) string {
	switch state {
	case 0:
		return colorGreen
	case 1:
		return colorYellow
	case 2:
		return colorRed
	case 3:
		return colorMagenta
	default:
		return ""
	}
}

func colorize(value, color string) string {
	if color == "" {
		return value
	}
	return color + value + colorReset
}

func writerIsTerminal(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func openBrowser(target string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command, args = "open", []string{target}
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		command, args = "xdg-open", []string{target}
	}
	cmd := exec.Command(command, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open %s: %w", target, err)
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release browser process: %w", err)
	}
	return nil
}

func programName() string {
	return "checkmk-fzf"
}

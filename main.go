package main

import (
	"context"
	"encoding/base64"
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
	colorReset  = "\x1b[0m"
	colorRed    = "\x1b[31m"
	colorGreen  = "\x1b[32m"
	colorYellow = "\x1b[33m"
)

var stateNames = map[int]string{0: "ok", 1: "warning", 2: "critical"}

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
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
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

	var fzfCrit, fzfMy bool
	fzfCommand := &cobra.Command{
		Use:   "fzf",
		Short: "Browse hosts and service details interactively",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return executeFZF(fzfCrit, fzfMy, stderr, secrets)
		},
	}
	fzfCommand.Flags().BoolVar(&fzfCrit, "crit", false, "only query critical services")
	fzfCommand.Flags().BoolVar(&fzfMy, "my", false, "only query aliases listed in config.yml")

	preview := &cobra.Command{
		Use:    "__preview ENCODED",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			decoded, err := base64.StdEncoding.DecodeString(args[0])
			if err != nil {
				return fmt.Errorf("decode preview: %w", err)
			}
			_, err = stdout.Write(decoded)
			return err
		},
	}
	open := &cobra.Command{
		Use:    "__open URL",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return openBrowser(args[0])
		},
	}
	refresh := &cobra.Command{
		Use:    "__refresh",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return executeRefresh(secrets)
		},
	}

	root.AddCommand(overview, show, fzfCommand, newAuthCommand(stdin, stdout, stderr, secrets), preview, open, refresh)
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

func executeFZF(crit, my bool, stderr io.Writer, secrets secretStore) error {
	fzfPath, err := exec.LookPath("fzf")
	if err != nil {
		return errors.New("fzf is not installed or not available in PATH")
	}

	c, cfg, hosts, err := loadHosts(secrets)
	if err != nil {
		return err
	}
	if my {
		if len(cfg.Hosts) == 0 {
			return errors.New("config hosts does not contain any hosts")
		}
	}

	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Alias < hosts[j].Alias })

	rows := make([]string, 0, len(hosts))
	minimumState := 0
	if crit {
		minimumState = 2
	}
	for _, current := range hosts {
		if my {
			allowed := false
			for _, alias := range cfg.Hosts {
				if current.Alias == alias {
					allowed = true
					break
				}
			}
			if !allowed {
				continue
			}
		}
		services := current.Services[:0]
		for _, svc := range current.Services {
			if svc.State >= minimumState {
				services = append(services, svc)
			}
		}
		current.Services = services
		if len(current.Services) == 0 {
			continue
		}
		sort.SliceStable(current.Services, func(i, j int) bool { return current.Services[i].State > current.Services[j].State })
		previewLines := make([]string, 0, len(current.Services))
		hostColor := ""
		for _, svc := range current.Services {
			previewLines = append(previewLines, colorize(formatService(svc), stateColor(svc.State)))
			if svc.State == 2 {
				hostColor = colorRed
			} else if svc.State == 1 && hostColor != colorRed {
				hostColor = colorYellow
			}
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(strings.Join(previewLines, "\n")))
		rows = append(rows, strings.Join([]string{current.Name, colorize(current.Alias, hostColor), encoded, c.hostURL(current.Name)}, "\t"))
	}

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find current executable: %w", err)
	}
	previewCommand := shellQuote(executable) + " __preview {3}"
	openCommand := shellQuote(executable) + " __open {4}"
	cmd := exec.Command(fzfPath,
		"--ansi", "--delimiter=\t", "--with-nth=2",
		"--preview", previewCommand,
		"--preview-window=right,70%",
		"--bind", "ctrl-o:execute-silent("+openCommand+")",
		"--header", "Ctrl-O: open in browser | Enter: open and exit",
	)
	cmd.Stdin = strings.NewReader(strings.Join(rows, "\n"))
	cmd.Stderr = stderr
	var selected strings.Builder
	cmd.Stdout = &selected
	err = cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && (exitErr.ExitCode() == 1 || exitErr.ExitCode() == 130) {
			return nil
		}
		return fmt.Errorf("fzf: %w", err)
	}

	fields := strings.Split(strings.TrimSuffix(selected.String(), "\n"), "\t")
	if len(fields) < 4 {
		return errors.New("fzf returned an invalid selection")
	}
	return openBrowser(fields[3])
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

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
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

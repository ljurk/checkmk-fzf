package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	styleBold    = "\x1b[1m"
	styleDim     = "\x1b[2m"
	styleInverse = "\x1b[7m"
	gridOK       = "\x1b[30;42m"
	gridWarning  = "\x1b[30;43m"
	gridCritical = "\x1b[97;41m"
	gridUnknown  = "\x1b[97;45m"
	shortcutKey  = "\x1b[1;30;47m"
)

type shortcut struct {
	key   string
	label string
}

type tuiRefreshMsg struct {
	hosts     []host
	fetchedAt time.Time
	err       error
}

type tuiOpenMsg struct{ err error }
type tuiTickMsg time.Time

type tuiModel struct {
	cfg        config
	client     *client
	secrets    secretStore
	allHosts   []host
	hosts      []host
	selected   int
	query      string
	searching  bool
	overview   bool
	critOnly   bool
	myOnly     bool
	width      int
	height     int
	fetchedAt  time.Time
	fromCache  bool
	refreshing bool
	statusErr  error
}

func executeTUI(crit, my bool, stdin io.Reader, stdout io.Writer, secrets secretStore) error {
	model, err := newTUIModel(crit, my, secrets)
	if err != nil {
		return err
	}
	program := tea.NewProgram(model, tea.WithInput(stdin), tea.WithOutput(stdout), tea.WithAltScreen())
	_, err = program.Run()
	if err != nil {
		return fmt.Errorf("run TUI: %w", err)
	}
	return nil
}

func newTUIModel(crit, my bool, secrets secretStore) (tuiModel, error) {
	cfg, err := loadConfig()
	if err != nil {
		return tuiModel{}, err
	}
	if my && len(cfg.Hosts) == 0 {
		return tuiModel{}, fmt.Errorf("config hosts does not contain any hosts")
	}

	model := tuiModel{cfg: cfg, secrets: secrets, critOnly: crit, myOnly: my, width: 100, height: 30}
	if entry, err := readCache(cfg); err == nil {
		model.client = clientForConfig(cfg, "")
		model.allHosts = entry.Hosts
		model.fetchedAt = entry.FetchedAt
		model.fromCache = true
		model.refreshing = time.Since(entry.FetchedAt) >= cacheFreshFor
	} else {
		client, err := authenticatedClient(cfg, secrets)
		if err != nil {
			return tuiModel{}, err
		}
		hosts, err := fetchAllHosts(context.Background(), client)
		if err != nil {
			return tuiModel{}, err
		}
		model.client = client
		model.allHosts = hosts
		model.fetchedAt = time.Now()
		_ = writeCache(cfg, hosts)
	}
	model.rebuildHosts()
	return model, nil
}

func (m tuiModel) Init() tea.Cmd {
	commands := []tea.Cmd{tuiTick()}
	if m.refreshing {
		commands = append(commands, m.refreshCmd())
	}
	return tea.Batch(commands...)
}

func (m tuiModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tuiTickMsg:
		return m, tuiTick()
	case tuiRefreshMsg:
		m.refreshing = false
		if msg.err != nil {
			m.statusErr = msg.err
			return m, nil
		}
		selectedName := m.selectedHostName()
		m.allHosts, m.fetchedAt, m.fromCache, m.statusErr = msg.hosts, msg.fetchedAt, false, nil
		m.rebuildHosts()
		m.selectHost(selectedName)
	case tuiOpenMsg:
		m.statusErr = msg.err
	case tea.KeyMsg:
		if m.searching {
			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit
			case "esc", "enter":
				m.searching = false
			case "backspace", "ctrl+h":
				if m.query != "" {
					_, size := utf8.DecodeLastRuneInString(m.query)
					m.query = m.query[:len(m.query)-size]
					m.rebuildHosts()
				}
			case "ctrl+u":
				m.query = ""
				m.rebuildHosts()
			default:
				if len(msg.Runes) > 0 && !msg.Alt {
					m.query += string(msg.Runes)
					m.rebuildHosts()
				}
			}
			return m, nil
		}

		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "/":
			m.searching = true
		case "esc":
			if m.query != "" {
				m.query = ""
				m.rebuildHosts()
			}
		case "up", "k":
			m.moveSelection(-m.verticalStep())
		case "down", "j":
			m.moveSelection(m.verticalStep())
		case "left", "h":
			if m.overview {
				m.moveSelection(-1)
			}
		case "right", "l":
			if m.overview {
				m.moveSelection(1)
			}
		case "home", "g":
			m.selected = 0
		case "end", "G":
			if len(m.hosts) > 0 {
				m.selected = len(m.hosts) - 1
			}
		case "pgup", "ctrl+u":
			m.selected -= max(1, m.listHeight())
			if m.selected < 0 {
				m.selected = 0
			}
		case "pgdown", "ctrl+d":
			m.selected += max(1, m.listHeight())
			if m.selected >= len(m.hosts) {
				m.selected = max(0, len(m.hosts)-1)
			}
		case "c":
			m.critOnly = !m.critOnly
			m.rebuildHosts()
		case "tab":
			selectedName := m.selectedHostName()
			m.overview = !m.overview
			m.rebuildHosts()
			m.selectHost(selectedName)
		case "m":
			if len(m.cfg.Hosts) == 0 {
				m.statusErr = fmt.Errorf("config hosts does not contain any hosts")
			} else {
				m.myOnly = !m.myOnly
				m.statusErr = nil
				m.rebuildHosts()
			}
		case "r":
			if !m.refreshing {
				m.refreshing, m.statusErr = true, nil
				return m, m.refreshCmd()
			}
		case "enter":
			if m.overview && len(m.hosts) > 0 {
				m.overview = false
				m.rebuildHosts()
				return m, nil
			}
			if len(m.hosts) > 0 {
				return m, openHostCmd(m.client.hostURL(m.hosts[m.selected].Name))
			}
		case "o":
			if len(m.hosts) > 0 {
				return m, openHostCmd(m.client.hostURL(m.hosts[m.selected].Name))
			}
		}
	}
	return m, nil
}

func (m tuiModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	leftWidth := max(18, min(40, m.width/3))
	rightWidth := max(1, m.width-leftWidth-3)
	listHeight := m.listHeight()

	title := styleBold + "Checkmk services" + colorReset
	filters := fmt.Sprintf("hosts %d/%d", len(m.hosts), len(m.allHosts))
	if m.critOnly {
		filters += "  " + colorRed + "CRIT only" + colorReset
	}
	if m.myOnly {
		filters += "  my hosts"
	}
	search := "Filter: " + m.query
	if m.searching {
		search += "█"
	} else {
		search += styleDim + "  (press / to edit)" + colorReset
	}
	if m.overview {
		body := m.overviewGrid()
		status := m.statusLine()
		help := renderShortcuts(m.width,
			shortcut{"↑↓←→", "move"}, shortcut{"q", "quit"}, shortcut{"enter", "details"},
			shortcut{"/", "search"}, shortcut{"c", "critical"}, shortcut{"tab", "services"}, shortcut{"r", "refresh"},
			shortcut{"o", "open"}, shortcut{"m", "my hosts"},
		)
		return title + "  " + styleDim + filters + "  overview" + colorReset + "\n" + search + "\n" + body + status + "\n" + help
	}

	left := []string{styleBold + padRight("HOSTS", leftWidth) + colorReset}
	right := []string{styleBold + padRight(m.serviceHeading(), rightWidth) + colorReset}
	start := m.listStart(listHeight)
	for row := 0; row < listHeight; row++ {
		index := start + row
		line := ""
		if index < len(m.hosts) {
			current := m.hosts[index]
			crit, warning := stateCounts(current.Services)
			line = fmt.Sprintf("%-*s %2dC %2dW", max(1, leftWidth-8), truncateRunes(current.Alias, max(1, leftWidth-8)), crit, warning)
			line = truncateRunes(line, leftWidth)
			if index == m.selected {
				line = styleInverse + padRight(line, leftWidth) + colorReset
			}
		}
		left = append(left, padRight(line, leftWidth))

		serviceLine := ""
		if len(m.hosts) > 0 && row < len(m.hosts[m.selected].Services) {
			svc := m.hosts[m.selected].Services[row]
			state := stateNames[svc.State]
			if state == "" {
				state = fmt.Sprintf("state-%d", svc.State)
			}
			serviceLine = fmt.Sprintf("%-10s %-24s %s", state, truncateRunes(svc.Title, 24), svc.Description)
			serviceLine = colorize(truncateRunes(serviceLine, rightWidth), stateColor(svc.State))
		}
		right = append(right, serviceLine)
	}

	var body strings.Builder
	for index := range left {
		body.WriteString(left[index])
		body.WriteString(" │ ")
		body.WriteString(right[index])
		body.WriteByte('\n')
	}
	status := m.statusLine()
	help := renderShortcuts(m.width,
		shortcut{"↑/↓", "move"}, shortcut{"q", "quit"}, shortcut{"/", "search"},
		shortcut{"tab", "overview"}, shortcut{"r", "refresh"}, shortcut{"enter/o", "open"},
		shortcut{"c", "critical"}, shortcut{"m", "my hosts"},
	)
	return title + "  " + styleDim + filters + colorReset + "\n" + search + "\n" + body.String() + status + "\n" + help
}

func (m *tuiModel) rebuildHosts() {
	allowed := make(map[string]bool, len(m.cfg.Hosts))
	for _, alias := range m.cfg.Hosts {
		allowed[alias] = true
	}
	needle := strings.ToLower(m.query)
	hosts := make([]host, 0, len(m.allHosts))
	for _, current := range m.allHosts {
		if m.myOnly && !allowed[current.Alias] || needle != "" && !strings.Contains(strings.ToLower(current.Alias), needle) {
			continue
		}
		if m.overview && m.critOnly && !hasServiceState(current.Services, 2) {
			continue
		}
		copyHost := host{Name: current.Name, Alias: current.Alias}
		for _, svc := range current.Services {
			if m.overview || !m.critOnly || svc.State == 2 {
				copyHost.Services = append(copyHost.Services, svc)
			}
		}
		if len(copyHost.Services) == 0 {
			continue
		}
		sort.SliceStable(copyHost.Services, func(i, j int) bool { return copyHost.Services[i].State > copyHost.Services[j].State })
		hosts = append(hosts, copyHost)
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Alias < hosts[j].Alias })
	m.hosts = hosts
	if m.selected >= len(m.hosts) {
		m.selected = max(0, len(m.hosts)-1)
	}
}

func (m tuiModel) refreshCmd() tea.Cmd {
	return func() tea.Msg {
		client, err := authenticatedClient(m.cfg, m.secrets)
		if err != nil {
			return tuiRefreshMsg{err: err}
		}
		hosts, err := fetchAllHosts(context.Background(), client)
		if err != nil {
			return tuiRefreshMsg{err: err}
		}
		fetchedAt := time.Now()
		if err := writeCache(m.cfg, hosts); err != nil {
			return tuiRefreshMsg{err: err}
		}
		return tuiRefreshMsg{hosts: hosts, fetchedAt: fetchedAt}
	}
}

func openHostCmd(target string) tea.Cmd {
	return func() tea.Msg { return tuiOpenMsg{err: openBrowser(target)} }
}

func tuiTick() tea.Cmd {
	return tea.Tick(time.Second, func(now time.Time) tea.Msg { return tuiTickMsg(now) })
}

func (m tuiModel) cacheStatus() string {
	if m.refreshing {
		return colorYellow + "● refreshing Checkmk data…" + colorReset
	}
	age := time.Since(m.fetchedAt)
	if age < 0 {
		age = 0
	}
	origin := "live"
	if m.fromCache {
		origin = "cache"
	}
	return fmt.Sprintf("%s●%s %s · updated %s ago", colorGreen, colorReset, origin, shortDuration(age))
}

func (m tuiModel) statusLine() string {
	status := m.cacheStatus()
	if m.statusErr != nil {
		status += "  " + colorRed + "Error: " + truncateRunes(m.statusErr.Error(), max(10, m.width/2)) + colorReset
	}
	return status
}

func (m tuiModel) overviewGrid() string {
	cardWidth := 24
	columns := max(1, (m.width+2)/(cardWidth+2))
	cardWidth = max(12, (m.width-(columns-1)*2)/columns)
	visibleRows := max(1, m.listHeight()/3)
	selectedRow := m.selected / columns
	startRow := max(0, selectedRow-visibleRows/2)
	totalRows := (len(m.hosts) + columns - 1) / columns
	if startRow+visibleRows > totalRows {
		startRow = max(0, totalRows-visibleRows)
	}

	var result strings.Builder
	result.WriteString(styleBold + "OVERVIEW" + colorReset + "  " + colorGreen + "OK" + colorReset + "  " + colorYellow + "WARN" + colorReset + "  " + colorRed + "CRIT" + colorReset + "  " + colorMagenta + "UNKNOWN" + colorReset + "\n")
	for row := startRow; row < min(totalRows, startRow+visibleRows); row++ {
		top, bottom := make([]string, 0, columns), make([]string, 0, columns)
		for column := 0; column < columns; column++ {
			index := row*columns + column
			if index >= len(m.hosts) {
				top = append(top, strings.Repeat(" ", cardWidth))
				bottom = append(bottom, strings.Repeat(" ", cardWidth))
				continue
			}
			current := m.hosts[index]
			critical, warning := stateCounts(current.Services)
			marker := "  "
			if index == m.selected {
				marker = "› "
			}
			style := gridStyle(worstState(current.Services))
			topText := marker + truncateRunes(current.Alias, max(1, cardWidth-2))
			bottomText := fmt.Sprintf("  %d critical · %d warning", critical, warning)
			top = append(top, style+padRight(topText, cardWidth)+colorReset)
			bottom = append(bottom, style+padRight(truncateRunes(bottomText, cardWidth), cardWidth)+colorReset)
		}
		result.WriteString(strings.Join(top, "  ") + "\n")
		result.WriteString(strings.Join(bottom, "  ") + "\n\n")
	}
	for row := totalRows; row < visibleRows; row++ {
		result.WriteString("\n\n\n")
	}
	return result.String()
}

func (m tuiModel) overviewColumns() int {
	return max(1, (m.width+2)/(24+2))
}

func (m tuiModel) verticalStep() int {
	if m.overview {
		return m.overviewColumns()
	}
	return 1
}

func (m *tuiModel) moveSelection(delta int) {
	if len(m.hosts) == 0 {
		return
	}
	m.selected = min(len(m.hosts)-1, max(0, m.selected+delta))
}

func worstState(services []service) int {
	worst := 0
	for _, svc := range services {
		if svc.State == 3 {
			return 3
		}
		if svc.State > worst {
			worst = svc.State
		}
	}
	return worst
}

func gridStyle(state int) string {
	switch state {
	case 1:
		return gridWarning
	case 2:
		return gridCritical
	case 3:
		return gridUnknown
	default:
		return gridOK
	}
}

func (m tuiModel) serviceHeading() string {
	if len(m.hosts) == 0 {
		return "SERVICES"
	}
	return "SERVICES · " + m.hosts[m.selected].Alias
}

func (m tuiModel) selectedHostName() string {
	if len(m.hosts) == 0 {
		return ""
	}
	return m.hosts[m.selected].Name
}

func (m *tuiModel) selectHost(name string) {
	for index := range m.hosts {
		if m.hosts[index].Name == name {
			m.selected = index
			return
		}
	}
}

func (m tuiModel) listHeight() int { return max(1, m.height-7) }

func (m tuiModel) listStart(height int) int {
	start := m.selected - height/2
	if start < 0 {
		return 0
	}
	if maximum := len(m.hosts) - height; start > maximum {
		return max(0, maximum)
	}
	return start
}

func stateCounts(services []service) (critical, warning int) {
	for _, svc := range services {
		if svc.State == 2 {
			critical++
		} else if svc.State == 1 {
			warning++
		}
	}
	return critical, warning
}

func hasServiceState(services []service, state int) bool {
	for _, svc := range services {
		if svc.State == state {
			return true
		}
	}
	return false
}

func padRight(value string, width int) string {
	missing := width - utf8.RuneCountInString(value)
	if missing <= 0 {
		return value
	}
	return value + strings.Repeat(" ", missing)
}

func shortDuration(duration time.Duration) string {
	if duration < time.Second {
		return "0s"
	}
	if duration < time.Minute {
		return fmt.Sprintf("%ds", int(duration.Seconds()))
	}
	if duration < time.Hour {
		return fmt.Sprintf("%dm", int(duration.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(duration.Hours()), int(duration.Minutes())%60)
}

func renderShortcuts(width int, shortcuts ...shortcut) string {
	var result strings.Builder
	used := 0
	for _, item := range shortcuts {
		separatorWidth := 0
		if used > 0 {
			separatorWidth = 2
		}
		itemWidth := utf8.RuneCountInString(item.key) + utf8.RuneCountInString(item.label) + 3
		if used+separatorWidth+itemWidth > width {
			break
		}
		if separatorWidth > 0 {
			result.WriteString("  ")
		}
		result.WriteString(shortcutKey)
		result.WriteString(" ")
		result.WriteString(item.key)
		result.WriteString(" ")
		result.WriteString(colorReset)
		result.WriteString(" ")
		result.WriteString(styleDim)
		result.WriteString(item.label)
		result.WriteString(colorReset)
		used += separatorWidth + itemWidth
	}
	return result.String()
}

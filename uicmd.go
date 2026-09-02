package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lsegal/glorp/webui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// dashboardScanPorts is how many consecutive ports `glorp ui` probes. A watch
// process walks upward from its starting port whenever one is taken, so a
// short contiguous scan finds every instance started with the default port.
const dashboardScanPorts = 16

// dashboardProbeTimeout bounds each localhost probe. Anything listening on
// loopback answers well inside this, and an unrelated service that accepts the
// connection but never replies must not stall the scan.
const dashboardProbeTimeout = 750 * time.Millisecond

// dashboardInstance is one running glorp web UI found on localhost.
type dashboardInstance struct {
	Port     int
	Targets  []string
	Running  int
	Queued   int
	Complete int
	Failed   int
}

// URL is the address to open in a browser.
func (d dashboardInstance) URL() string {
	return fmt.Sprintf("http://localhost:%d", d.Port)
}

// Label describes an instance well enough to tell two of them apart in the
// picker: which targets it watches and how busy it is.
func (d dashboardInstance) Label() string {
	targets := "no targets"
	if len(d.Targets) > 0 {
		targets = strings.Join(d.Targets, ", ")
	}
	return fmt.Sprintf("port %d — %s (%d running, %d queued)", d.Port, targets, d.Running, d.Queued)
}

func uiFlagSet() *flag.FlagSet {
	flags := flag.NewFlagSet("ui", flag.ExitOnError)
	flags.Int("port", webui.DefaultPort, "first localhost port to scan for a dashboard")
	return flags
}

func runUICommand(args []string) int {
	flags := uiFlagSet()
	flags.Usage = func() {
		cmd, _ := lookupCommand("ui")
		fmt.Fprintln(os.Stderr, cmd.usage)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		flags.Usage()
		return 2
	}
	startPort := flagValue[int](flags, "port")
	if startPort < 1 || startPort > 65535 {
		fmt.Fprintln(os.Stderr, "port must be between 1 and 65535")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	records := readDashboardRecords(dashboardRecordsDir(os.Getenv))
	ports := dashboardCandidatePorts(records, startPort, dashboardScanPorts)
	found := discoverDashboards(ctx, http.DefaultClient, localhostDashboardURL, ports)
	pruneDashboardRecords(records, found)
	selected, err := selectDashboard(found, startPort, isTerminal(os.Stdout) && isTerminal(os.Stdin), os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if selected == nil {
		return 0
	}
	fmt.Fprintf(os.Stdout, "Opening %s\n", selected.URL())
	if err := openBrowser(runtime.GOOS, selected.URL()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// selectDashboard resolves the scan into the single dashboard to open. A lone
// instance opens directly, several prompt on an interactive terminal, and a
// non-interactive run takes the lowest port so scripts stay predictable. A nil
// dashboard with a nil error means the user dismissed the picker.
func selectDashboard(found []dashboardInstance, startPort int, interactive bool, out io.Writer) (*dashboardInstance, error) {
	switch {
	case len(found) == 0:
		return nil, fmt.Errorf("no running glorp dashboard found on ports %d-%d; start one with `glorp watch`", startPort, startPort+dashboardScanPorts-1)
	case len(found) == 1:
		return &found[0], nil
	case !interactive:
		fmt.Fprintf(out, "Found %d glorp dashboards; opening the one on port %d\n", len(found), found[0].Port)
		return &found[0], nil
	}
	return pickDashboard(found)
}

// localhostDashboardURL is the real address of a dashboard on a given port.
// It is injected into discoverDashboards so tests can point the scan at
// httptest servers on arbitrary ports.
func localhostDashboardURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

// pruneDashboardRecords deletes records whose port no longer answers as a glorp
// dashboard, so a crashed instance's note does not linger for every later run.
func pruneDashboardRecords(records []dashboardRecord, found []dashboardInstance) {
	live := make(map[int]bool, len(found))
	for _, instance := range found {
		live[instance.Port] = true
	}
	for _, record := range records {
		if !live[record.Port] {
			removeDashboardRecord(record)
		}
	}
}

// discoverDashboards probes the given ports for glorp dashboards, returning
// what it found ordered by port.
func discoverDashboards(ctx context.Context, client *http.Client, baseURL func(port int) string, ports []int) []dashboardInstance {
	var mu sync.Mutex
	var found []dashboardInstance
	var wait sync.WaitGroup
	for _, port := range ports {
		wait.Add(1)
		go func(port int) {
			defer wait.Done()
			instance, ok := probeDashboard(ctx, client, baseURL(port), port)
			if !ok {
				return
			}
			mu.Lock()
			found = append(found, instance)
			mu.Unlock()
		}(port)
	}
	wait.Wait()
	sort.Slice(found, func(i, j int) bool { return found[i].Port < found[j].Port })
	return found
}

// probeDashboard checks whether a glorp web UI is listening at base. The state
// endpoint doubles as the identity check: an unrelated local server may accept
// the connection, but only glorp answers /api/state with a snapshot object.
func probeDashboard(ctx context.Context, client *http.Client, base string, port int) (dashboardInstance, bool) {
	ctx, cancel := context.WithTimeout(ctx, dashboardProbeTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/state", nil)
	if err != nil {
		return dashboardInstance{}, false
	}
	response, err := client.Do(request)
	if err != nil {
		return dashboardInstance{}, false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return dashboardInstance{}, false
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return dashboardInstance{}, false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return dashboardInstance{}, false
	}
	if _, ok := raw["snapshot"]; !ok {
		return dashboardInstance{}, false
	}
	if _, ok := raw["logs"]; !ok {
		return dashboardInstance{}, false
	}
	var state webui.State
	if err := json.Unmarshal(body, &state); err != nil {
		return dashboardInstance{}, false
	}
	return dashboardInstance{
		Port:     port,
		Targets:  state.Snapshot.Targets,
		Running:  state.Snapshot.Running,
		Queued:   state.Snapshot.Queued,
		Complete: state.Snapshot.Completed,
		Failed:   state.Snapshot.Failed,
	}, true
}

// dashboardPicker is the interactive chooser shown when more than one glorp
// dashboard is running.
type dashboardPicker struct {
	dashboards []dashboardInstance
	cursor     int
	chosen     int
	quit       bool
}

func newDashboardPicker(dashboards []dashboardInstance) dashboardPicker {
	return dashboardPicker{dashboards: dashboards, chosen: -1}
}

func (p dashboardPicker) Init() tea.Cmd { return nil }

func (p dashboardPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return p, nil
	}
	switch key.String() {
	case "ctrl+c", "q", "esc":
		p.quit = true
		return p, tea.Quit
	case "up", "k":
		if p.cursor > 0 {
			p.cursor--
		}
	case "down", "j":
		if p.cursor < len(p.dashboards)-1 {
			p.cursor++
		}
	case "home", "g":
		p.cursor = 0
	case "end", "G":
		p.cursor = len(p.dashboards) - 1
	case "enter", " ":
		p.chosen = p.cursor
		return p, tea.Quit
	}
	return p, nil
}

var (
	pickerTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	pickerSelected = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	pickerHint     = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)

func (p dashboardPicker) View() string {
	var view strings.Builder
	view.WriteString(pickerTitle.Render("Select a glorp dashboard to open") + "\n\n")
	for index, instance := range p.dashboards {
		line := "  " + instance.Label()
		if index == p.cursor {
			line = pickerSelected.Render("> " + instance.Label())
		}
		view.WriteString(line + "\n")
	}
	view.WriteString("\n" + pickerHint.Render("↑/↓ move · enter open · q cancel") + "\n")
	return view.String()
}

// pickDashboard runs the interactive picker, returning nil when the user
// cancels without choosing an instance.
func pickDashboard(dashboards []dashboardInstance) (*dashboardInstance, error) {
	model, err := tea.NewProgram(newDashboardPicker(dashboards)).Run()
	if err != nil {
		return nil, fmt.Errorf("select dashboard: %w", err)
	}
	picker, ok := model.(dashboardPicker)
	if !ok || picker.quit || picker.chosen < 0 || picker.chosen >= len(dashboards) {
		return nil, nil
	}
	return &dashboards[picker.chosen], nil
}

// browserCommand builds the platform command that opens a URL in the user's
// default browser.
func browserCommand(goos, url string) *exec.Cmd {
	switch goos {
	case "darwin":
		return exec.Command("open", url)
	case "windows":
		// "start" is a cmd builtin, and its first quoted argument is the window
		// title, so pass an empty title before the URL.
		return exec.Command("cmd", "/c", "start", "", url)
	default:
		return exec.Command("xdg-open", url)
	}
}

func openBrowser(goos, url string) error {
	cmd := browserCommand(goos, url)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("open %s in a browser: %w", url, err)
	}
	return nil
}

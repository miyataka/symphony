package statusdashboard

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

const (
	defaultWidth = 100
	wideRowWidth = 56
)

type Options struct {
	Width int
	Color bool
}

type Snapshot struct {
	Running   []RunningEntry
	MaxAgents int
	Now       time.Time
}

type RunningEntry struct {
	Identifier string
	State      string
	AgentKind  string
	RetryCount int
	TurnCount  int
	StartedAt  time.Time
}

type Runner struct {
	Writer          io.Writer
	RefreshInterval time.Duration
	RenderInterval  time.Duration
	Options         Options
	Snapshot        func() Snapshot
}

func (r Runner) Run(ctx context.Context) error {
	if r.Writer == nil {
		return fmt.Errorf("status dashboard writer is required")
	}
	if r.Snapshot == nil {
		return fmt.Errorf("status dashboard snapshot function is required")
	}
	refreshInterval := r.RefreshInterval
	if refreshInterval <= 0 {
		refreshInterval = time.Second
	}
	if r.RenderInterval > refreshInterval {
		refreshInterval = r.RenderInterval
	}
	if err := RenderToTerminal(r.Writer, r.Snapshot(), r.Options); err != nil {
		return err
	}

	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := RenderToTerminal(r.Writer, r.Snapshot(), r.Options); err != nil {
				return err
			}
		}
	}
}

func Render(snapshot Snapshot, opts Options) string {
	width := opts.Width
	if width <= 0 {
		width = defaultWidth
	}
	if snapshot.Now.IsZero() {
		snapshot.Now = time.Now()
	}

	styles := newStyles(opts.Color)
	lines := []string{
		styles.title.Render("SYMPHONY STATUS"),
		styles.label.Render(fmt.Sprintf("Agents: %d/%d", len(snapshot.Running), snapshot.MaxAgents)),
		styles.section.Render("Running"),
	}

	if len(snapshot.Running) == 0 {
		lines = append(lines, styles.dim.Render("No active agents"))
	} else {
		lines = append(lines, tableHeader(width, styles), tableSeparator(width, styles))
		for _, entry := range sortedEntries(snapshot.Running) {
			lines = append(lines, renderRunningEntry(entry, snapshot.Now, width, styles))
		}
	}

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func RenderToTerminal(w io.Writer, snapshot Snapshot, opts Options) error {
	_, err := fmt.Fprint(w, "\x1b[H\x1b[2J", Render(snapshot, opts), "\n")
	return err
}

type styles struct {
	title   lipgloss.Style
	label   lipgloss.Style
	section lipgloss.Style
	header  lipgloss.Style
	row     lipgloss.Style
	dim     lipgloss.Style
}

func newStyles(color bool) styles {
	if !color {
		return styles{}
	}
	return styles{
		title:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("57")).Padding(0, 1),
		label:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42")),
		section: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39")),
		header:  lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
		row:     lipgloss.NewStyle().Foreground(lipgloss.Color("15")),
		dim:     lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
	}
}

func sortedEntries(entries []RunningEntry) []RunningEntry {
	sorted := append([]RunningEntry(nil), entries...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Identifier < sorted[j].Identifier
	})
	return sorted
}

func tableHeader(width int, styles styles) string {
	return styles.header.Render(runningRow("Issue", "State", "Agent", "Age", "Meta", width))
}

func tableSeparator(width int, styles styles) string {
	return styles.header.Render(strings.Repeat("-", width))
}

func renderRunningEntry(entry RunningEntry, now time.Time, width int, styles styles) string {
	age := formatAge(now.Sub(entry.StartedAt))
	meta := fmt.Sprintf("retry %d turns %d", entry.RetryCount, entry.TurnCount)
	return styles.row.Render(runningRow(entry.Identifier, entry.State, entry.AgentKind, age, meta, width))
}

func runningRow(issue, state, agent, age, meta string, width int) string {
	if width < wideRowWidth {
		return cell(strings.Join([]string{issue, state, agent, age, meta}, " "), width)
	}
	fixed := 8 + 11 + 9 + 7 + 15
	issueWidth := max(6, width-fixed)
	return strings.Join([]string{
		cell(issue, issueWidth),
		cell(state, 11),
		cell(agent, 9),
		cell(age, 7),
		cell(meta, 15),
	}, "  ")
}

func cell(value string, width int) string {
	value = strings.Join(strings.Fields(value), " ")
	value = fitPlain(value, width)
	return value + strings.Repeat(" ", max(0, width-len(value)))
}

func fitPlain(value string, width int) string {
	if width <= 0 || len(value) <= width {
		return value
	}
	if width <= 3 {
		return value[:width]
	}
	return value[:width-3] + "..."
}

func formatAge(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	seconds := int(duration.Round(time.Second).Seconds())
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	seconds = seconds % 60
	if minutes < 60 {
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	hours := minutes / 60
	minutes = minutes % 60
	return fmt.Sprintf("%dh %dm", hours, minutes)
}

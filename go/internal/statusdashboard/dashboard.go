package statusdashboard

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

const (
	defaultWidth          = 115
	runningIDWidth        = 8
	runningStageWidth     = 14
	runningPIDWidth       = 8
	runningAgeWidth       = 12
	runningHealthWidth    = 12
	runningTokensWidth    = 10
	runningSessionWidth   = 14
	runningEventMinWidth  = 12
	runningRowChromeWidth = 18
	wideRowWidth          = 72
)

type Options struct {
	Width int
	Color bool
}

type Snapshot struct {
	Running      []RunningEntry
	Retrying     []RetryEntry
	MaxAgents    int
	Now          time.Time
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	Throughput   float64
	RateLimits   *RateLimits
	ProjectURL   string
	DashboardURL string
	NextRefresh  time.Duration
}

type RunningEntry struct {
	Identifier       string
	State            string
	AgentKind        string
	RetryCount       int
	TurnCount        int
	StartedAt        time.Time
	SessionID        string
	ProcessID        string
	TotalTokens      int
	HealthStatus     string
	HealthIdle       time.Duration
	LastEvent        string
	LastEventMessage string
}

type RetryEntry struct {
	Identifier string
	Attempt    int
	DueIn      time.Duration
	Error      string
}

type RateLimits struct {
	LimitID   string
	Primary   RateLimitBucket
	Secondary RateLimitBucket
	Credits   Credits
}

type RateLimitBucket struct {
	Remaining *int
	Limit     *int
	ResetIn   time.Duration
}

type Credits struct {
	Unlimited  bool
	HasCredits *bool
	Balance    *float64
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
		styles.title.Render("╭─ SYMPHONY STATUS"),
		styles.label.Render(fmt.Sprintf("│ Agents: %d/%d", len(snapshot.Running), snapshot.MaxAgents)),
		styles.label.Render(fmt.Sprintf("│ Throughput: %s tps", formatCount(int(snapshot.Throughput)))),
		styles.label.Render(fmt.Sprintf("│ Runtime: %s", formatDuration(totalRuntime(snapshot), true))),
		styles.label.Render(fmt.Sprintf("│ Tokens: in %s | out %s | total %s", formatCount(snapshot.InputTokens), formatCount(snapshot.OutputTokens), formatCount(effectiveTotalTokens(snapshot)))),
		styles.label.Render("│ Rate Limits: " + formatRateLimits(snapshot.RateLimits)),
	}
	if strings.TrimSpace(snapshot.ProjectURL) != "" {
		lines = append(lines, styles.label.Render("│ Project: "+strings.TrimSpace(snapshot.ProjectURL)))
	} else {
		lines = append(lines, styles.label.Render("│ Project: n/a"))
	}
	if strings.TrimSpace(snapshot.DashboardURL) != "" {
		lines = append(lines, styles.label.Render("│ Dashboard: "+strings.TrimSpace(snapshot.DashboardURL)))
	}
	lines = append(lines, styles.label.Render("│ Next refresh: "+formatNextRefresh(snapshot.NextRefresh)))
	lines = append(lines, styles.section.Render("├─ Running"), "│")

	lines = append(lines, tableHeader(width, styles), tableSeparator(width, styles))
	for _, entry := range sortedEntries(snapshot.Running) {
		lines = append(lines, renderRunningEntry(entry, snapshot.Now, width, opts.Color, styles))
	}
	if len(snapshot.Running) == 0 {
		lines = append(lines, "│  "+styles.dim.Render("No active agents"))
	}
	lines = append(lines, "│", styles.section.Render("├─ Backoff queue"), "│")
	if len(snapshot.Retrying) == 0 {
		lines = append(lines, "│  "+styles.dim.Render("No queued retries"))
	} else {
		for _, entry := range sortedRetries(snapshot.Retrying) {
			lines = append(lines, renderRetryEntry(entry, styles))
		}
	}
	lines = append(lines, "╰─")

	return fitFrame(strings.Join(lines, "\n"), width, opts.Color)
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
		title:   lipgloss.NewStyle().Bold(true),
		label:   lipgloss.NewStyle().Bold(true),
		section: lipgloss.NewStyle().Bold(true),
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

func sortedRetries(entries []RetryEntry) []RetryEntry {
	sorted := append([]RetryEntry(nil), entries...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].DueIn == sorted[j].DueIn {
			return sorted[i].Identifier < sorted[j].Identifier
		}
		return sorted[i].DueIn < sorted[j].DueIn
	})
	return sorted
}

func tableHeader(width int, styles styles) string {
	return "│   " + styles.header.Render(runningRow("ID", "STAGE", "PID", "AGE / TURN", "HEALTH", "TOKENS", "SESSION", "EVENT", width))
}

func tableSeparator(width int, styles styles) string {
	return "│   " + styles.header.Render(strings.Repeat("-", runningTableWidth(width)))
}

func renderRunningEntry(entry RunningEntry, now time.Time, width int, color bool, styles styles) string {
	age := formatAgeAndTurns(now.Sub(entry.StartedAt), entry.TurnCount)
	message := entry.LastEventMessage
	if strings.TrimSpace(message) == "" {
		message = entry.LastEvent
	}
	if strings.TrimSpace(message) == "" && entry.AgentKind != "" {
		message = entry.AgentKind
	}
	if strings.TrimSpace(message) == "" && entry.RetryCount > 0 {
		message = fmt.Sprintf("retry %d", entry.RetryCount)
	}
	pid := entry.ProcessID
	if pid == "" {
		pid = "n/a"
	}
	dot := "*"
	if color {
		dot = "●"
	}
	row := runningRow(
		entry.Identifier,
		entry.State,
		pid,
		age,
		formatHealth(entry.HealthStatus, entry.HealthIdle),
		formatCount(entry.TotalTokens),
		compactSessionID(entry.SessionID),
		humanizeMessage(message),
		width,
	)
	return styles.row.Render("│ " + dot + " " + row)
}

func renderRetryEntry(entry RetryEntry, styles styles) string {
	identifier := entry.Identifier
	if strings.TrimSpace(identifier) == "" {
		identifier = "unknown"
	}
	return "│  " + styles.row.Render(strings.TrimSpace(strings.Join([]string{
		"↻",
		identifier,
		fmt.Sprintf("attempt=%d", entry.Attempt),
		"in",
		formatPreciseDuration(entry.DueIn),
		formatRetryError(entry.Error),
	}, " ")))
}

func runningRow(issue, state, pid, age, health, tokens, session, event string, width int) string {
	if width < wideRowWidth {
		return cell(strings.Join([]string{issue, state, pid, age, health, tokens, session, event}, " "), max(1, width-4), false)
	}
	return strings.Join([]string{
		cell(issue, runningIDWidth, false),
		cell(state, runningStageWidth, false),
		cell(pid, runningPIDWidth, false),
		cell(age, runningAgeWidth, false),
		cell(health, runningHealthWidth, false),
		cell(tokens, runningTokensWidth, true),
		cell(session, runningSessionWidth, false),
		cell(event, runningEventWidth(width), false),
	}, " ")
}

func cell(value string, width int, alignRight bool) string {
	value = strings.Join(strings.Fields(value), " ")
	value = fitPlain(value, width)
	if alignRight {
		return strings.Repeat(" ", max(0, width-len(value))) + value
	}
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

func formatAgeAndTurns(duration time.Duration, turns int) string {
	age := formatDuration(duration, false)
	if turns > 0 {
		return fmt.Sprintf("%s / %d", age, turns)
	}
	return age
}

func formatDuration(duration time.Duration, zeroMinutes bool) string {
	if duration < 0 {
		duration = 0
	}
	seconds := int(duration.Round(time.Second).Seconds())
	if zeroMinutes && seconds < 60 {
		return fmt.Sprintf("0m %ds", seconds)
	}
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

func totalRuntime(snapshot Snapshot) time.Duration {
	var total time.Duration
	for _, entry := range snapshot.Running {
		if !entry.StartedAt.IsZero() {
			total += snapshot.Now.Sub(entry.StartedAt)
		}
	}
	if total < 0 {
		return 0
	}
	return total
}

func effectiveTotalTokens(snapshot Snapshot) int {
	if snapshot.TotalTokens != 0 {
		return snapshot.TotalTokens
	}
	return snapshot.InputTokens + snapshot.OutputTokens
}

func runningTableWidth(width int) int {
	if width < wideRowWidth {
		return max(1, width-4)
	}
	return runningIDWidth + runningStageWidth + runningPIDWidth + runningAgeWidth + runningHealthWidth + runningTokensWidth + runningSessionWidth + runningEventWidth(width) + 7
}

func runningEventWidth(width int) int {
	fixed := runningIDWidth + runningStageWidth + runningPIDWidth + runningAgeWidth + runningHealthWidth + runningTokensWidth + runningSessionWidth
	return max(runningEventMinWidth, width-fixed-runningRowChromeWidth)
}

func formatCount(value int) string {
	sign := ""
	if value < 0 {
		sign = "-"
		value = -value
	}
	raw := strconv.Itoa(value)
	parts := []string{}
	for len(raw) > 3 {
		parts = append([]string{raw[len(raw)-3:]}, parts...)
		raw = raw[:len(raw)-3]
	}
	parts = append([]string{raw}, parts...)
	return sign + strings.Join(parts, ",")
}

func formatHealth(status string, idle time.Duration) string {
	compact := compactHealthStatus(status)
	if compact == "" {
		return "Health n/a"
	}
	if idle <= 0 {
		return compact
	}
	return compact + " " + formatIdle(idle)
}

func compactHealthStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "":
		return ""
	case "active":
		return "Act"
	case "quiet":
		return "Q"
	case "suspect":
		return "Sus"
	case "stalled":
		return "Stl"
	default:
		trimmed := strings.TrimSpace(status)
		if len(trimmed) <= 3 {
			return strings.Title(trimmed)
		}
		return strings.Title(trimmed[:3])
	}
}

func formatIdle(duration time.Duration) string {
	if duration < time.Minute {
		return fmt.Sprintf("%ds", int(duration/time.Second))
	}
	if duration < time.Hour {
		return fmt.Sprintf("%dm", int(duration/time.Minute))
	}
	return fmt.Sprintf("%dh", int(duration/time.Hour))
}

func compactSessionID(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "n/a"
	}
	if len(sessionID) <= 10 {
		return sessionID
	}
	return sessionID[:4] + "..." + sessionID[len(sessionID)-6:]
}

func humanizeMessage(message string) string {
	message = sanitizeInline(message)
	replacements := map[string]string{
		"thread/tokenUsage/updated":       "thread token usage updated",
		"codex/event/task_started":        "task started",
		"codex/event/token_count":         "token count updated",
		"turn_completed":                  "turn completed",
		"codex/event/agent_message_delta": "agent message streaming",
	}
	if replacement, ok := replacements[message]; ok {
		return replacement
	}
	return message
}

func sanitizeInline(value string) string {
	value = strings.ReplaceAll(value, "\\r\\n", " ")
	value = strings.ReplaceAll(value, "\\r", " ")
	value = strings.ReplaceAll(value, "\\n", " ")
	value = strings.ReplaceAll(value, "\r\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.Join(strings.Fields(value), " ")
}

func formatRetryError(value string) string {
	value = sanitizeInline(value)
	if value == "" {
		return ""
	}
	return "error=" + fitPlain(value, 96)
}

func formatPreciseDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	millis := duration.Milliseconds()
	return fmt.Sprintf("%d.%03ds", millis/1000, millis%1000)
}

func formatNextRefresh(duration time.Duration) string {
	if duration <= 0 {
		return "n/a"
	}
	return fmt.Sprintf("%ds", int((duration+time.Second-1)/time.Second))
}

func formatRateLimits(rateLimits *RateLimits) string {
	if rateLimits == nil {
		return "unavailable"
	}
	limitID := strings.TrimSpace(rateLimits.LimitID)
	if limitID == "" {
		limitID = "unknown"
	}
	return strings.Join([]string{
		limitID,
		"primary " + formatRateLimitBucket(rateLimits.Primary),
		"secondary " + formatRateLimitBucket(rateLimits.Secondary),
		formatCredits(rateLimits.Credits),
	}, " | ")
}

func formatRateLimitBucket(bucket RateLimitBucket) string {
	base := "n/a"
	switch {
	case bucket.Remaining != nil && bucket.Limit != nil:
		base = formatCount(*bucket.Remaining) + "/" + formatCount(*bucket.Limit)
	case bucket.Remaining != nil:
		base = "remaining " + formatCount(*bucket.Remaining)
	case bucket.Limit != nil:
		base = "limit " + formatCount(*bucket.Limit)
	}
	if bucket.ResetIn > 0 {
		base += " reset " + formatNextRefresh(bucket.ResetIn)
	}
	return base
}

func formatCredits(credits Credits) string {
	switch {
	case credits.Unlimited:
		return "credits unlimited"
	case credits.HasCredits != nil && *credits.HasCredits && credits.Balance != nil:
		return fmt.Sprintf("credits %.2f", *credits.Balance)
	case credits.HasCredits != nil && *credits.HasCredits:
		return "credits available"
	case credits.HasCredits != nil:
		return "credits none"
	default:
		return "credits n/a"
	}
}

func fitFrame(frame string, width int, color bool) string {
	lines := strings.Split(frame, "\n")
	for i, line := range lines {
		if color {
			lines[i] = fitVisible(line, width)
		} else {
			lines[i] = fitPlain(line, width)
		}
	}
	return strings.Join(lines, "\n")
}

func fitVisible(value string, width int) string {
	if width <= 0 || lipgloss.Width(value) <= width {
		return value
	}
	value = ansiPattern.ReplaceAllString(value, "")
	if width <= 3 {
		return fitPlain(value, width)
	}
	return fitPlain(value, width-3) + "..."
}

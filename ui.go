package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const logCapacity = 200
const interfaceTitle = "glitchwd/VIctoryᵖˣ"

const (
	cyan    = lipgloss.Color("#00F5D4")
	blue    = lipgloss.Color("#00BBF9")
	purple  = lipgloss.Color("#9B5DE5")
	magenta = lipgloss.Color("#F15BB5")
	amber   = lipgloss.Color("#FBBF24")
	red     = lipgloss.Color("#FB7185")
	slate   = lipgloss.Color("#334155")
)

var (
	heading = lipgloss.NewStyle().Foreground(blue).Bold(true)
	muted   = lipgloss.NewStyle().Foreground(lipgloss.Color("#94A3B8"))
	warning = lipgloss.NewStyle().Foreground(amber)
	failure = lipgloss.NewStyle().Foreground(red)
)

type headerInfo struct {
	osName, osID, kernel, cpu, gpu, driver, vram string
}

type spinnerTick time.Time

const historySize = 64

type graphHistory struct {
	values       [historySize]float64
	next, length int
}

func (h *graphHistory) add(value float64) {
	h.values[h.next] = value
	h.next = (h.next + 1) % historySize
	h.length = min(h.length+1, historySize)
}

type actionResult struct {
	path string
	err  error
	copy bool
}

type model struct {
	ctx                  context.Context
	workers              *sync.WaitGroup
	cfg                  config
	gamescope, nvidiaSMI string

	width, height          int
	host                   hostMetrics
	info                   headerInfo
	fpsHistory, gpuHistory graphHistory
	startedAt, endedAt     time.Time
	spinner                int
	now                    time.Time
	session                session
	cancelRun              context.CancelFunc

	logs           []logLine
	filter, scroll int
	notice         string
	sharing        bool

	portTab, displayTab, fetching bool
	newsIndex, newsScroll         int
	newsExpanded                  bool
	feed                          portFeed
	feedErr                       error
	upstream                      upstreamResult
	upstreamErr                   error
	checkingUpstream              bool
}

func newModel(ctx context.Context, workers *sync.WaitGroup, cfg config) *model {
	gamescope, _ := exec.LookPath("gamescope")
	nvidiaSMI, _ := exec.LookPath("nvidia-smi")
	return &model{ctx: ctx, workers: workers, cfg: cfg, gamescope: gamescope, nvidiaSMI: nvidiaSMI, logs: make([]logLine, 0, logCapacity)}
}

func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.SetWindowTitle(interfaceTitle), sampleHost(m.ctx, m.nvidiaSMI, 0), readHeader(m.ctx, m.nvidiaSMI)}
	if m.cfg.checkUpdates {
		cmds = append(cmds, m.checkUpstream(), nextUpstreamCheck())
	}
	return tea.Batch(cmds...)
}

func (m *model) checkUpstream() tea.Cmd {
	if m.checkingUpstream {
		return nil
	}
	m.checkingUpstream = true
	return refreshUpstream(m.ctx, m.cfg.dir)
}

func (m *model) start(op operation) tea.Cmd {
	if m.cancelRun != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancelRun = cancel
	m.displayTab = false
	m.session = session{op: op, state: starting, seriesS: m.cfg.seriesS, gamescope: m.cfg.gamescope, display: m.cfg.display}
	m.logs, m.scroll, m.notice = m.logs[:0], 0, ""
	m.fpsHistory = graphHistory{}
	m.startedAt, m.endedAt, m.spinner = time.Time{}, time.Time{}, 0
	events := make(chan tea.Msg, 128)
	m.workers.Add(1)
	cfg, appCtx := m.cfg, m.ctx
	go func() {
		defer m.workers.Done()
		defer close(events)
		result := launch(ctx, cfg, op, events)
		select {
		case events <- result:
		case <-appCtx.Done():
		}
	}()
	return m.nextEvent(events)
}

type runEvent struct {
	events <-chan tea.Msg
	msg    tea.Msg
}

func (m *model) nextEvent(events <-chan tea.Msg) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		select {
		case msg, ok := <-events:
			if ok {
				return runEvent{events, msg}
			}
		case <-ctx.Done():
		}
		return nil
	}
}

func (m *model) handleRun(event runEvent) tea.Cmd {
	switch msg := event.msg.(type) {
	case processStarted:
		m.startedAt = time.Now()
		if m.session.state == starting {
			m.session.state = running
			return tea.Batch(m.nextEvent(event.events), spin(m.ctx, m.startedAt))
		}
	case processLine:
		m.now = time.Now()
		line := m.session.stats.record(string(msg), m.now)
		if fps := m.session.stats.fps; fps.at.Equal(m.now) {
			m.fpsHistory.add(fps.value)
		}
		if len(m.logs) == logCapacity {
			copy(m.logs, m.logs[1:])
			m.logs = m.logs[:logCapacity-1]
		}
		m.logs = append(m.logs, line)
		if m.scroll > 0 && m.showLine(line) {
			m.scroll = min(m.scroll+1, logCapacity-1)
		}
	case processFinished:
		m.endedAt = time.Now()
		m.cancelRun()
		m.cancelRun = nil
		switch {
		case msg.err != nil:
			m.session.state = failed
			m.notice = strings.ReplaceAll(msg.err.Error(), "\n", " · ")
		case msg.stopped || m.session.state == stopping:
			m.session.state = stopped
			m.notice = "Session stopped."
		default:
			m.session.state = finished
			m.notice = "Session finished."
		}
		if msg.dropped > 0 {
			m.notice += fmt.Sprintf(" %d log lines skipped or shortened.", msg.dropped)
		}
		switch {
		case msg.logErr != nil:
			m.notice += " Local log incomplete: " + msg.logErr.Error()
		case msg.logName != "":
			m.notice += " Local log: " + filepath.Base(msg.logName)
		}
		return nil
	}
	return m.nextEvent(event.events)
}

func (m *model) share(clipboard bool) tea.Cmd {
	if m.sharing {
		return nil
	}
	m.sharing = true
	m.notice = "Preparing session summary…"
	ctx, summary := m.ctx, m.session.summary()
	return func() tea.Msg {
		if clipboard {
			return actionResult{copy: true, err: copySummary(ctx, summary)}
		}
		path, err := writeReport(summary)
		return actionResult{path: path, err: err}
	}
}

func (m *model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case hostMetrics:
		m.host, m.now = msg, time.Now()
		if value, err := strconv.ParseFloat(msg.gpuUtil, 64); err == nil {
			m.gpuHistory.add(value)
		} else {
			m.gpuHistory = graphHistory{}
		}
		return m, sampleHost(m.ctx, m.nvidiaSMI, time.Second)
	case headerInfo:
		m.info = msg
	case spinnerTick:
		if m.session.state == running && m.startedAt.Equal(time.Time(msg)) {
			m.spinner++
			m.now = time.Now()
			return m, spin(m.ctx, m.startedAt)
		}
	case runEvent:
		return m, m.handleRun(msg)
	case actionResult:
		m.sharing = false
		switch {
		case msg.err != nil:
			m.notice = msg.err.Error()
		case msg.copy:
			m.notice = "Session summary copied."
		default:
			m.notice = "Report: " + filepath.Base(msg.path)
		}
	case portResult:
		m.fetching, m.feedErr = false, msg.err
		if msg.err == nil {
			m.feed = msg.feed
			m.newsIndex, m.newsScroll = 0, 0
		}
	case upstreamTick:
		return m, tea.Batch(m.checkUpstream(), nextUpstreamCheck())
	case upstreamResult:
		m.checkingUpstream, m.upstreamErr = false, msg.err
		if msg.err == nil {
			m.upstream = msg
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "tab":
			m.cycleTab(1)
		case "shift+tab":
			m.cycleTab(-1)
		case "left", "h":
			if m.portTab && m.width >= 76 {
				m.newsIndex = max(0, m.newsIndex-1)
			} else {
				m.cycleTab(-1)
			}
		case "right":
			if m.portTab && m.width >= 76 {
				m.newsIndex = min(max(0, len(m.feed.News)-1), m.newsIndex+1)
			} else {
				m.cycleTab(1)
			}
		case "home":
			if m.portTab {
				m.newsIndex, m.newsScroll = 0, 0
			}
		case "end":
			if m.portTab {
				m.newsIndex = max(0, len(m.feed.News)-1)
			}
		case "d":
			m.displayTab = !m.displayTab
			m.portTab = false
		case "p", "i", "o", "a", "l", "enter":
			if m.displayTab {
				m.changeDisplay(msg.String())
			} else if m.portTab && msg.String() == "enter" {
				m.newsExpanded = !m.newsExpanded
			}
		case "s":
			if m.cancelRun == nil {
				return m, m.start(play)
			}
			m.session.state = stopping
			m.cancelRun()
		case "t":
			return m, m.start(translate)
		case "c":
			if m.cancelRun == nil {
				m.cfg.seriesS = !m.cfg.seriesS
			}
		case "g":
			if m.cancelRun == nil {
				switch {
				case m.cfg.gamescope:
					m.cfg.gamescope = false
					m.notice = ""
				case m.gamescope == "":
					m.notice = "Gamescope is unavailable."
				default:
					m.cfg.gamescope = true
					m.notice = ""
				}
			} else if m.displayTab {
				m.notice = "Stop the session with S to switch the display backend."
			}
		case "f":
			m.filter, m.scroll = (m.filter+1)%3, 0
		case "[", "up":
			if m.portTab {
				m.newsIndex = max(0, m.newsIndex-1)
			} else {
				m.scroll = min(m.scroll+4, max(0, len(m.visibleLogs())-1))
			}
		case "]", "down":
			if m.portTab {
				m.newsIndex = min(max(0, len(m.feed.News)-1), m.newsIndex+1)
			} else {
				m.scroll = max(0, m.scroll-4)
			}
		case "r", "y":
			return m, m.share(msg.String() == "y")
		case "u":
			update := m.checkUpstream()
			if m.portTab && !m.fetching {
				m.fetching = true
				return m, tea.Batch(update, refreshPortWatch(m.ctx))
			}
			return m, update
		}
	}
	return m, nil
}

func (m *model) cycleTab(direction int) {
	current := 0
	if m.displayTab {
		current = 1
	} else if m.portTab {
		current = 2
	}
	next := (current + direction + 3) % 3
	m.displayTab, m.portTab = next == 1, next == 2
	m.newsScroll = 0
}

func (m *model) changeDisplay(key string) {
	switch {
	case m.cancelRun != nil:
		m.notice = "Stop the session with S to change display settings."
	case !m.cfg.gamescope:
		m.notice = "Enable Gamescope with G to change display settings."
	default:
		m.cfg.display.change(key)
		m.notice = ""
	}
}

func (m *model) showLine(line logLine) bool {
	return m.filter == 0 || m.filter == 1 && line.severity != "" || m.filter == 2 && line.metric
}

func (m *model) visibleLogs() []logLine {
	if m.filter == 0 {
		return m.logs
	}
	visible := make([]logLine, 0, len(m.logs))
	for _, line := range m.logs {
		if m.showLine(line) {
			visible = append(visible, line)
		}
	}
	return visible
}

func clip(line string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(line, width, "…")
}

func (m *model) metric(v reading) string {
	if v.at.IsZero() {
		return "-"
	}
	value := fmt.Sprintf("%.1f", v.value)
	if m.session.state == running && m.now.Sub(v.at) > 12*time.Second {
		value += " (stale)"
	}
	return value
}

func (m *model) portWatch() []string {
	rows := []string{heading.Render("PortWatch — third-party headlines")}
	switch {
	case m.fetching:
		rows = append(rows, "Fetching public headlines…")
	case m.feedErr != nil:
		rows = append(rows, "Refresh failed: "+terminalText(m.feedErr.Error()))
	case m.feed.News == nil && m.feed.Updated.IsZero():
		rows = append(rows, "Press U to fetch from raw.githubusercontent.com.")
	}
	if !m.feed.Updated.IsZero() {
		rows = append(rows, "Feed updated "+m.feed.Updated.UTC().Format("2006-01-02 15:04 UTC"))
	}
	for i, n := range m.feed.News {
		prefix := "  "
		if i == m.newsIndex {
			prefix = "› "
		}
		rows = append(rows, prefix+strings.TrimSpace(n.Date+"  "+n.Title))
	}
	if m.newsExpanded && m.newsIndex < len(m.feed.News) {
		selected := m.feed.News[m.newsIndex]
		rows = append(rows, "", "Selected headline", strings.TrimSpace(selected.Date+"  "+selected.Title))
	}
	if m.feed.News != nil && len(m.feed.News) == 0 {
		rows = append(rows, "No headlines.")
	}
	return rows
}

func (m *model) upstreamStatus() string {
	switch {
	case m.checkingUpstream:
		return "Checking GitHub…"
	case m.upstreamErr != nil:
		return warning.Render("Check failed · " + m.upstreamErr.Error())
	case m.upstream.sha == "":
		return "U to check GitHub"
	case strings.HasPrefix(m.upstream.checkout, "Upstream commit"):
		return warning.Render(m.upstream.checkout)
	default:
		return m.upstream.checkout
	}
}

func (m *model) upstreamRows(width int) []string {
	u := m.upstream
	rows := []string{m.upstreamStatus()}
	if u.sha != "" {
		rows = append(rows, upstreamBranch+" · "+u.sha[:8])
		if u.releaseErr != nil {
			rows = append(rows, "Release check failed · "+u.releaseErr.Error())
		} else {
			rows = append(rows, "Latest release · "+u.release)
		}
		if u.subject != "" {
			rows = append(rows, strings.Split(ansi.Wrap(u.subject, width, ""), "\n")...)
		}
		if !u.commitAt.IsZero() {
			rows = append(rows, "Committed · "+u.commitAt.Local().Format("2006-01-02 15:04 MST"))
		}
		rows = append(rows, "Last successful check · "+u.checkedAt.Local().Format("15:04 MST"))
	}
	if m.cfg.checkUpdates {
		return append(rows, "Automatic · every 15 min")
	}
	return append(rows, "Manual · U to refresh")
}

func (m *model) displayMenu() []string {
	if !m.cfg.gamescope {
		backend := "G  Wine direct"
		if m.gamescope == "" {
			backend += " (Gamescope unavailable)"
		}
		return []string{backend}
	}
	d := m.cfg.display
	preset := "custom"
	if i := d.presetIndex(); i >= 0 {
		preset = displayPresets[i].name
	}
	scaling, mode, limit := "linear", "windowed", "unlimited"
	if d.fsr {
		scaling = "FSR"
	}
	if d.fullscreen {
		mode = "fullscreen"
	}
	if d.fpsLimit > 0 {
		limit = fmt.Sprintf("%d FPS", d.fpsLimit)
	}
	return []string{
		"G  Gamescope · P  " + preset,
		"I  Game: " + d.game.String(),
		"O  Output: " + d.window.String(),
		"A  Scaling: " + scaling,
		"L  Limit: " + limit,
		"Enter  Toggle mode (Start: " + mode + ")",
	}
}

func (m *model) compactView() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	if m.width < 32 || m.height < 8 {
		return clip("Resize terminal • Q Quit", m.width)
	}

	title := interfaceTitle
	if m.session.op != "" {
		title += " · " + string(m.session.op)
	}
	title += " · " + m.session.state.String()
	rows := []string{heading.Render(title)}
	if m.notice != "" {
		rows = append(rows, terminalText(m.notice))
	}
	footer := []string{"S Run/stop · T Translate · C Console · Q Quit", "Tab/D Display · F Filter · [/] Log · R/Y Report · U"}
	if m.width < 56 {
		footer = []string{"S Run/stop · T Translate · Q Quit", "Tab/D Display · C Console", "F Filter · [/] Scroll · R/Y Report"}
	}
	if m.displayTab {
		footer = []string{"Tab PortWatch · S Run/stop · D/Q"}
		if m.width >= 36 && m.height >= 9 && m.cfg.gamescope {
			footer = append(footer, "In game: Super+F toggles fullscreen")
		}
		rows = append(rows, m.displayMenu()...)
		rows = rows[:min(len(rows), m.height-len(footer))]
	} else if m.portTab {
		footer = []string{"U Refresh · Tab Monitor · Q Quit"}
		rows = append(rows, heading.Render("Belobug · GitHub"))
		rows = append(rows, m.upstreamRows(m.width)...)
		rows = append(rows, m.portWatch()...)
		rows = rows[:min(len(rows), m.height-len(footer))]
	} else {
		space := m.height - len(footer)
		metrics := []string{
			"Belobug · " + m.upstreamStatus(),
			m.target(),
			m.frameReadout(),
			m.ramReadout(),
			m.vramReadout(),
			m.gpuReadout(),
		}
		rows = append(rows, metrics[:min(len(metrics), max(0, space-len(rows)-2))]...)
		filter := [...]string{"All", "Warnings/errors", "Metrics"}
		visible := m.visibleLogs()
		rows = append(rows, heading.Render(fmt.Sprintf("Log · %s · %d lines", filter[m.filter], len(visible))))
		end := len(visible) - min(m.scroll, max(0, len(visible)-1))
		start := max(0, end-(space-len(rows)))
		for _, line := range visible[start:end] {
			text := clip(line.text, m.width)
			switch line.severity {
			case "warning":
				text = warning.Render(text)
			case "error":
				text = failure.Render(text)
			}
			rows = append(rows, text)
		}
	}
	for _, row := range footer {
		rows = append(rows, muted.Render(row))
	}
	for i, row := range rows {
		rows[i] = clip(row, m.width)
	}
	return strings.Join(rows, "\n")
}

func spin(ctx context.Context, started time.Time) tea.Cmd {
	return func() tea.Msg {
		timer := time.NewTimer(120 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			return spinnerTick(started)
		case <-ctx.Done():
			return nil
		}
	}
}

func readHeader(ctx context.Context, nvidiaSMI string) tea.Cmd {
	return func() tea.Msg {
		info := headerInfo{osName: "Linux"}
		data, err := os.ReadFile("/etc/os-release")
		if os.IsNotExist(err) {
			data, err = os.ReadFile("/usr/lib/os-release")
		}
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				key, value, ok := strings.Cut(line, "=")
				if !ok {
					continue
				}
				if decoded, err := strconv.Unquote(value); err == nil {
					value = decoded
				} else {
					value = strings.Trim(value, "'\"")
				}
				switch key {
				case "PRETTY_NAME":
					info.osName = terminalText(value)
				case "ID":
					info.osID = value
				}
			}
		}
		if release, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
			info.kernel = terminalText(strings.TrimSpace(string(release)))
		}
		if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				key, value, ok := strings.Cut(line, ":")
				if ok && strings.TrimSpace(key) == "model name" {
					info.cpu = terminalText(strings.TrimSpace(value))
					break
				}
			}
		}
		if nvidiaSMI != "" {
			probe := headerCommand(ctx, nvidiaSMI,
				"--query-gpu=name,driver_version,memory.total", "--format=csv,noheader,nounits")
			line, _, _ := strings.Cut(probe, "\n")
			fields := strings.SplitN(line, ",", 3)
			if len(fields) == 3 {
				info.gpu = strings.TrimSpace(fields[0])
				info.driver = strings.TrimSpace(fields[1])
				if mb, err := strconv.Atoi(strings.TrimSpace(fields[2])); err == nil {
					info.vram = fmt.Sprintf("%d MiB", mb)
				}
			}
		}
		return info
	}
}

func headerCommand(ctx context.Context, name string, args ...string) string {
	ctx, cancel := context.WithTimeout(ctx, 850*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 150 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := range lines {
		lines[i] = terminalText(lines[i])
	}
	return strings.Join(lines, "\n")
}

func ink(color lipgloss.Color, text string) string {
	return lipgloss.NewStyle().Foreground(color).Render(text)
}

func keycap(color lipgloss.Color, key string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("#0F172A")).Background(color).Bold(true).Render(" " + key + " ")
}

func fit(line string, width int) string {
	line = clip(line, width)
	return line + strings.Repeat(" ", max(0, width-ansi.StringWidth(line)))
}

func frame(width, height int, title string, color, lineColor lipgloss.Color, body []string) string {
	inner := width - 2
	top := "╭"
	if title != "" && inner >= 8 {
		title = clip(title, inner-6)
		top += "─┤ " + title + " ├"
	}
	top += strings.Repeat("─", max(0, width-1-ansi.StringWidth(top))) + "╮"
	rows := make([]string, 0, height+2)
	rows = append(rows, ink(color, top))
	for i := 0; i < height; i++ {
		var row string
		if i < len(body) {
			row = body[i]
		}
		edge := ink(lineColor, "│")
		rows = append(rows, edge+" "+fit(row, inner-2)+" "+edge)
	}
	rows = append(rows, ink(lineColor, "╰"+strings.Repeat("─", inner)+"╯"))
	return strings.Join(rows, "\n")
}

const emblemWidth = 18

func emblem(fedora bool) []string {
	pixels := [...]string{
		"   HAAAAAAAAA",
		"  HAAAAAAAAAABB",
		" HABB       BBBD",
		" HA            BD",
		" HACC   CC CCCCBD",
		" HACC   CC  CC BD",
		" AB TT TT   TT BD",
		" AB TT TT   TT BD",
		" AB  PPP    PP BD",
		" AB  PPP   PPPPBD",
		" AB            DD",
		"  BBBBDDDDDDDBDD",
		"   BDDDDDDDDDDD",
		"     DDDDDDDDD",
	}
	if fedora {
		pixels = [...]string{
			"      HAAAAA",
			"     HAAAAAAAB",
			"   HHAAAAAAAABBD",
			"  H            D",
			" HACC   CC CCCCBD",
			" HACC   CC  CC BD",
			" HA TT TT   TT BD",
			" AB TT TT   TT BD",
			" AB  PPP    PP BD",
			" AB  PPP   PPPPBD",
			"  A            DD",
			"   BBBBBDDBBBBDD",
			"     BBDDDDDDD",
			"      DDDDDD",
		}
	}
	palette := [...]lipgloss.Color{
		'H': "#87C9EE", 'A': "#51A2DA", 'B': "#326C9D", 'D': "#183C60",
		'C': cyan, 'T': "#00B9B5", 'P': magenta,
	}
	rows := make([]string, 0, len(pixels)/2)
	for y := 0; y < len(pixels); y += 2 {
		upper, lower := fit(pixels[y], emblemWidth), fit(pixels[y+1], emblemWidth)
		var row strings.Builder
		for x := 0; x < emblemWidth; x++ {
			top, bottom := palette[upper[x]], palette[lower[x]]
			switch {
			case top == "" && bottom == "":
				row.WriteByte(' ')
			case top == bottom:
				row.WriteString(ink(top, "█"))
			case bottom == "":
				row.WriteString(ink(top, "▀"))
			case top == "":
				row.WriteString(ink(bottom, "▄"))
			default:
				row.WriteString(lipgloss.NewStyle().Foreground(top).Background(bottom).Render("▀"))
			}
		}
		rows = append(rows, row.String())
	}
	return rows
}

func (m *model) target() string {
	seriesS, gamescope, display := m.cfg.seriesS, m.cfg.gamescope, m.cfg.display
	if m.session.op == play && m.cancelRun != nil {
		seriesS, gamescope, display = m.session.seriesS, m.session.gamescope, m.session.display
	}
	profile := "Series X"
	if seriesS {
		profile = "Series S"
	}
	if !gamescope {
		return profile + " · Wine direct"
	}
	preset := "custom"
	if i := display.presetIndex(); i >= 0 {
		preset = displayPresets[i].name
	}
	return profile + " · Gamescope " + preset
}

func (m *model) uptime() string {
	if m.startedAt.IsZero() {
		return "00:00"
	}
	end := m.endedAt
	if end.IsZero() {
		end = m.now
	}
	seconds := int(max(0, end.Sub(m.startedAt)) / time.Second)
	if seconds >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
	}
	return fmt.Sprintf("%02d:%02d", seconds/60, seconds%60)
}

func (m *model) statusPill() string {
	label, color := "○ "+strings.ToUpper(m.session.state.String()), lipgloss.Color("#94A3B8")
	switch m.session.state {
	case running:
		frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
		label, color = string(frames[m.spinner%len(frames)])+" RUNNING", cyan
	case failed:
		label, color = "✖ CRASHED", red
	case starting, stopping:
		color = amber
	case finished:
		label, color = "✓ FINISHED", blue
	}
	return keycap(color, label)
}

func meter(used, total float64, width int, color lipgloss.Color) (string, int) {
	if total <= 0 {
		return ink(slate, strings.Repeat("─", width)), 0
	}
	ratio := min(1, max(0, used/total))
	fill := int(ratio*float64(width) + 0.5)
	return ink(color, strings.Repeat("━", fill)) + ink(slate, strings.Repeat("─", width-fill)), int(ratio*100 + 0.5)
}

func (m *model) ramReadout() string {
	label := muted.Render("Host RAM ")
	if m.host.ramTotalMiB == 0 {
		return label + "—"
	}
	used, total := float64(m.host.ramUsedMiB), float64(m.host.ramTotalMiB)
	bar, pct := meter(used, total, 5, blue)
	return fmt.Sprintf("%s%s %d%% %.0f/%.0fG", label, bar, pct, used/1024, total/1024)
}

func (m *model) vramReadout() string {
	label := muted.Render("VRAM     ")
	used, usedErr := strconv.Atoi(m.host.vramUsedMiB)
	total, totalErr := strconv.Atoi(m.host.vramTotalMiB)
	if usedErr != nil || totalErr != nil || total <= 0 {
		return label + "—"
	}
	bar, _ := meter(float64(used), float64(total), 5, purple)
	return fmt.Sprintf("%s%s %d/%dM", label, bar, used, total)
}

func (m *model) gpuReadout() string {
	util, temp := "—", "—"
	if m.host.gpuUtil != "" {
		util = m.host.gpuUtil + "%"
	}
	if m.host.gpuTemp != "" {
		temp = m.host.gpuTemp + "°C"
	}
	return muted.Render("GPU      ") + util + muted.Render("   Temp ") + temp
}

func (m *model) frameReadout() string {
	s := m.session.stats
	return muted.Render("FPS ") + m.metric(s.fps) + muted.Render("  /  ") + m.metric(s.frameTime) + muted.Render(" ms")
}

func column(rows []string, width int) string {
	lines := make([]string, len(rows))
	for i, row := range rows {
		lines[i] = fit(row, width)
	}
	return strings.Join(lines, "\n")
}

func (m *model) headerBox() string {
	display := func(label, value string) string {
		return ink(blue, fit(label, 7)) + muted.Render(": ") + displayValue(value)
	}
	gpuDetails := []string{}
	for _, value := range []string{m.info.driver, m.info.vram} {
		if value != "" {
			gpuDetails = append(gpuDetails, value)
		}
	}
	info := []string{
		display("OS", m.info.osName),
		display("Kernel", m.info.kernel),
		display("CPU", m.info.cpu),
		display("GPU", m.info.gpu),
		"         " + strings.Join(gpuDetails, " · "),
		display("Target", m.target()),
		display("Uptime", m.uptime()),
	}
	if m.width < 128 || m.portTab || m.displayTab {
		info = append(info, display("Belobug", m.upstreamStatus()))
	}
	hot := []string{m.statusPill(), m.ramReadout(), m.vramReadout(), m.gpuReadout(), m.frameReadout()}
	const hotWidth, gap = 30, "  "
	width := m.width - 4
	left := column(emblem(m.info.osID == "fedora"), emblemWidth)
	centerWidth := width - emblemWidth - hotWidth - 2*len(gap)
	if m.width < 90 {
		left = ""
		centerWidth = width - hotWidth - len(gap)
	}
	columns := []string{column(info, centerWidth), gap, column(hot, hotWidth)}
	if left != "" {
		columns = append([]string{left, gap}, columns...)
	}
	body := strings.Split(lipgloss.JoinHorizontal(lipgloss.Center, columns...), "\n")
	return frame(m.width, len(body), interfaceTitle, blue, slate, body)
}

func (h graphHistory) curve(width int, scale float64, color lipgloss.Color) [2]string {
	var top, bottom strings.Builder
	levels := []rune(" ⣀⣤⣶⣿")
	count := min(h.length, width)
	for i := 0; i < count; i++ {
		value := h.values[(h.next-count+i+historySize)%historySize]
		scale = max(scale, value)
	}
	for i := 0; i < width-count; i++ {
		top.WriteRune(' ')
		bottom.WriteRune('·')
	}
	for i := 0; i < count; i++ {
		value := h.values[(h.next-count+i+historySize)%historySize]
		level := int(min(8, max(0, 8*value/scale)) + 0.5)
		top.WriteRune(levels[max(0, level-4)])
		bottom.WriteRune(levels[min(4, level)])
	}
	return [2]string{ink(color, top.String()), ink(color, bottom.String())}
}

func (m *model) midPanels(height int) string {
	upstreamW := 0
	available := m.width - 1
	if m.width >= 128 {
		upstreamW = m.width / 3
		available -= upstreamW + 1
	}
	leftW := available / 2
	rightW := available - leftW
	fps := m.fpsHistory.curve(leftW-4, 60, cyan)
	gpu := m.gpuHistory.curve(rightW-4, 100, purple)
	s := m.session.stats
	left := []string{
		m.frameReadout(),
		fps[0], fps[1],
		fmt.Sprintf("Draws/frame %s   Dropped %s", m.metric(s.draws), m.metric(s.dropped)),
		fmt.Sprintf("Private RAM %s MiB", m.metric(s.privateRAM)),
		fmt.Sprintf("VIctory VRAM %s / %s MiB", m.metric(s.vram), m.metric(s.vramBudget)),
		fmt.Sprintf("%s   %s", warning.Render(fmt.Sprintf("WARN %d", s.warnings)), failure.Render(fmt.Sprintf("ERR %d", s.errors))),
	}
	right := []string{m.gpuReadout(), gpu[0], gpu[1], m.ramReadout(), m.vramReadout()}
	panels := []string{
		frame(leftW, height, "VIctory · FPS history", blue, blue, left),
		" ",
		frame(rightW, height, "Host · GPU history", purple, purple, right),
	}
	if upstreamW > 0 {
		panels = append(panels, " ", frame(upstreamW, height, "Belobug · GitHub", magenta, slate, m.upstreamRows(upstreamW-4)))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, panels...)
}

func (m *model) tabBar() string {
	label := func(text string, active bool, accent lipgloss.Color) string {
		if active {
			return keycap(accent, text)
		}
		return muted.Render(" " + text + " ")
	}
	return label("Monitor", !m.portTab && !m.displayTab, blue) + " " +
		label("Display", m.displayTab, purple) + " " + label("PortWatch", m.portTab, magenta)
}

func (m *model) footer() string {
	key := func(k, label string) string { return keycap(blue, k) + muted.Render(" "+label) }
	join := func(items ...string) string { return strings.Join(items, "  ") }
	if m.displayTab {
		rows := []string{join(key("S", "Run/stop"), key("Tab", "PortWatch"), key("D", "Monitor"), key("G", "Gamescope"), key("Q", "Quit"))}
		if m.cfg.gamescope {
			rows = append(rows, muted.Render("In game: Super+F toggles fullscreen"))
		}
		return strings.Join(rows, "\n")
	}
	if m.portTab {
		return join(key("U", "Refresh"), key("Tab", "Monitor"), key("D", "Display"), key("Q", "Quit"))
	}
	return join(key("S", "Run/stop"), key("T", "Translate"), key("C", "Console"), key("Tab/D", "Display")) +
		"\n" + join(key("F", "Filter"), key("[/]", "Scroll"), key("R", "Report"), key("Y", "Copy"), key("U", "Updates"), key("Q", "Quit"))
}

func (m *model) bottomPanel(height int) string {
	var title string
	var body []string
	if m.notice != "" {
		body = append(body, terminalText(m.notice))
	}
	switch {
	case m.displayTab:
		title = "Display settings"
		menu := m.displayMenu()
		columnWidth := 0
		for i := 0; i < len(menu); i += 2 {
			columnWidth = max(columnWidth, ansi.StringWidth(menu[i]))
		}
		for i := 0; i < len(menu); i += 2 {
			row := fit(menu[i], columnWidth)
			if i+1 < len(menu) {
				row += "  " + menu[i+1]
			}
			body = append(body, row)
		}
	case m.portTab:
		leftW := (m.width - 1) / 2
		return lipgloss.JoinHorizontal(lipgloss.Top,
			frame(leftW, height, "Belobug · GitHub", magenta, slate, m.upstreamRows(leftW-4)),
			" ", frame(m.width-1-leftW, height, "PortWatch · public headlines", cyan, cyan, append(body, m.portWatch()[1:]...)))
	default:
		filter := [...]string{"All", "Warnings/errors", "Metrics"}
		visible := m.visibleLogs()
		title = fmt.Sprintf("Log stream · %s · %d lines", filter[m.filter], len(visible))
		end := len(visible) - min(m.scroll, max(0, len(visible)-1))
		start := max(0, end-max(0, height-len(body)))
		for _, line := range visible[start:end] {
			text := clip(line.text, m.width-4)
			switch line.severity {
			case "warning":
				text = warning.Render(text)
			case "error":
				text = failure.Render(text)
			}
			body = append(body, text)
		}
	}
	return frame(m.width, height, title, cyan, cyan, body)
}

func (m *model) View() string {
	if m.width < 76 || m.height < 24 {
		return m.compactView()
	}
	headerBox, tabBar, footer := m.headerBox(), m.tabBar(), m.footer()
	usedH := lipgloss.Height(headerBox) + lipgloss.Height(tabBar) + lipgloss.Height(footer)
	sections := []string{headerBox, tabBar}
	if !m.portTab && !m.displayTab {
		midH := min(7, m.height-usedH-9)
		midCols := m.midPanels(midH)
		sections = append(sections, midCols)
		usedH += lipgloss.Height(midCols)
	}
	botH := max(4, m.height-usedH-3)
	sections = append(sections, m.bottomPanel(botH), " ", footer)
	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

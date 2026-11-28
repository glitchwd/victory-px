package main

import (
	"context"
	"errors"
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
	forkMenu                      bool
	forkIndex                     int
	forkError                     string
	pickingPath                   bool
	updatingRelease               bool
}

func newModel(ctx context.Context, workers *sync.WaitGroup, cfg config) *model {
	gamescope, _ := exec.LookPath("gamescope")
	nvidiaSMI, _ := exec.LookPath("nvidia-smi")
	if cfg.forkName == "" && cfg.dir != "" && cfg.binary != "" {
		for _, build := range cfg.forks {
			if build.Dir == cfg.dir && build.Binary == cfg.binary {
				cfg.forkName = build.Name
				break
			}
		}
		if cfg.forkName == "" {
			cfg.forks = append(cfg.forks, forkBuild{Dir: cfg.dir, Binary: cfg.binary})
		}
	}
	return &model{ctx: ctx, workers: workers, cfg: cfg, gamescope: gamescope, nvidiaSMI: nvidiaSMI, logs: make([]logLine, 0, logCapacity),
		forkMenu: cfg.dir == "" || cfg.title == ""}
}

func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.SetWindowTitle(interfaceTitle), sampleHost(m.ctx, m.nvidiaSMI, 0), readHeader(m.ctx, m.nvidiaSMI)}
	if m.cfg.checkUpdates || m.cfg.forkName == nvidiaRTForkName {
		cmds = append(cmds, m.checkUpstream(), nextUpstreamCheck())
	}
	return tea.Batch(cmds...)
}

func (m *model) checkUpstream() tea.Cmd {
	if m.checkingUpstream {
		return nil
	}
	m.checkingUpstream = true
	return refreshBuildUpstream(m.ctx, forkBuild{Name: m.cfg.forkName, Dir: m.cfg.dir})
}

func (m *model) start(op operation) tea.Cmd {
	if m.cancelRun != nil {
		return nil
	}
	if m.cfg.binary == "" || m.cfg.title == "" {
		m.openForkMenu()
		m.forkError = "Choose a VIctory build and the game executable first."
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
			m.notice = privateLogText(terminalText(strings.ReplaceAll(msg.err.Error(), "\n", " · ")))
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
			m.notice += " Local log incomplete: " + privateLogText(terminalText(msg.logErr.Error()))
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

func (m *model) shareCorpus() tea.Cmd {
	if m.sharing {
		return nil
	}
	m.sharing = true
	m.notice = "Preparing Whiplash corpus report…"
	ram := ""
	if m.host.ramTotalMiB > 0 {
		ram = fmt.Sprintf("%.1f GiB", float64(m.host.ramTotalMiB)/1024)
	}
	runtimeLabel := m.cfg.runtimeLabel
	if runtimeLabel == "" && m.cfg.wine != "" {
		runtimeLabel = filepath.Base(m.cfg.wine)
	}
	var dur time.Duration
	if !m.startedAt.IsZero() {
		if !m.endedAt.IsZero() {
			dur = m.endedAt.Sub(m.startedAt)
		} else {
			dur = time.Since(m.startedAt)
		}
	}
	meta := corpusMeta{
		forkName:     m.cfg.forkName,
		forkRepo:     forkBuild{Name: m.cfg.forkName}.releaseRepository(),
		commit:       m.upstream.sha,
		osName:       m.info.osName,
		kernel:       m.info.kernel,
		cpu:          m.info.cpu,
		gpu:          m.info.gpu,
		driver:       m.info.driver,
		vram:         m.info.vram,
		ramGiB:       ram,
		runtimeLabel: runtimeLabel,
		duration:     dur,
		now:          time.Now(),
	}
	sess := m.session
	return func() tea.Msg {
		// Write first with placeholder attachment name, or use deterministic name.
		content := sess.corpusFullReport(meta, "corpus-<timestamp>.md")
		path, err := writeCorpusReport(content)
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
		m.checkingUpstream = false
		if msg.directory == "" || msg.directory == m.cfg.dir {
			m.upstreamErr = msg.err
			if msg.err == nil {
				m.upstream = msg
			}
		}
	case pickerResult:
		m.pickingPath = false
		m.applyPickerResult(msg)
		if !m.forkMenu && m.cfg.forkName == nvidiaRTForkName {
			return m, tea.Batch(m.checkUpstream(), nextUpstreamCheck())
		}
	case releaseUpdateResult:
		m.updatingRelease = false
		if msg.err != nil {
			m.forkError = msg.err.Error()
			return m, nil
		}
		if err := saveForkBuild(msg.build); err != nil {
			m.forkError = err.Error()
			return m, nil
		}
		for i, existing := range m.cfg.forks {
			if existing.Name == msg.build.Name {
				m.cfg.forks[i] = msg.build
				break
			}
		}
		if m.cfg.forkName == msg.build.Name {
			m.cfg.binary = msg.build.Binary
		}
		m.forkError = ""
		m.notice = fmt.Sprintf("Updated %s to %s", msg.build.label(), msg.tag)
		return m, m.checkUpstream()
	case tea.MouseMsg:
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
			return m, m.forkClick(msg.X, msg.Y)
		}
	case tea.KeyMsg:
		if m.forkMenu && msg.String() != "q" && msg.String() != "ctrl+c" {
			return m, m.forkMenuKey(msg.String())
		}
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
		case "b":
			m.openForkMenu()
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
		case "ctrl+x", "X":
			if m.cancelRun != nil {
				m.session.state = stopping
				m.notice = "PANIC: stopping session..."
				m.cancelRun()
			}
		case "t":
			if m.cfg.forkName == nvidiaRTForkName {
				m.notice = "Shader translation is not available in VIctory NVIDIA-RT."
				return m, nil
			}
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
		case "w":
			return m, m.shareCorpus()
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

func (m *model) openForkMenu() {
	m.forkMenu, m.forkError = true, ""
	for i, build := range m.cfg.forks {
		if build.Name == m.cfg.forkName {
			m.forkIndex = i
			break
		}
	}
}

func (m *model) forkMenuKey(key string) tea.Cmd {
	if m.pickingPath {
		return nil
	}
	switch key {
	case "esc", "b":
		m.forkMenu = false
	case "up":
		cols, _, _ := m.forkGridSize()
		m.forkIndex = max(0, m.forkIndex-cols)
	case "down":
		cols, _, _ := m.forkGridSize()
		m.forkIndex = min(m.forkIndex+cols, len(m.cfg.forks))
	case "left":
		m.forkIndex = max(0, m.forkIndex-1)
	case "right":
		m.forkIndex = min(m.forkIndex+1, len(m.cfg.forks))
	case "enter", "n", "e", "g", "f", "u", "x", "delete", "backspace":
		if m.cancelRun != nil {
			m.forkError = "Stop the session with S after closing this menu."
			return nil
		}
		switch key {
		case "enter":
			if m.forkIndex < len(m.cfg.forks) {
				m.applyFork(m.cfg.forks[m.forkIndex])
				if m.cfg.forkName == nvidiaRTForkName {
					return tea.Batch(m.checkUpstream(), nextUpstreamCheck())
				}
				return m.checkUpstream()
			} else {
				m.pickingPath, m.forkError = true, ""
				return chooseFork(m.ctx, forkBuild{}, m.cfg.dir, m.cfg.forks)
			}
		case "n":
			m.pickingPath, m.forkError = true, ""
			return chooseFork(m.ctx, forkBuild{}, m.cfg.dir, m.cfg.forks)
		case "e":
			if m.forkIndex >= len(m.cfg.forks) || m.cfg.forks[m.forkIndex].Name == "" {
				m.forkError = "Select a saved build to edit, or use N to add one."
				return nil
			}
			m.pickingPath, m.forkError = true, ""
			return chooseFork(m.ctx, m.cfg.forks[m.forkIndex], "", nil)
		case "g", "f":
			m.pickingPath, m.forkError = true, ""
			return chooseGame(m.ctx, m.cfg.title, key == "f")
		case "u":
			if m.cfg.title == "" {
				m.forkError = "Choose the game executable or folder first (G or F)."
				return nil
			}
			if m.forkIndex >= len(m.cfg.forks) {
				m.forkError = "Select a saved build to update, or add one first (N)."
				return nil
			}
			build := m.cfg.forks[m.forkIndex]
			if build.Dir == "" || build.Binary == "" {
				m.forkError = "Set the build folder and executable first (E)."
				return nil
			}
			if build.releaseRepository() == "" {
				m.forkError = "Selected build does not track GitHub releases."
				return nil
			}
			m.updatingRelease, m.forkError = true, ""
			return downloadLatestRelease(m.ctx, build)
		case "x", "delete", "backspace":
			if m.forkIndex >= len(m.cfg.forks) || m.cfg.forks[m.forkIndex].Name == "" {
				m.forkError = "Select a saved build to delete."
				return nil
			}
			build := m.cfg.forks[m.forkIndex]
			if err := deleteForkBuild(build.Name); err != nil {
				m.forkError = err.Error()
				return nil
			}
			m.cfg.forks = append(m.cfg.forks[:m.forkIndex], m.cfg.forks[m.forkIndex+1:]...)
			if m.forkIndex >= len(m.cfg.forks) && m.forkIndex > 0 {
				m.forkIndex--
			}
			if m.cfg.forkName == build.Name {
				m.cfg.forkName, m.cfg.dir, m.cfg.binary = "", "", ""
				m.upstream, m.upstreamErr = upstreamResult{}, nil
			}
			m.forkError = ""
			m.notice = "Removed fork: " + build.label()
		}
	}
	return nil
}

func (m *model) applyFork(build forkBuild) {
	previousDir := m.cfg.dir
	if err := m.cfg.selectFork(build); err != nil {
		m.forkError = err.Error()
		return
	}
	if previousDir != m.cfg.dir {
		m.upstream, m.upstreamErr = upstreamResult{}, nil
	}
	m.forkMenu, m.forkError = m.cfg.title == "", ""
	m.notice = "Selected fork: " + build.label()
}

func (m *model) applyPickerResult(result pickerResult) {
	if result.err != nil {
		if !errors.Is(result.err, errPickerCanceled) {
			m.forkError = result.err.Error()
		}
		return
	}
	if result.build.Name != "" {
		if err := saveForkBuild(result.build); err != nil {
			m.forkError = err.Error()
			return
		}
		for i, build := range m.cfg.forks {
			if build.Name == result.build.Name {
				m.cfg.forks[i] = result.build
				m.forkIndex = i
				m.applyFork(result.build)
				return
			}
		}
		m.cfg.forks = append(m.cfg.forks, result.build)
		m.forkIndex = len(m.cfg.forks) - 1
		m.applyFork(result.build)
		return
	}
	cfg := m.cfg
	if err := cfg.selectTitle(result.title); err != nil {
		m.forkError = err.Error()
		return
	}
	path, err := gameConfigPath()
	if err == nil {
		err = writeLocalConfig(path, cfg.title)
	}
	if err != nil {
		m.forkError = err.Error()
		return
	}
	m.cfg.title = cfg.title
	m.notice = "Game selected: " + filepath.Base(cfg.title)
}

var forkActions = [...]struct{ key, label string }{
	{"n", "N Add build"},
	{"e", "E Edit build"},
	{"g", "G Choose game executable"},
	{"f", "F Choose game folder"},
}

func (m *model) forkHeaderLines() []string {
	if m.width < 76 || m.height < 26 {
		return nil
	}
	logo := clip(asciiLogo(), m.width)
	headerBox := m.headerBox()
	return append(strings.Split(logo, "\n"), strings.Split(headerBox, "\n")...)
}

func (m *model) forkGridSize() (cols, rows, start int) {
	cols = max(1, min(4, m.width/22))
	headerH := len(m.forkHeaderLines())
	rows = max(1, (m.height-12-headerH)/5)
	perPage := cols * rows
	start = (m.forkIndex / perPage) * perPage
	return
}

func forkTierInfo(build forkBuild, isSelected, isEquipped bool) (color lipgloss.Color, tier, glyph string) {
	switch build.Name {
	case nvidiaRTForkName:
		color, tier, glyph = amber, "COVERT", "╾━╤デ╦︻"
	case nvidiaForkName:
		color, tier, glyph = magenta, "CLASSIFIED", "╾━╤デ═"
	case vilocityForkName:
		color, tier, glyph = purple, "RESTRICTED", "═╦══━"
	default:
		color, tier, glyph = blue, "MIL-SPEC", "╠══━"
	}
	if isSelected {
		return cyan, tier, glyph
	}
	if isEquipped {
		return color, tier, glyph
	}
	return color, tier, glyph
}

func (m *model) forkClick(x, y int) tea.Cmd {
	rows := strings.Split(ansi.Strip(m.View()), "\n")
	if y < 0 || y >= len(rows) || x < 0 || x >= ansi.StringWidth(rows[y]) {
		return nil
	}
	if !m.forkMenu {
		line := rows[y]
		fields := strings.Fields(line)
		if len(fields) >= 2 && (fields[0] == "B" && fields[1] == "Forks" || fields[0] == "S" && fields[1] == "Run/stop") {
			at := strings.LastIndex(line, "Forks")
			if at < 0 {
				return nil
			}
			before := strings.TrimRight(line[:at], " ")
			left, right := ansi.StringWidth(before)-1, ansi.StringWidth(line[:at])+len("Forks")
			if strings.HasSuffix(before, "B") && x >= left && x < right {
				m.openForkMenu()
			}
		}
		return nil
	}
	if m.pickingPath || m.width < 32 || m.height < 13 {
		return nil
	}
	cols, gridRows, start := m.forkGridSize()
	cellWidth := m.width / cols
	gridTop := 3 + len(m.forkHeaderLines())
	const cellHeight = 5
	gridBottom := gridTop + gridRows*cellHeight
	if y >= gridTop && y < gridBottom && x < cols*cellWidth {
		index := start + ((y-gridTop)/cellHeight)*cols + x/cellWidth
		if index > len(m.cfg.forks) {
			index = len(m.cfg.forks)
		}
		m.forkIndex = index
		return m.forkMenuKey("enter")
	}
	action := y - (gridBottom + 3)
	if action >= 0 && action < len(forkActions) {
		return m.forkMenuKey(forkActions[action].key)
	}
	return nil
}

func (m *model) forkMenuView() string {
	if m.width < 32 || m.height < 13 {
		return clip("Resize terminal • Esc Back", m.width)
	}
	cols, gridRows, start := m.forkGridSize()
	perPage := cols * gridRows
	totalSlots := len(m.cfg.forks) + 1
	totalPages := max(1, (totalSlots+perPage-1)/perPage)
	currentPage := (start / perPage) + 1

	headerLine := fmt.Sprintf("LOADOUT // VICTORY-PX INVENTORY   [PAGE %d/%d]", currentPage, totalPages)
	rows := m.forkHeaderLines()
	rows = append(rows, heading.Render(headerLine))
	game := "Choose an executable or folder"
	if m.cfg.title != "" {
		game = terminalText(filepath.Base(m.cfg.title))
	}
	rows = append(rows, "TARGET · "+game, muted.Render("Select a build slot to equip"))

	cellWidth := m.width / cols
	innerWidth := max(4, cellWidth-2)
	barWidth := max(2, innerWidth-2)

	for row := 0; row < gridRows; row++ {
		var l0, l1, l2, l3, l4 strings.Builder
		for col := 0; col < cols; col++ {
			index := start + row*cols + col
			selected := index == m.forkIndex

			borderColor := slate
			rarityColor := slate
			title := ""
			glyphStatus := ""
			bar := strings.Repeat("░", barWidth)

			switch {
			case index < len(m.cfg.forks):
				build := m.cfg.forks[index]
				equipped := build.Name == m.cfg.forkName
				rColor, _, glyph := forkTierInfo(build, selected, equipped)
				rarityColor = rColor
				borderColor = rColor
				if selected {
					borderColor = cyan
				}
				bar = strings.Repeat("▓", barWidth)
				prefix := "  "
				status := "READY"
				if equipped {
					prefix = "★ "
					status = "ACTIVE"
				}
				title = prefix + terminalText(build.label())
				glyphStatus = glyph + " " + status
			case index == len(m.cfg.forks):
				rarityColor = cyan
				borderColor = slate
				if selected {
					borderColor = cyan
				}
				bar = strings.Repeat("▒", barWidth)
				title = "+ Add build"
				glyphStatus = "[+] NEW CASE"
			}

			tl, tr, bl, br, hz, vt := "┌", "┐", "└", "┘", "─", "│"
			if selected {
				tl, tr, bl, br, hz, vt = "╔", "╗", "╚", "╝", "═", "║"
			}

			l0.WriteString(ink(borderColor, tl+strings.Repeat(hz, innerWidth)+tr))
			l1.WriteString(ink(borderColor, vt) + " " + ink(rarityColor, bar) + " " + ink(borderColor, vt))
			l2.WriteString(ink(borderColor, vt) + fit(clip(title, innerWidth-2), innerWidth) + ink(borderColor, vt))
			l3.WriteString(ink(borderColor, vt) + fit(clip(glyphStatus, innerWidth-2), innerWidth) + ink(borderColor, vt))
			l4.WriteString(ink(borderColor, bl+strings.Repeat(hz, innerWidth)+br))
		}
		rows = append(rows, l0.String(), l1.String(), l2.String(), l3.String(), l4.String())
	}

	// Inspect strip (2 lines + 1 spacer = 3 lines before actions)
	rows = append(rows, "")
	if m.forkIndex < len(m.cfg.forks) {
		build := m.cfg.forks[m.forkIndex]
		equipped := build.Name == m.cfg.forkName
		rColor, tier, _ := forkTierInfo(build, false, equipped)
		status := "READY"
		if equipped {
			status = "★ ACTIVE"
		}
		inspectTop := fmt.Sprintf("INSPECT: %s  |  Tier: %s  |  %s", terminalText(build.label()), ink(rColor, tier), status)
		inspectPath := muted.Render(fmt.Sprintf("DIR: %s  •  BIN: %s", terminalText(build.Dir), terminalText(filepath.Base(build.Binary))))
		rows = append(rows, inspectTop, inspectPath)
	} else {
		rows = append(rows, "INSPECT: + Add build  |  Register a compiled VIctory directory & victory.exe", muted.Render("Press Enter or N to open the system file picker"))
	}

	for _, action := range forkActions {
		rows = append(rows, ink(blue, action.label))
	}
	switch {
	case m.pickingPath:
		rows = append(rows, "Waiting for system picker…")
	case m.updatingRelease:
		rows = append(rows, "Downloading latest release…")
	case m.forkError != "":
		rows = append(rows, failure.Render(terminalText(m.forkError)))
	default:
		rows = append(rows, "")
	}
	rows = append(rows, muted.Render("Arrows Select · Enter Equip · E Edit · U Update · X Delete · Esc Back"))
	for i, row := range rows {
		rows[i] = clip(row, m.width)
	}
	return strings.Join(rows, "\n")
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
		if m.upstream.releaseOnly && m.upstream.release != "" {
			return "Latest published build · " + m.upstream.release
		}
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
	if u.repository != "" {
		rows = append(rows, u.repository)
	}
	if u.releaseOnly {
		if u.release != "" {
			rows = append(rows, "Latest published build · "+u.release)
		}
		if u.releaseErr != nil {
			rows = append(rows, "Release check failed · "+u.releaseErr.Error())
		}
		if !u.publishedAt.IsZero() {
			rows = append(rows, "Published · "+u.publishedAt.Local().Format("2006-01-02 15:04 MST"))
		}
		if u.assetCount > 0 {
			rows = append(rows, fmt.Sprintf("Release assets · %d", u.assetCount))
		}
		if !u.checkedAt.IsZero() {
			rows = append(rows, "Last successful check · "+u.checkedAt.Local().Format("15:04 MST"))
		}
		if m.cfg.checkUpdates || m.cfg.forkName == nvidiaRTForkName {
			return append(rows, "Automatic · every 15 min")
		}
		return append(rows, "Manual · U to refresh")
	}
	if u.sha != "" {
		branch := u.branch
		if branch == "" {
			branch = upstreamBranch
		}
		rows = append(rows, branch+" · "+u.sha[:8])
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
		backend := "G  Direct display"
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
	footer := []string{"S Run/stop · T Translate · C Console · Q Quit", "B Forks · Tab/D Display · F Filter · [/] Log · R/Y/W Report · U"}
	if m.width < 56 {
		footer = []string{"S Run/stop · T Translate · Q Quit", "B Forks · Tab/D Display · C Console", "F Filter · [/] Scroll · R/Y/W Report"}
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
		rows = append(rows, heading.Render("Selected fork · GitHub"))
		rows = append(rows, m.upstreamRows(m.width)...)
		rows = append(rows, m.portWatch()...)
		rows = rows[:min(len(rows), m.height-len(footer))]
	} else {
		space := m.height - len(footer)
		metrics := []string{
			"Fork · " + (forkBuild{Name: m.cfg.forkName}).label(),
			"Runtime · " + m.runtimeName(),
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
		return profile + " · Direct display"
	}
	preset := "custom"
	if i := display.presetIndex(); i >= 0 {
		preset = displayPresets[i].name
	}
	return profile + " · Gamescope " + preset
}

func (m *model) runtimeName() string {
	if m.cfg.runtimeLabel != "" {
		return m.cfg.runtimeLabel
	}
	if m.cfg.wine != "" {
		return filepath.Base(m.cfg.wine)
	}
	return "Not configured"
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
		display("Fork", (forkBuild{Name: m.cfg.forkName}).label()),
	}
	if m.width < 128 || m.portTab || m.displayTab {
		info = append(info, display("GitHub", m.upstreamStatus()))
	}
	info = append(info, display("Runtime", m.runtimeName()))
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
		panels = append(panels, " ", frame(upstreamW, height, "Selected fork · GitHub", magenta, slate, m.upstreamRows(upstreamW-4)))
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
	line1 := join(key("S", "Run/stop"), key("T", "Translate"), key("C", "Console"), key("Tab/D", "Display"), key("B", "Forks"))
	line2 := join(key("F", "Filter"), key("[/]", "Scroll"), key("R", "Report"), key("Y", "Copy"), key("W", "Corpus"), key("U", "Updates"), key("Q", "Quit"))
	return clip(line1, m.width) + "\n" + clip(line2, m.width)
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
			frame(leftW, height, "Selected fork · GitHub", magenta, slate, m.upstreamRows(leftW-4)),
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

func asciiLogo() string {
	viStyle := lipgloss.NewStyle().Foreground(cyan).Bold(true)
	ctoryStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#CBD5E1"))
	shadowStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#64748B"))

	l1 := viStyle.Render("██╗   ██╗██╗") + " " + ctoryStyle.Render(" ██████╗████████╗ ██████╗ ██████╗ ██╗   ██╗") + shadowStyle.Render(" ᵖˣ")
	l2 := viStyle.Render("██║   ██║██║") + " " + ctoryStyle.Render("██╔════╝╚══██╔══╝██╔═══██╗██╔══██╗╚██╗ ██╔╝")
	l3 := viStyle.Render("╚██████╔╝██║") + " " + shadowStyle.Render("╚██████╗   ██║   ╚██████╔╝██████╔╝ ╚████╔╝ ")
	return l1 + "\n" + l2 + "\n" + l3
}

func (m *model) View() string {
	if m.forkMenu {
		return m.forkMenuView()
	}
	if m.width < 76 || m.height < 24 {
		return m.compactView()
	}
	headerBox, tabBar, footer := m.headerBox(), m.tabBar(), m.footer()
	tabRow := clip(tabBar+"  "+keycap(amber, "B")+muted.Render(" Forks"), m.width)
	var sections []string
	usedH := lipgloss.Height(headerBox) + lipgloss.Height(tabRow) + lipgloss.Height(footer)
	if m.height >= 26 {
		logo := clip(asciiLogo(), m.width)
		sections = append(sections, logo)
		usedH += lipgloss.Height(logo)
	}
	sections = append(sections, headerBox, tabRow)
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

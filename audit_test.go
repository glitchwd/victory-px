package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestCLISetup(t *testing.T) {
	dir := t.TempDir()
	title := filepath.Join(dir, "title with spaces.exe")
	binary := filepath.Join(dir, "victory.exe")
	for _, name := range []string{title, binary} {
		if err := os.WriteFile(name, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("VICTORY_DIR", "/private/default")
	t.Setenv("VICTORY_EXE", "/private/default.exe")
	t.Setenv("WINEPREFIX", filepath.Join(dir, "new prefix"))
	var out bytes.Buffer
	if code := runCLI([]string{"--help"}, &out, &out); code != 0 || strings.Contains(out.String(), "/private/") {
		t.Fatalf("help code %d, output %q", code, out.String())
	}
	out.Reset()
	if code := runCLI([]string{"--version"}, &out, &out); code != 0 || !strings.HasPrefix(out.String(), "victory-px ") {
		t.Fatalf("version code %d, output %q", code, out.String())
	}

	args := []string{"--victory-dir", dir, "--exe", title, "--wine", "/bin/true"}
	cfg, _, err := parseFlags(args, io.Discard, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "build VIctory") {
		t.Fatalf("expected missing VIctory build, got %v", err)
	}
	cfg.binary = binary
	cfg, err = cfg.validate()
	if err != nil || cfg.dir != dir || cfg.title != title {
		t.Fatalf("validate without victory.toml or default binary: %+v, %v", cfg, err)
	}
	cmd, err := commandFor(cfg, play)
	winTitle, _ := winePath(title)
	want := []string{"/bin/true", binary, "run", winTitle, "--execute", "--console", "series-x"}
	if err != nil || strings.Join(cmd.Args, "\n") != strings.Join(want, "\n") {
		t.Fatalf("launch arguments: %v, %v", cmd, err)
	}
	if _, err := commandFor(cfg, translate); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing shader archive: %v", err)
	}
	archive := filepath.Join(dir, "shaders.rpf")
	if err := os.WriteFile(archive, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd, err = commandFor(cfg, translate)
	winArchive, _ := winePath(archive)
	if err != nil || cmd.Args[len(cmd.Args)-1] != winArchive {
		t.Fatalf("translation arguments: %v, %v", cmd, err)
	}

	devices := filepath.Join(os.Getenv("WINEPREFIX"), "dosdevices")
	if err := os.MkdirAll(devices, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := checkWineDrive(); err == nil {
		t.Fatal("accepted initialized prefix without Z:")
	}
	if err := os.Symlink("/", filepath.Join(devices, "z:")); err != nil {
		t.Fatal(err)
	}
	if err := checkWineDrive(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessGroupCleanup(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprintf("stop=%t", stop), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			events := make(chan tea.Msg, 8)
			done := make(chan processFinished, 1)
			script := `sleep 60 & printf '%s\n' "$!"`
			if stop {
				// The child reports its PID after ignoring TERM, before the test cancels.
				script = `sh -c 'trap "" TERM; printf "%s\n" "$$"; exec sleep 60' & wait`
			}
			go func() {
				done <- runProcess(ctx, exec.Command("sh", "-c", script), nil, events)
			}()
			var pid int
			select {
			case event := <-events:
				if _, ok := event.(processStarted); !ok {
					t.Fatalf("first event %T", event)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case event := <-events:
				line, ok := event.(processLine)
				if !ok {
					t.Fatalf("output event %T", event)
				}
				pid, _ = strconv.Atoi(string(line))
				if pid <= 1 {
					t.Fatalf("invalid child PID %q", line)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if stop {
				cancel()
			}
			select {
			case result := <-done:
				if result.err != nil || !result.started || result.stopped != stop {
					t.Fatalf("unexpected process result: %+v", result)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("process group was not drained")
			}
			stat := fmt.Sprintf("/proc/%d/stat", pid)
			deadline := time.Now().Add(time.Second)
			for {
				data, err := os.ReadFile(stat)
				if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				i := strings.LastIndex(string(data), ") ")
				// Reaping an orphan is the job of its reparented process, not this runner.
				if i >= 0 && strings.HasPrefix(string(data[i+2:]), "Z ") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("descendant %d survived session cleanup", pid)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func TestOutputDrainAndFailedStart(t *testing.T) {
	var raw bytes.Buffer
	input := strings.Repeat("x", 24*1024) + "\nnext line\n"
	events := make(chan tea.Msg, 2)
	result := readOutput(strings.NewReader(input), &raw, events)
	if result.err != nil || result.dropped != 1 || raw.String() != input {
		t.Fatalf("long line interrupted output: %+v", result)
	}
	if !strings.HasSuffix(string((<-events).(processLine)), "[line truncated]") || (<-events) != processLine("next line") {
		t.Fatal("truncation corrupted the next line")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg := config{dir: t.TempDir(), wine: "/missing-victory-px-test-wine", saveLog: true}
	result = launch(context.Background(), cfg, play, events)
	if result.err == nil || result.logErr != nil || result.started {
		t.Fatalf("bad start result: %+v", result)
	}
	files, err := filepath.Glob(filepath.Join(os.Getenv("XDG_STATE_HOME"), "victory-px", "logs", "*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("failed start left a log: %v, %v", files, err)
	}
}

func TestComputePipelineCrashWithSuccessfulWrapperExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	const script = `printf '%s\n' "$1" '[gamescope] [Info]  launch: Primary child shut down!'`
	const fault = "0218:err:vulkan:vkCreateComputePipelines Exception 0xc0000005 in Unix call."
	// Leave room only for processStarted so the result cannot depend on UI log delivery.
	events := make(chan tea.Msg, 1)
	result := runProcess(ctx, exec.Command("sh", "-c", script, "sh", fault), nil, events)
	if !errors.Is(result.err, errComputePipelineCrash) || result.stopped || result.dropped != 2 {
		t.Fatalf("unexpected process result: %+v", result)
	}
	var stats runStats
	if line := stats.record(fault, time.Now()); line.severity != "error" || stats.errors != 1 {
		t.Fatalf("fault missing from session error count: %+v", line)
	}
	result = runProcess(ctx, exec.Command("sh", "-c", script, "sh",
		"01c8:fixme:vulkan:win32u_vkCreateBuffer Unsupported handle types 0x80"), nil, make(chan tea.Msg, 1))
	if result.err != nil || result.stopped {
		t.Fatalf("unexpected process result with a Wine fixme: %+v", result)
	}
}

func TestLogMetricsAndPrivateSummary(t *testing.T) {
	now := time.Now()
	s := session{op: play, state: finished}
	s.stats.record("[1 2] 3.5 fps, 285.7 ms/frame (info), 150 draws/frame, dropped 2 without a host pipeline", now)
	s.stats.record("1 FPS   953.5 ms/frame | 1526 draws/frame", now)
	s.stats.record("INFO victory::memory: private 4096 MiB; driver: video 0 of 8152 MiB", now)
	s.stats.record("2026-01-01T00:00:00Z ERROR password=secret /private/alice Z:\\other\\dir hostname.example", now)
	if s.stats.fps.value != 1 || s.stats.frameTime.value != 953.5 || s.stats.draws.value != 1526 ||
		s.stats.dropped.value != 2 || s.stats.privateRAM.value != 4096 || s.stats.vram.at.IsZero() ||
		s.stats.vramBudget.value != 8152 || s.stats.errors != 1 {
		t.Fatalf("unexpected parsed counters: %+v", s.stats)
	}
	summary := s.summary()
	for _, secret := range []string{"password", "secret", "/private", "alice", "Z:", "hostname", "2026-01-01"} {
		if strings.Contains(summary, secret) {
			t.Fatalf("summary contains %q", secret)
		}
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	first, err := writeReport(summary)
	if err != nil {
		t.Fatal(err)
	}
	second, err := writeReport(summary)
	if err != nil || first == second {
		t.Fatalf("duplicate report path or write error: %v", err)
	}
	info, err := os.Stat(first)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("report mode: %v, %v", info, err)
	}
	data, err := os.ReadFile(first)
	if err != nil || string(data) != summary {
		t.Fatalf("report does not match selected summary: %v", err)
	}
}

func TestPortWatchReadOnly(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantErr    bool
	}{
		{"headlines", `{"updated":"2026-01-01T00:00:00Z","news":[{"title":"Nice\u001b[31m\n headline","date":"2026-01-01"}]}`, false},
		{"missing news", `{"somethingElse":[]}`, true},
		{"too large", strings.Repeat(" ", maxTrackerBytes+1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.RawQuery != "" || r.Header.Get("User-Agent") != "victory-px" {
					t.Error("unexpected request metadata")
				}
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			feed, err := readPortWatch(context.Background(), server.Client(), server.URL)
			if (err != nil) != tc.wantErr {
				t.Fatalf("readPortWatch: %v", err)
			}
			if !tc.wantErr && (len(feed.News) != 1 || feed.News[0].Title != "Nice headline") {
				t.Fatalf("unexpected news: %+v", feed)
			}
		})
	}
}

func TestTabNavigation(t *testing.T) {
	var m model
	bar := ansi.Strip(m.tabBar())
	if strings.Join(strings.Fields(bar), " ") != "Monitor Display PortWatch" {
		t.Fatalf("unexpected tab bar: %q", bar)
	}

	for _, step := range []struct {
		key           tea.KeyMsg
		display, port bool
	}{
		{key: tea.KeyMsg{Type: tea.KeyTab}, display: true},
		{key: tea.KeyMsg{Type: tea.KeyTab}, port: true},
		{key: tea.KeyMsg{Type: tea.KeyTab}},
		{key: tea.KeyMsg{Type: tea.KeyShiftTab}, port: true},
		{key: tea.KeyMsg{Type: tea.KeyShiftTab}, display: true},
		{key: tea.KeyMsg{Type: tea.KeyShiftTab}},
		{key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}}, display: true},
		{key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}}},
	} {
		m.Update(step.key)
		if m.displayTab != step.display || m.portTab != step.port {
			t.Fatalf("key %s selected Display=%t, PortWatch=%t, want Display=%t, PortWatch=%t",
				step.key, m.displayTab, m.portTab, step.display, step.port)
		}
	}
}

func TestTerminalSizes(t *testing.T) {
	m := model{session: session{op: play, state: running}, notice: strings.Repeat("例", 80)}
	m.upstream = upstreamResult{sha: strings.Repeat("a", 40), subject: strings.Repeat("Shader fix ", 16),
		checkout: "Upstream commit absent locally", release: "v1.2", checkedAt: time.Now()}
	for range [20]struct{}{} {
		m.logs = append(m.logs, logLine{text: strings.Repeat("😀 ", 150)})
		m.feed.News = append(m.feed.News, portNews{Title: strings.Repeat("é ", 90)})
	}
	for _, width := range []int{1, 16, 32, 44, 55, 56, 75, 76, 80, 82, 110, 127, 128, 180, 240} {
		for _, height := range []int{1, 8, 12, 18, 24, 40} {
			m.width, m.height = width, height
			for _, tab := range []bool{false, true} {
				m.portTab = tab
				view := m.View()
				if width >= 76 && height >= 24 && !strings.Contains(view, "Upstream commit absent locally") {
					t.Fatalf("missing update status at %dx%d, PortWatch=%t", width, height, tab)
				}
				if !utf8.ValidString(view) || strings.Count(view, "\n")+1 > height {
					t.Fatalf("invalid height or UTF-8 at %dx%d, PortWatch=%t", width, height, tab)
				}
				for _, row := range strings.Split(view, "\n") {
					if ansi.StringWidth(row) > width {
						t.Fatalf("overflow at %dx%d, PortWatch=%t: %q", width, height, tab, row)
					}
				}
			}
		}
	}
}

func TestHostMetricParsing(t *testing.T) {
	used, total := parseRAM("MemTotal: 65536 kB\nMemAvailable: 8192 kB\n")
	if used != 56 || total != 64 {
		t.Fatalf("RAM used/total: %d/%d", used, total)
	}
	if _, total := parseRAM("MemTotal: 65536 kB\n"); total != 0 {
		t.Fatal("missing counters produced a reading")
	}
	var m hostMetrics
	parseGPU("12, [N/A], 256, 4096\n80, 50, 512, 8192\n", &m)
	if m.gpuUtil != "12" || m.gpuTemp != "" || m.vramTotalMiB != "4096" {
		t.Fatalf("multi-GPU parsing: %+v", m)
	}
}

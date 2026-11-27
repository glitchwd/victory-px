package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSavedForkSelection(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("VICTORY_DIR", t.TempDir())
	var out bytes.Buffer
	for _, name := range []string{"fxceless", "belobug"} {
		dir := t.TempDir()
		binary := filepath.Join(dir, name+".exe")
		if err := os.WriteFile(binary, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if code := runCLI([]string{"--save-fork", name, "--victory-dir", dir, "--victory-bin", binary}, &out, &out); code != 0 {
			t.Fatalf("save code %d: %s", code, &out)
		}
	}
	out.Reset()
	if code := runCLI([]string{"--list-forks"}, &out, &out); code != 0 || out.String() != "belobug\nfxceless\n" {
		t.Fatalf("list code %d: %q", code, &out)
	}
	out.Reset()
	if code := runCLI([]string{"--fork", "missing"}, &out, &out); code != 2 || !strings.Contains(out.String(), "unknown fork") {
		t.Fatalf("unknown fork code %d: %q", code, &out)
	}
	builds, err := loadForkBuilds()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config{forks: builds, title: filepath.Join(t.TempDir(), "title.exe"), wine: "/bin/true", seriesS: true}
	if err := cfg.selectFork(builds[0]); err != nil {
		t.Fatal(err)
	}
	m := newModel(context.Background(), nil, cfg)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	if !strings.Contains(m.View(), "belobug") || !strings.Contains(m.View(), "ACTIVE") || !strings.Contains(m.View(), "fxceless") {
		t.Fatalf("expected both builds in picker: %s", m.View())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m.cancelRun = func() {}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.cfg.forkName != "belobug" || m.forkError == "" {
		t.Fatal("picker accepted a switch while the session was active")
	}
	m.cancelRun = nil
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.forkMenu || m.cfg.forkName != "fxceless" {
		t.Fatal("picker did not apply the second build")
	}
	cmd, err := commandFor(m.cfg, play)
	if err != nil {
		t.Fatal(err)
	}
	winTitle, err := winePath(cfg.title)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Dir != builds[1].Dir || cmd.Args[1] != builds[1].Binary ||
		cmd.Args[3] != winTitle || cmd.Args[6] != "series-s" {
		t.Fatalf("switch did not carry the new paths and active options: %v", cmd)
	}
}

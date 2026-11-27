package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestGamescopeLaunch(t *testing.T) {
	var output bytes.Buffer
	cfg, _, err := parseFlags([]string{
		"--gamescope", "--display-preset", "upscale", "--game-size", "1280x800",
		"--window-size", "1920x1200", "--fullscreen", "--fps-limit", "30",
	}, &output, func(string) string { return "" })
	if err != nil {
		t.Fatal(output.String())
	}
	cfg.dir = t.TempDir()
	cfg.wine = "/test/bin/wine"
	cfg.binary = filepath.Join(cfg.dir, "victory build.exe")
	cfg.title = filepath.Join(cfg.dir, "title with spaces.exe")
	archive := filepath.Join(cfg.dir, "shaders.rpf")
	if err := os.WriteFile(archive, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd, err := commandFor(cfg, play)
	if err != nil {
		t.Fatal(err)
	}
	title, err := winePath(cfg.title)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gamescope", "-w", "1280", "-h", "800", "-W", "1920", "-H", "1200",
		"-F", "fsr", "-f", "--framerate-limit", "30", "--", cfg.wine, cfg.binary,
		"run", title, "--execute", "--console", "series-x"}
	if cmd.Dir != cfg.dir || !slices.Equal(cmd.Args, want) {
		t.Fatalf("got %q; want %q", cmd.Args, want)
	}
	cfg.display.fsr = false
	translation, err := commandFor(cfg, translate)
	if err != nil || translation.Args[0] != cfg.wine || slices.Contains(translation.Args, "-f") {
		t.Fatalf("translation should run directly through Wine: %v, %v", translation, err)
	}
}

func TestGamescopeFlagValidation(t *testing.T) {
	for _, args := range [][]string{
		{"--window-size", "1280x720"},
		{"--gamescope", "--game-size", "1280x0"},
		{"--gamescope", "--window-size", ""},
		{"--gamescope", "--display-preset", "missing"},
		{"--gamescope", "--fps-limit", "-1"},
	} {
		var output bytes.Buffer
		if _, _, err := parseFlags(args, &output, func(string) string { return "" }); err == nil || output.Len() == 0 {
			t.Errorf("expected a diagnostic for %q", args)
		}
	}
}

func TestDisplayControlsAndSummary(t *testing.T) {
	m := &model{gamescope: "/test/bin/gamescope", width: 48, height: 9}
	m.cfg.display.applyPreset(0)
	for _, key := range []rune{'d', 'g', 'p', 'i', 'o', 'l'} {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	for _, label := range []string{"Game: 1280x800", "Output: 1920x1200", "Scaling: FSR", "Limit: 30 FPS", "Start: fullscreen", "Super+F"} {
		if !strings.Contains(m.View(), label) {
			t.Errorf("display pane is missing %q", label)
		}
	}
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.cancelRun = cancel
	before := m.cfg.display
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.cfg.display != before {
		t.Fatal("changed display settings while a session was active")
	}
	s := session{op: play, gamescope: true, display: before}
	if got := s.summary(); !strings.Contains(got, "Requested game resolution: 1280x800") || strings.Contains(got, m.gamescope) {
		t.Fatalf("unexpected display summary: %s", got)
	}
}

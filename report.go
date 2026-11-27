package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type runState int

const (
	idle runState = iota
	starting
	running
	stopping
	stopped
	finished
	failed
)

func (s runState) String() string {
	return [...]string{"Idle", "Starting", "Running", "Stopping", "Stopped", "Finished", "Failed"}[s]
}

type session struct {
	op                 operation
	state              runState
	seriesS, gamescope bool
	display            displayOptions
	stats              runStats
}

func (s session) summary() string {
	var b strings.Builder
	b.WriteString("# victory-px session\n\n")
	fmt.Fprintf(&b, "- Status: %s\n", s.state)
	switch s.op {
	case play:
		console := "Series X"
		if s.seriesS {
			console = "Series S"
		}
		fmt.Fprintf(&b, "- Operation: Run\n- Console: %s\n- Gamescope: %t\n", console, s.gamescope)
		if s.gamescope {
			fmt.Fprintf(&b, "- Requested game resolution: %s\n- Requested output size: %s\n", s.display.game, s.display.window)
			fmt.Fprintf(&b, "- FSR: %t\n- Start fullscreen: %t\n- FPS limit (0 = unlimited): %d\n",
				s.display.fsr, s.display.fullscreen, s.display.fpsLimit)
		}
	case translate:
		b.WriteString("- Operation: Translate\n")
	}
	fmt.Fprintf(&b, "- Warnings observed: %d\n- Errors observed: %d\n", s.stats.warnings, s.stats.errors)
	for _, metric := range []struct {
		label   string
		reading reading
	}{
		{"FPS", s.stats.fps}, {"ms/frame", s.stats.frameTime}, {"Draws/frame", s.stats.draws},
		{"Dropped/frame", s.stats.dropped}, {"Private RAM (MiB)", s.stats.privateRAM},
		{"VIctory VRAM (MiB)", s.stats.vram}, {"VIctory VRAM budget (MiB)", s.stats.vramBudget},
	} {
		if !metric.reading.at.IsZero() {
			fmt.Fprintf(&b, "- Last %s: %.2f\n", metric.label, metric.reading.value)
		}
	}
	return b.String()
}

func createStateFile(subdir, pattern string) (*os.File, error) {
	root := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(root) {
		dir, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		root = filepath.Join(dir, ".local", "state")
	}
	dir := filepath.Join(root, "victory-px", subdir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return os.CreateTemp(dir, pattern)
}

func writeReport(summary string) (string, error) {
	f, err := createStateFile("reports", "session-*.md")
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(summary)
	if err = errors.Join(err, f.Close()); err != nil {
		return "", errors.Join(err, os.Remove(f.Name()))
	}
	return f.Name(), nil
}

func copySummary(ctx context.Context, summary string) error {
	var commands [][]string
	if os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("WAYLAND_SOCKET") != "" {
		commands = append(commands, []string{"wl-copy"})
	}
	if os.Getenv("DISPLAY") != "" {
		commands = append(commands, []string{"xclip", "-selection", "clipboard"}, []string{"xsel", "--clipboard", "--input"})
	}
	var lastErr error
	for _, args := range commands {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		path, err := exec.LookPath(args[0])
		if err != nil {
			if !errors.Is(err, exec.ErrNotFound) {
				lastErr = err
			}
			continue
		}
		timeout, cancel := context.WithTimeout(ctx, 2*time.Second)
		cmd := exec.CommandContext(timeout, path, args[1:]...)
		cmd.Stdin = strings.NewReader(summary)
		cmd.WaitDelay = 200 * time.Millisecond
		err = cmd.Run()
		if timeout.Err() != nil {
			err = timeout.Err()
		}
		cancel()
		if err == nil {
			return nil
		}
		lastErr = fmt.Errorf("%s: %w", args[0], err)
	}
	if lastErr != nil {
		return lastErr
	}
	return errors.New("no clipboard helper for this display; install wl-copy, xclip or xsel")
}

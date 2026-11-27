package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

var errPickerCanceled = errors.New("selection canceled")

type pickerResult struct {
	build forkBuild
	title string
	err   error
}

func nativePicker(ctx context.Context, args ...string) (string, error) {
	if os.Getenv("WAYLAND_DISPLAY") == "" && os.Getenv("DISPLAY") == "" {
		return "", errors.New("a graphical session is needed for the file picker")
	}
	dialog, err := exec.LookPath("zenity")
	if err != nil {
		return "", errors.New("install Zenity to open the system picker")
	}
	output, err := exec.CommandContext(ctx, dialog, args...).Output()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return "", errPickerCanceled
	}
	return strings.TrimSuffix(string(output), "\n"), err
}

func choosePath(ctx context.Context, prompt, initial string, directory bool) (string, error) {
	args := []string{"--file-selection", "--title=" + prompt}
	if initial != "" {
		args = append(args, "--filename="+initial)
	}
	if directory {
		args = append(args, "--directory")
	} else {
		args = append(args, "--file-filter=Windows executables | *.exe *.EXE")
	}
	return nativePicker(ctx, args...)
}

func chooseFork(ctx context.Context, build forkBuild, suggestedDir string, existing []forkBuild) tea.Cmd {
	return func() tea.Msg {
		initialBinary := build.Binary
		initialDir := build.Dir
		if build.Name == "" {
			name, err := nativePicker(ctx, "--entry", "--title=Add VIctory build", "--text=Build name (for example neptune)")
			if err != nil {
				return pickerResult{err: err}
			}
			build.Name = strings.TrimSpace(name)
			if !forkNamePattern.MatchString(build.Name) {
				return pickerResult{err: errors.New("use 1–40 letters, digits, dots, underscores or hyphens for the build name")}
			}
			for _, other := range existing {
				if other.Name == build.Name {
					return pickerResult{err: errors.New("that build name already exists; use Edit build")}
				}
			}
			initialDir = suggestedDir
		}
		if initialDir != "" {
			initialDir += string(os.PathSeparator)
		}
		dir, err := choosePath(ctx, "Choose the VIctory build folder", initialDir, true)
		if err != nil {
			return pickerResult{err: err}
		}
		if dir != build.Dir || initialBinary == "" {
			initialBinary = dir + string(os.PathSeparator)
		}
		binary, err := choosePath(ctx, "Choose the VIctory executable", initialBinary, false)
		if err != nil {
			return pickerResult{err: err}
		}
		build.Dir, build.Binary = dir, binary
		return pickerResult{build: build}
	}
}

func chooseGame(ctx context.Context, current string, fromFolder bool) tea.Cmd {
	return func() tea.Msg {
		if fromFolder {
			var initial string
			if current != "" {
				initial = filepath.Dir(current) + string(os.PathSeparator)
			}
			dir, err := choosePath(ctx, "Choose the game folder", initial, true)
			if err != nil {
				return pickerResult{err: err}
			}
			current = dir + string(os.PathSeparator)
		}
		title, err := choosePath(ctx, "Choose the game executable", current, false)
		return pickerResult{title: title, err: err}
	}
}

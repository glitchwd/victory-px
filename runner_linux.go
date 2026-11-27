package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/sys/unix"
)

type operation string

const (
	play      operation = "Run"
	translate operation = "Translate"
)

type processStarted struct{}
type processLine string
type processFinished struct {
	err, logErr      error
	stopped, started bool
	dropped          int
	logName          string
}

func commandFor(cfg config, op operation) (*exec.Cmd, error) {
	wineArgs := []string{cfg.binary}
	switch op {
	case play:
		title, err := winePath(cfg.title)
		if err != nil {
			return nil, err
		}
		console := "series-x"
		if cfg.seriesS {
			console = "series-s"
		}
		wineArgs = append(wineArgs, "run", title, "--execute", "--console", console)
	case translate:
		shaders, err := regularFile(filepath.Join(filepath.Dir(cfg.title), "shaders.rpf"))
		if err != nil {
			return nil, fmt.Errorf("shader archive: %w", err)
		}
		archive, err := winePath(shaders)
		if err != nil {
			return nil, err
		}
		wineArgs = append(wineArgs, "translate", archive)
	default:
		return nil, errors.New("unknown operation")
	}
	cmd := exec.Command(cfg.wine, wineArgs...)
	if op == play && cfg.gamescope {
		args := append(cfg.display.gamescopeArgs(), "--", cfg.wine)
		cmd = exec.Command("gamescope", append(args, wineArgs...)...)
	}
	cmd.Dir = cfg.dir
	return cmd, nil
}

func runProcess(ctx context.Context, cmd *exec.Cmd, raw io.Writer, events chan<- tea.Msg) (result processFinished) {
	if ctx.Err() != nil {
		result.stopped = true
		return
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		result.err = err
		return
	}
	defer reader.Close()
	defer writer.Close()
	cmd.Stdout, cmd.Stderr = writer, writer
	cmd.SysProcAttr = &unix.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		result.err = err
		return
	}
	result.started = true
	_ = writer.Close()
	select {
	case events <- processStarted{}:
	case <-ctx.Done():
	}

	output := make(chan processFinished, 1)
	go func() { output <- readOutput(reader, raw, events) }()
	pid := cmd.Process.Pid
	exited := make(chan error, 1)
	go func() {
		// Keep the group leader unreaped until the final group signal so its PID cannot be reused.
		var info unix.Siginfo
		for {
			err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
			if !errors.Is(err, unix.EINTR) {
				exited <- err
				return
			}
		}
	}()
	var waitErr, signalErr error
	var exitSeen bool
	select {
	case waitErr = <-exited:
		exitSeen = true
	case <-ctx.Done():
		result.stopped = true
		signalErr = signalGroup(pid, unix.SIGTERM)
		grace := time.NewTimer(750 * time.Millisecond)
		select {
		case waitErr = <-exited:
			exitSeen = true
		case <-grace.C:
		}
		grace.Stop()
	}
	if !errors.Is(waitErr, unix.ECHILD) {
		signalErr = errors.Join(signalErr, signalGroup(pid, unix.SIGKILL))
	}
	if !exitSeen {
		waitErr = <-exited
	}
	err = cmd.Wait()
	if result.stopped {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			err = nil
		}
	}
	if deadlineErr := reader.SetReadDeadline(time.Now().Add(time.Second)); deadlineErr != nil {
		_ = reader.Close()
		err = errors.Join(err, deadlineErr)
	}
	drained := <-output
	result.err = errors.Join(drained.err, err, waitErr, signalErr)
	result.logErr, result.dropped = drained.logErr, drained.dropped
	return
}

func signalGroup(pid int, signal unix.Signal) error {
	err := unix.Kill(-pid, signal)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("signal process group: %w", err)
	}
	return nil
}

const maxLogBytes = 32 << 20

const wineComputePipelineFault = ":err:vulkan:vkCreateComputePipelines Exception 0xc0000005 in Unix call."

var errComputePipelineCrash = errors.New("Vulkan compute pipeline crashed under Wine (0xc0000005)")

func readOutput(reader io.Reader, raw io.Writer, events chan<- tea.Msg) (result processFinished) {
	buffer := bufio.NewReaderSize(reader, 8*1024)
	var truncated bool
	remaining := maxLogBytes
	for {
		chunk, err := buffer.ReadSlice('\n')
		if raw != nil && len(chunk) > 0 && result.logErr == nil {
			n, writeErr := raw.Write(chunk[:min(len(chunk), remaining)])
			remaining -= n
			switch {
			case writeErr != nil:
				result.logErr = writeErr
			case n < len(chunk):
				result.logErr = errors.New("32 MiB limit reached")
			}
		}
		if len(chunk) > 0 && !truncated {
			line := terminalText(string(chunk))
			if strings.Contains(line, wineComputePipelineFault) {
				result.err = errComputePipelineCrash
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				line += " [line truncated]"
				result.dropped++
			}
			select {
			case events <- processLine(line):
			default:
				result.dropped++
			}
		}
		truncated = errors.Is(err, bufio.ErrBufferFull)
		switch {
		case err == nil, truncated:
		case errors.Is(err, io.EOF):
			return
		default:
			result.err = errors.Join(result.err, fmt.Errorf("read process output: %w", err))
			return
		}
	}
}

func launch(ctx context.Context, cfg config, op operation, events chan<- tea.Msg) processFinished {
	cmd, err := commandFor(cfg, op)
	if err != nil {
		return processFinished{err: err}
	}
	if !cfg.saveLog {
		return runProcess(ctx, cmd, nil, events)
	}
	f, err := createStateFile("logs", "session-*.log")
	if err != nil {
		result := runProcess(ctx, cmd, nil, events)
		result.logErr = err
		return result
	}
	result := runProcess(ctx, cmd, f, events)
	result.logErr = errors.Join(result.logErr, f.Close())
	if result.started {
		result.logName = f.Name()
	} else {
		result.logErr = errors.Join(result.logErr, os.Remove(f.Name()))
	}
	return result
}

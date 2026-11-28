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
		if cfg.forkName == nvidiaRTForkName {
			if cfg.seriesS {
				wineArgs = append(wineArgs, "play", "--series-s")
			} else {
				wineArgs = append(wineArgs, "play")
			}
			wineArgs = append(wineArgs, nvidiaRTLaunchArgs()...)
			break
		}
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
		if cfg.forkName == nvidiaRTForkName {
			return nil, errors.New("VIctory NVIDIA-RT does not provide the translate command")
		}
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
		displayArgs := cfg.display.gamescopeArgs()
		if cfg.forkName == nvidiaRTForkName {
			// This fork uses DLSS itself; avoid stacking Gamescope FSR on top.
			displayArgs = cfg.display.gamescopeArgsWithoutFSR()
		}
		args := append(displayArgs, "--", cfg.wine)
		cmd = exec.Command("gamescope", append(args, wineArgs...)...)
	}
	cmd.Dir = cfg.dir
	if op == play && cfg.forkName == nvidiaRTForkName {
		cmd.Dir = filepath.Dir(cfg.title)
	}
	return cmd, nil
}

// The release's PLAY.cmd uses these same fast-path settings by default. Keep
// them in PX so its normal run/stop and Gamescope controls still apply.
func nvidiaRTLaunchArgs() []string {
	return []string{
		"--res", "1440", "--dlss", "quality", "--fps", "1000",
		"--set", "VICTORY_HDR=0", "--set", "VICTORY_REFLEX=boost",
		"--set", "VICTORY_FULLSCREEN=1", "--set", "VICTORY_PIPELINE_SAMPLER_TABLE=1",
		"--set", "VICTORY_PIPELINE_WAIT_ONCE=1", "--set", "VICTORY_PIPELINE_WAIT_BACKLOG=64",
		"--set", "VICTORY_PIPELINE_SLOW_WORKERS=8", "--set", "VICTORY_TEXTURE_LOOK_ONCE=1",
		"--set", "VICTORY_TEXTURE_READ_AHEAD=1", "--set", "VICTORY_STAGING_NO_ZERO=1",
		"--set", "VICTORY_SEND_COPIED_FIND=1", "--set", "VICTORY_FILES_BUFFERED=1",
		"--set", "VICTORY_MEMORY_FRESH=1", "--set", "VICTORY_MEMORY_ARENAS_AHEAD=1",
		"--set", "VICTORY_MEMORY_QUIET=1", "--set", "VICTORY_MEMORY_LAND_SENT=1",
		"--set", "VICTORY_MEMORY_ASK_RUNS=1", "--set", "VICTORY_MEMORY_LEAN=1",
		"--set", "VICTORY_MEMORY_DIRTY=1", "--set", "VICTORY_MEMORY_BUFFER_FAULTS=1",
		"--set", "VICTORY_MEMORY_FAULT_PAGES=40000", "--set", "VICTORY_REFRESH_PROBE_US=300",
		"--set", "VICTORY_DECLARE_ACROSS_LISTS=1", "--set", "VICTORY_REFRESH_PROBE_TEXTURES=1",
		"--set", "VICTORY_REFRESH_SPLIT=1", "--set", "VICTORY_WAIT_SPLIT=1",
		"--set", "VICTORY_DSTORAGE_STATS=1", "--set", "VICTORY_PIPELINE_PIECES=1",
		"--set", "VICTORY_PIPELINE_PIECES_REFUSE_UNTRANSLATED=1",
		"--set", "VICTORY_PIPELINE_PIECES_ONLY=graphics", "--set", "VICTORY_REFRESH_SET=1",
		"--set", "VICTORY_TITLE_WRITE_RANGE=1", "--set", "VICTORY_UNDER_PARTS=1",
		"--set", "VICTORY_SEND_THREAD=1",
	}
}

func runProcess(ctx context.Context, cmd *exec.Cmd, savedLog io.Writer, events chan<- tea.Msg) (result processFinished) {
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
	go func() { output <- readOutput(reader, savedLog, events) }()
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

func readOutput(reader io.Reader, savedLog io.Writer, events chan<- tea.Msg) (result processFinished) {
	buffer := bufio.NewReaderSize(reader, 8*1024)
	var truncated bool
	remaining := maxLogBytes
	for {
		chunk, err := buffer.ReadSlice('\n')
		if len(chunk) > 0 && !truncated {
			line := terminalText(string(chunk))
			if strings.Contains(line, wineComputePipelineFault) {
				result.err = errComputePipelineCrash
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				line = "[line truncated]"
				result.dropped++
			} else {
				line = privateLogText(line)
			}
			if savedLog != nil && result.logErr == nil {
				entry := line + "\n"
				if len(entry) > remaining {
					result.logErr = errors.New("32 MiB limit reached")
				} else if n, writeErr := io.WriteString(savedLog, entry); writeErr != nil {
					result.logErr = writeErr
				} else if n < len(entry) {
					result.logErr = io.ErrShortWrite
				} else {
					remaining -= n
				}
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

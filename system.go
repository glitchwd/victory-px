package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type hostMetrics struct {
	ramUsedMiB, ramTotalMiB                     uint64
	gpuUtil, gpuTemp, vramUsedMiB, vramTotalMiB string
}

func parseRAM(data string) (usedMiB, totalMiB uint64) {
	var total, available *uint64
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[2] != "kB" {
			continue
		}
		switch fields[0] {
		case "MemTotal:", "MemAvailable:":
			value, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				continue
			}
			if fields[0] == "MemTotal:" {
				total = &value
			} else {
				available = &value
			}
		}
	}
	if total == nil || available == nil || *available > *total {
		return 0, 0
	}
	return (*total - *available) / 1024, *total / 1024
}

func parseGPU(data string, metrics *hostMetrics) {
	line, _, _ := strings.Cut(data, "\n")
	fields := strings.Split(line, ",")
	if len(fields) != 4 {
		return
	}
	destinations := []*string{&metrics.gpuUtil, &metrics.gpuTemp, &metrics.vramUsedMiB, &metrics.vramTotalMiB}
	for i, field := range fields {
		if value, err := strconv.Atoi(strings.TrimSpace(field)); err == nil {
			*destinations[i] = strconv.Itoa(value)
		}
	}
}

func sampleHost(ctx context.Context, nvidiaSMI string, delay time.Duration) tea.Cmd {
	return func() tea.Msg {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil
		}
		var metrics hostMetrics
		if data, err := os.ReadFile("/proc/meminfo"); err == nil {
			metrics.ramUsedMiB, metrics.ramTotalMiB = parseRAM(string(data))
		}
		if nvidiaSMI != "" {
			probe, cancel := context.WithTimeout(ctx, 850*time.Millisecond)
			defer cancel()
			cmd := exec.CommandContext(probe, nvidiaSMI,
				"--query-gpu=utilization.gpu,temperature.gpu,memory.used,memory.total",
				"--format=csv,noheader,nounits")
			cmd.WaitDelay = 150 * time.Millisecond
			if data, err := cmd.Output(); err == nil {
				parseGPU(string(data), &metrics)
			}
		}
		return metrics
	}
}

func (m hostMetrics) display() []string {
	ram := "Host RAM: unavailable"
	if m.ramTotalMiB > 0 {
		ram = fmt.Sprintf("Host RAM: %.1f / %.1f GiB", float64(m.ramUsedMiB)/1024, float64(m.ramTotalMiB)/1024)
	}
	gpu := "NVIDIA GPU: unavailable"
	if m.gpuUtil != "" || m.gpuTemp != "" {
		gpu = fmt.Sprintf("NVIDIA GPU 0: %s%%, %s°C", displayValue(m.gpuUtil), displayValue(m.gpuTemp))
	}
	vram := "NVIDIA VRAM: unavailable"
	if m.vramTotalMiB != "" {
		vram = fmt.Sprintf("NVIDIA VRAM: %s / %s MiB", displayValue(m.vramUsedMiB), m.vramTotalMiB)
	}
	return []string{ram, gpu, vram}
}

func displayValue(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

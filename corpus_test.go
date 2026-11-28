package main

import (
	"strings"
	"testing"
	"time"
)

func TestCorpusReportAndShortMessage(t *testing.T) {
	now := time.Date(2026, 11, 28, 9, 20, 0, 0, time.UTC)
	var stats runStats
	stats.record("INFO 42.0 fps 23.81 ms/frame", now)
	stats.record("INFO 60.0 fps 16.67 ms/frame", now.Add(time.Second))
	stats.record("ERROR something failed", now.Add(2*time.Second))

	s := session{
		op:        play,
		state:     finished,
		gamescope: true,
		display:   displayOptions{game: resolution{1280, 720}, window: resolution{1920, 1080}, fsr: true},
		stats:     stats,
	}
	meta := corpusMeta{
		forkName:     "nvidia-rt",
		forkRepo:     "Kemerd/VIctory-NVIDIA-RT",
		commit:       "abc1234",
		osName:       "Fedora Linux 41",
		kernel:       "6.11.0",
		cpu:          "AMD Ryzen 7 7800X3D",
		gpu:          "NVIDIA GeForce RTX 3070",
		driver:       "565.57.01",
		vram:         "8192 MiB",
		ramGiB:       "32.0 GiB",
		runtimeLabel: "GE-Proton9-20",
		duration:     45 * time.Second,
		now:          now,
	}

	short := s.corpusShortMessage(meta, "corpus-test.md")
	if len(short) > 2000 {
		t.Fatalf("short message exceeds 2000 chars: %d", len(short))
	}
	for _, want := range []string{
		"Claim:", "Evidence type: measured", "Build: NVIDIA",
		"FPS min/avg/max 42.0/51.0/60.0", "ms/frame min/avg/max 16.67/20.24/23.81",
		"NVIDIA GeForce RTX 3070 (8192 MiB)", "Attached: corpus-test.md",
	} {
		if !strings.Contains(short, want) {
			t.Fatalf("short message missing %q:\n%s", want, short)
		}
	}

	full := s.corpusFullReport(meta, "corpus-test.md")
	for _, want := range []string{
		"## WORKING ON lines", "## Short message", "## Full report",
		"### Header fields", "### What was run or measured",
		"### Findings", "- Platform scope: NVIDIA", "- Evidence: PROBED",
		"### Compliance checklist",
	} {
		if !strings.Contains(full, want) {
			t.Fatalf("full report missing %q", want)
		}
	}
	for _, forbidden := range []string{"/home/", "/tmp/", "C:\\"} {
		if strings.Contains(full, forbidden) {
			t.Fatalf("full report leaked local path %q", forbidden)
		}
	}
}

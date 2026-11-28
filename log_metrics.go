package main

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

var (
	fpsRE       = regexp.MustCompile(`(?i)\b([0-9]+(?:\.[0-9]+)?)\s+fps\b`)
	frameTimeRE = regexp.MustCompile(`(?i)\b([0-9]+(?:\.[0-9]+)?)\s+ms/frame\b`)
	drawsRE     = regexp.MustCompile(`(?i)\b([0-9]+(?:\.[0-9]+)?)\s+draws/frame\b`)
	dropsRE     = regexp.MustCompile(`(?i)\bdropped\s+([0-9]+(?:\.[0-9]+)?)(?:/frame\b|\s+without a host pipeline\b)`)
	privateRE   = regexp.MustCompile(`(?i)\bprivate\s+([0-9]+)\s+MiB\b`)
	vramRE      = regexp.MustCompile(`(?i)\bdriver:\s+video\s+([0-9]+)\s+of\s+([0-9]+)\s+MiB\b`)
	levelRE     = regexp.MustCompile(`^(?:\S+T\S+\s+)?(WARN(?:ING)?|ERROR)(?:[ :\[]|$)`)
	logUnitsRE  = regexp.MustCompile("(?i)\\b(?:[0-9]+(?:\\.[0-9]+)?[ \\t]+(?:ms|draws)/frame|dropped[ \\t]+[0-9]+(?:\\.[0-9]+)?/frame)\\b")
	logPathRE   = regexp.MustCompile("(?i)(?:[/\\\\]|\\b[a-z]:[^\\s:]|\\b(?:path|root|storage|filename|directory|cwd|log)[\"']?\\s*[:=]|%(?:2f|5c|3a))")
)

type reading struct {
	value float64
	at    time.Time
}

type metricAgg struct {
	min, max, sum float64
	count         int
}

func (a *metricAgg) add(value float64) {
	if a.count == 0 || value < a.min {
		a.min = value
	}
	if a.count == 0 || value > a.max {
		a.max = value
	}
	a.sum += value
	a.count++
}

func (a metricAgg) avg() float64 {
	if a.count == 0 {
		return 0
	}
	return a.sum / float64(a.count)
}

type runStats struct {
	fps, frameTime, draws, dropped reading
	fpsAgg, frameTimeAgg           metricAgg
	privateRAM, vram, vramBudget   reading
	warnings, errors               int
}

type logLine struct {
	text     string
	severity string
	metric   bool
}

func terminalText(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), r == '\u2028', r == '\u2029':
			return -1
		default:
			return r
		}
	}, ansi.Strip(strings.TrimRight(s, "\r\n")))
}

func privateLogText(s string) string {
	if logPathRE.MatchString(logUnitsRE.ReplaceAllString(s, " ")) {
		return "path hidden"
	}
	return s
}

func (s *runStats) record(text string, now time.Time) logLine {
	line := logLine{text: privateLogText(text)}
	for _, metric := range []struct {
		re  *regexp.Regexp
		dst *reading
	}{
		{fpsRE, &s.fps}, {frameTimeRE, &s.frameTime}, {drawsRE, &s.draws},
		{dropsRE, &s.dropped}, {privateRE, &s.privateRAM},
	} {
		if match := metric.re.FindStringSubmatch(text); match != nil {
			if value, err := strconv.ParseFloat(match[1], 64); err == nil {
				*metric.dst = reading{value, now}
				switch metric.dst {
				case &s.fps:
					s.fpsAgg.add(value)
				case &s.frameTime:
					s.frameTimeAgg.add(value)
				}
				line.metric = true
			}
		}
	}
	if match := vramRE.FindStringSubmatch(text); match != nil {
		used, usedErr := strconv.ParseFloat(match[1], 64)
		total, totalErr := strconv.ParseFloat(match[2], 64)
		if usedErr == nil && totalErr == nil {
			s.vram, s.vramBudget = reading{used, now}, reading{total, now}
			line.metric = true
		}
	}
	if level := levelRE.FindStringSubmatch(text); level != nil {
		if level[1] == "ERROR" {
			line.severity = "error"
		} else {
			line.severity = "warning"
		}
	} else if strings.Contains(text, wineComputePipelineFault) ||
		(strings.HasPrefix(text, "thread '") && strings.Contains(text, "panicked at")) {
		line.severity = "error"
	}
	switch line.severity {
	case "warning":
		s.warnings++
	case "error":
		s.errors++
	}
	return line
}

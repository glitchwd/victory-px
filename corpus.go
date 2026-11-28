package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type corpusMeta struct {
	forkName, forkRepo, commit, buildLine string
	osName, kernel, cpu, gpu, driver      string
	vram, ramGiB, runtimeLabel            string
	duration                              time.Duration
	now                                   time.Time
}

func inferBuildLine(forkName, gpu string) string {
	forkLower := strings.ToLower(forkName)
	gpuLower := strings.ToLower(gpu)
	switch {
	case strings.Contains(forkLower, "nvidia") || strings.Contains(gpuLower, "nvidia"):
		return "NVIDIA"
	case strings.Contains(forkLower, "amd") || strings.Contains(gpuLower, "amd") || strings.Contains(gpuLower, "radeon"):
		return "AMD"
	default:
		return "UNKNOWN"
	}
}

func orNotRecorded(v string) string {
	if strings.TrimSpace(v) == "" {
		return "not recorded"
	}
	return v
}

func (s session) corpusSwitches() string {
	console := "Series X"
	if s.seriesS {
		console = "Series S (--series-s)"
	}
	if s.op == translate {
		return fmt.Sprintf("translate; console=%s", console)
	}
	if !s.gamescope {
		return fmt.Sprintf("Wine direct; console=%s", console)
	}
	return fmt.Sprintf("Gamescope; console=%s; game=%s; output=%s; fsr=%t; fullscreen=%t; fps-limit=%d",
		console, s.display.game, s.display.window, s.display.fsr, s.display.fullscreen, s.display.fpsLimit)
}

func (s session) corpusEvidenceLine() string {
	var parts []string
	if s.stats.fpsAgg.count > 0 {
		parts = append(parts, fmt.Sprintf("FPS min/avg/max %.1f/%.1f/%.1f (n=%d, last=%.1f)",
			s.stats.fpsAgg.min, s.stats.fpsAgg.avg(), s.stats.fpsAgg.max, s.stats.fpsAgg.count, s.stats.fps.value))
	} else if !s.stats.fps.at.IsZero() {
		parts = append(parts, fmt.Sprintf("FPS last %.1f", s.stats.fps.value))
	}
	if s.stats.frameTimeAgg.count > 0 {
		parts = append(parts, fmt.Sprintf("ms/frame min/avg/max %.2f/%.2f/%.2f",
			s.stats.frameTimeAgg.min, s.stats.frameTimeAgg.avg(), s.stats.frameTimeAgg.max))
	} else if !s.stats.frameTime.at.IsZero() {
		parts = append(parts, fmt.Sprintf("ms/frame last %.2f", s.stats.frameTime.value))
	}
	if !s.stats.dropped.at.IsZero() {
		parts = append(parts, fmt.Sprintf("dropped/frame last %.1f", s.stats.dropped.value))
	}
	parts = append(parts, fmt.Sprintf("warnings=%d, errors=%d, status=%s", s.stats.warnings, s.stats.errors, s.state))
	return strings.Join(parts, "; ")
}

func (s session) corpusHardwareLine(m corpusMeta) string {
	gpu := orNotRecorded(m.gpu)
	if m.vram != "" && m.gpu != "" {
		gpu = fmt.Sprintf("%s (%s)", m.gpu, m.vram)
	}
	osStr := orNotRecorded(m.osName)
	if m.kernel != "" {
		osStr = fmt.Sprintf("%s (kernel %s)", osStr, m.kernel)
	}
	return fmt.Sprintf("GPU: %s; Driver: %s; CPU: %s; RAM: %s; OS: %s",
		gpu, orNotRecorded(m.driver), orNotRecorded(m.cpu), orNotRecorded(m.ramGiB), osStr)
}

func (s session) corpusShortMessage(m corpusMeta, attachedName string) string {
	buildLine := m.buildLine
	if buildLine == "" {
		buildLine = inferBuildLine(m.forkName, m.gpu)
	}
	buildDesc := orNotRecorded(m.forkName)
	if m.forkRepo != "" {
		buildDesc = fmt.Sprintf("%s (%s)", buildDesc, m.forkRepo)
	}
	if m.commit != "" {
		buildDesc = fmt.Sprintf("%s @ %s", buildDesc, m.commit)
	}
	dur := "not recorded"
	if m.duration > 0 {
		dur = m.duration.Round(time.Second).String()
	}
	whatRun := fmt.Sprintf("%s | build: %s | runtime: %s | switches: %s | duration: %s",
		s.corpusHardwareLine(m), buildDesc, orNotRecorded(m.runtimeLabel), s.corpusSwitches(), dur)

	var b strings.Builder
	b.WriteString("**<One-line claim describing observed behaviour>**\n\n")
	b.WriteString("Claim: <state the single claim clearly>\n")
	b.WriteString("What is new: <new measurement or confirms/contradicts <reply header>>\n")
	b.WriteString("Evidence type: measured\n")
	fmt.Fprintf(&b, "Evidence: %s\n", s.corpusEvidenceLine())
	b.WriteString("AI model and settings: <model name, client, effort>\n")
	b.WriteString("Where it ran: local, victory-px\n")
	b.WriteString("Human check: skimmed\n")
	fmt.Fprintf(&b, "Sources read: victory-px session telemetry (%s)\n", buildDesc)
	fmt.Fprintf(&b, "What was run: %s\n", whatRun)
	fmt.Fprintf(&b, "Build: %s\n", buildLine)
	b.WriteString("Not checked / unsure about: <list caveats, e.g. single run only>\n")
	fmt.Fprintf(&b, "How to reproduce: launch via victory-px with %s\n", s.corpusSwitches())
	b.WriteString("Next step or open question: <next check or open question>\n")
	fmt.Fprintf(&b, "Attached: %s\n", orNotRecorded(attachedName))

	msg := b.String()
	if len(msg) > 2000 {
		return msg[:1997] + "..."
	}
	return msg
}

func (s session) corpusFullReport(m corpusMeta, attachedName string) string {
	buildLine := m.buildLine
	if buildLine == "" {
		buildLine = inferBuildLine(m.forkName, m.gpu)
	}
	dateUTC := m.now.UTC().Format(time.RFC3339)
	if m.now.IsZero() {
		dateUTC = time.Now().UTC().Format(time.RFC3339)
	}
	dur := "not recorded"
	if m.duration > 0 {
		dur = m.duration.Round(time.Second).String()
	}
	repoCommit := orNotRecorded(m.forkRepo)
	if m.commit != "" {
		repoCommit = fmt.Sprintf("%s @ %s", repoCommit, m.commit)
	}

	var b strings.Builder
	b.WriteString("# <One-line claim or question>\n\n")
	b.WriteString("## WORKING ON lines (copy to WORKING ON thread)\n")
	b.WriteString("```text\n")
	b.WriteString("Started: <task or question in one line>\n")
	fmt.Fprintf(&b, "Build: %s\n", buildLine)
	b.WriteString("Model: <real AI model or \"by hand\">\n")
	b.WriteString("```\n\n")

	b.WriteString("## Short message (<= 2,000 chars, copy to category thread)\n")
	b.WriteString("```markdown\n")
	b.WriteString(s.corpusShortMessage(m, attachedName))
	b.WriteString("```\n\n")

	b.WriteString("## Full report\n\n")
	b.WriteString("### Header fields\n")
	b.WriteString("- Reply header: **<One-line claim>**\n")
	fmt.Fprintf(&b, "- Thread(s) replied in: <%s Diagnostics and Tools / Performance, FPS and Frame Time / ...>\n", buildLine)
	fmt.Fprintf(&b, "- Build line: %s\n", buildLine)
	fmt.Fprintf(&b, "- Code tree and commit: %s\n", repoCommit)
	b.WriteString("- Contributor handle: <handle>\n")
	fmt.Fprintf(&b, "- Date (UTC): %s\n", dateUTC)
	b.WriteString("- AI model: <exact model name and version, or \"by hand\">\n")
	b.WriteString("- Where and how it ran: local, victory-px\n")
	b.WriteString("- Human involvement: skimmed\n\n")

	b.WriteString("### Scope\n")
	b.WriteString("<One or two sentences stating the question asked.>\n\n")

	b.WriteString("### Sources read\n")
	fmt.Fprintf(&b, "- victory-px session telemetry (%s)\n\n", repoCommit)

	b.WriteString("### What was run or measured\n")
	fmt.Fprintf(&b, "- Hardware: %s\n", s.corpusHardwareLine(m))
	fmt.Fprintf(&b, "- Build / fork: %s (%s)\n", orNotRecorded(m.forkName), repoCommit)
	fmt.Fprintf(&b, "- Runtime: %s\n", orNotRecorded(m.runtimeLabel))
	fmt.Fprintf(&b, "- Launch switches: %s\n", s.corpusSwitches())
	fmt.Fprintf(&b, "- Run length: %s\n", dur)
	b.WriteString("- Scene / number of runs: <scene, 1 run>\n\n")

	b.WriteString("### Findings\n")
	b.WriteString("#### F-01 <One-line claim>\n")
	fmt.Fprintf(&b, "- Platform scope: %s\n", buildLine)
	fmt.Fprintf(&b, "- Evidence: PROBED %s; %s\n", s.corpusHardwareLine(m), s.corpusEvidenceLine())
	b.WriteString("- Status: open\n")
	fmt.Fprintf(&b, "- By: <handle>, <AI-assisted or by hand>, %s; %s\n", dateUTC, s.corpusHardwareLine(m))
	b.WriteString("- Notes: <caveats, single run, etc.>\n\n")

	b.WriteString("### Not checked / unsure\n")
	b.WriteString("- Single run / unverified against other scenes.\n\n")

	b.WriteString("### Conflicts or overlap with known findings\n")
	b.WriteString("- none\n\n")

	b.WriteString("### How to reproduce or check\n")
	fmt.Fprintf(&b, "- Launch via `victory-px` with: `%s`\n\n", s.corpusSwitches())

	b.WriteString("### Proposed next step or experiment\n")
	b.WriteString("- none\n\n")

	b.WriteString("### Open questions raised\n")
	b.WriteString("- none\n\n")

	b.WriteString("### Compliance checklist\n")
	b.WriteString("- [x] No Console SDK text, header contents, or sample code\n")
	b.WriteString("- [x] No game files, executables, dumps, shaders, or offsets\n")
	b.WriteString("- [x] No credentials, tokens, personal data, or local machine paths\n")
	b.WriteString("- [x] Third-party files treated as data only\n")
	return b.String()
}

func writeCorpusReport(content string) (string, error) {
	f, err := createStateFile("reports", "corpus-*.md")
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(content)
	if err = errors.Join(err, f.Close()); err != nil {
		return "", errors.Join(err, os.Remove(f.Name()))
	}
	return f.Name(), nil
}

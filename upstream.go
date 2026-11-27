package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	upstreamRepo     = "Belobug/VIctory-Belobug-AMD"
	upstreamBranch   = "a-publier"
	upstreamAPI      = "https://api.github.com/repos/" + upstreamRepo
	upstreamInterval = 15 * time.Minute
	maxUpstreamBytes = 512 << 10
)

var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

type upstreamTick struct{}

type upstreamResult struct {
	sha, subject, checkout, release string
	commitAt, checkedAt             time.Time
	err, releaseErr                 error
}

func nextUpstreamCheck() tea.Cmd {
	return tea.Tick(upstreamInterval, func(time.Time) tea.Msg { return upstreamTick{} })
}

func refreshUpstream(ctx context.Context, dir string) tea.Cmd {
	return func() tea.Msg {
		client := &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		result := readUpstream(ctx, client, upstreamAPI)
		if result.err == nil {
			result.checkout = compareCheckout(ctx, dir, result.sha)
		}
		return result
	}
}

func readUpstream(ctx context.Context, client *http.Client, endpoint string) (result upstreamResult) {
	result.checkedAt = time.Now()
	var commits []struct {
		SHA    string
		Commit struct {
			Message   string
			Committer struct{ Date time.Time }
		}
	}
	_, result.err = githubJSON(ctx, client, endpoint+"/commits?sha="+upstreamBranch+"&per_page=1", &commits)
	if result.err != nil {
		return
	}
	if len(commits) != 1 || !commitSHA.MatchString(commits[0].SHA) {
		result.err = errors.New("invalid branch response")
		return
	}
	commit := commits[0]
	subject, _, _ := strings.Cut(commit.Commit.Message, "\n")
	result.sha, result.subject = commit.SHA, clip(terminalText(subject), 240)
	result.commitAt = commit.Commit.Committer.Date
	var release struct {
		Tag string `json:"tag_name"`
	}
	status, err := githubJSON(ctx, client, endpoint+"/releases/latest", &release)
	switch {
	case status == http.StatusNotFound:
		result.release = "None published"
	case err != nil:
		result.releaseErr = err
	default:
		result.release = clip(terminalText(release.Tag), 120)
		if result.release == "" {
			result.releaseErr = errors.New("invalid release response")
		}
	}
	return
}

func githubJSON(ctx context.Context, client *http.Client, url string, dest any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, errors.New("invalid GitHub request")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "victory-px")
	resp, err := client.Do(req)
	if err != nil {
		return 0, errors.New("GitHub connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("GitHub HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamBytes+1))
	if err != nil || len(data) > maxUpstreamBytes {
		return resp.StatusCode, errors.New("GitHub response unreadable or too large")
	}
	if err := json.Unmarshal(data, dest); err != nil {
		return resp.StatusCode, errors.New("invalid GitHub response")
	}
	return resp.StatusCode, nil
}

func compareCheckout(ctx context.Context, dir, sha string) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	git := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"--no-replace-objects", "-C", dir}, args...)...)
		// Partial clones can otherwise fetch missing objects during read-only commands.
		cmd.Env = append(cmd.Environ(), "GIT_NO_LAZY_FETCH=1")
		cmd.WaitDelay = 150 * time.Millisecond
		return cmd.Output()
	}
	if !commitSHA.MatchString(sha) {
		return "Checkout comparison unavailable"
	}
	origin, err := git("config", "--local", "--get", "remote.origin.url")
	if err != nil {
		return "Checkout comparison unavailable"
	}
	switch strings.ToLower(strings.TrimSuffix(strings.TrimSpace(string(origin)), ".git")) {
	case "https://github.com/belobug/victory-belobug-amd", "git@github.com:belobug/victory-belobug-amd", "ssh://git@github.com/belobug/victory-belobug-amd":
	default:
		return "Checkout comparison unavailable"
	}
	head, err := git("rev-parse", "--verify", "HEAD")
	if err != nil {
		return "Checkout comparison unavailable"
	}
	if strings.TrimSpace(string(head)) == sha {
		return "Matches checked-out revision"
	}
	_, err = git("merge-base", "--is-ancestor", sha, "HEAD")
	if err == nil {
		return "Included in checked-out revision"
	}
	if ctx.Err() != nil {
		return "Checkout comparison unavailable"
	}
	if _, err := git("cat-file", "-e", sha+"^{commit}"); err != nil {
		if ctx.Err() != nil {
			return "Checkout comparison unavailable"
		}
		return "Upstream commit absent locally"
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return "Upstream commit outside checkout history"
	}
	return "Checkout comparison unavailable"
}

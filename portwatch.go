package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const trackerURL = "https://raw.githubusercontent.com/4lwayz/portwatch-vi/main/data/tracker.json"
const maxTrackerBytes = 2 << 20

type portNews struct {
	Date, Title string
}

type portFeed struct {
	Updated time.Time
	News    []portNews
}

type portResult struct {
	feed portFeed
	err  error
}

func refreshPortWatch(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		client := &http.Client{
			Timeout: 6 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		feed, err := readPortWatch(ctx, client, trackerURL)
		return portResult{feed: feed, err: err}
	}
}

func readPortWatch(ctx context.Context, client *http.Client, endpoint string) (portFeed, error) {
	var feed portFeed
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return feed, err
	}
	req.Header.Set("User-Agent", "victory-px")
	resp, err := client.Do(req)
	if err != nil {
		return feed, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return feed, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxTrackerBytes+1))
	if err != nil {
		return feed, err
	}
	if len(data) > maxTrackerBytes {
		return feed, errors.New("feed exceeds size limit")
	}
	var tracker struct {
		Updated time.Time
		News    []portNews
	}
	if err := json.Unmarshal(data, &tracker); err != nil {
		return feed, fmt.Errorf("decode feed: %w", err)
	}
	if tracker.News == nil {
		return feed, errors.New("feed has no news array")
	}
	feed.Updated = tracker.Updated
	const maxHeadlines = 10
	feed.News = make([]portNews, 0, maxHeadlines)
	for _, item := range tracker.News {
		if len(feed.News) == maxHeadlines {
			break
		}
		if date, err := time.Parse("2006-01-02", item.Date); err == nil {
			item.Date = date.Format("2006-01-02")
		} else {
			item.Date = ""
		}
		item.Title = terminalText(item.Title)
		if item.Title != "" {
			feed.News = append(feed.News, item)
		}
	}
	return feed, nil
}

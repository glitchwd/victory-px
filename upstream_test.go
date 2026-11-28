package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpstreamPublicRequests(t *testing.T) {
	const sha = "4eb6cfdf9e61cb112f1f811344ec0dd309eddd6c"
	for _, releaseStatus := range []int{http.StatusOK, http.StatusNotFound, http.StatusForbidden} {
		t.Run(fmt.Sprint(releaseStatus), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.ContentLength != 0 || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("update requests must be unauthenticated reads")
				}
				switch r.URL.RequestURI() {
				case "/commits?sha=a-publier&per_page=1":
					fmt.Fprintf(w, `[{"sha":%q,"commit":{"message":"Shader\u001b[31m fix\nOther text"}}]`, sha)
				case "/releases/latest":
					w.WriteHeader(releaseStatus)
					fmt.Fprint(w, `{"tag_name":"v1.2"}`)
				default:
					t.Errorf("unexpected request: %s", r.URL.RequestURI())
				}
			}))
			defer server.Close()
			result := readUpstream(context.Background(), server.Client(), server.URL)
			if result.err != nil || result.sha != sha || result.subject != "Shader fix" || result.checkedAt.IsZero() {
				t.Fatalf("unexpected branch result: %+v", result)
			}
			want := map[int]string{http.StatusOK: "v1.2", http.StatusNotFound: "None published"}[releaseStatus]
			if result.release != want || (result.releaseErr != nil) != (releaseStatus == http.StatusForbidden) {
				t.Fatalf("unexpected release result: %+v", result)
			}
		})
	}
}

func TestCompareCheckoutUsesLocalObjects(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		prefix := []string{"-C", dir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}
		out, err := exec.Command("git", append(prefix, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "--object-format=sha1")
	git("remote", "add", "origin", "https://github.com/"+upstreamRepo+".git")
	git("commit", "--allow-empty", "-m", "One")
	first := git("rev-parse", "HEAD")
	git("commit", "--allow-empty", "-m", "Two")
	second := git("rev-parse", "HEAD")
	check := func(sha, want string) {
		t.Helper()
		if got := compareCheckout(context.Background(), dir, sha); got != want {
			t.Fatalf("compareCheckout(%q) = %q, want %q", sha, got, want)
		}
	}
	check(second, "Matches checked-out revision")
	check(first, "Included in checked-out revision")
	check(strings.Repeat("f", 40), "Upstream commit absent locally")
	git("checkout", "--detach", first)
	check(second, "Upstream commit outside checkout history")
	git("remote", "set-url", "origin", "https://example.invalid/private.git")
	check(first, "Checkout comparison unavailable")
}

func TestInstallLatestRelease(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "victory.exe")
	if err := os.WriteFile(target, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `[{"tag_name":"v2026.11.28.01","assets":[{"name":"victory.exe","browser_download_url":"%s/download/victory.exe"}]}]`, serverURL)
		case "/download/victory.exe":
			w.Write([]byte("new-victory-exe"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	build := forkBuild{Name: nvidiaForkName, Dir: dir, Binary: target}
	res := installLatestRelease(context.Background(), server.Client(), server.URL+"/releases", build)
	if res.err != nil {
		t.Fatalf("installLatestRelease failed: %v", res.err)
	}
	if res.tag != "v2026.11.28.01" {
		t.Fatalf("got tag %q, want v2026.11.28.01", res.tag)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new-victory-exe" {
		t.Fatalf("got binary content %q, want new-victory-exe", string(got))
	}
}

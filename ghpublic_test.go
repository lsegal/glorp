package main

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func newPublicHeader(link string) http.Header {
	header := http.Header{}
	if link != "" {
		header.Set("Link", link)
	}
	return header
}

func TestIsPublicRepoWithoutCacheNeverCallsNetwork(t *testing.T) {
	gh := GHCLI{publicAPI: func(context.Context, string) ([]byte, http.Header, int, error) {
		t.Fatal("publicAPI should not be called when publicRepoCache is nil")
		return nil, nil, 0, nil
	}}
	if gh.isPublicRepo(context.Background(), "owner/repo") {
		t.Fatal("isPublicRepo() = true, want false without a cache")
	}
}

func TestIsPublicRepoCachesResult(t *testing.T) {
	calls := 0
	gh := GHCLI{
		publicRepoCache: &sync.Map{},
		publicAPI: func(context.Context, string) ([]byte, http.Header, int, error) {
			calls++
			return []byte(`{"private":false}`), nil, http.StatusOK, nil
		},
	}
	if !gh.isPublicRepo(context.Background(), "owner/repo") {
		t.Fatal("isPublicRepo() = false, want true")
	}
	if !gh.isPublicRepo(context.Background(), "owner/repo") {
		t.Fatal("isPublicRepo() = false on second call, want true")
	}
	if calls != 1 {
		t.Fatalf("publicAPI calls = %d, want 1 (result should be cached)", calls)
	}
}

func TestIsPublicRepoFalseForPrivateRepo(t *testing.T) {
	gh := GHCLI{
		publicRepoCache: &sync.Map{},
		publicAPI: func(context.Context, string) ([]byte, http.Header, int, error) {
			return []byte(`{"private":true}`), nil, http.StatusOK, nil
		},
	}
	if gh.isPublicRepo(context.Background(), "owner/repo") {
		t.Fatal("isPublicRepo() = true, want false for a private repo")
	}
}

func TestIsPublicRepoFalseOnRequestFailure(t *testing.T) {
	gh := GHCLI{
		publicRepoCache: &sync.Map{},
		publicAPI: func(context.Context, string) ([]byte, http.Header, int, error) {
			return nil, nil, 0, errors.New("network unreachable")
		},
	}
	if gh.isPublicRepo(context.Background(), "owner/repo") {
		t.Fatal("isPublicRepo() = true, want false when the probe request fails")
	}
}

func TestIsPublicRepoFalseOn404(t *testing.T) {
	gh := GHCLI{
		publicRepoCache: &sync.Map{},
		publicAPI: func(context.Context, string) ([]byte, http.Header, int, error) {
			return []byte(`{"message":"Not Found"}`), nil, http.StatusNotFound, nil
		},
	}
	if gh.isPublicRepo(context.Background(), "owner/repo") {
		t.Fatal("isPublicRepo() = true, want false on a 404")
	}
}

func TestApiGETUsesPublicAPIWhenPublic(t *testing.T) {
	var requestedURL string
	ranGH := false
	gh := GHCLI{
		publicAPI: func(_ context.Context, requestURL string) ([]byte, http.Header, int, error) {
			requestedURL = requestURL
			return []byte(`{"state":"open"}`), nil, http.StatusOK, nil
		},
		runCommand: func(context.Context, ...string) ([]byte, error) {
			ranGH = true
			return nil, nil
		},
	}
	body, err := gh.apiGET(context.Background(), true, "repos/owner/repo/issues/7")
	if err != nil || string(body) != `{"state":"open"}` {
		t.Fatalf("apiGET() = (%q, %v)", body, err)
	}
	if ranGH {
		t.Fatal("apiGET() ran the gh CLI even though the public request succeeded")
	}
	if requestedURL != "https://api.github.com/repos/owner/repo/issues/7" {
		t.Fatalf("requested URL = %q", requestedURL)
	}
}

func TestApiGETFallsBackToGHOnPublicFailure(t *testing.T) {
	var ghArgs []string
	gh := GHCLI{
		publicAPI: func(context.Context, string) ([]byte, http.Header, int, error) {
			return nil, nil, http.StatusForbidden, nil
		},
		runCommand: func(_ context.Context, args ...string) ([]byte, error) {
			ghArgs = args
			return []byte(`{"state":"open"}`), nil
		},
	}
	body, err := gh.apiGET(context.Background(), true, "repos/owner/repo/issues/7")
	if err != nil || string(body) != `{"state":"open"}` {
		t.Fatalf("apiGET() = (%q, %v)", body, err)
	}
	want := []string{"api", "repos/owner/repo/issues/7"}
	if !reflect.DeepEqual(ghArgs, want) {
		t.Fatalf("gh args = %#v, want %#v", ghArgs, want)
	}
}

func TestApiGETSkipsPublicAPIWhenNotPublic(t *testing.T) {
	gh := GHCLI{
		publicAPI: func(context.Context, string) ([]byte, http.Header, int, error) {
			t.Fatal("publicAPI should not be called when public is false")
			return nil, nil, 0, nil
		},
		runCommand: func(context.Context, ...string) ([]byte, error) {
			return []byte(`{"state":"open"}`), nil
		},
	}
	if _, err := gh.apiGET(context.Background(), false, "repos/owner/repo/issues/7"); err != nil {
		t.Fatalf("apiGET() error = %v", err)
	}
}

func TestApiGETPassesExtraGHArgsOnFallback(t *testing.T) {
	var ghArgs []string
	gh := GHCLI{
		runCommand: func(_ context.Context, args ...string) ([]byte, error) {
			ghArgs = args
			return []byte(`[]`), nil
		},
	}
	if _, err := gh.apiGET(context.Background(), false, "repos/owner/repo/issues/7/dependencies/blocked_by", "--header", "X-GitHub-Api-Version: 2022-11-28"); err != nil {
		t.Fatalf("apiGET() error = %v", err)
	}
	want := []string{"api", "repos/owner/repo/issues/7/dependencies/blocked_by", "--header", "X-GitHub-Api-Version: 2022-11-28"}
	if !reflect.DeepEqual(ghArgs, want) {
		t.Fatalf("gh args = %#v, want %#v", ghArgs, want)
	}
}

func TestApiGETPaginatedFollowsLinkHeaderWhenPublic(t *testing.T) {
	calls := 0
	gh := GHCLI{
		publicAPI: func(_ context.Context, requestURL string) ([]byte, http.Header, int, error) {
			calls++
			if calls == 1 {
				if requestURL != "https://api.github.com/repos/owner/repo/issues/7/comments" {
					t.Fatalf("first request URL = %q", requestURL)
				}
				return []byte(`[{"body":"first"}]`), newPublicHeader(`<https://api.github.com/repos/owner/repo/issues/7/comments?page=2>; rel="next"`), http.StatusOK, nil
			}
			if requestURL != "https://api.github.com/repos/owner/repo/issues/7/comments?page=2" {
				t.Fatalf("second request URL = %q", requestURL)
			}
			return []byte(`[{"body":"second"}]`), newPublicHeader(""), http.StatusOK, nil
		},
	}
	body, err := gh.apiGETPaginated(context.Background(), true, "repos/owner/repo/issues/7/comments")
	if err != nil {
		t.Fatalf("apiGETPaginated() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("public API calls = %d, want 2", calls)
	}
	want := `[{"body":"first"},{"body":"second"}]`
	if string(body) != want {
		t.Fatalf("apiGETPaginated() = %s, want %s", body, want)
	}
}

func TestApiGETPaginatedFallsBackToGHOnFailure(t *testing.T) {
	var ghArgs []string
	gh := GHCLI{
		publicAPI: func(context.Context, string) ([]byte, http.Header, int, error) {
			return nil, nil, http.StatusForbidden, nil
		},
		runCommand: func(_ context.Context, args ...string) ([]byte, error) {
			ghArgs = args
			return []byte(`[{"body":"from gh"}]`), nil
		},
	}
	body, err := gh.apiGETPaginated(context.Background(), true, "repos/owner/repo/issues/7/comments")
	if err != nil || string(body) != `[{"body":"from gh"}]` {
		t.Fatalf("apiGETPaginated() = (%s, %v)", body, err)
	}
	want := []string{"api", "repos/owner/repo/issues/7/comments", "--paginate"}
	if !reflect.DeepEqual(ghArgs, want) {
		t.Fatalf("gh args = %#v, want %#v", ghArgs, want)
	}
}

func TestNextPageURL(t *testing.T) {
	for _, test := range []struct {
		name string
		link string
		want string
	}{
		{name: "no link header", link: "", want: ""},
		{name: "next only", link: `<https://api.github.com/x?page=2>; rel="next"`, want: "https://api.github.com/x?page=2"},
		{name: "prev and next", link: `<https://api.github.com/x?page=1>; rel="prev", <https://api.github.com/x?page=3>; rel="next"`, want: "https://api.github.com/x?page=3"},
		{name: "last only, no next", link: `<https://api.github.com/x?page=9>; rel="last"`, want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := nextPageURL(test.link); got != test.want {
				t.Fatalf("nextPageURL(%q) = %q, want %q", test.link, got, test.want)
			}
		})
	}
}

func TestPublicIssueSearchQuery(t *testing.T) {
	for _, test := range []struct {
		name      string
		repo      string
		filter    string
		allIssues bool
		selfLogin string
		want      string
	}{
		{name: "default filter substitutes self login", repo: "owner/repo", filter: defaultIssueFilter, selfLogin: "lsegal", want: "repo:owner/repo is:issue state:open is:issue state:open author:lsegal"},
		{name: "all issues ignores filter", repo: "owner/repo", filter: defaultIssueFilter, allIssues: true, selfLogin: "lsegal", want: "repo:owner/repo is:issue state:open"},
		{name: "custom filter without author", repo: "owner/repo", filter: "label:bug", want: "repo:owner/repo is:issue state:open label:bug"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := publicIssueSearchQuery(test.repo, test.filter, test.allIssues, test.selfLogin); got != test.want {
				t.Fatalf("publicIssueSearchQuery() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestResolveSelfLoginCaches(t *testing.T) {
	calls := 0
	gh := GHCLI{
		selfLoginCache: &sync.Map{},
		runCommand: func(context.Context, ...string) ([]byte, error) {
			calls++
			return []byte("lsegal\n"), nil
		},
	}
	login, err := gh.resolveSelfLogin(context.Background())
	if err != nil || login != "lsegal" {
		t.Fatalf("resolveSelfLogin() = (%q, %v)", login, err)
	}
	if _, err := gh.resolveSelfLogin(context.Background()); err != nil {
		t.Fatalf("resolveSelfLogin() second call error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("gh calls = %d, want 1 (login should be cached)", calls)
	}
}

func TestListPublicIssuesResolvesSelfLoginAndFiltersPullRequests(t *testing.T) {
	var searchURL string
	gh := GHCLI{
		selfLoginCache: &sync.Map{},
		runCommand: func(context.Context, ...string) ([]byte, error) {
			return []byte("lsegal\n"), nil
		},
		publicAPI: func(_ context.Context, requestURL string) ([]byte, http.Header, int, error) {
			searchURL = requestURL
			return []byte(`{"items":[{"number":7,"title":"bug","body":"details","state":"open","created_at":"2026-07-20T17:38:43Z","labels":[{"name":"agent-ready"}]},{"number":8,"title":"a pr","pull_request":{}}]}`), nil, http.StatusOK, nil
		},
	}
	issues, ok := gh.listPublicIssues(context.Background(), "owner/repo", defaultIssueFilter, false)
	if !ok {
		t.Fatal("listPublicIssues() ok = false, want true")
	}
	if len(issues) != 1 || issues[0].Number != 7 || issues[0].Title != "bug" || len(issues[0].Labels) != 1 || issues[0].Labels[0].Name != "agent-ready" {
		t.Fatalf("issues = %#v", issues)
	}
	if !strings.Contains(searchURL, "repo%3Aowner%2Frepo") || !strings.Contains(searchURL, "author%3Alsegal") {
		t.Fatalf("search URL = %q, want it to encode repo and resolved author qualifiers", searchURL)
	}
}

func TestListPublicIssuesFallsBackOnFailure(t *testing.T) {
	gh := GHCLI{
		publicAPI: func(context.Context, string) ([]byte, http.Header, int, error) {
			return nil, nil, http.StatusForbidden, nil
		},
	}
	if _, ok := gh.listPublicIssues(context.Background(), "owner/repo", "", true); ok {
		t.Fatal("listPublicIssues() ok = true, want false on request failure")
	}
}

func TestListIssuesUsesPublicSearchAPIForPublicRepo(t *testing.T) {
	gh := GHCLI{
		publicRepoCache: &sync.Map{},
		runCommand: func(context.Context, ...string) ([]byte, error) {
			t.Fatal("gh CLI should not run for a public repo's issue list")
			return nil, nil
		},
		publicAPI: func(_ context.Context, requestURL string) ([]byte, http.Header, int, error) {
			switch requestURL {
			case "https://api.github.com/repos/owner/repo":
				return []byte(`{"private":false}`), nil, http.StatusOK, nil
			case "https://api.github.com/repos/owner/repo/issues/7/dependencies/blocked_by":
				return []byte(`[]`), nil, http.StatusOK, nil
			}
			return []byte(`{"items":[{"number":7,"title":"bug","state":"open","created_at":"2026-07-20T17:38:43Z"}]}`), nil, http.StatusOK, nil
		},
	}
	gh.AllIssues = true
	issues, err := gh.ListIssues(context.Background(), "owner/repo")
	if err != nil {
		t.Fatalf("ListIssues() error = %v", err)
	}
	if len(issues) != 1 || issues[0].Number != 7 {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestListIssuesFallsBackToGHWhenNotPublic(t *testing.T) {
	publicAPICalls := 0
	var calls [][]string
	gh := GHCLI{
		publicRepoCache: &sync.Map{},
		publicAPI: func(context.Context, string) ([]byte, http.Header, int, error) {
			publicAPICalls++
			return []byte(`{"private":true}`), nil, http.StatusOK, nil
		},
		runCommand: func(_ context.Context, args ...string) ([]byte, error) {
			calls = append(calls, append([]string(nil), args...))
			if args[0] == "issue" {
				return []byte(`[{"number":7,"title":"bug"}]`), nil
			}
			return []byte(`[]`), nil
		},
	}
	gh.Filter = defaultIssueFilter
	issues, err := gh.ListIssues(context.Background(), "owner/repo")
	if err != nil {
		t.Fatalf("ListIssues() error = %v", err)
	}
	if len(issues) != 1 || issues[0].Number != 7 {
		t.Fatalf("issues = %#v", issues)
	}
	want := issueListArgs("owner/repo", defaultIssueFilter, false)
	if len(calls) == 0 || !reflect.DeepEqual(calls[0], want) {
		t.Fatalf("gh calls = %#v, want first call %#v", calls, want)
	}
	if publicAPICalls != 1 {
		t.Fatalf("publicAPI calls = %d, want 1 (only the visibility probe, not an issue search)", publicAPICalls)
	}
}

func TestOriginatingWorkStateUsesPublicAPIForPublicRepo(t *testing.T) {
	ranGH := false
	responses := []string{
		`{"private":false}`,
		`{"state":"open"}`,
		`[]`,
	}
	call := 0
	gh := GHCLI{
		publicRepoCache: &sync.Map{},
		runCommand: func(context.Context, ...string) ([]byte, error) {
			ranGH = true
			return nil, errors.New("should not run gh")
		},
		publicAPI: func(context.Context, string) ([]byte, http.Header, int, error) {
			body := responses[call]
			call++
			return []byte(body), nil, http.StatusOK, nil
		},
	}
	state, err := gh.OriginatingWorkState(context.Background(), "owner/repo", 7)
	if err != nil || state.IssueState != "open" {
		t.Fatalf("OriginatingWorkState() = (%#v, %v)", state, err)
	}
	if ranGH {
		t.Fatal("OriginatingWorkState() ran the gh CLI for a public repo")
	}
}

func TestLoadDependenciesUsesPublicAPIForPublicRepo(t *testing.T) {
	ranGH := false
	gh := GHCLI{
		publicRepoCache: &sync.Map{},
		runCommand: func(context.Context, ...string) ([]byte, error) {
			ranGH = true
			return nil, errors.New("should not run gh")
		},
		publicAPI: func(_ context.Context, requestURL string) ([]byte, http.Header, int, error) {
			switch requestURL {
			case "https://api.github.com/repos/owner/repo":
				return []byte(`{"private":false}`), nil, http.StatusOK, nil
			case "https://api.github.com/repos/owner/repo/issues/4":
				return []byte(`{"state":"open"}`), nil, http.StatusOK, nil
			case "https://api.github.com/repos/owner/repo/issues/9/dependencies/blocked_by":
				return []byte(`[]`), nil, http.StatusOK, nil
			}
			t.Fatalf("unexpected request URL: %s", requestURL)
			return nil, nil, 0, nil
		},
	}
	issue := &Issue{Number: 9, Body: "depends on #4"}
	if err := gh.loadDependencies(context.Background(), "owner/repo", issue); err != nil {
		t.Fatalf("loadDependencies() error = %v", err)
	}
	if len(issue.DependsOn) != 1 || issue.DependsOn[0].Number != 4 || issue.DependsOn[0].State != "open" {
		t.Fatalf("DependsOn = %#v", issue.DependsOn)
	}
	if ranGH {
		t.Fatal("loadDependencies() ran the gh CLI for a public repo")
	}
}

func TestListCommentsUsesPublicAPIForPublicRepo(t *testing.T) {
	ranGH := false
	gh := GHCLI{
		publicRepoCache: &sync.Map{},
		runCommand: func(context.Context, ...string) ([]byte, error) {
			ranGH = true
			return nil, errors.New("should not run gh")
		},
		publicAPI: func(_ context.Context, requestURL string) ([]byte, http.Header, int, error) {
			if requestURL == "https://api.github.com/repos/owner/repo" {
				return []byte(`{"private":false}`), nil, http.StatusOK, nil
			}
			return []byte(`[{"body":"hi","created_at":"2026-07-20T12:00:00Z"}]`), newPublicHeader(""), http.StatusOK, nil
		},
	}
	comments, err := gh.ListComments(context.Background(), "owner/repo", 7)
	if err != nil {
		t.Fatalf("ListComments() error = %v", err)
	}
	if len(comments) != 1 || comments[0].Body != "hi" {
		t.Fatalf("comments = %#v", comments)
	}
	if ranGH {
		t.Fatal("ListComments() ran the gh CLI for a public repo")
	}
}

// Package github implements the GitHub REST API provider.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/erikibolya/git-airlock/internal/provider"
)

const defaultAPI = "https://api.github.com"

// Provider talks to github.com or GitHub Enterprise.
type Provider struct {
	api    string
	repo   string
	owner  string
	token  string
	client *http.Client
}

// New builds a GitHub provider. api may be empty for github.com.
func New(api, repo, token string, client *http.Client) *Provider {
	if api == "" {
		api = defaultAPI
	}
	owner := repo
	if i := strings.Index(repo, "/"); i > 0 {
		owner = repo[:i]
	}
	return &Provider{
		api:    strings.TrimRight(api, "/"),
		repo:   repo,
		owner:  owner,
		token:  token,
		client: client,
	}
}

func (p *Provider) Name() string { return "github" }

func (p *Provider) Ping(ctx context.Context) error {
	var user struct {
		Login string `json:"login"`
	}
	if err := p.do(ctx, http.MethodGet, "/user", nil, &user); err != nil {
		return err
	}
	if user.Login == "" {
		return fmt.Errorf("empty user")
	}
	return nil
}

func (p *Provider) FindOpenPR(ctx context.Context, head, base string) (*provider.PR, error) {
	q := url.Values{}
	q.Set("state", "open")
	q.Set("head", p.owner+":"+head)
	q.Set("base", base)
	q.Set("per_page", "100")
	var list []apiPR
	if err := p.do(ctx, http.MethodGet, "/repos/"+p.repo+"/pulls?"+q.Encode(), nil, &list); err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Head.Ref == head && list[i].Base.Ref == base {
			return list[i].toPR(), nil
		}
	}
	return nil, nil
}

func (p *Provider) FindPR(ctx context.Context, head, base string) (*provider.PR, error) {
	q := url.Values{}
	q.Set("state", "all")
	q.Set("head", p.owner+":"+head)
	q.Set("base", base)
	q.Set("sort", "updated")
	q.Set("direction", "desc")
	q.Set("per_page", "50")
	var list []apiPR
	if err := p.do(ctx, http.MethodGet, "/repos/"+p.repo+"/pulls?"+q.Encode(), nil, &list); err != nil {
		return nil, err
	}
	var best *provider.PR
	for i := range list {
		if list[i].Head.Ref != head || list[i].Base.Ref != base {
			continue
		}
		if best == nil || list[i].Number > best.Number {
			best = list[i].toPR()
		}
	}
	return best, nil
}

// ListPRs returns the PRs targeting base in the given state ("open" or "all"), newest
// first, so Auto-PR can match branches locally instead of querying per branch.
func (p *Provider) ListPRs(ctx context.Context, base, state string) ([]*provider.PR, error) {
	q := url.Values{}
	if state != "" {
		q.Set("state", state)
	}
	if base != "" {
		q.Set("base", base)
	}
	q.Set("sort", "updated")
	q.Set("direction", "desc")
	q.Set("per_page", "100")
	var list []apiPR
	if err := p.do(ctx, http.MethodGet, "/repos/"+p.repo+"/pulls?"+q.Encode(), nil, &list); err != nil {
		return nil, err
	}
	out := make([]*provider.PR, 0, len(list))
	for i := range list {
		if base != "" && list[i].Base.Ref != base {
			continue
		}
		out = append(out, list[i].toPR())
	}
	return out, nil
}

func (p *Provider) CreatePR(ctx context.Context, req provider.CreateRequest) (*provider.PR, error) {
	body := map[string]any{
		"head":  req.Head,
		"base":  req.Base,
		"title": req.Title,
		"body":  req.Body,
	}
	var pr apiPR
	err := p.do(ctx, http.MethodPost, "/repos/"+p.repo+"/pulls", body, &pr)
	if err != nil {
		var apiErr *provider.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnprocessableEntity || apiErr.Status == http.StatusConflict) {
			return nil, provider.ErrAlreadyExists
		}
		return nil, err
	}
	return pr.toPR(), nil
}

func (p *Provider) GetPR(ctx context.Context, number int) (*provider.PR, error) {
	var pr apiPR
	if err := p.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d", p.repo, number), nil, &pr); err != nil {
		return nil, err
	}
	return pr.toPR(), nil
}

func (p *Provider) Checks(ctx context.Context, pr *provider.PR) (provider.Checks, error) {
	if pr.HeadSHA == "" {
		return provider.ChecksPending, nil
	}
	var runs checkRunsResponse
	runsErr := p.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=100&filter=latest", p.repo, pr.HeadSHA), nil, &runs)
	var combined combinedStatus
	statusErr := p.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/commits/%s/status", p.repo, pr.HeadSHA), nil, &combined)
	if runsErr != nil && provider.Retryable(runsErr) {
		return provider.ChecksPending, runsErr
	}
	if statusErr != nil && provider.Retryable(statusErr) {
		return provider.ChecksPending, statusErr
	}
	return evaluate(runs, combined), nil
}

func (p *Provider) Merge(ctx context.Context, number int, strategy string) error {
	method, ok := provider.NormalizeMergeStrategy(strategy)
	if !ok {
		return fmt.Errorf("unknown merge strategy %q", strategy)
	}
	return p.do(ctx, http.MethodPut, fmt.Sprintf("/repos/%s/pulls/%d/merge", p.repo, number),
		map[string]any{"merge_method": method}, nil)
}

func (p *Provider) DeleteBranch(ctx context.Context, branch string) error {
	err := p.do(ctx, http.MethodDelete, fmt.Sprintf("/repos/%s/git/refs/heads/%s", p.repo, branch), nil, nil)
	if provider.IsNotFound(err) {
		return nil
	}
	return err
}

func (p *Provider) do(ctx context.Context, method, path string, in, out any) error {
	var reader io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.api+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &provider.APIError{Op: method + " " + path, Status: resp.StatusCode, Body: string(data)}
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
	}
	return nil
}

type apiPR struct {
	Number    int    `json:"number"`
	HTMLURL   string `json:"html_url"`
	State     string `json:"state"`
	Merged    bool   `json:"merged"`
	Mergeable *bool  `json:"mergeable"`
	Head      struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (p apiPR) toPR() *provider.PR {
	mergeable := p.Mergeable != nil && *p.Mergeable
	return &provider.PR{
		Number:    p.Number,
		URL:       p.HTMLURL,
		State:     p.State,
		Merged:    p.Merged,
		Mergeable: mergeable,
		Head:      p.Head.Ref,
		HeadSHA:   p.Head.SHA,
	}
}

type checkRunsResponse struct {
	CheckRuns []struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	} `json:"check_runs"`
}

type combinedStatus struct {
	State      string `json:"state"`
	TotalCount int    `json:"total_count"`
}

func evaluate(runs checkRunsResponse, combined combinedStatus) provider.Checks {
	total := 0
	failed := false
	pending := false
	for _, r := range runs.CheckRuns {
		total++
		switch r.Conclusion {
		case "failure", "cancelled", "timed_out", "action_required", "stale", "startup_failure":
			failed = true
		case "success", "neutral", "skipped":
			// ok
		default:
			pending = true // queued or in_progress
		}
		if r.Status != "completed" {
			pending = true
		}
	}
	if combined.TotalCount > 0 {
		total++
		switch combined.State {
		case "failure":
			failed = true
		case "success":
		default:
			pending = true
		}
	}
	if total == 0 {
		return provider.ChecksNone
	}
	if failed {
		return provider.ChecksFailure
	}
	if pending {
		return provider.ChecksPending
	}
	return provider.ChecksSuccess
}

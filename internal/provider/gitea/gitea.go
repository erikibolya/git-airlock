// Package gitea implements the Gitea REST API provider.
package gitea

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/erikibolya/git-airlock/internal/provider"
)

// Provider talks to a Gitea instance.
type Provider struct {
	api    string
	repo   string
	token  string
	client *http.Client
}

// New builds a Gitea provider. api is the REST base, e.g.
// https://gitea.example/api/v1.
func New(api, repo, token string, client *http.Client) *Provider {
	return &Provider{
		api:    strings.TrimRight(api, "/"),
		repo:   repo,
		token:  token,
		client: client,
	}
}

func (p *Provider) Name() string { return "gitea" }

func (p *Provider) Ping(ctx context.Context) error {
	var version struct {
		Version string `json:"version"`
	}
	if err := p.do(ctx, http.MethodGet, "/version", nil, &version); err != nil {
		return err
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := p.do(ctx, http.MethodGet, "/user", nil, &user); err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	if user.Login == "" {
		return fmt.Errorf("auth: empty user")
	}
	return nil
}

func (p *Provider) FindOpenPR(ctx context.Context, head, base string) (*provider.PR, error) {
	var list []apiPR
	if err := p.do(ctx, http.MethodGet, "/repos/"+p.repo+"/pulls?state=open&limit=50", nil, &list); err != nil {
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
	var list []apiPR
	if err := p.do(ctx, http.MethodGet, "/repos/"+p.repo+"/pulls?state=all&limit=50", nil, &list); err != nil {
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
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict {
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
	var status statusResponse
	if err := p.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/commits/%s/status", p.repo, pr.HeadSHA), nil, &status); err != nil {
		if provider.IsNotFound(err) {
			return provider.ChecksNone, nil
		}
		return provider.ChecksPending, err
	}
	return evaluate(status), nil
}

func (p *Provider) Merge(ctx context.Context, number int) error {
	return p.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/pulls/%d/merge", p.repo, number),
		map[string]any{"Do": "merge"}, nil)
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
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "token "+p.token)
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
	Mergeable bool   `json:"mergeable"`
	Head      struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (p apiPR) toPR() *provider.PR {
	return &provider.PR{
		Number:    p.Number,
		URL:       p.HTMLURL,
		State:     p.State,
		Merged:    p.Merged,
		Mergeable: p.Mergeable,
		HeadSHA:   p.Head.SHA,
	}
}

type statusResponse struct {
	State    string `json:"state"`
	Statuses []struct {
		Status  string `json:"status"`
		Context string `json:"context"`
	} `json:"statuses"`
}

// evaluate gates on pull_request contexts only. Gitea cancels push runs when a
// newer push arrives and reports the cancelled status as failure, which would
// otherwise block every merge.
func evaluate(status statusResponse) provider.Checks {
	seen := 0
	failed := false
	allSuccess := true
	for _, s := range status.Statuses {
		if !strings.Contains(strings.ToLower(s.Context), "pull_request") {
			continue
		}
		seen++
		switch s.Status {
		case "success":
		case "failure", "error":
			failed = true
			allSuccess = false
		default:
			allSuccess = false
		}
	}
	if seen == 0 {
		return provider.ChecksPending
	}
	if failed {
		return provider.ChecksFailure
	}
	if allSuccess {
		return provider.ChecksSuccess
	}
	return provider.ChecksPending
}

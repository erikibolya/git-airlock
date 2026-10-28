// Package gitlab implements the GitLab REST API provider.
package gitlab

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

const defaultAPI = "https://gitlab.com/api/v4"

// Provider talks to gitlab.com or a self-hosted GitLab.
type Provider struct {
	api     string
	project string
	token   string
	client  *http.Client
}

// New builds a GitLab provider. api may be empty for gitlab.com.
func New(api, repo, token string, client *http.Client) *Provider {
	if api == "" {
		api = defaultAPI
	}
	return &Provider{
		api:     strings.TrimRight(api, "/"),
		project: url.PathEscape(repo),
		token:   token,
		client:  client,
	}
}

func (p *Provider) Name() string { return "gitlab" }

func (p *Provider) Ping(ctx context.Context) error {
	var user struct {
		Username string `json:"username"`
	}
	if err := p.do(ctx, http.MethodGet, "/user", nil, &user); err != nil {
		return err
	}
	if user.Username == "" {
		return errors.New("empty user")
	}
	return nil
}

func (p *Provider) FindOpenPR(ctx context.Context, head, base string) (*provider.PR, error) {
	q := url.Values{}
	q.Set("state", "opened")
	q.Set("source_branch", head)
	q.Set("target_branch", base)
	q.Set("per_page", "100")
	var list []mr
	if err := p.do(ctx, http.MethodGet, p.mrPath()+"?"+q.Encode(), nil, &list); err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].SourceBranch == head && list[i].TargetBranch == base {
			return list[i].toPR(), nil
		}
	}
	return nil, nil
}

func (p *Provider) CreatePR(ctx context.Context, req provider.CreateRequest) (*provider.PR, error) {
	body := map[string]any{
		"source_branch": req.Head,
		"target_branch": req.Base,
		"title":         req.Title,
		"description":   req.Body,
	}
	var out mr
	err := p.do(ctx, http.MethodPost, p.mrPath(), body, &out)
	if err != nil {
		var apiErr *provider.APIError
		if errors.As(err, &apiErr) &&
			(apiErr.Status == http.StatusConflict ||
				(apiErr.Status == http.StatusBadRequest && strings.Contains(strings.ToLower(apiErr.Body), "already exists"))) {
			return nil, provider.ErrAlreadyExists
		}
		return nil, err
	}
	return out.toPR(), nil
}

func (p *Provider) GetPR(ctx context.Context, number int) (*provider.PR, error) {
	var out mr
	if err := p.do(ctx, http.MethodGet, fmt.Sprintf("%s/%d", p.mrPath(), number), nil, &out); err != nil {
		return nil, err
	}
	return out.toPR(), nil
}

func (p *Provider) Checks(ctx context.Context, pr *provider.PR) (provider.Checks, error) {
	if pr.HeadSHA == "" {
		return provider.ChecksPending, nil
	}
	q := url.Values{}
	q.Set("sha", pr.HeadSHA)
	q.Set("per_page", "1")
	q.Set("order_by", "id")
	q.Set("sort", "desc")
	var pipelines []struct {
		Status string `json:"status"`
	}
	err := p.do(ctx, http.MethodGet, fmt.Sprintf("/projects/%s/pipelines?%s", p.project, q.Encode()), nil, &pipelines)
	if err != nil {
		if provider.IsNotFound(err) {
			return provider.ChecksNone, nil
		}
		return provider.ChecksPending, err
	}
	if len(pipelines) == 0 {
		return provider.ChecksNone, nil
	}
	return evaluate(pipelines[0].Status), nil
}

func (p *Provider) Merge(ctx context.Context, number int) error {
	return p.do(ctx, http.MethodPut, fmt.Sprintf("%s/%d/merge", p.mrPath(), number), map[string]any{}, nil)
}

func (p *Provider) mrPath() string {
	return "/projects/" + p.project + "/merge_requests"
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
	req.Header.Set("PRIVATE-TOKEN", p.token)
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

type mr struct {
	IID          int    `json:"iid"`
	WebURL       string `json:"web_url"`
	State        string `json:"state"`
	MergeStatus  string `json:"merge_status"`
	SHA          string `json:"sha"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
}

func (m mr) toPR() *provider.PR {
	merged := m.State == "merged"
	return &provider.PR{
		Number:    m.IID,
		URL:       m.WebURL,
		State:     m.State,
		Merged:    merged,
		Mergeable: m.MergeStatus == "can_be_merged",
		HeadSHA:   m.SHA,
	}
}

func evaluate(status string) provider.Checks {
	switch status {
	case "success":
		return provider.ChecksSuccess
	case "failed", "canceled":
		return provider.ChecksFailure
	case "":
		return provider.ChecksNone
	default:
		return provider.ChecksPending
	}
}

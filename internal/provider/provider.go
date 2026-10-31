// Package provider defines the git hosting operations Git Airlock needs.
package provider

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// PR is the provider-neutral pull request shape.
type PR struct {
	Number    int
	URL       string
	State     string
	Merged    bool
	Mergeable bool
	// Head is the source branch name; used for branch cleanup after merging.
	Head    string
	HeadSHA string
}

// Checks is the aggregated CI state of a PR head.
type Checks string

const (
	ChecksSuccess Checks = "success"
	ChecksPending Checks = "pending"
	ChecksFailure Checks = "failure"
	ChecksNone    Checks = "none"
)

// MergeStrategy selects how the provider combines the PR branch.
const (
	MergeStrategyMerge  = "merge"
	MergeStrategySquash = "squash"
	MergeStrategyRebase = "rebase"
)

// NormalizeMergeStrategy returns the default for an empty value and reports
// whether the strategy is known.
func NormalizeMergeStrategy(s string) (string, bool) {
	switch s {
	case "":
		return MergeStrategyMerge, true
	case MergeStrategyMerge, MergeStrategySquash, MergeStrategyRebase:
		return s, true
	default:
		return s, false
	}
}

// CreateRequest describes a new pull request.
type CreateRequest struct {
	Head  string
	Base  string
	Title string
	Body  string
}

// Provider is implemented by gitea and github.
type Provider interface {
	Name() string
	Ping(ctx context.Context) error
	FindOpenPR(ctx context.Context, head, base string) (*PR, error)
	// FindPR returns the newest PR for the branch pair in any state, so the
	// worker can tell "already merged" from "needs a new PR".
	FindPR(ctx context.Context, head, base string) (*PR, error)
	// ListPRs returns the PRs targeting base in the given state ("open", "all"),
	// newest first. Auto-PR uses it once per cycle instead of two queries per
	// branch; Head is always set so callers can match branches locally.
	ListPRs(ctx context.Context, base, state string) ([]*PR, error)
	CreatePR(ctx context.Context, req CreateRequest) (*PR, error)
	GetPR(ctx context.Context, number int) (*PR, error)
	Checks(ctx context.Context, pr *PR) (Checks, error)
	Merge(ctx context.Context, number int, strategy string) error
	// DeleteBranch removes a branch after a successful merge.
	DeleteBranch(ctx context.Context, branch string) error
}

// ErrAlreadyExists means an open PR already exists for the branch pair.
var ErrAlreadyExists = errors.New("pull request already exists")

// APIError is a non-2xx response from a provider.
type APIError struct {
	Op     string
	Status int
	Body   string
}

func (e *APIError) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 300 {
		body = body[:300] + "..."
	}
	return fmt.Sprintf("%s: HTTP %d: %s", e.Op, e.Status, body)
}

// Retryable reports whether the worker should leave the request queued.
func Retryable(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == http.StatusTooManyRequests || apiErr.Status >= 500
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	return false
}

// IsNotFound reports a 404.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

// NewHTTPClient builds a client with an optional private CA bundle.
func NewHTTPClient(caPath string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	if caPath != "" {
		pem, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("read CA: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates found in %s", caPath)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: transport}, nil
}

// ResolveToken finds an API token without ever printing it.
//
// Order: AIRLOCK_TOKEN, <PROVIDER>_TOKEN, `gh auth token` (GitHub), and
// finally the userinfo embedded in the private remote URL.
func ResolveToken(providerName, remoteURL string) (string, error) {
	for _, key := range []string{"AIRLOCK_TOKEN", strings.ToUpper(providerName) + "_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v, nil
		}
	}
	if providerName == "github" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
		if err == nil {
			if tok := strings.TrimSpace(string(out)); tok != "" {
				return tok, nil
			}
		}
	}
	if remoteURL != "" {
		if u, err := url.Parse(remoteURL); err == nil && u.User != nil {
			if pass, ok := u.User.Password(); ok && pass != "" {
				return pass, nil
			}
			if user := u.User.Username(); strings.Count(user, ":") == 0 && user != "git" {
				return user, nil
			}
		}
	}
	return "", fmt.Errorf("no API token: set AIRLOCK_TOKEN or %s_TOKEN", strings.ToUpper(providerName))
}

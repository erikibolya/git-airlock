package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the repo-local .airlock.toml file.
type Config struct {
	// Repo is the hosting repository path, e.g. "owner/name".
	Repo string `toml:"repo"`
	// Remote is the name of the private git remote (VPN only), e.g. "company".
	Remote string `toml:"remote"`
	// Mirror is the bare repository used as the local push target and sync hub.
	Mirror string `toml:"mirror"`
	// Queue is the shared directory with requests/, results/, processed/.
	Queue string `toml:"queue"`
	// QueueRemote optionally carries refs/airlock/* over git (e.g. ssh://)
	// instead of reading them from the local mirror.
	QueueRemote string `toml:"queue_remote"`
	// Provider is "gitea", "github" or "gitlab".
	Provider string `toml:"provider"`
	// API is the provider REST base URL (required for Gitea).
	API string `toml:"api"`
	// CA is an optional PEM bundle for a private/self-signed CA.
	CA string `toml:"ca"`
	// Base is the default target branch for pull requests.
	Base string `toml:"base"`
	// Merge is the default merge policy: never, when-green or now.
	Merge string `toml:"merge"`
	// MergeStrategy is the provider merge method: merge, squash or rebase.
	MergeStrategy string `toml:"merge_strategy"`
	// AutoPR opens a pull request for every local branch that is ahead of Base.
	AutoPR bool `toml:"auto_pr"`
	// AutoPRIgnore lists branch globs (filepath.Match) that AutoPR skips.
	AutoPRIgnore []string `toml:"auto_pr_ignore"`
}

// Filename is the per-repository config file name.
const Filename = ".airlock.toml"

// Resolve returns the config path to use. An explicit path wins, then
// AIRLOCK_CONFIG, then the nearest .airlock.toml walking up from cwd.
func Resolve(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if p := os.Getenv("AIRLOCK_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, Filename)
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("no %s found (run `airlock init` or pass --config)", Filename)
}

// Load reads, defaults and validates a config file.
func Load(path string) (*Config, error) {
	var c Config
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.expandPaths(); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Remote == "" {
		c.Remote = "company"
	}
	if c.Queue == "" {
		c.Queue = "~/.airlock/queue"
	}
	if c.Base == "" {
		c.Base = "main"
	}
	if c.Merge == "" {
		c.Merge = "when-green"
	}
	if c.MergeStrategy == "" {
		c.MergeStrategy = "merge"
	}
	if c.Mirror == "" && c.Repo != "" {
		c.Mirror = "~/.airlock/" + Slug(c.Repo) + ".git"
	}
}

func (c *Config) expandPaths() error {
	var err error
	if c.Mirror, err = ExpandHome(c.Mirror); err != nil {
		return err
	}
	if c.Queue, err = ExpandHome(c.Queue); err != nil {
		return err
	}
	if c.CA, err = ExpandHome(c.CA); err != nil {
		return err
	}
	return nil
}

// Validate checks the values the rest of the tool relies on.
func (c *Config) Validate() error {
	if c.Repo == "" || !strings.Contains(c.Repo, "/") {
		return errors.New("repo must be owner/name")
	}
	switch c.Provider {
	case "gitea":
		if c.API == "" {
			return errors.New("api is required for provider gitea")
		}
	case "github", "gitlab":
	default:
		return fmt.Errorf("provider must be gitea, github or gitlab, got %q", c.Provider)
	}
	if c.Mirror == "" {
		return errors.New("mirror is required")
	}
	if c.Queue == "" {
		return errors.New("queue is required")
	}
	if c.Base == "" {
		return errors.New("base is required")
	}
	switch c.Merge {
	case "never", "when-green", "now":
	default:
		return fmt.Errorf("merge must be never, when-green or now, got %q", c.Merge)
	}
	switch c.MergeStrategy {
	case "merge", "squash", "rebase":
	default:
		return fmt.Errorf("merge_strategy must be merge, squash or rebase, got %q", c.MergeStrategy)
	}
	return nil
}

// ExpandHome expands a leading ~ to the user's home directory.
func ExpandHome(p string) (string, error) {
	if p == "" || p[0] != '~' {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if p == "~" {
		return home, nil
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}

// Slug turns owner/name into a filesystem-friendly owner-name.
func Slug(repo string) string {
	return strings.ReplaceAll(repo, "/", "-")
}

// Write creates a commented config file.
func Write(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf(`# Git Airlock configuration
# See https://github.com/erikibolya/git-airlock for the full documentation.

# Hosting repository path (owner/name).
repo = %q

# Private git remote that only the workstation can reach (VPN).
remote = %q

# Bare mirror used as the push target for the isolated machine.
mirror = %q

# Shared directory for requests/results, or "git" for refs/airlock/*.
queue = %q

# Optional: push/fetch queue refs to this remote instead of the local mirror.
queue_remote = %q

# gitea, github or gitlab.
provider = %q

# REST API base (Gitea only; GitHub defaults to https://api.github.com).
api = %q

# Optional PEM CA bundle for a private/self-signed certificate.
ca = %q

# Default pull-request target branch.
base = %q

# Default merge policy: never, when-green or now.
merge = %q

# Merge method used by the provider: merge, squash or rebase.
merge_strategy = %q

# Open a pull request automatically for every branch that is ahead of base.
auto_pr = false

# Branch patterns that auto_pr ignores (filepath.Match syntax).
# auto_pr_ignore = ["wip/*", "dependabot/*"]
`, c.Repo, c.Remote, c.Mirror, c.Queue, c.QueueRemote, c.Provider, c.API, c.CA, c.Base, c.Merge, c.MergeStrategy)
	return os.WriteFile(path, []byte(body), 0o644)
}

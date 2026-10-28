// Command airlock lets a network-isolated machine push commits and request
// pull-request actions without giving it access to the private git remote.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/erikibolya/git-airlock/internal/config"
	"github.com/erikibolya/git-airlock/internal/gitutil"
	"github.com/erikibolya/git-airlock/internal/install"
	"github.com/erikibolya/git-airlock/internal/provider"
	"github.com/erikibolya/git-airlock/internal/provider/gitea"
	"github.com/erikibolya/git-airlock/internal/provider/github"
	"github.com/erikibolya/git-airlock/internal/queue"
	"github.com/erikibolya/git-airlock/internal/worker"
)

var version = "dev"

type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			if ee.msg != "" {
				fmt.Fprintln(os.Stderr, "airlock:", ee.msg)
			}
			os.Exit(ee.code)
		}
		fmt.Fprintln(os.Stderr, "airlock:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}
	switch args[0] {
	case "init":
		return cmdInit(args[1:])
	case "sync":
		return cmdSync(ctx, args[1:])
	case "pr":
		return cmdPR(ctx, args[1:])
	case "worker":
		return cmdWorker(ctx, args[1:])
	case "doctor":
		return cmdDoctor(ctx, args[1:])
	case "install-task":
		return cmdInstallTask(args[1:])
	case "instructions":
		return cmdInstructions(args[1:])
	case "version", "--version", "-v":
		fmt.Printf("git-airlock %s\n", versionString())
		return nil
	case "help", "--help", "-h":
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func versionString() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

func printUsage() {
	fmt.Print(`git-airlock - push and merge PRs across a network air gap

Usage:
  airlock init [--provider gitea|github] [--repo owner/name] [--remote company]
               [--api URL] [--ca PEM] [--base BRANCH] [--mirror PATH]
               [--queue PATH] [--configure-git] [--force]
  airlock sync [--config PATH]
  airlock pr open   [--head BRANCH] [--base BRANCH] [--title TEXT] [--body TEXT]
                    [--merge never|when-green|now] [--dry-run]
                    [--wait 2m|--no-wait] [--config PATH]
  airlock pr status <number> [--wait 2m|--no-wait] [--config PATH]
  airlock pr merge  <number> [--when-green] [--wait 2m|--no-wait] [--config PATH]
  airlock worker    [--config PATH] [--no-sync] [--loop 1m]
  airlock doctor    [--config PATH]
  airlock install-task [--config PATH] [--name "Git Airlock"]
                       [--interval 1m] [--print]
  airlock instructions
  airlock version

The isolated machine writes requests into the shared queue; the workstation
worker (with VPN access) executes them. See README.md for the full model.
`)
}

// ---------- config / provider helpers ----------

func loadConfig(path string) (*config.Config, string, error) {
	p, err := config.Resolve(path)
	if err != nil {
		return nil, "", err
	}
	cfg, err := config.Load(p)
	if err != nil {
		return nil, p, err
	}
	return cfg, p, nil
}

func privateRemoteURL(cfg *config.Config) string {
	if u, err := gitutil.RemoteURL(cfg.Mirror, cfg.Remote); err == nil {
		return u
	}
	if u, err := gitutil.RemoteURL(".", cfg.Remote); err == nil {
		return u
	}
	return ""
}

func newProvider(cfg *config.Config) (provider.Provider, error) {
	token, err := provider.ResolveToken(cfg.Provider, privateRemoteURL(cfg))
	if err != nil {
		return nil, err
	}
	client, err := provider.NewHTTPClient(cfg.CA)
	if err != nil {
		return nil, err
	}
	switch cfg.Provider {
	case "gitea":
		return gitea.New(cfg.API, cfg.Repo, token, client), nil
	case "github":
		return github.New(cfg.API, cfg.Repo, token, client), nil
	default:
		return nil, fmt.Errorf("unsupported provider %q", cfg.Provider)
	}
}

// ---------- init ----------

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	providerName := fs.String("provider", "gitea", "gitea or github")
	repoFlag := fs.String("repo", "", "owner/name")
	remoteName := fs.String("remote", "company", "name for the private remote")
	apiFlag := fs.String("api", "", "provider API base (Gitea)")
	caFlag := fs.String("ca", "", "PEM CA bundle")
	baseFlag := fs.String("base", "", "default PR target branch")
	mirrorFlag := fs.String("mirror", "", "bare mirror path")
	queueFlag := fs.String("queue", "", "shared queue directory")
	mergeFlag := fs.String("merge", "when-green", "default merge policy")
	configure := fs.Bool("configure-git", false, "create the mirror and repoint git remotes")
	force := fs.Bool("force", false, "overwrite an existing config")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, err := gitutil.Root()
	if err != nil {
		return fmt.Errorf("not inside a git repository: %w", err)
	}

	remoteURL, remoteErr := gitutil.RemoteURL(root, *remoteName)
	if remoteErr != nil {
		remoteURL, remoteErr = gitutil.RemoteURL(root, "origin")
	}
	if remoteErr != nil {
		return errors.New("no private remote found (looked for --remote and origin)")
	}
	repo := *repoFlag
	if repo == "" {
		repo, err = gitutil.ParseRepo(remoteURL)
		if err != nil {
			return err
		}
	}
	if *baseFlag == "" {
		*baseFlag = gitutil.DefaultBranch(root)
	}
	if *mirrorFlag == "" {
		*mirrorFlag = "~/.airlock/" + config.Slug(repo) + ".git"
	}
	if *queueFlag == "" {
		*queueFlag = "~/.airlock/queue"
	}
	if *apiFlag == "" && *providerName == "gitea" {
		*apiFlag = giteaAPI(remoteURL)
		if *apiFlag == "" {
			return errors.New("cannot derive the Gitea API URL; pass --api")
		}
	}

	cfg := &config.Config{
		Repo:     repo,
		Remote:   *remoteName,
		Mirror:   *mirrorFlag,
		Queue:    *queueFlag,
		Provider: *providerName,
		API:      *apiFlag,
		CA:       *caFlag,
		Base:     *baseFlag,
		Merge:    *mergeFlag,
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	path := filepath.Join(root, config.Filename)
	if _, err := os.Stat(path); err == nil && !*force {
		return fmt.Errorf("%s already exists (use --force to overwrite)", path)
	}
	if err := config.Write(path, cfg); err != nil {
		return err
	}

	if *configure {
		expanded, err := config.ExpandHome(cfg.Mirror)
		if err != nil {
			return err
		}
		if _, err := os.Stat(expanded); os.IsNotExist(err) {
			fmt.Printf("creating bare mirror %s\n", expanded)
			if err := gitutil.CloneBare(context.Background(), root, expanded); err != nil {
				return err
			}
		}
		original, err := gitutil.ConfigureRemotes(context.Background(), root, expanded, cfg.Remote)
		if err != nil {
			return err
		}
		_ = original // never print it: it may contain a token
		fmt.Printf("git origin -> %s, private remote -> %s\n", expanded, cfg.Remote)
	}

	fmt.Printf("wrote %s\n\n", path)
	fmt.Println("Next steps:")
	fmt.Printf("  1. On the workstation with VPN:  airlock worker --config %q\n", path)
	fmt.Printf("  2. Install the timer:            airlock install-task --config %q\n", path)
	fmt.Printf("  3. From the agent:               airlock pr open --wait\n")
	fmt.Println("\nAdd the agent rules with: airlock instructions")
	return nil
}

func giteaAPI(remote string) string {
	s := strings.TrimSpace(remote)
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		scheme := u.Scheme
		if scheme == "" {
			scheme = "https"
		}
		return scheme + "://" + u.Host + "/api/v1"
	}
	// scp-like: git@host:owner/name
	if i := strings.Index(s, "@"); i >= 0 {
		rest := s[i+1:]
		if j := strings.Index(rest, ":"); j > 0 {
			return "https://" + rest[:j] + "/api/v1"
		}
	}
	return ""
}

// ---------- sync ----------

func cmdSync(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "config path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, _, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	report, err := gitutil.Sync(ctx, cfg.Mirror, cfg.Remote)
	printSyncReport(report)
	if errors.Is(err, gitutil.ErrDiverged) {
		return &exitError{code: 1, msg: "branches diverged; rebase or merge locally first"}
	}
	return err
}

func printSyncReport(r gitutil.SyncReport) {
	if len(r.Pushed) > 0 {
		fmt.Println("pushed:        ", strings.Join(r.Pushed, ", "))
	}
	if len(r.FastForwarded) > 0 {
		fmt.Println("fast-forwarded:", strings.Join(r.FastForwarded, ", "))
	}
	if len(r.Diverged) > 0 {
		fmt.Println("diverged:      ", strings.Join(r.Diverged, ", "))
	}
	for _, w := range r.Warnings {
		fmt.Println("warning:       ", w)
	}
}

// ---------- pr ----------

func cmdPR(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("pr needs open, status or merge")
	}
	switch args[0] {
	case "open":
		return cmdPROpen(ctx, args[1:])
	case "status":
		return cmdPRStatus(ctx, args[1:])
	case "merge":
		return cmdPRMerge(ctx, args[1:])
	default:
		return fmt.Errorf("unknown pr action %q", args[0])
	}
}

func cmdPROpen(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("pr open", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "config path")
	headFlag := fs.String("head", "", "source branch (default: current)")
	baseFlag := fs.String("base", "", "target branch (default: config)")
	titleFlag := fs.String("title", "", "PR title (default: last commit subject)")
	bodyFlag := fs.String("body", "", "PR body")
	mergeFlag := fs.String("merge", "", "never, when-green or now")
	dryRun := fs.Bool("dry-run", false, "validate without creating anything")
	wait := fs.Duration("wait", 2*time.Minute, "how long to wait for the worker")
	noWait := fs.Bool("no-wait", false, "queue and exit immediately")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("open takes no positional arguments")
	}
	cfg, _, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	root, err := gitutil.Root()
	if err != nil {
		return err
	}
	head := *headFlag
	if head == "" {
		head, err = gitutil.CurrentBranch(root)
		if err != nil {
			return fmt.Errorf("cannot detect current branch: %w", err)
		}
	}
	base := *baseFlag
	if base == "" {
		base = cfg.Base
	}
	if head == base {
		return fmt.Errorf("head and base are both %q; check out a feature branch or pass --head/--base", base)
	}
	title := *titleFlag
	if title == "" {
		title, _ = gitutil.HeadSubject(root)
	}
	merge := *mergeFlag
	if merge == "" {
		merge = cfg.Merge
	}
	req := &queue.Request{
		Action: queue.ActionOpen,
		Head:   head,
		Base:   base,
		Title:  title,
		Body:   *bodyFlag,
		Merge:  merge,
		DryRun: *dryRun,
	}
	return enqueueAndWait(ctx, cfg, req, *wait, *noWait)
}

func cmdPRStatus(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("pr status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "config path")
	wait := fs.Duration("wait", 2*time.Minute, "how long to wait for the worker")
	noWait := fs.Bool("no-wait", false, "queue and exit immediately")
	number, err := parseNumber(fs, args)
	if err != nil {
		return err
	}
	cfg, _, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	req := &queue.Request{Action: queue.ActionStatus, Number: number}
	return enqueueAndWait(ctx, cfg, req, *wait, *noWait)
}

func cmdPRMerge(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("pr merge", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "config path")
	whenGreen := fs.Bool("when-green", false, "wait for green checks first")
	wait := fs.Duration("wait", 2*time.Minute, "how long to wait for the worker")
	noWait := fs.Bool("no-wait", false, "queue and exit immediately")
	number, err := parseNumber(fs, args)
	if err != nil {
		return err
	}
	cfg, _, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	merge := "now"
	if *whenGreen {
		merge = "when-green"
	}
	req := &queue.Request{Action: queue.ActionMerge, Number: number, Merge: merge}
	return enqueueAndWait(ctx, cfg, req, *wait, *noWait)
}

// parseNumber parses flags before the PR number, then reparses flags that
// follow it. It allows both `pr status 32 --wait 1m` and `--wait 1m 32`.
func parseNumber(fs *flag.FlagSet, args []string) (int, error) {
	if err := fs.Parse(args); err != nil {
		return 0, err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return 0, errors.New("PR number is required")
	}
	n, err := strconv.Atoi(rest[0])
	if err != nil {
		return 0, fmt.Errorf("PR number must be numeric: %q", rest[0])
	}
	if n <= 0 {
		return 0, errors.New("PR number must be positive")
	}
	if len(rest) > 1 {
		if err := fs.Parse(rest[1:]); err != nil {
			return 0, err
		}
	}
	return n, nil
}

func enqueueAndWait(ctx context.Context, cfg *config.Config, req *queue.Request, wait time.Duration, noWait bool) error {
	q := queue.New(cfg.Queue)
	id, err := q.Enqueue(req)
	if err != nil {
		return err
	}
	fmt.Printf("queued %s (%s)\n", id, req.Action)
	fmt.Printf("result: %s\n", filepath.Join(q.ResultsDir(), id+".json"))
	if noWait || wait <= 0 {
		return &exitError{code: 3, msg: "queued; the worker will process it"}
	}
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	res, err := q.Wait(waitCtx, id, 5*time.Second, printResult)
	if err != nil {
		return &exitError{code: 3, msg: fmt.Sprintf("still queued after %s; check %s later", wait, filepath.Join(q.ResultsDir(), id+".json"))}
	}
	if res.OK {
		return nil
	}
	return &exitError{code: 1, msg: res.Message}
}

func printResult(res *queue.Result) {
	data, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(data))
}

// ---------- worker ----------

func cmdWorker(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "config path")
	noSync := fs.Bool("no-sync", false, "skip the mirror sync")
	loop := fs.Duration("loop", 0, "run forever with this interval (0 = once)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, path, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	p, err := newProvider(cfg)
	if err != nil {
		return err
	}
	q := queue.New(cfg.Queue)
	w := worker.New(cfg, p)
	for {
		if !*noSync {
			report, err := gitutil.Sync(ctx, cfg.Mirror, cfg.Remote)
			printSyncReport(report)
			if err != nil {
				q.Log("sync failed: %v", err)
				fmt.Fprintln(os.Stderr, "sync:", err)
			}
		}
		n, err := w.Run(ctx)
		if err != nil {
			q.Log("worker failed: %v", err)
			fmt.Fprintln(os.Stderr, "worker:", err)
		} else if n > 0 {
			fmt.Printf("processed %d request(s) from %s\n", n, path)
		}
		if *loop <= 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(*loop):
		}
	}
}

// ---------- doctor ----------

func cmdDoctor(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "config path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, path, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	fmt.Printf("config:   %s\n", path)
	fmt.Printf("repo:     %s\n", cfg.Repo)
	fmt.Printf("provider: %s\n", cfg.Provider)
	fmt.Printf("mirror:   %s\n", cfg.Mirror)
	fmt.Printf("queue:    %s\n", cfg.Queue)

	failed := false
	check := func(name string, fn func() error) {
		if err := fn(); err != nil {
			fmt.Printf("[fail] %s: %v\n", name, err)
			failed = true
			return
		}
		fmt.Printf("[ ok ] %s\n", name)
	}

	check("mirror exists", func() error {
		cmd := exec.CommandContext(ctx, "git", "-C", cfg.Mirror, "rev-parse", "--is-bare-repository")
		out, err := cmd.Output()
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(out)) != "true" {
			return errors.New("not a bare repository")
		}
		return nil
	})
	check("private remote reachable", func() error {
		cmdCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cmdCtx, "git", "-C", cfg.Mirror, "ls-remote", cfg.Remote, "HEAD")
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		return cmd.Run()
	})
	if cfg.CA != "" {
		check("CA bundle", func() error {
			_, err := os.Stat(cfg.CA)
			return err
		})
	}
	p, err := newProvider(cfg)
	if err != nil {
		fmt.Printf("[fail] token: %v\n", err)
		failed = true
	} else {
		check("API auth", func() error { return p.Ping(ctx) })
	}
	check("queue writable", func() error {
		q := queue.New(cfg.Queue)
		if err := q.Ensure(); err != nil {
			return err
		}
		f, err := os.CreateTemp(q.TmpDir(), "doctor-")
		if err != nil {
			return err
		}
		name := f.Name()
		f.Close()
		return os.Remove(name)
	})
	check("scheduler", func() error {
		status, err := install.Status("Git Airlock")
		if err != nil {
			return err
		}
		fmt.Printf("       status: %s\n", status)
		if strings.Contains(strings.ToLower(status), "not installed") || strings.Contains(strings.ToLower(status), "disabled") {
			return errors.New("worker timer is not installed")
		}
		return nil
	})

	if failed {
		return &exitError{code: 1, msg: "one or more checks failed"}
	}
	return nil
}

// ---------- install-task ----------

func cmdInstallTask(args []string) error {
	fs := flag.NewFlagSet("install-task", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "config path")
	name := fs.String("name", "Git Airlock", "scheduled task/unit name")
	interval := fs.Duration("interval", time.Minute, "worker interval")
	printOnly := fs.Bool("print", false, "print the generated files and commands")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, path, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	return install.Install(install.Options{
		Name:       *name,
		ConfigPath: path,
		Interval:   *interval,
		Print:      *printOnly,
	})
}

// ---------- instructions ----------

func cmdInstructions(args []string) error {
	fmt.Print(`## Git Airlock

This repository uses Git Airlock. "origin" is a local bare mirror, not the
private remote; a scheduled worker on a VPN-connected workstation forwards it.

- Push your work with: git push origin <branch>
  Never force-push origin.
- Open a pull request and merge it once CI is green:
  airlock pr open --wait
- Open without merging: airlock pr open --merge never
- Check an existing PR: airlock pr status <number>
- Merge an existing PR after checks: airlock pr merge <number> --when-green
- Validate without changing anything: add --dry-run

Exit codes from airlock pr: 0 = done, 1 = failed, 3 = still queued
(the worker retries; check the queue results later, do not resubmit).
`)
	return nil
}

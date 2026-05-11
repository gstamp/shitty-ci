package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"shitty-ci/internal/cli"
	"shitty-ci/internal/config"
	"shitty-ci/internal/db"
	"shitty-ci/internal/gitutil"
	"shitty-ci/internal/proto"
	"shitty-ci/internal/server"
	"shitty-ci/internal/types"
	"shitty-ci/internal/xdg"
)

func usage() {
	fmt.Fprintln(os.Stderr, "usage: shitty-ci <command> [args]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "commands:")
	fmt.Fprintln(os.Stderr, "  shitty-ci server [--install-systemd]")
	fmt.Fprintln(os.Stderr, "  shitty-ci repos add <owner/repo>")
	fmt.Fprintln(os.Stderr, "  shitty-ci repos remove <owner/repo>")
	fmt.Fprintln(os.Stderr, "  shitty-ci repos list")
	fmt.Fprintln(os.Stderr, "  shitty-ci secret <set|unset|list> ...")
	fmt.Fprintln(os.Stderr, "  shitty-ci builds [--repo owner/repo] [--all] [--limit N]")
	fmt.Fprintln(os.Stderr, "  shitty-ci logs [--tail N] [--follow|-f] [<build id prefix | commit sha>]")
	fmt.Fprintln(os.Stderr, "  shitty-ci cancel <build id prefix | commit sha>")
	fmt.Fprintln(os.Stderr, "  shitty-ci status")
	fmt.Fprintln(os.Stderr, "  shitty-ci config")
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	switch cmd {
	case "server":
		fs := flag.NewFlagSet("server", flag.ExitOnError)
		install := fs.Bool("install-systemd", false, "install systemd user unit")
		_ = fs.Parse(os.Args[2:])
		if *install {
			if err := cli.InstallSystemd(); err != nil {
				fatal(err)
			}
			return
		}
		runServer()
	case "repos":
		if len(os.Args) < 3 {
			usage()
		}
		switch os.Args[2] {
		case "add":
			if len(os.Args) != 4 {
				fatal(fmt.Errorf("usage: shitty-ci repos add <owner/repo>"))
			}
			resp, err := cli.RPC(proto.Request{Cmd: "repos_add", Repo: os.Args[3]})
			mustRPC(resp, err)
			fmt.Println("ok")
		case "remove":
			if len(os.Args) != 4 {
				fatal(fmt.Errorf("usage: shitty-ci repos remove <owner/repo>"))
			}
			resp, err := cli.RPC(proto.Request{Cmd: "repos_remove", Repo: os.Args[3]})
			mustRPC(resp, err)
			fmt.Println("ok")
		case "list":
			resp, err := cli.RPC(proto.Request{Cmd: "repos_list"})
			mustRPC(resp, err)
			data, _ := resp.Data.(map[string]any)
			repos, _ := data["repos"].([]any)
			for _, r := range repos {
				m := r.(map[string]any)
				fmt.Printf("%s/%s\n", m["owner"], m["name"])
			}
		default:
			usage()
		}
	case "secret":
		if len(os.Args) < 3 {
			fatal(fmt.Errorf("usage: shitty-ci secret <set|unset|list> ..."))
		}
		sub := os.Args[2]
		fs := flag.NewFlagSet("secret "+sub, flag.ExitOnError)
		repoOpt := fs.String("repo", "", "owner/repo (default: cwd GitHub origin if tracked)")
		if err := fs.Parse(os.Args[3:]); err != nil {
			fatal(err)
		}
		repoFull, err := cli.TrackedRepoFull(*repoOpt)
		if err != nil {
			fatal(err)
		}
		switch sub {
		case "set":
			args := fs.Args()
			if len(args) < 2 {
				fatal(fmt.Errorf("usage: shitty-ci secret set [--repo owner/repo] KEY VALUE..."))
			}
			key := args[0]
			value := strings.Join(args[1:], " ")
			resp, err := cli.RPC(proto.Request{Cmd: "secret_set", Repo: repoFull, SecretKey: key, SecretValue: value})
			mustRPC(resp, err)
			fmt.Println("ok")
		case "unset":
			args := fs.Args()
			if len(args) != 1 {
				fatal(fmt.Errorf("usage: shitty-ci secret unset [--repo owner/repo] KEY"))
			}
			resp, err := cli.RPC(proto.Request{Cmd: "secret_unset", Repo: repoFull, SecretKey: args[0]})
			mustRPC(resp, err)
			fmt.Println("ok")
		case "list":
			if fs.NArg() != 0 {
				fatal(fmt.Errorf("usage: shitty-ci secret list [--repo owner/repo]"))
			}
			resp, err := cli.RPC(proto.Request{Cmd: "secret_list", Repo: repoFull})
			mustRPC(resp, err)
			raw, err := json.Marshal(resp.Data)
			if err != nil {
				fatal(err)
			}
			var kd proto.SecretKeysData
			if err := json.Unmarshal(raw, &kd); err != nil {
				fatal(err)
			}
			for _, k := range kd.Keys {
				fmt.Println(k)
			}
		default:
			fatal(fmt.Errorf("unknown secret subcommand %q (try set, unset, list)", sub))
		}
	case "builds":
		fs := flag.NewFlagSet("builds", flag.ContinueOnError)
		repo := fs.String("repo", "", "filter by owner/repo (overrides cwd default)")
		all := false
		fs.BoolVar(&all, "all", false, "list builds from all tracked repos (ignore cwd-based default filter)")
		limit := fs.Int("limit", 50, "max rows")
		_ = fs.Parse(os.Args[2:])

		repoFilter := strings.TrimSpace(*repo)
		usedCwdDefault := false
		if repoFilter == "" && !all {
			if o, n, ok := gitutil.DetectGitHubRepoFromDir(""); ok {
				candidate := o + "/" + n
				lr, err := cli.RPC(proto.Request{Cmd: "repos_list"})
				if err == nil && lr.OK {
					raw, err := json.Marshal(lr.Data)
					if err == nil {
						var rd proto.ReposData
						if json.Unmarshal(raw, &rd) == nil {
							for _, r := range rd.Repos {
								if r.Full == candidate {
									repoFilter = candidate
									usedCwdDefault = true
									break
								}
							}
						}
					}
				}
			}
		}

		resp, err := cli.RPC(proto.Request{Cmd: "builds_list", Repo: repoFilter, Limit: *limit})
		mustRPC(resp, err)
		raw, err := json.Marshal(resp.Data)
		if err != nil {
			fatal(err)
		}
		var bd proto.BuildsData
		if err := json.Unmarshal(raw, &bd); err != nil {
			fatal(err)
		}
		cli.PrintBuilds(os.Stdout, bd.Builds, cli.StdoutUseColor(), cli.StdoutIsTerminal())
		if usedCwdDefault && cli.StdoutIsTerminal() {
			fmt.Fprintln(os.Stdout)
			cli.PrintBuildsCwdHint(os.Stdout, cli.StdoutUseColor())
		}
	case "logs":
		fs := flag.NewFlagSet("logs", flag.ExitOnError)
		tailN := fs.Int("tail", 0, "if > 0, print only the last N lines (first snapshot only unless combined with --follow)")
		follow := false
		fs.BoolVar(&follow, "follow", false, "keep polling the log and print new output (Ctrl+C to stop)")
		fs.BoolVar(&follow, "f", false, "alias for -follow")
		fs.Usage = func() {
			fmt.Fprintf(fs.Output(), "usage: shitty-ci logs [--tail N] [--follow|-f] [<build id prefix | commit sha>]\n\n")
			fs.PrintDefaults()
		}
		if err := fs.Parse(os.Args[2:]); err != nil {
			fatal(err)
		}
		if fs.NArg() > 1 {
			fs.Usage()
			os.Exit(2)
		}
		if *tailN < 0 {
			fatal(fmt.Errorf("--tail must be >= 0"))
		}
		spec := strings.TrimSpace(fs.Arg(0))
		if spec == "" {
			id, err := cli.DefaultLogsBuildID()
			if err != nil {
				fatal(err)
			}
			spec = id
		}

		fetchLogText := func() (string, error) {
			resp, err := cli.RPC(proto.Request{Cmd: "logs_get", BuildID: spec})
			if err != nil {
				return "", err
			}
			if !resp.OK {
				return "", fmt.Errorf("%s", strings.TrimSpace(resp.Error))
			}
			data, _ := resp.Data.(map[string]any)
			switch v := data["text"].(type) {
			case string:
				return v, nil
			default:
				return fmt.Sprint(v), nil
			}
		}

		full, err := fetchLogText()
		if err != nil {
			fatal(err)
		}

		if !follow {
			out := full
			if *tailN > 0 {
				out = tailLines(full, *tailN)
			}
			fmt.Print(out)
			break
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		first := full
		if *tailN > 0 {
			first = tailLines(full, *tailN)
		}
		fmt.Print(first)

		seen := full
		ticker := time.NewTicker(750 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				next, err := fetchLogText()
				if err != nil {
					fmt.Fprintf(os.Stderr, "shitty-ci logs: %v\n", err)
					continue
				}
				if strings.HasPrefix(next, seen) {
					if delta := next[len(seen):]; delta != "" {
						fmt.Print(delta)
					}
					seen = next
					continue
				}
				fmt.Fprint(os.Stderr, "\n[shitty-ci logs: log truncated or replaced; showing refreshed tail]\n")
				seen = next
				if *tailN > 0 {
					fmt.Print(tailLines(next, *tailN))
				} else {
					fmt.Print(next)
				}
			}
		}
	case "cancel":
		if len(os.Args) != 3 {
			fatal(fmt.Errorf("usage: shitty-ci cancel <build id prefix | commit sha>"))
		}
		resp, err := cli.RPC(proto.Request{Cmd: "cancel", BuildID: os.Args[2]})
		mustRPC(resp, err)
		fmt.Println("ok")
	case "status":
		resp, err := cli.RPC(proto.Request{Cmd: "status"})
		mustRPC(resp, err)
		raw, err := json.Marshal(resp.Data)
		if err != nil {
			fatal(err)
		}
		var st types.DaemonStatus
		if err := json.Unmarshal(raw, &st); err != nil {
			fatal(err)
		}
		cli.PrintStatus(os.Stdout, st, cli.StdoutUseColor(), cli.StdoutIsTerminal())
	case "config":
		resp, err := cli.RPC(proto.Request{Cmd: "config_show"})
		mustRPC(resp, err)
		data, _ := resp.Data.(map[string]any)
		fmt.Printf("config_path=%v\n", data["path"])
		fmt.Printf("resolved_data_dir=%v\n", data["resolved_data_dir"])
		fmt.Printf("poll_interval=%v\n", data["poll_interval"])
		fmt.Printf("max_concurrent_builds=%v\n", data["max_concurrent_builds"])
		fmt.Printf("build_timeout=%v\n", data["build_timeout"])
		fmt.Printf("workspace_ttl=%v\n", data["workspace_ttl"])
		fmt.Printf("github_token_set=%v\n", data["has_github_token"])
	default:
		usage()
	}
}

func runServer() {
	_ = config.EnsureConfigDir()
	store := config.NewStore(config.DefaultConfigPath())
	if err := store.Refresh(); err != nil {
		fatal(err)
	}
	dataDir := store.DataDirResolved()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		fatal(err)
	}
	sqlDB, err := db.Open(xdg.DBPath(dataDir))
	if err != nil {
		fatal(err)
	}
	defer sqlDB.Close()

	app := server.NewApp(sqlDB, store, dataDir, xdg.SocketPath(dataDir))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx); err != nil && ctx.Err() == nil {
		fatal(err)
	}
}

func tailLines(s string, n int) string {
	if n <= 0 || s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

func mustRPC(resp proto.Response, err error) {
	if err != nil {
		fatal(err)
	}
	if !resp.OK {
		fatal(fmt.Errorf("%s", strings.TrimSpace(resp.Error)))
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err.Error())
	os.Exit(1)
}

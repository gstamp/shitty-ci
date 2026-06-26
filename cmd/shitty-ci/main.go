package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
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
	gh "shitty-ci/internal/github"
	"shitty-ci/internal/gitutil"
	"shitty-ci/internal/proto"
	"shitty-ci/internal/server"
	"shitty-ci/internal/types"
	"shitty-ci/internal/xdg"

	"gopkg.in/yaml.v3"
)

func usage() {
	fmt.Fprintln(os.Stderr, "usage: shitty-ci [global flags] <command> [args]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "global flags:")
	fmt.Fprintln(os.Stderr, "  --server host:port   remote daemon address (env: SHITTY_CI_SERVER)")
	fmt.Fprintln(os.Stderr, "  --token str          auth token (env: SHITTY_CI_TOKEN)")
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
	fmt.Fprintln(os.Stderr, "  shitty-ci retry <build id prefix | commit sha>")
	fmt.Fprintln(os.Stderr, "  shitty-ci token")
	fmt.Fprintln(os.Stderr, "  shitty-ci status")
	fmt.Fprintln(os.Stderr, "  shitty-ci config")
	fmt.Fprintln(os.Stderr, "  shitty-ci github-app setup")
	os.Exit(2)
}

func main() {
	cli.ServerAddr = os.Getenv("SHITTY_CI_SERVER")
	cli.AuthToken = os.Getenv("SHITTY_CI_TOKEN")

	filtered := []string{os.Args[0]}
	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--server":
			i++
			if i < len(os.Args) {
				cli.ServerAddr = os.Args[i]
			}
		case "--token":
			i++
			if i < len(os.Args) {
				cli.AuthToken = os.Args[i]
			}
		default:
			filtered = append(filtered, os.Args[i])
		}
	}
	if cli.AuthToken == "" && cli.ServerAddr != "" {
		cfg, err := config.Load(config.DefaultConfigPath())
		if err == nil {
			dataDir := xdg.DataDir(cfg.DataDir)
			if tok, err := config.ReadToken(xdg.AuthTokenPath(dataDir)); err == nil {
				cli.AuthToken = tok
			}
		}
	}
	os.Args = filtered

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
		limit := fs.Int("limit", 25, "max rows")
		watch := false
		fs.BoolVar(&watch, "watch", false, "keep polling and redrawing (Ctrl+C to stop)")
		fs.BoolVar(&watch, "w", false, "alias for --watch")
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

		fetchBuilds := func() ([]types.Build, error) {
			resp, err := cli.RPC(proto.Request{Cmd: "builds_list", Repo: repoFilter, Limit: *limit})
			if err != nil {
				return nil, err
			}
			if !resp.OK {
				return nil, fmt.Errorf("%s", resp.Error)
			}
			raw, err := json.Marshal(resp.Data)
			if err != nil {
				return nil, err
			}
			var bd proto.BuildsData
			if err := json.Unmarshal(raw, &bd); err != nil {
				return nil, err
			}
			return bd.Builds, nil
		}

		builds, err := fetchBuilds()
		if err != nil {
			fatal(err)
		}
		cli.PrintBuilds(os.Stdout, builds, cli.StdoutUseColor(), cli.StdoutIsTerminal())
		if usedCwdDefault && cli.StdoutIsTerminal() {
			fmt.Fprintln(os.Stdout)
			cli.PrintBuildsCwdHint(os.Stdout, cli.StdoutUseColor())
		}

		if !watch {
			break
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()

		isTerm := cli.StdoutIsTerminal()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				builds, err := fetchBuilds()
				if err != nil {
					continue
				}
				if isTerm {
					fmt.Print("\033[H\033[J")
				} else {
					fmt.Println("---")
				}
				cli.PrintBuilds(os.Stdout, builds, cli.StdoutUseColor(), isTerm)
				if usedCwdDefault && isTerm {
					fmt.Fprintln(os.Stdout)
					cli.PrintBuildsCwdHint(os.Stdout, cli.StdoutUseColor())
				}
			}
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
	case "retry":
		if len(os.Args) != 3 {
			fatal(fmt.Errorf("usage: shitty-ci retry <build id prefix | commit sha>"))
		}
		resp, err := cli.RPC(proto.Request{Cmd: "retry", BuildID: os.Args[2]})
		mustRPC(resp, err)
		data, _ := resp.Data.(map[string]any)
		if id, ok := data["build_id"].(string); ok {
			fmt.Printf("ok (new build id: %s)\n", id)
		} else {
			fmt.Println("ok")
		}
	case "token":
		if cli.ServerAddr != "" {
			fatal(fmt.Errorf("token subcommand only works locally"))
		}
		cfg, err := config.Load(config.DefaultConfigPath())
		if err != nil {
			fatal(err)
		}
		dataDir := xdg.DataDir(cfg.DataDir)
		tok, err := config.ReadToken(xdg.AuthTokenPath(dataDir))
		if err != nil {
			fatal(fmt.Errorf("no auth token found; configure remote listening first"))
		}
		fmt.Println(tok)
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
		fmt.Printf("github_app_set=%v\n", data["has_github_app"])
		if v, ok := data["listen"].(string); ok && v != "" {
			fmt.Printf("listen=%v\n", v)
		}
	case "github-app":
		if len(os.Args) < 3 {
			fatal(fmt.Errorf("usage: shitty-ci github-app <setup>"))
		}
		switch os.Args[2] {
		case "setup":
			if err := runGitHubAppSetup(); err != nil {
				fatal(err)
			}
		default:
			fatal(fmt.Errorf("unknown github-app subcommand %q (try setup)", os.Args[2]))
		}
	default:
		usage()
	}
}

func runGitHubAppSetup() error {
	const margin = "  "

	fmt.Println()
	fmt.Println("GitHub App setup")
	fmt.Println("━━━━━━━━━━━━━━━")
	fmt.Println()

	// ── Generate a unique app name ──
	var suffix [3]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fmt.Errorf("generate name suffix: %w", err)
	}
	appName := "shitty-ci-" + hex.EncodeToString(suffix[:])

	manifest := gh.NewManifest()
	manifest.Name = appName

	// Use URL parameters instead of the base64 manifest blob — they survive
	// login redirects, which the manifest parameter does not (known GitHub bug).
	registerURL := manifest.URLParamsURL()

	// ── Step 1: print registration URL ──
	fmt.Println("Step 1 — Create the GitHub App")
	fmt.Println("────────────────────────────────")
	fmt.Println()
	fmt.Println("Open this URL in your browser:")
	fmt.Println()
	fmt.Println(margin + registerURL)
	fmt.Println()
	fmt.Println("The fields will be pre-filled. Just scroll down and click")
	fmt.Println("\"Create GitHub App\" at the bottom.")
	fmt.Println()
	fmt.Println("After creating the app, you'll land on the app settings page.")
	fmt.Println("You need two things from there:")
	fmt.Println()
	fmt.Println("  1. The App ID (shown at the top of the page)")
	fmt.Println("  2. A private key (scroll to \"Private keys\" → Generate one)")
	fmt.Println()

	var appID int64
	var pemBytes []byte
	var slug string

	for {
		fmt.Print("App ID: ")
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Scan()
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			return fmt.Errorf("no app id provided")
		}
		if _, err := fmt.Sscanf(input, "%d", &appID); err != nil || appID <= 0 {
			fmt.Println("  Invalid App ID — should be a number like 123456")
			continue
		}
		break
	}

	for {
		fmt.Print("Path to private key (.pem): ")
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Scan()
		keyPath := strings.TrimSpace(scanner.Text())
		if keyPath == "" {
			return fmt.Errorf("no key path provided")
		}
		var err error
		pemBytes, err = os.ReadFile(keyPath)
		if err != nil {
			fmt.Printf("  Could not read %s: %v\n", keyPath, err)
			continue
		}
		if err := verifyPEM(pemBytes); err != nil {
			fmt.Printf("  Invalid PEM file: %v\n", err)
			continue
		}

		// Verify the PEM + App ID combo by calling GET /app
		fmt.Print("  Verifying… ")
		appInfo, err := gh.GetApp(appID, pemBytes)
		if err != nil {
			fmt.Printf("authentication failed: %v\n", err)
			fmt.Println("  Make sure the App ID and private key match.")
			continue
		}
		slug = appInfo.Slug
		fmt.Printf("✓ app \"%s\" by %s\n", slug, appInfo.Owner.Login)

		// Save PEM to config dir
		savedKeyPath := configPath("github-app.pem")
		if keyPath != savedKeyPath {
			if err := os.WriteFile(savedKeyPath, pemBytes, 0o600); err != nil {
				return fmt.Errorf("save private key: %w", err)
			}
		}
		break
	}

	fmt.Println()

	// ── Step 2: install the app ──
	installURL := fmt.Sprintf("https://github.com/apps/%s/installations/new", slug)
	fmt.Println("Step 2 — Install the app")
	fmt.Println("─────────────────────────")
	fmt.Println()
	fmt.Println("Open this URL in your browser:")
	fmt.Println()
	fmt.Println(margin + installURL)
	fmt.Println()
	fmt.Println("Click \"Install\" on your user or organization.")
	fmt.Println()
	fmt.Print("Press Enter after installing > ")
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan()
	fmt.Println()

	// ── Detect installation ──
	fmt.Print("Detecting installation… ")
	var installations []gh.Installation
	var err error
	for attempt := 0; attempt < 15; attempt++ {
		installations, err = gh.ListInstallations(appID, pemBytes)
		if err == nil && len(installations) > 0 {
			break
		}
		if attempt < 14 {
			time.Sleep(2 * time.Second)
		}
	}
	if err != nil {
		return fmt.Errorf("list installations: %w", err)
	}
	if len(installations) == 0 {
		return fmt.Errorf("no installations found — make sure you installed the app on your account or org")
	}

	var inst gh.Installation
	if len(installations) == 1 {
		inst = installations[0]
	} else {
		fmt.Println()
		fmt.Println("Multiple installations found. Which one should I use?")
		for i, inst := range installations {
			fmt.Printf("  %d) %s (%s)\n", i+1, inst.Account.Login, inst.Account.Type)
		}
		fmt.Print("Number (1): ")
		choice := readLine()
		idx := 0
		if choice != "" {
			if _, err := fmt.Sscanf(choice, "%d", &idx); err == nil {
				idx--
			}
		}
		if idx < 0 || idx >= len(installations) {
			idx = 0
		}
		inst = installations[idx]
	}
	fmt.Printf("done! (installation ID: %d, account: %s)\n\n", inst.ID, inst.Account.Login)

	// ── Write config ──
	cfgPath := config.DefaultConfigPath()
	fmt.Print("Updating config… ")

	var cfgMap map[string]any
	if b, err := os.ReadFile(cfgPath); err == nil && len(b) > 0 {
		_ = yaml.Unmarshal(b, &cfgMap)
	}
	if cfgMap == nil {
		cfgMap = make(map[string]any)
	}

	cfgMap["github_app"] = map[string]any{
		"app_id":           appID,
		"installation_id":  inst.ID,
		"private_key_path": configPath("github-app.pem"),
	}

	out, err := yaml.Marshal(cfgMap)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(cfgPath, out, 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	fmt.Printf("done!\n\n")

	// ── Verify ──
	fmt.Print("Verifying token exchange… ")
	token, err := gh.GetInstallationToken(appID, inst.ID, pemBytes)
	if err != nil {
		return fmt.Errorf("token exchange failed: %w", err)
	}
	if token == "" {
		return fmt.Errorf("token exchange returned empty token")
	}
	fmt.Printf("✓ token works!\n\n")

	fmt.Println("╭──────────────────────────────────────────────────────────╮")
	fmt.Println("│                                                          │")
	fmt.Println("│  Setup complete! The next push to a tracked repo will    │")
	fmt.Println("│  show per-step check runs in the GitHub Checks tab.      │")
	fmt.Println("│                                                          │")
	fmt.Printf("│  Config: %-52s│\n", cfgPath)
	fmt.Printf("│  Key:    %-52s│\n", configPath("github-app.pem"))
	fmt.Println("│                                                          │")
	fmt.Println("╰──────────────────────────────────────────────────────────╯")
	fmt.Println()
	return nil
}

// configPath returns a path under the shitty-ci config directory.
func configPath(name string) string {
	p := config.DefaultConfigPath()
	dir := p[:len(p)-len("/config.yml")]
	return dir + "/" + name
}

// readLine reads a trimmed line from stdin.
func readLine() string {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan()
	return strings.TrimSpace(scanner.Text())
}

// verifyPEM checks that the bytes look like a valid RSA private key.
func verifyPEM(pemBytes []byte) error {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return fmt.Errorf("no PEM block found")
	}
	if _, err := x509.ParsePKCS1PrivateKey(block.Bytes); err != nil {
		if _, err2 := x509.ParsePKCS8PrivateKey(block.Bytes); err2 != nil {
			return fmt.Errorf("not a valid RSA private key (PKCS1: %v, PKCS8: %v)", err, err2)
		}
	}
	return nil
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

	cfg := store.Get()
	var authToken string
	if cfg.Listen != "" {
		var err error
		authToken, err = config.LoadOrGenerateToken(xdg.AuthTokenPath(dataDir))
		if err != nil {
			fatal(err)
		}
	}
	app := server.NewApp(sqlDB, store, dataDir, xdg.SocketPath(dataDir), authToken)

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

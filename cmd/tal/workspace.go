package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"talaria/internal/app"
	"talaria/internal/browser"
	"talaria/internal/config"
	"talaria/internal/workspace"
)

// browserConfig turns the config file's browser section into the app's.
func browserConfig(cfg *config.Config) app.BrowserConfig {
	b := cfg.Browser
	return app.BrowserConfig{
		Enabled:   b.Enabled,
		NoSandbox: b.NoSandbox,
		Options: browser.Options{
			ProfileDir: b.ProfileDir, Binary: b.Binary, AllowPrivate: b.AllowPrivate,
			PageIdle: time.Duration(b.IdleMinutes) * time.Minute,
		},
	}
}

// runWorkspace handles `tal workspace [status|up|down|shell|remove [wipe]]`.
func runWorkspace(ctx context.Context, cfg *config.Config, args []string) error {
	return runWorkspaceWith(ctx, cfg, args, &workspace.Manager{Log: func(s string) { fmt.Println(s) }})
}

func runWorkspaceWith(ctx context.Context, cfg *config.Config, args []string, m *workspace.Manager) error {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "status":
		s, err := m.Status(ctx)
		if err != nil {
			return err
		}
		fmt.Println(s.Describe())
		if !s.Running {
			fmt.Println("\nset it up with: tal workspace up")
		} else {
			fmt.Println("\nuse it with: tal -env workspace   (or offer it to chats: telegram.environments / discord.environments)")
		}
		return nil
	case "up":
		w := cfg.Workspace
		if err := m.Up(ctx, workspace.Options{Network: w.Network, Memory: w.Memory, CPUs: w.CPUs}); err != nil {
			return err
		}
		if err := m.WaitReady(ctx, 20*time.Second); err != nil {
			return err
		}
		fmt.Printf("\nthe workspace is ready: container %s, your files in %s (volume %s)\n", workspace.Container, workspace.Dir, workspace.Volume)
		fmt.Println("use it with: tal -env workspace")
		fmt.Println("or offer it to chats by listing \"workspace\" under telegram.environments / discord.environments")
		return nil
	case "down":
		if err := m.Down(ctx); err != nil {
			return err
		}
		fmt.Println("stopped; your files are kept. `tal workspace up` starts it again.")
		return nil
	case "remove":
		wipe := len(args) > 1 && args[1] == "wipe"
		if len(args) > 1 && !wipe {
			return fmt.Errorf("unknown argument %q (try: tal workspace remove, or tal workspace remove wipe)", args[1])
		}
		if err := m.Remove(ctx, wipe); err != nil {
			return err
		}
		if wipe {
			fmt.Println("removed the container and its volume: every file, login and browser profile in the workspace is gone.")
		} else {
			fmt.Println("removed the container. Your files and logins are kept in the volume; `tal workspace up` makes a new container around them.")
		}
		return nil
	case "shell":
		cmd := exec.CommandContext(ctx, "docker", "exec", "-it", workspace.Container, "bash")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("could not open a shell (is the workspace running? try: tal workspace up): %w", err)
		}
		return nil
	}
	return fmt.Errorf("unknown workspace command %q (try: tal workspace status, up, down, shell, remove)", sub)
}

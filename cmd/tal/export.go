package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"talaria/internal/export"
	"talaria/internal/web"
)

// runExport is `tal export [-o FILE] [-since DATE] [-archived] [-min-tools N]`: the kept web conversations as
// trajectories, one JSON object per line in the chat-completions message format, for training or testing a model elsewhere.
func (c *cli) runExportCmd(args []string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dest := fs.String("o", "", "where to write the file (default: standard output)")
	since := fs.String("since", "", "only conversations touched on or after this date, such as 2026-11-01")
	archived := fs.Bool("archived", false, "include archived conversations")
	minTools := fs.Int("min-tools", 0, "leave out conversations with fewer tool calls than this")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errors.New("use: tal export [-o FILE] [-since DATE] [-archived] [-min-tools N]")
	}
	o := export.Options{IncludeArchived: *archived, MinToolCalls: *minTools}
	if *since != "" {
		t, err := time.Parse("2006-01-02", *since)
		if err != nil {
			return errors.New("-since is a date such as 2026-11-01")
		}
		o.Since = t
	}
	if g, err := buildGuard(c.cfg, c.home, c.getenv, os.Environ()); err == nil && g != nil {
		o.Scrub = g.For("cli", "", 0).Scrub
	} else if err != nil {
		return err
	}
	convos, err := web.OpenConvos(filepath.Join(c.home, "web_chats.json"))
	if err != nil {
		return err
	}
	var out io.Writer = os.Stdout
	if *dest != "" {
		f, err := os.OpenFile(*dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}
	n, err := export.Write(out, convos.Sources(), o)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %d conversations. They hold what you and your tools said: treat the file as private.\n", n)
	return nil
}

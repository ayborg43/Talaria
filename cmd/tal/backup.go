package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"talaria/internal/backup"
	"talaria/internal/config"
)

// runBackup is `tal backup [-o FILE]`: everything in Talaria's home except the
// browser's profile and the vault's key file, in one archive.
func runBackup(cfg *config.Config, home string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dest := fs.String("o", "", "where to write the archive (default: talaria-backup-DATE.tar.gz here)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errors.New("use: tal backup [-o FILE]")
	}
	if *dest == "" {
		*dest = "talaria-backup-" + time.Now().Format("20060102-150405") + ".tar.gz"
	}
	var exclude []string
	if cfg != nil && cfg.Vault.KeyFile != "" {
		exclude = append(exclude, expandHome(cfg.Vault.KeyFile))
	}
	exclude = append(exclude, filepath.Join(home, "vault.key"))

	f, err := os.OpenFile(*dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	rep, err := backup.Create(home, f, backup.Options{Exclude: exclude})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(*dest) // never leave half an archive that looks like a backup
		return err
	}
	fmt.Fprintf(out, "wrote %s: %d files, %d bytes before compression\n", *dest, rep.Files, rep.Bytes)
	for _, s := range rep.Skipped {
		fmt.Fprintf(out, "  left out: %s\n", s)
	}
	fmt.Fprintln(out, "\nTreat this file as a secret: it holds your history, notes, and anything written inline in the config.")
	fmt.Fprintln(out, "The vault inside is encrypted, but its key file is not included: keep the key (or remember the passphrase) somewhere else,")
	fmt.Fprintln(out, "or the saved logins cannot be opened after a restore.")
	return nil
}

// runRestore is `tal restore FILE [-replace]`.
func runRestore(home string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	replace := fs.Bool("replace", false, "move the current state aside and restore over it")
	// Allow the flag after the file name, as people type it.
	var rest []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return errors.New("use: tal restore FILE [-replace]")
		}
		if fs.NArg() == 0 {
			break
		}
		rest = append(rest, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(rest) != 1 {
		return errors.New("use: tal restore FILE [-replace]")
	}
	f, err := os.Open(rest[0])
	if err != nil {
		return err
	}
	defer f.Close()
	moved, n, err := backup.Restore(f, home, *replace, nil)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "restored %d files into %s\n", n, home)
	if moved != "" {
		fmt.Fprintf(out, "the state that was there is kept at %s: delete it once you are happy\n", moved)
	}
	fmt.Fprintln(out, "Start Talaria again. The vault opens with its own passphrase or key file, which a backup does not carry.")
	return nil
}

func expandHome(p string) string {
	if len(p) >= 2 && p[:2] == "~/" {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

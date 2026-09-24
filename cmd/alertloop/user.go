package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/golovanov-dev/alertloop/internal/auth"
	"github.com/golovanov-dev/alertloop/internal/config"
	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

const userUsage = "usage: alertloop [--config FILE] user add [--password-stdin] <login>\n" +
	"       alertloop [--config FILE] user passwd [--password-stdin] <login>\n" +
	"       alertloop [--config FILE] user disable <login>\n" +
	"       alertloop [--config FILE] user enable <login>\n" +
	"       alertloop [--config FILE] user list"

// userCommand is a parsed `alertloop user …` command line.
type userCommand struct {
	action    string
	login     string
	fromStdin bool
}

// parseUserArgs checks the command line of `alertloop user` before anything
// is opened.
func parseUserArgs(args []string) (userCommand, error) {
	if len(args) == 0 {
		return userCommand{}, errors.New(userUsage)
	}
	cmd := userCommand{action: args[0]}
	fs := flag.NewFlagSet("user "+cmd.action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var stdin *bool
	wantLogin := true
	switch cmd.action {
	case "add", "passwd":
		stdin = fs.Bool("password-stdin", false, "read the password from the first line of stdin")
	case "disable", "enable":
	case "list":
		wantLogin = false
	default:
		return userCommand{}, fmt.Errorf("unknown user command %q\n%s", cmd.action, userUsage)
	}
	if err := fs.Parse(args[1:]); err != nil {
		return userCommand{}, fmt.Errorf("%w\n%s", err, userUsage)
	}
	if stdin != nil {
		cmd.fromStdin = *stdin
	}
	switch {
	case wantLogin && fs.NArg() == 1:
		cmd.login = fs.Arg(0)
	case !wantLogin && fs.NArg() == 0:
	default:
		return userCommand{}, errors.New(userUsage)
	}
	return cmd, nil
}

// runUser is `alertloop user …`: console accounts managed from the host, for
// the users after the first and for recovering access. It opens the database
// of cfg and applies pending migrations, as a start would.
func runUser(ctx context.Context, cfg config.Config, cmd userCommand, stdin io.Reader, stdout io.Writer) (err error) {
	if err := cfg.Validate(); err != nil {
		return err
	}
	store, err := storage.Open(cfg.Database.Driver, cfg.Database.DSN)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer func() {
		if cerr := store.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close storage: %w", cerr)
		}
	}()
	if err := store.Migrate(ctx); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	// The report is written in one piece at the end, so a failed write to
	// stdout is one error to return.
	var out strings.Builder
	if err := userAction(ctx, store, cmd, stdin, &out); err != nil {
		return err
	}
	_, err = io.WriteString(stdout, out.String())
	return err
}

func userAction(ctx context.Context, store storage.Store, cmd userCommand, stdin io.Reader, out *strings.Builder) error {
	if cmd.action == "list" {
		return listUsers(ctx, store, out)
	}
	login, err := auth.NormalizeLogin(cmd.login)
	if err != nil {
		return err
	}
	if cmd.action == "add" {
		password, generated, err := readOrGeneratePassword(cmd.fromStdin, stdin)
		if err != nil {
			return err
		}
		u, err := auth.NewUser(login, password)
		if err != nil {
			return err
		}
		if err := store.CreateUser(ctx, u); err != nil {
			return err
		}
		out.WriteString("user " + login + " created\n")
		printGenerated(out, generated, password)
		return nil
	}

	u, err := store.UserByLogin(ctx, login)
	if errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("no user %q", login)
	}
	if err != nil {
		return err
	}
	switch cmd.action {
	case "passwd":
		password, generated, err := readOrGeneratePassword(cmd.fromStdin, stdin)
		if err != nil {
			return err
		}
		hash, err := auth.HashPassword(password)
		if err != nil {
			return err
		}
		if err := store.SetUserPassword(ctx, u.ID, hash, ""); err != nil {
			return err
		}
		out.WriteString("password of " + login + " changed; their sessions have ended\n")
		printGenerated(out, generated, password)
		if u.DisabledAt != nil {
			out.WriteString("note: " + login + " is disabled and cannot sign in until enabled: alertloop user enable " + login + "\n")
		}
	case "disable":
		if err := store.DisableUser(ctx, u.ID, time.Now().UTC()); err != nil {
			return err
		}
		out.WriteString("user " + login + " disabled; their sessions have ended\n")
	case "enable":
		if u.DisabledAt == nil {
			out.WriteString("user " + login + " is already enabled\n")
			return nil
		}
		if err := store.EnableUser(ctx, u.ID); err != nil {
			return err
		}
		out.WriteString("user " + login + " enabled; they sign in with their current password " +
			"(set a new one with: alertloop user passwd " + login + ")\n")
	}
	return nil
}

// readOrGeneratePassword takes the first line of stdin, or makes a password up.
func readOrGeneratePassword(fromStdin bool, stdin io.Reader) (password string, generated bool, err error) {
	if !fromStdin {
		p, err := auth.GeneratePassword()
		return p, true, err
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", false, fmt.Errorf("read the password from stdin: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), false, nil
}

func printGenerated(out *strings.Builder, generated bool, password string) {
	if generated {
		out.WriteString("password (shown once): " + password + "\n")
	}
}

func listUsers(ctx context.Context, store storage.Store, out *strings.Builder) error {
	users, err := store.ListUsers(ctx)
	if err != nil {
		return err
	}
	if len(users) == 0 {
		out.WriteString("no users; create one with: alertloop user add <login>\n")
		return nil
	}
	fmt.Fprintf(out, "%-24s %-9s %-20s %s\n", "LOGIN", "STATE", "LAST LOGIN (UTC)", "ID")
	for _, u := range users {
		state, last := "active", "never"
		if u.DisabledAt != nil {
			state = "disabled"
		}
		if u.LastLoginAt != nil {
			last = u.LastLoginAt.UTC().Format("2006-01-02 15:04:05")
		}
		fmt.Fprintf(out, "%-24s %-9s %-20s %s\n", u.Login, state, last, u.ID)
	}
	return nil
}

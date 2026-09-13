package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/adminauth"
	"github.com/bhaktiyudha/bykami/api/internal/store"
)

// adminCmd manages operator credentials for the console.
//
// It has to be a subcommand rather than a page in the console, and the reason
// is a bootstrap: creating a credential through the console would need somebody
// already signed in to it, and until the first credential exists nobody is.
// Doing it from a shell breaks the circle without inventing a second way in —
// same shape as `bykami frames import`, and for a related reason.
//
//	bykami admin password add kasir-1
//	bykami admin password list
//	bykami admin password rm kasir-1
//	bykami admin password manage kasir-1
//	bykami admin password unmanage kasir-1
func adminCmd(dsn, adminPhones string, args []string) error {
	if len(args) == 0 {
		return errors.New(`admin: want "password"`)
	}
	if args[0] != "password" {
		return fmt.Errorf("admin: unknown command %q", args[0])
	}
	args = args[1:]
	if len(args) == 0 {
		return errors.New(`admin password: want "add", "list", "rm", "manage" or "unmanage"`)
	}

	db, err := store.Open(dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	registry := adminauth.New(db, nil)
	ctx := context.Background()

	switch args[0] {
	case "add":
		if len(args) != 2 {
			return errors.New("admin password add: want <label>")
		}
		pw, err := registry.Add(ctx, args[1])
		if err != nil {
			return err
		}
		fmt.Printf("Added %q.\n\n", args[1])
		fmt.Printf("Password:  %s\n\n", pw)
		fmt.Printf("This password will never be shown again. Anyone who reads it has\n")
		fmt.Printf("full access to the console. Write it down, give it to the operator,\n")
		fmt.Printf("and then forget it.\n")
		return nil

	case "list":
		if len(args) != 1 {
			return errors.New("admin password list: takes no arguments")
		}
		return adminList(ctx, registry)

	case "rm":
		if len(args) != 2 {
			return errors.New("admin password rm: want <label>")
		}
		if err := registry.Remove(ctx, args[1]); err != nil {
			return err
		}
		fmt.Printf("%q disabled. Their sessions stop working on the next request.\n", args[1])
		fmt.Printf("To let them back in, add a new credential — the old password is gone.\n")
		return nil

	case "manage":
		if len(args) != 2 {
			return errors.New("admin password manage: want <label>")
		}
		if err := registry.SetManage(ctx, args[1]); err != nil {
			return err
		}
		fmt.Printf("%q can now manage operators.\n", args[1])
		return nil

	case "unmanage":
		if len(args) != 2 {
			return errors.New("admin password unmanage: want <label>")
		}
		if err := registry.UnsetManage(ctx, args[1]); err != nil {
			return err
		}
		fmt.Printf("%q can no longer manage operators.\n", args[1])
		return nil

	default:
		return fmt.Errorf("admin password: unknown command %q", args[0])
	}
}

func adminList(ctx context.Context, registry *adminauth.Registry) error {
	all, err := registry.List(ctx)
	if err != nil {
		return err
	}
	if len(all) == 0 {
		fmt.Println("no credentials; nobody can sign in to the console")
		fmt.Println("  bykami -db … admin password add kasir-1")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "LABEL\tCREATED\tLAST USED\tSTATE\tMANAGER")
	for _, c := range all {
		state := "ok"
		if c.Disabled {
			state = "disabled"
		}
		mgr := "no"
		if c.CanManage {
			mgr = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", c.Label, day(c.CreatedAt), dayPtr(c.LastUsedAt), state, mgr)
	}
	return w.Flush()
}

func day(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Format("2006-01-02 15:04")
}

func dayPtr(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "never"
	}
	return t.Format("2006-01-02 15:04")
}

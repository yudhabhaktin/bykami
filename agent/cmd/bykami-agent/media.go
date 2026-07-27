package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bhaktiyudha/bykami/agent/internal/printer"
	"github.com/bhaktiyudha/bykami/agent/internal/store"
)

// media is the operator's media counter, and it is deliberately a subcommand
// rather than an HTTP route.
//
// Everything else the booth does is reachable at http://localhost, where the
// defence is that the machine itself is the boundary. Loading media is the one
// operation that would let a hostile page do real damage without touching the
// booth: inflating the counter disables the "not enough paper" refusal, and the
// next customer's strip stops halfway through with no warning. A subcommand
// cannot be reached from a browser at all.
//
//	bykami-agent media status
//	bykami-agent media load 700 "roll 1"
//	bykami-agent media adjust -5 "jam, five sheets wasted"
func media(root string, args []string) error {
	if len(args) == 0 {
		return errors.New(`media: want "status", "load" or "adjust"`)
	}

	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	db, err := store.Open(filepath.Join(abs, "booth.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	// A nil backend: nothing here prints, and a queue that could would be a
	// second process racing the running agent for the printer.
	q := printer.New(db, nil, slog.New(slog.NewTextHandler(os.Stdout, nil)))
	ctx := context.Background()

	switch args[0] {
	case "status":
		remaining, err := q.Remaining(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("%d sheets remaining (%d 2x6 strips)\n", remaining, remaining*2)
		if remaining <= 0 {
			fmt.Println("the booth will refuse to print until a roll is loaded")
		}
		return nil

	case "load":
		if len(args) < 2 {
			return fmt.Errorf("media load: want a sheet count (one roll is %d)", printer.RollSheets)
		}
		sheets, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("media load: %q is not a number", args[1])
		}
		if err := q.LoadRoll(ctx, sheets, strings.Join(args[2:], " ")); err != nil {
			return err
		}

	case "adjust":
		if len(args) < 3 {
			// The reason is required by the ledger, not by this parser: an
			// unexplained correction is the row nobody can defend later.
			return errors.New(`media adjust: want a signed sheet count and a reason`)
		}
		sheets, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("media adjust: %q is not a number", args[1])
		}
		if err := q.AdjustMedia(ctx, sheets, strings.Join(args[2:], " ")); err != nil {
			return err
		}

	default:
		return fmt.Errorf(`media: unknown command %q: want "status", "load" or "adjust"`, args[0])
	}

	remaining, err := q.Remaining(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("%d sheets remaining\n", remaining)
	return nil
}

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/frames"
	"github.com/bhaktiyudha/bykami/api/internal/store"
)

// frameCmd is the frame catalogue from the shell, alongside the console.
//
// The console is the tool an operator uses, and this does not replace it. It
// was written when the console had no working login at all — that flow was the
// customer OTP one, which needs a WhatsApp provider that did not exist — so the
// catalogue could not otherwise be filled for as long as that took.
//
// That reason has since expired: `bykami admin enroll` gives the console a
// login that depends on nobody, and an operator can sign in and upload a frame.
// This stays because a shell path to the catalogue is worth having on its own —
// it is how the box is repaired when the console is the thing that is broken,
// and it needs no browser on a machine that can reach app.bykami.id.
//
// Same shape as `bykami-agent media`: a subcommand, run by somebody who already
// has a shell on the box, reaching the database directly.
//
//	bykami frames list
//	bykami frames import strip-4.png "Klasik Empat" klasik
//	bykami frames publish klasik-empat
//	bykami frames unpublish klasik-empat
//	bykami frames season ramadan-2027 2027-02-08 2027-03-09
func frameCmd(dsn string, args []string) error {
	if len(args) == 0 {
		return errors.New(`frames: want "list", "import", "publish", "unpublish", "season" or "delete"`)
	}

	db, err := store.Open(dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	cat := frames.New(db)
	ctx := context.Background()

	switch args[0] {
	case "list":
		return frameList(ctx, cat)

	case "import":
		if len(args) < 3 {
			return errors.New(`frames import: want <file.png> <name> [group]`)
		}
		group := ""
		if len(args) > 3 {
			group = args[3]
		}
		return frameImport(ctx, cat, args[1], args[2], group)

	case "publish", "unpublish":
		if len(args) != 2 {
			return fmt.Errorf("frames %s: want <id>", args[0])
		}
		on := args[0] == "publish"
		if err := cat.SetPublished(ctx, args[1], on); err != nil {
			return err
		}
		if on {
			fmt.Printf("%s published; booths will pull it within a few minutes\n", args[1])
		} else {
			fmt.Printf("%s withdrawn; booths will drop it within a few minutes\n", args[1])
		}
		return nil

	case "season":
		if len(args) != 4 {
			return errors.New(`frames season: want <id> <from YYYY-MM-DD|-> <last-day YYYY-MM-DD|->`)
		}
		from, err := parseDay(args[2])
		if err != nil {
			return fmt.Errorf("start date: %w", err)
		}
		until, err := parseDay(args[3])
		if err != nil {
			return fmt.Errorf("end date: %w", err)
		}
		// The argument is the last day it runs, as in the console. Stored as the
		// instant it stops, so a frame ending on Lebaran survives Lebaran.
		if !until.IsZero() {
			until = until.AddDate(0, 0, 1)
		}
		if err := cat.SetSeason(ctx, args[1], from, until); err != nil {
			return err
		}
		fmt.Printf("%s season saved\n", args[1])
		return nil

	case "delete":
		if len(args) != 2 {
			return errors.New("frames delete: want <id>")
		}
		if err := cat.Delete(ctx, args[1]); err != nil {
			return err
		}
		fmt.Printf("%s deleted\n", args[1])
		return nil

	default:
		return fmt.Errorf("frames: unknown command %q", args[0])
	}
}

func frameImport(ctx context.Context, cat *frames.Catalogue, path, name, group string) error {
	png, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	f, err := cat.Create(ctx, frames.NewFrame{Name: name, Group: group, PNG: png})
	if err != nil {
		// The catalogue's errors are all about the file, so say what to change
		// about the file rather than only that it failed.
		switch {
		case errors.Is(err, frames.ErrOpaque):
			return fmt.Errorf("%s has no transparent area: the photo holes must be "+
				"transparent, not white", filepath.Base(path))
		case errors.Is(err, frames.ErrNoCells):
			return fmt.Errorf("%s has no photo holes big enough to read: a hole may be "+
				"any shape, but must cover at least 1%% of the sheet", filepath.Base(path))
		case errors.Is(err, frames.ErrSheetSize):
			return fmt.Errorf("%w — printable sizes are %s", err, frames.SheetSizes())
		}
		return err
	}

	fmt.Printf("%s imported as %q: %s, %d photo slots, %d KB\n",
		filepath.Base(path), f.ID, f.Layout, len(f.Cells), (f.Bytes+512)/1024)
	for i, c := range f.Cells {
		fmt.Printf("  slot %d  x=%-5d y=%-5d %dx%d\n", i+1, c.X, c.Y, c.W, c.H)
	}
	// Unpublished, exactly as an upload through the console is. Detection is
	// inference from a picture and somebody has to look at it — printing one is
	// how that is done from a shell.
	fmt.Printf("\nNot on the booths yet. Check the slots above sit on the holes, then:\n")
	fmt.Printf("  bykami frames publish %s\n", f.ID)
	return nil
}

func frameList(ctx context.Context, cat *frames.Catalogue) error {
	all, err := cat.List(ctx)
	if err != nil {
		return err
	}
	if len(all) == 0 {
		fmt.Println("no frames; booths are running on the designs built into the agent")
		return nil
	}

	now := time.Now().UTC()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tGROUP\tLAYOUT\tSLOTS\tSTATE\tSEASON")
	for _, f := range all {
		state := "draft"
		switch {
		case f.Live(now):
			state = "on booths"
		case f.Published:
			// Published but not live is the case worth naming: an operator
			// looking for a frame they published needs to know the window is
			// why it is not on the booth.
			state = "out of season"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n",
			f.ID, dash(f.Group), f.Layout, len(f.Cells), state, season(f))
	}
	return w.Flush()
}

func season(f frames.Frame) string {
	const d = "2006-01-02"
	switch {
	case f.ActiveFrom.IsZero() && f.ActiveUntil.IsZero():
		return "always"
	case f.ActiveFrom.IsZero():
		return "until " + f.ActiveUntil.AddDate(0, 0, -1).Format(d)
	case f.ActiveUntil.IsZero():
		return "from " + f.ActiveFrom.Format(d)
	}
	return f.ActiveFrom.Format(d) + "→" + f.ActiveUntil.AddDate(0, 0, -1).Format(d)
}

// parseDay reads a date, or "-" for no bound at that end.
func parseDay(s string) (time.Time, error) {
	if s = strings.TrimSpace(s); s == "" || s == "-" {
		return time.Time{}, nil
	}
	// UTC, unlike the console's Jakarta midnight. A date typed on the server is
	// typed by somebody reading the server's clock, and the difference is seven
	// hours at the edges of a season that runs for a month.
	return time.Parse("2006-01-02", s)
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

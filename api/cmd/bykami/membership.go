package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/bhaktiyudha/bykami/api/internal/identity"
	"github.com/bhaktiyudha/bykami/api/internal/loyalty"
	"github.com/bhaktiyudha/bykami/api/internal/membership"
	"github.com/bhaktiyudha/bykami/api/internal/store"
)

// membershipCmd brings the paper cards in.
//
// This is the migration the change is judged on: the cards in customers' hands
// become rows without anyone re-registering. Each row is a name, a WhatsApp
// number, the stamps already ticked and the card's barcode, which is what the
// printed card carries and what makes re-running safe.
//
// The gifts already collected on paper are minted and immediately marked
// redeemed, naming the operator who ran this — without that step every member
// with a nearly-full card collects four free gifts in the same week.
//
//	bykami -db … -admin-phones 08… membership import -file cards.csv
//
// Re-running the same file is safe: a row whose barcode is already in the
// ledger is skipped, and nothing is credited or issued twice.
func membershipCmd(dsn string, operator string, args []string) error {
	if len(args) == 0 {
		return errors.New(`membership: want "import"`)
	}

	switch args[0] {
	case "import":
		fs := flag.NewFlagSet("membership import", flag.ContinueOnError)
		file := fs.String("file", "", "CSV of paper cards: name,whatsapp,stamps,barcode")
		outlet := fs.String("outlet", "", "outlet id to attribute the import to")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *file == "" {
			return errors.New("membership import: -file is required")
		}
		if operator == "" {
			// Provenance is the point: the gifts minted here are marked as
			// having been handed over by somebody, and an anonymous import
			// would be a set of gifts nobody can be asked about.
			return errors.New("membership import: no operator to attribute this to; " +
				"pass -admin-phones with at least one number")
		}
		return membershipImport(dsn, *file, operator, *outlet)

	default:
		return fmt.Errorf("membership: unknown command %q", args[0])
	}
}

func membershipImport(dsn, path, operator, outlet string) error {
	rows, err := readCards(path)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("%s: no rows", path)
	}

	db, err := store.Open(dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	svc := membership.New(db, loyalty.New(db), identity.New(db, disabledSender{}), nil)
	res, err := svc.Import(context.Background(), rows, operator, outlet)
	if err != nil {
		return err
	}

	fmt.Printf("%s: %d imported, %d already present, %d cards\n",
		path, res.Imported, res.Skipped, len(res.Cards))
	for _, card := range res.Cards {
		fmt.Printf("  kartu %d  %s  %d/%d  %s\n",
			card.CardNo, card.Phone, card.Filled, card.StampsPerCard, card.Name)
	}
	if res.Imported == 0 && res.Skipped > 0 {
		fmt.Println("\nNothing changed: every barcode in this file is already in the database.")
	}
	return nil
}

// readCards parses the file. An optional header row is skipped rather than
// imported as a member called "name", which is the mistake a shop's export
// makes once every time.
func readCards(path string) ([]membership.ImportRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	// Ragged rows are a shop's spreadsheet, not a protocol error; the fields
	// are named one by one below and a short row is rejected there.
	r.FieldsPerRecord = -1

	var out []membership.ImportRow
	for line := 1; ; line++ {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		if len(rec) == 0 || strings.TrimSpace(rec[0]) == "" {
			continue
		}
		if line == 1 && strings.EqualFold(strings.TrimSpace(rec[0]), "name") {
			continue
		}
		if len(rec) < 4 {
			return nil, fmt.Errorf("%s:%d: want name,whatsapp,stamps,barcode", path, line)
		}
		stamps, err := strconv.Atoi(strings.TrimSpace(rec[2]))
		if err != nil {
			return nil, fmt.Errorf("%s:%d: stamps %q is not a number", path, line, rec[2])
		}
		out = append(out, membership.ImportRow{
			Name:    strings.TrimSpace(rec[0]),
			Phone:   strings.TrimSpace(rec[1]),
			Stamps:  stamps,
			Barcode: strings.TrimSpace(rec[3]),
		})
	}
	return out, nil
}

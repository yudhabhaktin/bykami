package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/mfa"
	"github.com/bhaktiyudha/bykami/api/internal/phone"
	"github.com/bhaktiyudha/bykami/api/internal/qr"
	"github.com/bhaktiyudha/bykami/api/internal/store"
	"github.com/bhaktiyudha/bykami/api/internal/totp"
)

// issuer is the name the authenticator app shows above the code. Short on
// purpose: it sits in a list on a phone next to a dozen others.
const issuer = "bykami"

// adminCmd enrols and revokes the authenticators the operator console logs in
// with.
//
// It has to be a subcommand rather than a page in the console, and the reason
// is a bootstrap: enrolling through the console would need somebody already
// signed in to it, and until the first enrolment exists nobody is. Doing it
// from a shell breaks the circle without inventing a second way in — same shape
// as `bykami frames import`, and for a related reason.
//
// It is not a way to grant access. The allow-list that decides who may use the
// console is -admin-phones, in the service configuration, and a secret created
// here for a number that is not on it produces valid codes that open nothing.
// That is what makes running this safe for anybody who already has the
// database, which is anybody who could read the secrets out of it anyway.
//
//	bykami admin enroll 081234567890 [qr.png]
//	bykami admin list
//	bykami admin unlock 081234567890
//	bykami admin revoke 081234567890
func adminCmd(dsn, adminPhones string, args []string) error {
	if len(args) == 0 {
		return errors.New(`admin: want "enroll", "list", "unlock" or "revoke"`)
	}

	db, err := store.Open(dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	registry := mfa.New(db)
	ctx := context.Background()

	switch args[0] {
	case "enroll":
		if len(args) < 2 || len(args) > 3 {
			return errors.New("admin enroll: want <phone> [qr.png]")
		}
		pngPath := ""
		if len(args) == 3 {
			pngPath = args[2]
		}
		return adminEnroll(ctx, registry, adminPhones, args[1], pngPath)

	case "list":
		if len(args) != 1 {
			return errors.New("admin list: takes no arguments")
		}
		return adminList(ctx, registry, adminPhones)

	case "unlock":
		if len(args) != 2 {
			return errors.New("admin unlock: want <phone>")
		}
		if err := registry.Unlock(ctx, args[1]); err != nil {
			return err
		}
		fmt.Printf("%s unlocked; they can try again now\n", args[1])
		return nil

	case "revoke":
		if len(args) != 2 {
			return errors.New("admin revoke: want <phone>")
		}
		if err := registry.Revoke(ctx, args[1]); err != nil {
			return err
		}
		fmt.Printf("%s revoked. Codes from that phone stop working immediately.\n", args[1])
		fmt.Printf("To let them back in, enrol again — it is a new secret and a new QR code.\n")
		return nil

	default:
		return fmt.Errorf("admin: unknown command %q", args[0])
	}
}

func adminEnroll(ctx context.Context, registry *mfa.Registry, adminPhones, rawPhone, pngPath string) error {
	e164, secret, err := registry.Enroll(ctx, rawPhone)
	if err != nil {
		if errors.Is(err, mfa.ErrAlreadyEnrolled) {
			// Refused rather than replaced, because replacing would silently
			// break whatever is on that operator's phone and nobody would find
			// out until they next tried to sign in.
			return fmt.Errorf("%w — revoke it first if the phone is lost:\n"+
				"  bykami -db … admin revoke %s", err, rawPhone)
		}
		return err
	}

	uri := totp.URI(issuer, e164, secret)
	code, err := qr.Encode(uri)
	if err != nil {
		return fmt.Errorf("admin enroll: %w", err)
	}

	fmt.Printf("Enrolled %s.\n\n", e164)
	fmt.Print(code.Terminal())
	fmt.Printf("\nScan that with Google Authenticator, Microsoft Authenticator, 1Password,\n")
	fmt.Printf("Aegis, or whatever the operator already has.\n\n")

	// Printed alongside the QR, always, because the picture is the part most
	// likely not to work: a terminal on a legacy code page draws the blocks as
	// question marks, and a narrow window wraps the symbol into nonsense.
	fmt.Printf("If the square above is unreadable, type this key in by hand instead:\n")
	fmt.Printf("  %s\n\n", spaced(totp.Encode(secret)))

	if pngPath != "" {
		png, err := code.PNG(8)
		if err != nil {
			return fmt.Errorf("admin enroll: %w", err)
		}
		// 0600: this file is the credential until it is scanned and deleted.
		if err := os.WriteFile(pngPath, png, 0o600); err != nil {
			return fmt.Errorf("admin enroll: write %s: %w", pngPath, err)
		}
		fmt.Printf("Also written to %s. Delete it once it has been scanned — anyone\n", pngPath)
		fmt.Printf("who opens that file can sign in as this operator.\n\n")
	}

	// The other half of the credential, said plainly. The secret has been shown
	// on a screen and possibly written to a file, and neither can be un-shown.
	fmt.Printf("The key above is the whole credential. It has been displayed once and is\n")
	fmt.Printf("not recoverable from the database in this form; if it went somewhere it\n")
	fmt.Printf("should not have, revoke and enrol again.\n")

	switch {
	case adminPhones == "":
		// Nothing was passed, which is the normal way to run this — the flag
		// belongs to the service, not to a shell. So say what still has to be
		// true rather than asserting it is not, which would be a warning that
		// fires every time and teaches everyone to ignore it.
		fmt.Printf("\nThis opens nothing on its own: %s must also be in the service's\n", e164)
		fmt.Printf("-admin-phones. Check ansible/roles/app -> app_admin_phones, and pass\n")
		fmt.Printf("-admin-phones here if you want this command to check it for you.\n")
	case !listed(adminPhones, e164):
		// A warning rather than an error, because the order does not matter:
		// enrolling first and editing the unit afterwards is a perfectly
		// reasonable way round, and refusing would make this command depend on
		// a flag that is only really meaningful to the running service.
		fmt.Printf("\nNote: %s is not in the -admin-phones passed here, so this\n", e164)
		fmt.Printf("authenticator opens nothing yet. Add it to the service configuration\n")
		fmt.Printf("and restart:  ansible/roles/app -> app_admin_phones\n")
	}
	return nil
}

func adminList(ctx context.Context, registry *mfa.Registry, adminPhones string) error {
	all, err := registry.List(ctx)
	if err != nil {
		return err
	}
	if len(all) == 0 {
		fmt.Println("no authenticators enrolled; nobody can sign in to the console")
		fmt.Println("  bykami -db … admin enroll 081234567890")
		return nil
	}

	now := time.Now().UTC()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PHONE\tENROLLED\tLAST USED\tSTATE")
	for _, e := range all {
		state := "ok"
		switch {
		case e.Locked(now):
			state = fmt.Sprintf("locked %dm", int(e.LockedUntil.Sub(now).Minutes())+1)
		case adminPhones == "":
			// No allow-list was passed, so whether this number is an operator
			// is simply not known here. Saying "ok" would be a guess and
			// "not an operator" would be a lie.
			state = "enrolled"
		case !listed(adminPhones, e.Phone):
			// Worth its own state: an enrolment that works perfectly and opens
			// nothing looks identical to a broken one from the operator's side.
			state = "not an operator"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.Phone, day(e.CreatedAt), day(e.LastUsed), state)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if adminPhones == "" {
		fmt.Println("\nWhether each number may actually use the console is not shown: that is")
		fmt.Println("-admin-phones, which belongs to the service. Pass it here to have it checked.")
	}
	return nil
}

// listed reports whether a normalised number is in the allow-list the service
// was given. The list is parsed the same way main.go parses it, then normalised
// the same way admin.New normalises it, so that a number written 0812… here and
// +62812… there still matches.
func listed(adminPhones, e164 string) bool {
	for _, raw := range splitPhones(adminPhones) {
		if got, err := phone.Normalize(raw); err == nil && got == e164 {
			return true
		}
	}
	return false
}

// spaced breaks a base32 secret into groups of four, which is the difference
// between a key somebody can retype off a screen and one they give up on.
func spaced(s string) string {
	var b strings.Builder
	for i, c := range s {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func day(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Format("2006-01-02 15:04")
}

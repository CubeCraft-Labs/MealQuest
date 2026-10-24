package dinners_test

import (
	"crypto/rand"
	"errors"
	"fmt"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
)

// newRemoval is a new removal id, as the PWA makes with crypto.randomUUID.
func newRemoval() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// regular adds a Regular named name and returns its id.
func (e *env) regular(t *testing.T, name string) string {
	t.Helper()
	c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: &name}, member)
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// TestArchiveForARemovalIsMadeOnce: a dinner archived for a removal records
// it as made, so sending it again succeeds and withdrawing it then answers
// that it was made.
func TestArchiveForARemovalIsMadeOnce(t *testing.T) {
	t.Parallel()
	e := newUnstartedEnv(t)
	id := e.regular(t, "Chili")
	removal := newRemoval()

	if _, err := e.svc.Archive(t.Context(), id, removal, member); err != nil {
		t.Fatal(err)
	}
	if d, err := e.svc.Archive(t.Context(), id, removal, member); err != nil || d.Status != dinners.StatusArchived {
		t.Fatalf("archive again = %+v, %v", d, err)
	}
	if w, err := e.svc.WithdrawRemoval(t.Context(), id, removal, member); err != nil || w != (dinners.Withdrawal{Outcome: dinners.RemovalArchived, Status: dinners.StatusArchived}) {
		t.Fatalf("withdraw a removal made = %+v, %v; want archived, dinner archived", w, err)
	}
	if row := e.dinner(t, id); row.Status != dinners.StatusArchived {
		t.Errorf("status = %s", row.Status)
	}
}

// TestWithdrawnRemovalArrivingLateArchivesNothing: a removal withdrawn
// before it arrives — sent by a tab that closed, its answer lost — is
// refused when it does arrive, and the dinner stays, for good.
func TestWithdrawnRemovalArrivingLateArchivesNothing(t *testing.T) {
	t.Parallel()
	e := newUnstartedEnv(t)
	id := e.regular(t, "Chili")
	removal := newRemoval()

	if w, err := e.svc.WithdrawRemoval(t.Context(), id, removal, member); err != nil || w != (dinners.Withdrawal{Outcome: dinners.RemovalWithdrawn, Status: dinners.StatusRegular}) {
		t.Fatalf("withdraw = %+v, %v; want withdrawn, dinner regular", w, err)
	}
	if _, err := e.svc.Archive(t.Context(), id, removal, member); !errors.Is(err, dinners.ErrRemovalWithdrawn) {
		t.Fatalf("late archive = %v, want ErrRemovalWithdrawn", err)
	}
	if row := e.dinner(t, id); row.Status != dinners.StatusRegular {
		t.Errorf("status after a withdrawn removal = %s, want regular", row.Status)
	}
	if w, err := e.svc.WithdrawRemoval(t.Context(), id, removal, member); err != nil || w != (dinners.Withdrawal{Outcome: dinners.RemovalWithdrawn, Status: dinners.StatusRegular}) {
		t.Errorf("withdraw again = %+v, %v; want withdrawn, dinner regular", w, err)
	}
	// A new removal by the household is made.
	if _, err := e.svc.Archive(t.Context(), id, newRemoval(), member); err != nil {
		t.Fatal(err)
	}
	if row := e.dinner(t, id); row.Status != dinners.StatusArchived {
		t.Errorf("status = %s, want archived", row.Status)
	}
}

// TestWithdrawalAnswersADinnerAnotherRemovalArchived: removal R1, sent by
// one device and answered with a failure, is withdrawn only after removal
// R2, from another device, archived the dinner. R1 is withdrawn — never
// made — but the answer says the dinner is archived, so the device that
// withdrew R1 does not take it for a dinner still shown.
func TestWithdrawalAnswersADinnerAnotherRemovalArchived(t *testing.T) {
	t.Parallel()
	e := newUnstartedEnv(t)
	id := e.regular(t, "Chili")
	r1, r2 := newRemoval(), newRemoval()

	if _, err := e.svc.Archive(t.Context(), id, r2, member); err != nil {
		t.Fatal(err)
	}
	w, err := e.svc.WithdrawRemoval(t.Context(), id, r1, member)
	if err != nil {
		t.Fatal(err)
	}
	if w != (dinners.Withdrawal{Outcome: dinners.RemovalWithdrawn, Status: dinners.StatusArchived}) {
		t.Errorf("withdraw R1 after R2 archived = %+v; want withdrawn, dinner archived", w)
	}
	if _, err := e.svc.Archive(t.Context(), id, r1, member); !errors.Is(err, dinners.ErrRemovalWithdrawn) {
		t.Errorf("late R1 = %v, want ErrRemovalWithdrawn", err)
	}
	if w, err := e.svc.WithdrawRemoval(t.Context(), id, r2, member); err != nil || w != (dinners.Withdrawal{Outcome: dinners.RemovalArchived, Status: dinners.StatusArchived}) {
		t.Errorf("withdraw R2 = %+v, %v; want archived, dinner archived", w, err)
	}
}

func TestRemovalRefusals(t *testing.T) {
	t.Parallel()
	e := newUnstartedEnv(t)
	chili := e.regular(t, "Chili")
	stew := e.regular(t, "Stew")
	removal := newRemoval()
	if _, err := e.svc.Archive(t.Context(), chili, removal, member); err != nil {
		t.Fatal(err)
	}

	var invalid *dinners.ValidationError
	for _, bad := range []string{"", "nope", "0190B8C4-7E2A-4C1D-9F3E-2A1B3C4D5E6F", "0190b8c4x7e2a-4c1d-9f3e-2a1b3c4d5e6f", "0190b8c4-7e2a-4c1d-9f3e-2a1b3c4d5e6"} {
		if _, err := e.svc.Archive(t.Context(), stew, bad, member); !errors.As(err, &invalid) {
			t.Errorf("archive for removal %q = %v, want a ValidationError", bad, err)
		}
		if _, err := e.svc.WithdrawRemoval(t.Context(), stew, bad, member); !errors.As(err, &invalid) {
			t.Errorf("withdraw removal %q = %v, want a ValidationError", bad, err)
		}
	}
	// A removal of one dinner is not a removal of another.
	if _, err := e.svc.Archive(t.Context(), stew, removal, member); !errors.As(err, &invalid) {
		t.Errorf("archive stew for chili's removal = %v, want a ValidationError", err)
	}
	if _, err := e.svc.WithdrawRemoval(t.Context(), stew, removal, member); !errors.As(err, &invalid) {
		t.Errorf("withdraw chili's removal as stew's = %v, want a ValidationError", err)
	}
	if row := e.dinner(t, stew); row.Status != dinners.StatusRegular {
		t.Errorf("stew status = %s, want regular", row.Status)
	}
	// An unknown dinner records no removal.
	unknown := newRemoval()
	if _, err := e.svc.Archive(t.Context(), "nope", unknown, member); !errors.Is(err, dinners.ErrNotFound) {
		t.Errorf("archive unknown dinner = %v, want ErrNotFound", err)
	}
	if _, err := e.svc.WithdrawRemoval(t.Context(), "nope", unknown, member); !errors.Is(err, dinners.ErrNotFound) {
		t.Errorf("withdraw for unknown dinner = %v, want ErrNotFound", err)
	}
	var n int
	if err := e.st.Reader.QueryRow(`SELECT count(*) FROM dinner_removals`).Scan(&n); err != nil || n != 1 {
		t.Errorf("removals recorded = %d, %v; want 1", n, err)
	}
}

package shopping_test

import (
	"errors"
	"net/http"
	"slices"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/chat"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/chat/chattest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify/notifytest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/shopping"
)

// TestGenerateEmitsListReadyOnce: the week's first Supply run stores
// list_ready keyed by the week, on push and Google Chat; remaking it, and
// making it again after Push to Calendar, stores nothing more.
func TestGenerateEmitsListReadyOnce(t *testing.T) {
	e := newEnv(t)
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"}, ing{"onion", "1", ""})
	e.dinner(t, "Pad Thai", ing{"rice noodles", "8", "oz"})
	e.dinner(t, "Pot Roast")
	e.plan(t, "Beef Chili", "Pad Thai", "Pot Roast")

	if _, err := e.svc.Generate(t.Context(), mustDate("2026-10-19")); !errors.Is(err, shopping.ErrPlanNotFound) {
		t.Fatalf("Generate of a week with no plan = %v", err)
	}
	if rows := notifytest.Rows(t, e.st, notify.ListReady); len(rows) != 0 {
		t.Fatalf("list_ready rows after a failed generation = %+v, want none", rows)
	}

	e.generate(t)
	rows := notifytest.Rows(t, e.st, notify.ListReady)
	if len(rows) != 2 || rows[0].Channel != notify.ChannelChat || rows[1].Channel != notify.ChannelPush {
		t.Fatalf("list_ready rows = %+v, want one on chat and one on push", rows)
	}
	for _, r := range rows {
		p := r.Payload
		if r.Key != "list_ready:2026-10-12" || p.URL != "/plan/shopping" || p.Week != "2026-10-12" ||
			p.Title != "This week's Supply run is ready" || p.Body != "3 items to buy for 2 dinners" {
			t.Errorf("%s row = %+v, want list_ready:2026-10-12, 3 items for 2 dinners, /plan/shopping", r.Channel, r)
		}
	}

	e.swap(t, "2026-10-14", "Pad Thai")
	e.swap(t, "2026-10-13", "Pot Roast")
	e.generate(t)
	if _, err := e.svc.GeneratePushed(t.Context(), week, planRevision(t, e)); err != nil {
		t.Fatal(err)
	}
	if again := notifytest.Rows(t, e.st, notify.ListReady); !slices.Equal(again, rows) {
		t.Errorf("list_ready rows after remaking = %+v, want the first list's only", again)
	}
}

// planRevision is the revision of the env's plan.
func planRevision(t *testing.T, e *env) int64 {
	t.Helper()
	var rev int64
	if err := e.st.Reader.QueryRow(`SELECT revision FROM week_plans WHERE id = ?`, e.planID).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	return rev
}

// TestSendToChat posts the Supply run's needed items by dinner, records
// sent_to_chat_at, sends a week once, and again only with resend.
func TestSendToChat(t *testing.T) {
	e := newEnv(t)
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"}, ing{"onion", "1", ""})
	e.dinner(t, "Pad Thai", ing{"rice noodles", "8", "oz"})
	e.plan(t, "Beef Chili", "Pad Thai")
	l := e.generate(t).List
	e.haveIt(t, l, "Pad Thai", "rice noodles")
	if _, err := e.chatSvc.Put(t.Context(), chattest.Webhook, "Family"); err != nil {
		t.Fatal(err)
	}

	sent, err := e.svc.SendToChat(t.Context(), week, false)
	if err != nil {
		t.Fatal(err)
	}
	if sent.SentToChatAt == nil {
		t.Fatal("sent_to_chat_at not recorded")
	}
	posts := e.chat.Posts()
	if len(posts) != 1 {
		t.Fatalf("Google Chat got %d posts, want 1", len(posts))
	}
	want := chat.SupplyRunText("Week 42 · Oct 12 – 18", []chat.SupplyRunGroup{
		{Dinner: "Beef Chili", Items: []chat.SupplyRunItem{{Name: "ground beef", Qty: "1 lb"}, {Name: "onion", Qty: "1"}}},
		{Dinner: "Pad Thai"},
	})
	if posts[0].Text() != want {
		t.Errorf("posted text = %q, want %q", posts[0].Text(), want)
	}
	if posts[0].Query.Get("threadKey") != chat.ThreadKey(week) {
		t.Errorf("posted in thread %q, want %q", posts[0].Query.Get("threadKey"), chat.ThreadKey(week))
	}
	if got, err := e.svc.Get(t.Context(), week); err != nil || got.SentToChatAt == nil || *got.SentToChatAt != *sent.SentToChatAt {
		t.Errorf("stored sent_to_chat_at = %v, %v, want %s", got.SentToChatAt, err, *sent.SentToChatAt)
	}

	again, err := e.svc.SendToChat(t.Context(), week, false)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(e.chat.Posts()); n != 1 || *again.SentToChatAt != *sent.SentToChatAt {
		t.Errorf("send again without resend: %d posts, sent_to_chat_at %s, want 1 post and %s", n, *again.SentToChatAt, *sent.SentToChatAt)
	}

	if _, err := e.st.Writer.Exec(`UPDATE shopping_lists SET title = 'Chili and noodles'`); err != nil {
		t.Fatal(err)
	}
	resent, err := e.svc.SendToChat(t.Context(), week, true)
	if err != nil {
		t.Fatal(err)
	}
	posts = e.chat.Posts()
	if len(posts) != 2 || *resent.SentToChatAt < *sent.SentToChatAt {
		t.Fatalf("resend: %d posts, sent_to_chat_at %s, want 2 posts and a later time than %s", len(posts), *resent.SentToChatAt, *sent.SentToChatAt)
	}
	if want := "Supply run · Chili and noodles"; posts[1].Text()[:len(want)] != want {
		t.Errorf("resent text = %q, want it headed by the title", posts[1].Text())
	}
}

// TestSendToChatFailuresRecordNothing: a send with no webhook, or one
// Google Chat refuses, leaves sent_to_chat_at and chat_send_unconfirmed_at
// unset; a week with no list, or a frozen one, is refused without posting.
func TestSendToChatFailuresRecordNothing(t *testing.T) {
	e := newEnv(t)
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"})
	e.plan(t, "Beef Chili")

	if _, err := e.svc.SendToChat(t.Context(), week, false); !errors.Is(err, shopping.ErrListNotFound) {
		t.Errorf("SendToChat with no list = %v, want ErrListNotFound", err)
	}
	e.generate(t)
	if _, err := e.svc.SendToChat(t.Context(), week, false); !errors.Is(err, chat.ErrNotConfigured) {
		t.Errorf("SendToChat with no webhook = %v, want chat.ErrNotConfigured", err)
	}
	if _, err := e.chatSvc.Put(t.Context(), chattest.Webhook, "Family"); err != nil {
		t.Fatal(err)
	}
	e.chat.SetStatus(http.StatusInternalServerError)
	if _, err := e.svc.SendToChat(t.Context(), week, false); !errors.Is(err, chat.ErrRefused) {
		t.Errorf("SendToChat answered 500 = %v, want chat.ErrRefused", err)
	}
	if l, err := e.svc.Get(t.Context(), week); err != nil || l.SentToChatAt != nil || l.ChatSendUnconfirmedAt != nil {
		t.Errorf("after failed sends sent_to_chat_at = %v, chat_send_unconfirmed_at = %v, %v, want both unset", l.SentToChatAt, l.ChatSendUnconfirmedAt, err)
	}

	if _, err := e.st.Writer.Exec(`UPDATE shopping_lists SET status = 'archived', frozen_at = '2026-10-19T04:05:00Z'`); err != nil {
		t.Fatal(err)
	}
	posts := len(e.chat.Posts())
	if _, err := e.svc.SendToChat(t.Context(), week, true); !errors.Is(err, shopping.ErrFrozen) {
		t.Errorf("SendToChat of a frozen list = %v, want ErrFrozen", err)
	}
	if n := len(e.chat.Posts()); n != posts {
		t.Errorf("a frozen list was posted")
	}
}

// TestSendToChatUnconfirmed: a send whose outcome is not known, because
// Google Chat accepted the post but recording it failed, or did not answer,
// is ErrSendUnconfirmed and leaves the send unconfirmed rather than unsent:
// a send without resend does not post again, and one with resend does,
// confirming it.
func TestSendToChatUnconfirmed(t *testing.T) {
	e := newEnv(t)
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"})
	e.plan(t, "Beef Chili")
	e.generate(t)
	if _, err := e.chatSvc.Put(t.Context(), chattest.Webhook, "Family"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer.Exec(`CREATE TRIGGER refuse_sent BEFORE UPDATE OF sent_to_chat_at ON shopping_lists BEGIN SELECT RAISE(ABORT, 'sent refused'); END`); err != nil {
		t.Fatal(err)
	}

	if _, err := e.svc.SendToChat(t.Context(), week, false); !errors.Is(err, shopping.ErrSendUnconfirmed) {
		t.Fatalf("SendToChat accepted but not recorded = %v, want ErrSendUnconfirmed", err)
	}
	if n := len(e.chat.Posts()); n != 1 {
		t.Fatalf("Google Chat got %d posts, want 1", n)
	}
	l, err := e.svc.Get(t.Context(), week)
	if err != nil || l.SentToChatAt != nil || l.ChatSendUnconfirmedAt == nil {
		t.Fatalf("after an unrecorded send sent_to_chat_at = %v, chat_send_unconfirmed_at = %v, %v, want unset and set", l.SentToChatAt, l.ChatSendUnconfirmedAt, err)
	}
	if _, err := e.st.Writer.Exec(`DROP TRIGGER refuse_sent`); err != nil {
		t.Fatal(err)
	}

	again, err := e.svc.SendToChat(t.Context(), week, false)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(e.chat.Posts()); n != 1 || again.ChatSendUnconfirmedAt == nil || *again.ChatSendUnconfirmedAt != *l.ChatSendUnconfirmedAt {
		t.Errorf("send again without resend: %d posts, chat_send_unconfirmed_at %v, want 1 post and %s", n, again.ChatSendUnconfirmedAt, *l.ChatSendUnconfirmedAt)
	}

	resent, err := e.svc.SendToChat(t.Context(), week, true)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(e.chat.Posts()); n != 2 || resent.SentToChatAt == nil || resent.ChatSendUnconfirmedAt != nil {
		t.Errorf("resend: %d posts, sent_to_chat_at %v, chat_send_unconfirmed_at %v, want 2 posts, set and unset", n, resent.SentToChatAt, resent.ChatSendUnconfirmedAt)
	}

	e.chat.Close()
	if _, err := e.svc.SendToChat(t.Context(), week, true); !errors.Is(err, shopping.ErrSendUnconfirmed) || !errors.Is(err, chat.ErrUnreachable) {
		t.Errorf("SendToChat unanswered = %v, want ErrSendUnconfirmed and chat.ErrUnreachable", err)
	}
	if l, err := e.svc.Get(t.Context(), week); err != nil || l.SentToChatAt == nil || *l.SentToChatAt != *resent.SentToChatAt || l.ChatSendUnconfirmedAt == nil {
		t.Errorf("after an unanswered resend sent_to_chat_at = %v, chat_send_unconfirmed_at = %v, %v, want %s and set", l.SentToChatAt, l.ChatSendUnconfirmedAt, err, *resent.SentToChatAt)
	}
}

// TestSendToChatPostsOnlyOnceBegun: a send that cannot be recorded as
// begun is not posted.
func TestSendToChatPostsOnlyOnceBegun(t *testing.T) {
	e := newEnv(t)
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"})
	e.plan(t, "Beef Chili")
	e.generate(t)
	if _, err := e.chatSvc.Put(t.Context(), chattest.Webhook, "Family"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer.Exec(`CREATE TRIGGER refuse_begin BEFORE UPDATE OF chat_send_unconfirmed_at ON shopping_lists WHEN NEW.chat_send_unconfirmed_at IS NOT NULL BEGIN SELECT RAISE(ABORT, 'begin refused'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SendToChat(t.Context(), week, false); err == nil {
		t.Fatal("SendToChat not recorded as begun succeeded, want an error")
	}
	if n := len(e.chat.Posts()); n != 0 {
		t.Errorf("Google Chat got %d posts of a send not recorded as begun, want 0", n)
	}
}

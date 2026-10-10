package chat

import (
	"fmt"
	"strings"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// This file is the one place the notification texts are written. The
// builders make the notify.Event of each event type, whose title and body
// the push channel shows as they are; Text turns its payload into the Google
// Chat message, such as "This week's plan is ready: Tacos, Bolognese,
// Tikka masala + 4 more. Open the plan →".

// TestText is the message POST /settings/chat-webhook/test sends.
const TestText = "MealQuest · test message"

// listedNames is how many names a message lists before "+ N more".
const listedNames = 3

// actions is the closing call to action of each event's Chat message.
var actions = map[notify.EventType]string{
	notify.PlanReady:      "Open the plan",
	notify.ListReady:      "Open the Supply run",
	notify.DayOfReminder:  "Open the recipe",
	notify.NewSuggestions: "Open Discover",
	notify.RotationReview: "Open MealQuest",
}

// carded are the events whose Chat message also has a card with an "Open"
// button.
var carded = map[notify.EventType]bool{notify.PlanReady: true, notify.ListReady: true}

// PlanReady is the plan_ready event of the week starting on week, drafted
// in the week starting on thisWeek, naming its dinners in plan order. Its
// title names the week as seen from thisWeek: "Next week's plan is ready",
// "This week's plan is ready", or "The plan for the week of Oct 26 is
// ready" for any other week.
func PlanReady(week, thisWeek store.LocalDate, dinners []string) notify.Event {
	return notify.Event{
		Type:  notify.PlanReady,
		Key:   string(notify.PlanReady) + ":" + week.String(),
		Title: planTitle(week, thisWeek),
		Body:  nameList(dinners),
		URL:   "/plan",
		Week:  week,
	}
}

// ListReady is the list_ready event of the Supply run of the week starting
// on week: needed items to buy for its dinners.
func ListReady(week store.LocalDate, needed, dinners int) notify.Event {
	return notify.Event{
		Type:  notify.ListReady,
		Key:   string(notify.ListReady) + ":" + week.String(),
		Title: "This week's Supply run is ready",
		Body:  fmt.Sprintf("%s to buy for %s", count(needed, "item", "items"), count(dinners, "dinner", "dinners")),
		URL:   "/plan/shopping",
		Week:  week,
	}
}

// DayOfReminder is the day_of_reminder event of the dinner dinnerID, named
// name, planned on date: "Tonight: Tacos · Recipe →", a title carrying its
// own call to action.
func DayOfReminder(date store.LocalDate, dinnerID, name string) notify.Event {
	return notify.Event{
		Type:  notify.DayOfReminder,
		Key:   string(notify.DayOfReminder) + ":" + date.String(),
		Title: "Tonight: " + name + " · Recipe →",
		URL:   "/dinner/" + dinnerID,
		Week:  household.WeekStart(date),
	}
}

// StartReminder is the day_of_reminder event, keyed
// day_of_reminder:start:{date}, reminding the household to start the
// crock-pot dinner dinnerID, named name and planned on date, by startBy,
// a time in the household zone: "Start the crock pot by 9:30 am".
func StartReminder(date store.LocalDate, dinnerID, name string, startBy time.Time) notify.Event {
	return notify.Event{
		Type:  notify.DayOfReminder,
		Key:   string(notify.DayOfReminder) + ":start:" + date.String(),
		Title: "Start the crock pot by " + startBy.Format("3:04 pm"),
		Body:  name,
		URL:   "/dinner/" + dinnerID,
		Week:  household.WeekStart(date),
	}
}

// NewSuggestions is the new_suggestions event keyed by id, the ISO week
// ("2026-W41") of a scheduled suggestion batch or the batch id of a manual
// one, of the week starting on week: how many new Side quests there are,
// and their names.
func NewSuggestions(id string, week store.LocalDate, names []string) notify.Event {
	return notify.Event{
		Type:  notify.NewSuggestions,
		Key:   string(notify.NewSuggestions) + ":" + id,
		Title: count(len(names), "new Side quest is in", "new Side quests are in"),
		Body:  nameList(names),
		URL:   "/discover",
		Week:  week,
	}
}

// RotationReview is the rotation_review event, keyed
// rotation_review:{dinnerID}:{planDate}, of the Rotation check of the
// dinner dinnerID, named name and tried on planDate: "How was Pho? Keep it
// in rotation?". It opens Home, where the Rotation check card asks.
func RotationReview(dinnerID, name string, planDate store.LocalDate) notify.Event {
	return notify.Event{
		Type:  notify.RotationReview,
		Key:   string(notify.RotationReview) + ":" + dinnerID + ":" + planDate.String(),
		Title: "How was " + name + "? Keep it in rotation?",
		URL:   "/",
		Week:  household.WeekStart(planDate),
	}
}

// SupplyRunItem is a Supply run item still needed, as sent to Google Chat;
// Qty is empty when the recipe gives none.
type SupplyRunItem struct {
	Name string
	Qty  string
}

// SupplyRunGroup is one dinner of a Supply run sent to Google Chat, with
// its items still needed.
type SupplyRunGroup struct {
	Dinner string
	Items  []SupplyRunItem
}

// SupplyRunText is the Google Chat message of the Supply run named name
// (its title, or its week label until it has one): each dinner with items
// still needed in bold, its items one per line, and the call to action.
//
//	Supply run · Week 42 · Oct 12 – 18
//
//	*Tacos*
//	• ground beef — 1 lb
//	• tortillas
//
//	Open the Supply run →
//
// A Supply run with nothing still needed says so.
func SupplyRunText(name string, groups []SupplyRunGroup) string {
	var b strings.Builder
	b.WriteString("Supply run · " + name)
	listed := false
	for _, g := range groups {
		if len(g.Items) == 0 {
			continue
		}
		listed = true
		b.WriteString("\n\n*" + g.Dinner + "*")
		for _, it := range g.Items {
			b.WriteString("\n• " + it.Name)
			if it.Qty != "" {
				b.WriteString(" — " + it.Qty)
			}
		}
	}
	if !listed {
		b.WriteString("\n\nNothing left to buy.")
	}
	b.WriteString("\n\n" + actions[notify.ListReady] + " →")
	return b.String()
}

// planTitle is the plan_ready title of the week starting on week, seen
// from the week starting on thisWeek.
func planTitle(week, thisWeek store.LocalDate) string {
	y, m, d := thisWeek.Date()
	next := store.LocalDateOf(time.Date(y, m, d+7, 0, 0, 0, 0, time.UTC))
	switch week {
	case thisWeek:
		return "This week's plan is ready"
	case next:
		return "Next week's plan is ready"
	}
	_, m, d = week.Date()
	return fmt.Sprintf("The plan for the week of %s %d is ready", m.String()[:3], d)
}

// Text is the Google Chat message of p: its title, its body after a colon
// and the event's call to action with an arrow. A title ending in an arrow,
// with no body, carries its own call to action and is the message as it is.
func Text(p notify.Payload) string {
	text := p.Title
	if p.Body == "" && strings.HasSuffix(text, "→") {
		return text
	}
	if p.Body != "" {
		if endsSentence(text) {
			text += " "
		} else {
			text += ": "
		}
		text += p.Body
	}
	if !endsSentence(text) {
		text += "."
	}
	if action, ok := actions[p.Event]; ok {
		text += " " + action + " →"
	}
	return text
}

// endsSentence reports whether s ends with sentence punctuation.
func endsSentence(s string) bool {
	return strings.HasSuffix(s, ".") || strings.HasSuffix(s, "?") || strings.HasSuffix(s, "!")
}

// nameList lists the first listedNames of names, then how many more there
// are: "Tacos, Bolognese, Tikka masala + 4 more".
func nameList(names []string) string {
	if len(names) <= listedNames {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s + %d more", strings.Join(names[:listedNames], ", "), len(names)-listedNames)
}

// count is n with the singular or plural noun.
func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

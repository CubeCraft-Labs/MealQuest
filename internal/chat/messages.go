package chat

import (
	"fmt"
	"strings"

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

// PlanReady is the plan_ready event of the week starting on week, naming
// its dinners in plan order.
func PlanReady(week store.LocalDate, dinners []string) notify.Event {
	return notify.Event{
		Type:  notify.PlanReady,
		Key:   string(notify.PlanReady) + ":" + week.String(),
		Title: "This week's plan is ready",
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
// name, planned on date.
func DayOfReminder(date store.LocalDate, dinnerID, name string) notify.Event {
	return notify.Event{
		Type:  notify.DayOfReminder,
		Key:   string(notify.DayOfReminder) + ":" + date.String(),
		Title: "Tonight's dinner",
		Body:  name,
		URL:   "/dinner/" + dinnerID,
		Week:  household.WeekStart(date),
	}
}

// NewSuggestions is the new_suggestions event of suggestion batch batchID,
// made in the week starting on week, naming its Side quests.
func NewSuggestions(batchID string, week store.LocalDate, names []string) notify.Event {
	return notify.Event{
		Type:  notify.NewSuggestions,
		Key:   string(notify.NewSuggestions) + ":" + batchID,
		Title: "New Side quests are in",
		Body:  nameList(names),
		URL:   "/discover",
		Week:  week,
	}
}

// RotationReview is the rotation_review event asking whether the dinner
// dinnerID, named name and tried in the week starting on week, becomes a
// Regular.
func RotationReview(dinnerID, name string, week store.LocalDate) notify.Event {
	return notify.Event{
		Type:  notify.RotationReview,
		Key:   string(notify.RotationReview) + ":" + dinnerID,
		Title: "Keep it in rotation?",
		Body:  "You tried " + name + ". Make it a Regular?",
		URL:   "/",
		Week:  week,
	}
}

// Text is the Google Chat message of p: its title, its body after a colon
// and the event's call to action with an arrow.
func Text(p notify.Payload) string {
	text := p.Title
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

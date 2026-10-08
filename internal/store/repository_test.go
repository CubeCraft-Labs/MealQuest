package store

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The tables below have no public sqlc queries yet (they arrive with the
// planning, suggestion and Supply run features), so these tests drive them
// with raw SQL to prove the schema stores, returns and updates their rows.

const repoTime = "2026-10-03T12:00:00Z"

type repoRow struct {
	table string
	cols  map[string]any
}

// repoParents are the rows the tables under test reference, in insert order.
var repoParents = map[string]repoRow{
	"dinners": {"dinners", map[string]any{
		"id": "dinner-1", "name": "Chili", "normalized_name": "chili",
		"status": "regular", "origin": "regular", "is_favorite": int64(0),
		"grounding_status": "manual", "created_at": repoTime, "updated_at": repoTime,
	}},
	"suggestion_batches": {"suggestion_batches", map[string]any{
		"id": "batch-1", "trigger": "manual", "status": "done",
		"created_at": repoTime, "updated_at": repoTime,
	}},
	"week_plans": {"week_plans", map[string]any{
		"id": "plan-1", "week_start": "2026-10-05", "status": "draft",
		"created_at": repoTime, "updated_at": repoTime,
	}},
	"shopping_lists": {"shopping_lists", map[string]any{
		"id": "list-1", "week_start": "2026-10-05", "plan_id": "plan-1",
		"title": "Supply run", "status": "current",
		"created_at": repoTime, "updated_at": repoTime,
	}},
	"shopping_groups": {"shopping_groups", map[string]any{
		"id": "group-1", "list_id": "list-1", "dinner_id": "dinner-1",
		"date": "2026-10-05", "position": int64(0), "created_at": repoTime, "updated_at": repoTime,
	}},
}

// readRepoRow reads cols of the row with id through the reader.
func readRepoRow(t *testing.T, s *Store, table, id string, cols []string) map[string]any {
	t.Helper()
	quoted := make([]string, len(cols))
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i, c := range cols {
		quoted[i] = `"` + c + `"`
		ptrs[i] = &vals[i]
	}
	q := fmt.Sprintf(`SELECT %s FROM "%s" WHERE id = ?`, strings.Join(quoted, ", "), table)
	if err := s.Reader.QueryRowContext(context.Background(), q, id).Scan(ptrs...); err != nil {
		t.Fatalf("read %s %s: %v", table, id, err)
	}
	got := make(map[string]any, len(cols))
	for i, c := range cols {
		got[c] = vals[i]
	}
	return got
}

func TestRepositoryInsertReadUpdate(t *testing.T) {
	cases := []struct {
		parents []string
		row     repoRow
		update  map[string]any
	}{
		{
			row: repoRow{"week_plans", map[string]any{
				"id": "plan-2", "week_start": "2026-10-12", "status": "draft",
				"ai_note": "Two Side quests this week.", "ai_run_id": nil, "calendar_synced_at": nil,
				"created_at": repoTime, "updated_at": repoTime,
			}},
			update: map[string]any{"status": "synced", "calendar_synced_at": "2026-10-04T09:00:00Z"},
		},
		{
			parents: []string{"dinners", "week_plans"},
			row: repoRow{"plan_days", map[string]any{
				"id": "day-1", "plan_id": "plan-1", "date": "2026-10-06", "dinner_id": "dinner-1",
				"is_new": int64(0), "calendar_event_id": nil,
				"created_at": repoTime, "updated_at": repoTime,
			}},
			update: map[string]any{"dinner_id": nil, "calendar_event_id": "evt-123"},
		},
		{
			parents: []string{"dinners", "suggestion_batches"},
			row: repoRow{"suggestions", map[string]any{
				"id": "sugg-1", "dinner_id": "dinner-1", "batch_id": "batch-1",
				"name": "White Chicken Chili", "reason": "You make chili often.", "cited_regular_ids": `["dinner-1"]`,
				"status": "pending", "decided_at": nil,
				"created_at": repoTime, "updated_at": repoTime,
			}},
			update: map[string]any{"status": "accepted", "decided_at": "2026-10-04T10:00:00Z"},
		},
		{
			parents: []string{"week_plans"},
			row: repoRow{"shopping_lists", map[string]any{
				"id": "list-2", "week_start": "2026-10-05", "plan_id": "plan-1",
				"title": "Supply run", "status": "current", "sent_to_chat_at": nil, "frozen_at": nil,
				"created_at": repoTime, "updated_at": repoTime,
			}},
			update: map[string]any{"title": nil, "status": "archived", "frozen_at": "2026-10-12T00:00:00Z"},
		},
		{
			parents: []string{"dinners", "week_plans", "shopping_lists"},
			row: repoRow{"shopping_groups", map[string]any{
				"id": "group-2", "list_id": "list-1", "dinner_id": "dinner-1", "date": "2026-10-06", "position": int64(0),
				"created_at": repoTime, "updated_at": repoTime,
			}},
			update: map[string]any{"position": int64(3)},
		},
		{
			parents: []string{"dinners", "week_plans", "shopping_lists", "shopping_groups"},
			row: repoRow{"shopping_items", map[string]any{
				"id": "item-1", "group_id": "group-1", "name": "Onion", "qty": "2",
				"needed": int64(1), "position": int64(0),
				"created_at": repoTime, "updated_at": repoTime,
			}},
			update: map[string]any{"needed": int64(0), "qty": "3"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.row.table, func(t *testing.T) {
			ctx := context.Background()
			s := migratedStore(t)
			for _, p := range tc.parents {
				parent := repoParents[p]
				if err := insertSchemaRow(s.Writer, parent.table, parent.cols); err != nil {
					t.Fatalf("seed %s: %v", p, err)
				}
			}

			table, id := tc.row.table, tc.row.cols["id"].(string)
			cols := slices.Sorted(maps.Keys(tc.row.cols))

			if err := insertSchemaRow(s.Writer, table, tc.row.cols); err != nil {
				t.Fatalf("insert: %v", err)
			}
			if got := readRepoRow(t, s, table, id, cols); !reflect.DeepEqual(got, tc.row.cols) {
				t.Fatalf("read after insert:\n got %v\nwant %v", got, tc.row.cols)
			}

			want := maps.Clone(tc.row.cols)
			maps.Copy(want, tc.update)
			want["updated_at"] = "2026-10-04T12:00:00Z"
			setCols := slices.Sorted(maps.Keys(tc.update))
			sets := make([]string, 0, len(setCols)+1)
			args := make([]any, 0, len(setCols)+2)
			for _, c := range append(setCols, "updated_at") {
				sets = append(sets, `"`+c+`" = ?`)
				args = append(args, want[c])
			}
			args = append(args, id)
			q := fmt.Sprintf(`UPDATE "%s" SET %s WHERE id = ?`, table, strings.Join(sets, ", "))
			res, err := s.Writer.ExecContext(ctx, q, args...)
			if err != nil {
				t.Fatalf("update: %v", err)
			}
			if n, err := res.RowsAffected(); err != nil || n != 1 {
				t.Fatalf("update affected %d rows (err %v), want 1", n, err)
			}
			if got := readRepoRow(t, s, table, id, cols); !reflect.DeepEqual(got, want) {
				t.Fatalf("read after update:\n got %v\nwant %v", got, want)
			}
		})
	}
}

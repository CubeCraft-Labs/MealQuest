package harness

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
)

// timeoutSkill is echoSkill with its own timeout.
type timeoutSkill struct {
	echoSkill
	timeout time.Duration
}

func (s timeoutSkill) Timeout() time.Duration { return s.timeout }

// slowSkill is timeoutSkill whose PostProcess succeeds only after its
// timeout has passed.
type slowSkill struct {
	timeoutSkill
}

func (s slowSkill) PostProcess(out echoOutput) (echoOutput, error) {
	time.Sleep(3 * s.timeout)
	return s.timeoutSkill.PostProcess(out)
}

// blocked is one scripted Generate call that blocks until released.
type blocked struct {
	started chan struct{}
	release chan struct{}
}

// queueBlocked queues r on f as a call that blocks until released.
func queueBlocked(f *llm.Fake, r llm.GenerateResult) blocked {
	b := blocked{started: make(chan struct{}), release: make(chan struct{})}
	r.Started, r.Release = b.started, b.release
	f.QueueGenerate(r)
	return b
}

// result is what one Run returned.
type result struct {
	out echoOutput
	err error
}

// goRun starts Run of s with scope on cfg and returns where its result
// arrives.
func goRun(ctx context.Context, cfg Config, s Skill[echoInput, echoOutput], scope string) <-chan result {
	ch := make(chan result, 1)
	go func() {
		out, err := Run(ctx, cfg, s, scope, echoInput{Word: "taco"})
		ch <- result{out, err}
	}()
	return ch
}

// await returns what arrives on ch, failing the test after a second.
func await[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

// waitStarted waits for b's Generate call to start.
func waitStarted(t *testing.T, b blocked, what string) {
	t.Helper()
	select {
	case <-b.started:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s to call Generate", what)
	}
}

// assertNotStarted checks that b's Generate call has not started after a
// grace period.
func assertNotStarted(t *testing.T, b blocked, what string) {
	t.Helper()
	select {
	case <-b.started:
		t.Fatalf("%s called Generate while no slot was free", what)
	case <-time.After(50 * time.Millisecond):
	}
}

// waitWaiters waits until n callers wait for the run of skill with scope.
func waitWaiters(t *testing.T, g *Guard, skill, scope string, n int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		g.mu.Lock()
		got := 0
		if f := g.flights[flightKey{skill: skill, scope: scope}]; f != nil {
			got = f.waiters
		}
		g.mu.Unlock()
		if got == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d callers wait for %s/%s, want %d", got, skill, scope, n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRunSameScopeSharesOneExecution(t *testing.T) {
	for name, script := range map[string]llm.GenerateResult{
		"output": reply(`{"echo":"taco"}`),
		"error":  {Err: llm.ErrAuth},
	} {
		t.Run(name, func(t *testing.T) {
			var f llm.Fake
			cfg := testConfig(t, &f)
			b := queueBlocked(&f, script)

			first := goRun(context.Background(), cfg, echoSkill{}, "week-1")
			waitStarted(t, b, "the first run")
			second := goRun(context.Background(), cfg, echoSkill{}, "week-1")
			waitWaiters(t, cfg.Guard, "echo", "week-1", 2)
			close(b.release)

			r1, r2 := await(t, first, "the first run"), await(t, second, "the second run")
			if r1 != r2 {
				t.Fatalf("results differ: %+v and %+v", r1, r2)
			}
			if name == "output" && (r1.err != nil || r1.out.Echo != "taco") {
				t.Fatalf("result = %+v, want taco", r1)
			}
			if name == "error" && !errors.Is(r1.err, llm.ErrAuth) {
				t.Fatalf("err = %v, want ErrAuth", r1.err)
			}
			assertCalls(t, &f, 1)
			if rows := runRows(t, cfg.Store); len(rows) != 1 {
				t.Fatalf("%d ai_runs rows, want 1 for the shared run", len(rows))
			}
		})
	}
}

func TestRunDifferentScopesRunIndependently(t *testing.T) {
	var f llm.Fake
	cfg := testConfig(t, &f)
	b1 := queueBlocked(&f, reply(`{"echo":"taco"}`))
	b2 := queueBlocked(&f, reply(`{"echo":"taco"}`))

	week1 := goRun(context.Background(), cfg, echoSkill{}, "week-1")
	week2 := goRun(context.Background(), cfg, echoSkill{}, "week-2")
	// Both calls are in Generate at once: neither waits for the other.
	waitStarted(t, b1, "a run")
	waitStarted(t, b2, "the other run")
	close(b1.release)
	close(b2.release)

	for _, ch := range []<-chan result{week1, week2} {
		if r := await(t, ch, "a run"); r.err != nil || r.out.Echo != "taco" {
			t.Fatalf("result = %+v", r)
		}
	}
	assertCalls(t, &f, 2)
}

func TestRunThirdCallWaitsForSlot(t *testing.T) {
	var f llm.Fake
	cfg := testConfig(t, &f)
	b1 := queueBlocked(&f, reply(`{"echo":"taco"}`))
	b2 := queueBlocked(&f, reply(`{"echo":"taco"}`))
	b3 := queueBlocked(&f, reply(`{"echo":"taco"}`))

	first := goRun(context.Background(), cfg, echoSkill{}, "week-1")
	waitStarted(t, b1, "the first run")
	second := goRun(context.Background(), cfg, echoSkill{}, "week-2")
	waitStarted(t, b2, "the second run")
	third := goRun(context.Background(), cfg, echoSkill{}, "week-3")
	assertNotStarted(t, b3, "the third run")
	if n := len(f.Requests()); n != 2 {
		t.Fatalf("Generate called %d times while both slots were taken, want 2", n)
	}

	close(b1.release)
	waitStarted(t, b3, "the third run")
	close(b2.release)
	close(b3.release)
	for _, ch := range []<-chan result{first, second, third} {
		if r := await(t, ch, "a run"); r.err != nil || r.out.Echo != "taco" {
			t.Fatalf("result = %+v", r)
		}
	}
	assertCalls(t, &f, 3)
}

func TestRunCancelledContextReleasesSlot(t *testing.T) {
	var f llm.Fake
	cfg := testConfig(t, &f)
	b1 := queueBlocked(&f, reply(`{"echo":"taco"}`))
	b2 := queueBlocked(&f, reply(`{"echo":"taco"}`))
	b3 := queueBlocked(&f, reply(`{"echo":"taco"}`))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := goRun(ctx, cfg, echoSkill{}, "week-1")
	waitStarted(t, b1, "the first run")
	second := goRun(context.Background(), cfg, echoSkill{}, "week-2")
	waitStarted(t, b2, "the second run")
	third := goRun(context.Background(), cfg, echoSkill{}, "week-3")
	assertNotStarted(t, b3, "the third run")

	cancel()
	if r := await(t, first, "the cancelled run"); !errors.Is(r.err, context.Canceled) {
		t.Fatalf("cancelled run err = %v, want context.Canceled", r.err)
	}
	// The cancelled run's slot goes to the third run; the first call's
	// release is never closed.
	waitStarted(t, b3, "the third run")
	close(b2.release)
	close(b3.release)
	for _, ch := range []<-chan result{second, third} {
		if r := await(t, ch, "a run"); r.err != nil || r.out.Echo != "taco" {
			t.Fatalf("result = %+v", r)
		}
	}
	assertCalls(t, &f, 3)
	var statuses []string
	for _, r := range runRows(t, cfg.Store) {
		statuses = append(statuses, r.Status)
	}
	if strings.Join(statuses, ",") != "provider_error,ok,ok" {
		t.Fatalf("statuses = %v, want the cancelled run recorded as provider_error", statuses)
	}
}

func TestRunCallerGivingUpLeavesSharedRun(t *testing.T) {
	var f llm.Fake
	cfg := testConfig(t, &f)
	b := queueBlocked(&f, reply(`{"echo":"taco"}`))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	leader := goRun(ctx, cfg, echoSkill{}, "week-1")
	waitStarted(t, b, "the first run")
	follower := goRun(context.Background(), cfg, echoSkill{}, "week-1")
	waitWaiters(t, cfg.Guard, "echo", "week-1", 2)

	cancel()
	if r := await(t, leader, "the cancelled caller"); !errors.Is(r.err, context.Canceled) {
		t.Fatalf("cancelled caller err = %v, want context.Canceled", r.err)
	}
	close(b.release)
	if r := await(t, follower, "the remaining caller"); r.err != nil || r.out.Echo != "taco" {
		t.Fatalf("remaining caller = %+v, want the shared run's output", r)
	}
	assertCalls(t, &f, 1)
}

func TestRunTimeout(t *testing.T) {
	t.Run("in Generate", func(t *testing.T) {
		var f llm.Fake
		cfg := testConfig(t, &f)
		queueBlocked(&f, reply(`{"echo":"taco"}`))
		_, err := Run(context.Background(), cfg, timeoutSkill{timeout: 20 * time.Millisecond}, "week-1", echoInput{Word: "taco"})
		if !errors.Is(err, llm.ErrTimeout) {
			t.Fatalf("err = %v, want ErrTimeout", err)
		}
		assertCalls(t, &f, 1)
		rows := runRows(t, cfg.Store)
		if len(rows) != 1 || rows[0].Status != string(StatusProviderError) || !strings.Contains(rows[0].Error.String, llm.ErrTimeout.Error()) {
			t.Fatalf("rows = %+v, want one provider_error row naming the timeout", rows)
		}
	})
	t.Run("work that succeeds late", func(t *testing.T) {
		var f llm.Fake
		cfg := testConfig(t, &f)
		f.QueueGenerate(reply(`{"echo":"taco"}`))
		s := slowSkill{timeoutSkill{timeout: 20 * time.Millisecond}}
		out, err := Run(context.Background(), cfg, s, "week-1", echoInput{Word: "taco"})
		if !errors.Is(err, llm.ErrTimeout) {
			t.Fatalf("err = %v, want ErrTimeout", err)
		}
		if out != (echoOutput{}) {
			t.Fatalf("out = %+v, want no output", out)
		}
		assertCalls(t, &f, 1)
		rows := runRows(t, cfg.Store)
		if len(rows) != 1 || rows[0].Status != string(StatusProviderError) || !strings.Contains(rows[0].Error.String, llm.ErrTimeout.Error()) {
			t.Fatalf("rows = %+v, want one provider_error row naming the timeout", rows)
		}
	})
	t.Run("row saved late", func(t *testing.T) {
		var f llm.Fake
		cfg := testConfig(t, &f)
		f.QueueGenerate(reply(`{"echo":"taco"}`))
		timeout := 100 * time.Millisecond
		// Holding the store's only writer connection past the deadline
		// keeps the ok row from being written in time, after the work
		// itself has ended in time.
		conn, err := cfg.Store.Writer.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		released := time.AfterFunc(3*timeout, func() { conn.Close() })
		defer released.Stop()
		out, err := Run(context.Background(), cfg, timeoutSkill{timeout: timeout}, "week-1", echoInput{Word: "taco"})
		if !errors.Is(err, llm.ErrTimeout) {
			t.Fatalf("err = %v, want ErrTimeout", err)
		}
		if out != (echoOutput{}) {
			t.Fatalf("out = %+v, want no output", out)
		}
		assertCalls(t, &f, 1)
		// The row records the timeout the caller received, not the ok
		// outcome the work had.
		rows := runRows(t, cfg.Store)
		if len(rows) != 1 || rows[0].Status != string(StatusProviderError) || !strings.Contains(rows[0].Error.String, llm.ErrTimeout.Error()) {
			t.Fatalf("rows = %+v, want one provider_error row naming the timeout", rows)
		}
	})
	t.Run("writer blocked", func(t *testing.T) {
		var f llm.Fake
		cfg := testConfig(t, &f)
		f.QueueGenerate(reply(`{"echo":"taco"}`))
		timeout := 50 * time.Millisecond
		saveTimeout := runSaveTimeout
		runSaveTimeout = 100 * time.Millisecond
		defer func() { runSaveTimeout = saveTimeout }()
		// The writer stays held until Run has returned, so neither save
		// can be written.
		conn, err := cfg.Store.Writer.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		_, err = Run(context.Background(), cfg, timeoutSkill{timeout: timeout}, "week-1", echoInput{Word: "taco"})
		elapsed := time.Since(started)
		conn.Close()
		if !errors.Is(err, llm.ErrTimeout) || !strings.Contains(err.Error(), "record ai_runs row") {
			t.Fatalf("err = %v, want ErrTimeout and the row write failure", err)
		}
		if bound := timeout + runSaveTimeout + time.Second; elapsed > bound {
			t.Fatalf("Run took %v with the writer blocked, want at most %v", elapsed, bound)
		}
		if rows := runRows(t, cfg.Store); len(rows) != 0 {
			t.Fatalf("rows = %+v, want none", rows)
		}
	})
	t.Run("waiting for a slot", func(t *testing.T) {
		var f llm.Fake
		cfg := testConfig(t, &f)
		cfg.Guard = testGuard(t, 1)
		b := queueBlocked(&f, reply(`{"echo":"taco"}`))
		holder := goRun(context.Background(), cfg, echoSkill{}, "week-1")
		waitStarted(t, b, "the slot holder")

		_, err := Run(context.Background(), cfg, timeoutSkill{timeout: 20 * time.Millisecond}, "week-2", echoInput{Word: "taco"})
		if !errors.Is(err, llm.ErrTimeout) {
			t.Fatalf("err = %v, want ErrTimeout", err)
		}
		close(b.release)
		if r := await(t, holder, "the slot holder"); r.err != nil {
			t.Fatal(r.err)
		}
		assertCalls(t, &f, 1)
	})
}

func TestRunRefusesBadGuardInput(t *testing.T) {
	if _, err := NewGuard(0); err == nil {
		t.Fatal("NewGuard(0) succeeded")
	}
	var f llm.Fake
	cfg := testConfig(t, &f)
	if _, err := Run(context.Background(), cfg, echoSkill{}, "", echoInput{Word: "taco"}); err == nil || !strings.Contains(err.Error(), "no scope") {
		t.Fatalf("empty scope err = %v", err)
	}
	if _, err := Run(context.Background(), cfg, timeoutSkill{}, "week-1", echoInput{Word: "taco"}); err == nil || !strings.Contains(err.Error(), "not positive") {
		t.Fatalf("zero timeout err = %v", err)
	}
	assertCalls(t, &f, 0)
	if rows := runRows(t, cfg.Store); len(rows) != 0 {
		t.Fatalf("refused runs wrote %d ai_runs rows, want none", len(rows))
	}
}

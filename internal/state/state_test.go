package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/christoph-jerolimov/loop/internal/item"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), ".loop"))
}

func create(t *testing.T, s *Store, id string) *Run {
	t.Helper()
	r, err := s.Create(&item.Item{ID: "backlog:" + id, NativeID: id, Source: "backlog", Title: "Item " + id}, "claude")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCreateSaveLoadRoundTrip(t *testing.T) {
	s := newStore(t)
	r := create(t, s, "auth")
	if r.Phase != PhaseQueued || r.Runner != "claude" || r.ItemID != "backlog:auth" || !strings.HasSuffix(r.ID, "-backlog-auth") {
		t.Errorf("new run = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(r.ID), FileName)); err != nil {
		t.Fatalf("run.yaml not written: %v", err)
	}
	r.Branch = "loop/auth"
	r.PR = &PR{Number: 3, URL: "https://github.com/o/r/pull/3", HeadSHA: "abc"}
	r.Sessions = append(r.Sessions, Session{ID: "sid", Kind: "session", LogFile: "x.log"})
	r.HandledComments = []int64{7, 8}
	r.NextPoll = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := s.Save(r); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "loop/auth" || got.PR == nil || got.PR.Number != 3 || len(got.Sessions) != 1 || got.Sessions[0].ID != "sid" || len(got.HandledComments) != 2 || !got.NextPoll.Equal(r.NextPoll) || got.Item.Title != "Item auth" {
		t.Errorf("loaded run = %+v", got)
	}
	if got.Dir() != s.Dir(r.ID) || got.LogFile() != filepath.Join(s.Dir(r.ID), "run.log") || got.SummaryFile() != filepath.Join(s.Dir(r.ID), "summary.md") {
		t.Errorf("paths: dir=%s log=%s summary=%s", got.Dir(), got.LogFile(), got.SummaryFile())
	}
	if _, err := s.Load("missing"); err == nil {
		t.Error("loading an unknown run must fail")
	}
	// A second run of the same item within the same second keeps its own folder.
	again := create(t, s, "auth")
	if again.ID == r.ID || !strings.HasPrefix(again.ID, r.ID) || again.Dir() == r.Dir() {
		t.Errorf("second run id = %q, want %q with a suffix", again.ID, r.ID)
	}
	if got, err := s.Load(r.ID); err != nil || got.Branch != "loop/auth" {
		t.Errorf("the first run must be untouched: %v %v", got, err)
	}
}

func TestSaveWithoutDirUsesStoreLayout(t *testing.T) {
	s := newStore(t)
	r := &Run{ID: "manual-run", ItemID: "x"}
	if err := os.MkdirAll(s.Dir(r.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(r); err != nil {
		t.Fatal(err)
	}
	if r.Dir() != s.Dir("manual-run") || r.Updated.IsZero() {
		t.Errorf("dir=%s updated=%v", r.Dir(), r.Updated)
	}
}

func TestListActiveForItemAndFind(t *testing.T) {
	s := newStore(t)
	if runs, err := s.List(); err != nil || runs != nil {
		t.Errorf("empty store: %v %v", runs, err)
	}
	older := create(t, s, "auth")
	older.Created = older.Created.Add(-time.Hour)
	older.Phase = PhaseDone
	s.Save(older)
	newer := create(t, s, "search")
	newer.Phase = PhaseMonitor
	s.Save(newer)
	// A stray file and a folder without run.yaml are ignored.
	os.WriteFile(filepath.Join(s.Root, "notes.txt"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(s.Root, "broken"), 0o755)

	runs, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].ID != newer.ID || runs[1].ID != older.ID {
		t.Errorf("List must be newest first, got %v", ids(runs))
	}
	active, err := s.Active()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ID != newer.ID {
		t.Errorf("Active = %v", ids(active))
	}
	if r, _ := s.ForItem("backlog:search"); r == nil || r.ID != newer.ID {
		t.Errorf("ForItem(search) = %v", r)
	}
	if r, _ := s.ForItem("backlog:auth"); r != nil {
		t.Errorf("ForItem must ignore finished runs, got %s", r.ID)
	}
	if r, _ := s.LastForItem("backlog:auth"); r == nil || r.ID != older.ID {
		t.Errorf("LastForItem must return finished runs too, got %v", r)
	}
	if r, _ := s.LastForItem("backlog:none"); r != nil {
		t.Errorf("LastForItem(none) = %v", r)
	}
	// A parked run whose PR is still open occupies the item; a done run or
	// a run whose PR was closed does not.
	if r, _ := s.OpenForItem("backlog:auth"); r != nil {
		t.Errorf("OpenForItem must ignore done runs, got %s", r.ID)
	}
	newer.Phase = PhaseBlocked
	newer.PR = &PR{Number: 4}
	s.Save(newer)
	if r, _ := s.OpenForItem("backlog:search"); r == nil || r.ID != newer.ID {
		t.Errorf("OpenForItem must return a blocked run with an open PR, got %v", r)
	}
	newer.PR.Closed = true
	s.Save(newer)
	if r, _ := s.OpenForItem("backlog:search"); r != nil {
		t.Errorf("OpenForItem must ignore a blocked run whose PR was closed, got %s", r.ID)
	}
	newer.Phase = PhaseMonitor
	newer.PR = nil
	s.Save(newer)

	cases := map[string]string{
		newer.ID:       newer.ID,
		newer.ID[:12]:  newer.ID, // prefix
		"backlog:auth": older.ID, // item id
		"search":       newer.ID, // native id
		older.ID[:8]:   "",       // ambiguous prefix: both runs start with today's date
		"nope":         "",
	}
	for ref, want := range cases {
		r, err := s.Find(ref)
		switch {
		case want == "" && ref == "nope":
			if err == nil || !strings.Contains(err.Error(), "no run matches") {
				t.Errorf("Find(%q) = %v, %v", ref, r, err)
			}
		case want == "":
			// An ambiguous prefix prefers the active run.
			if err != nil || r.ID != newer.ID {
				t.Errorf("Find(%q) = %v, %v; want the active run", ref, r, err)
			}
		default:
			if err != nil || r.ID != want {
				t.Errorf("Find(%q) = %v, %v; want %s", ref, r, err, want)
			}
		}
	}
}

func TestLogPhaseFailBlock(t *testing.T) {
	s := newStore(t)
	r := create(t, s, "auth")
	r.SetPhase(PhaseCheckout, "worktree ready")
	r.Log("hello %s", "world")
	if r.Phase != PhaseCheckout || len(r.Events) != 2 || r.Events[0].Phase != PhaseQueued || !strings.Contains(r.Events[0].Message, "queued -> checkout: worktree ready") || r.Events[1].Message != "hello world" {
		t.Errorf("events = %+v", r.Events)
	}
	log, err := os.ReadFile(r.LogFile())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "[queued] queued -> checkout") || !strings.HasSuffix(lines[1], "[checkout] hello world") {
		t.Errorf("run.log:\n%s", log)
	}

	r.Fail(errors.New("boom"))
	if r.Phase != PhaseFailed || r.Error != "boom" || r.Phase.Active() {
		t.Errorf("after Fail: phase=%s error=%q", r.Phase, r.Error)
	}
	r.Block("waiting for a human")
	if r.Phase != PhaseBlocked || r.Error != "waiting for a human" || r.Phase.Active() {
		t.Errorf("after Block: phase=%s error=%q", r.Phase, r.Error)
	}
	for i := 0; i < 600; i++ {
		r.Log("event %d", i)
	}
	if len(r.Events) != 500 || r.Events[499].Message != "event 599" {
		t.Errorf("events must be capped at the newest 500, got %d ending with %q", len(r.Events), r.Events[len(r.Events)-1].Message)
	}
}

func TestPhaseActive(t *testing.T) {
	for _, p := range []Phase{PhaseQueued, PhaseCheckout, PhaseSetup, PhaseSession, PhaseVerify, PhasePR, PhaseMonitor, PhaseFix, PhaseMerge, PhaseClose, PhaseCleanup} {
		if !p.Active() {
			t.Errorf("%s must be active", p)
		}
	}
	for _, p := range []Phase{PhaseDone, PhaseFailed, PhaseBlocked} {
		if p.Active() {
			t.Errorf("%s must not be active", p)
		}
	}
}

func TestLockIsExclusive(t *testing.T) {
	s := newStore(t)
	r := create(t, s, "auth")
	unlock, err := r.Lock()
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Load(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Lock(); err == nil || !strings.Contains(err.Error(), "another loop process") {
		t.Errorf("second lock must fail, got %v", err)
	}
	unlock()
	unlock2, err := other.Lock()
	if err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	unlock2()
}

func ids(runs []*Run) []string {
	var out []string
	for _, r := range runs {
		out = append(out, r.ID)
	}
	return out
}

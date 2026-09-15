// Package state persists runs as run.yaml under <project>/.loop/runs/<id>/.
package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/christoph-jerolimov/loop/internal/item"
)

// Phase is where a run is in its lifecycle.
type Phase string

// Phases in the order they usually happen.
const (
	PhaseQueued   Phase = "queued"
	PhaseCheckout Phase = "checkout"
	PhaseSetup    Phase = "setup"
	PhasePlan     Phase = "plan"
	PhaseSession  Phase = "session"
	PhaseVerify   Phase = "verify"
	PhasePR       Phase = "pr"
	PhaseMonitor  Phase = "monitor"
	PhaseFix      Phase = "fix"
	PhaseMerge    Phase = "merge"
	PhaseClose    Phase = "close"
	PhaseCleanup  Phase = "cleanup"
	PhaseDone     Phase = "done"
	PhaseFailed   Phase = "failed"
	PhaseBlocked  Phase = "blocked"
)

// Active reports whether the run still needs driving.
func (p Phase) Active() bool {
	switch p {
	case PhaseDone, PhaseFailed, PhaseBlocked:
		return false
	}
	return true
}

// FixReason says why a fix round runs.
type FixReason string

// Fix reasons.
const (
	FixReview   FixReason = "review"
	FixCI       FixReason = "ci"
	FixConflict FixReason = "conflict"
	FixVerify   FixReason = "verify"
)

// Outcome says how a done run ended.
type Outcome string

// Outcomes of a done run.
const (
	// OutcomeMerged: the PR was merged.
	OutcomeMerged Outcome = "merged"
	// OutcomeNoChanges: the session found nothing to change, so no PR was
	// opened. Only recurring items end this way; for a one-shot item a
	// session without commits is a failed attempt.
	OutcomeNoChanges Outcome = "no-changes"
)

// PR is the pull request of a run.
type PR struct {
	Number  int    `json:"number" yaml:"number"`
	NodeID  string `json:"node_id" yaml:"node_id"`
	URL     string `json:"url" yaml:"url"`
	HeadSHA string `json:"head_sha" yaml:"head_sha"`
	Draft   bool   `json:"draft" yaml:"draft"`
	Merged  bool   `json:"merged" yaml:"merged"`
	// Closed records that the PR was closed without a merge.
	Closed bool `json:"closed,omitempty" yaml:"closed,omitempty"`
}

// Open reports whether the PR is still open on the host.
func (p *PR) Open() bool { return p != nil && !p.Merged && !p.Closed }

// Event is one history entry.
type Event struct {
	Time    time.Time `json:"time" yaml:"time"`
	Phase   Phase     `json:"phase" yaml:"phase"`
	Message string    `json:"message" yaml:"message"`
}

// Run is one iteration over one item.
type Run struct {
	ID       string     `json:"id" yaml:"id"`
	ItemID   string     `json:"item_id" yaml:"item_id"`
	Item     *item.Item `json:"item" yaml:"item"`
	Branch   string     `json:"branch" yaml:"branch"`
	Workdir  string     `json:"workdir" yaml:"workdir"`
	Phase    Phase      `json:"phase" yaml:"phase"`
	Runner   string     `json:"runner" yaml:"runner"`
	Model    string     `json:"model,omitempty" yaml:"model,omitempty"`
	Attempt  int        `json:"attempt" yaml:"attempt"`
	Sessions []Session  `json:"sessions,omitempty" yaml:"sessions,omitempty"`

	PR              *PR       `json:"pr,omitempty" yaml:"pr,omitempty"`
	FixRounds       int       `json:"fix_rounds" yaml:"fix_rounds"`
	ConflictRounds  int       `json:"conflict_rounds" yaml:"conflict_rounds"`
	PendingFix      FixReason `json:"pending_fix,omitempty" yaml:"pending_fix,omitempty"`
	HandledComments []int64   `json:"handled_comments,omitempty" yaml:"handled_comments,omitempty"`
	HandledReviews  []int64   `json:"handled_reviews,omitempty" yaml:"handled_reviews,omitempty"`
	LastPushSHA     string    `json:"last_push_sha,omitempty" yaml:"last_push_sha,omitempty"`
	LastCIFixSHA    string    `json:"last_ci_fix_sha,omitempty" yaml:"last_ci_fix_sha,omitempty"`
	// LastStatus is the last "sha state description" posted as the loop
	// commit status, so unchanged state is not posted again every poll.
	LastStatus string `json:"last_status,omitempty" yaml:"last_status,omitempty"`
	// CIRerunSHA is the head whose failed jobs were already re-run once.
	CIRerunSHA string `json:"ci_rerun_sha,omitempty" yaml:"ci_rerun_sha,omitempty"`
	// PollFailures counts consecutive failed polls; it stretches the next
	// poll and resets on the first success.
	PollFailures int `json:"poll_failures,omitempty" yaml:"poll_failures,omitempty"`
	// MergeAttempts counts merge calls GitHub rejected as not mergeable.
	MergeAttempts int `json:"merge_attempts,omitempty" yaml:"merge_attempts,omitempty"`

	// SelfReviewed records that the self-review before the PR ran.
	SelfReviewed bool `json:"self_reviewed,omitempty" yaml:"self_reviewed,omitempty"`
	// Outcome says how a done run ended: merged, or no-changes.
	Outcome Outcome `json:"outcome,omitempty" yaml:"outcome,omitempty"`

	// Gate is the gate the run waits at; GateApproved is set by `loop approve`.
	Gate         string `json:"gate,omitempty" yaml:"gate,omitempty"`
	GateApproved string `json:"gate_approved,omitempty" yaml:"gate_approved,omitempty"`

	Error    string    `json:"error,omitempty" yaml:"error,omitempty"`
	Created  time.Time `json:"created" yaml:"created"`
	Updated  time.Time `json:"updated" yaml:"updated"`
	NextPoll time.Time `json:"next_poll,omitempty" yaml:"next_poll,omitempty"`
	Events   []Event   `json:"events,omitempty" yaml:"events,omitempty"`

	dir string
}

// Session records one agent invocation.
type Session struct {
	ID      string    `json:"id" yaml:"id"`
	Kind    string    `json:"kind" yaml:"kind"` // session, verify, review, ci, conflict, step
	Started time.Time `json:"started" yaml:"started"`
	Ended   time.Time `json:"ended" yaml:"ended"`
	CostUSD float64   `json:"cost_usd,omitempty" yaml:"cost_usd,omitempty"`
	Turns   int       `json:"turns,omitempty" yaml:"turns,omitempty"`
	Error   string    `json:"error,omitempty" yaml:"error,omitempty"`
	LogFile string    `json:"log_file" yaml:"log_file"`
}

// Store manages the runs folder.
//
// Listing keeps every run it has parsed in memory, keyed by run id, with
// the size and modification time of its run.yaml. A later listing parses
// a file again only when those changed, so a watch tick over thousands of
// finished runs costs a directory read and one stat per run instead of a
// YAML parse per run. Runs written by another loop process are picked up
// the same way. Callers get their own copy of every run, never the cached
// one, so a run being driven in one goroutine is not seen half-changed
// by a listing in another.
type Store struct {
	Root string // <project>/.loop/runs
	// Now stamps new runs; nil means the wall clock. The engine shares its
	// clock here so a recurring item's next occurrence is computed from
	// the same time that started the previous one.
	Now func() time.Time

	mu    sync.Mutex
	cache map[string]cached
}

// cached is one parsed run and the file state it was parsed from.
type cached struct {
	mod  time.Time
	size int64
	run  *Run
}

// NewStore creates the store under the given .loop folder.
func NewStore(loopDir string) *Store { return &Store{Root: filepath.Join(loopDir, "runs")} }

func (s *Store) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

// NewID builds a run id from the item and the current time.
func NewID(it *item.Item) string { return idAt(it, time.Now()) }

func idAt(it *item.Item, t time.Time) string {
	return fmt.Sprintf("%s-%s", t.Format("20060102-150405"), item.Slug(it.Source+"-"+it.NativeID, 40))
}

// Dir returns a run's folder.
func (s *Store) Dir(id string) string { return filepath.Join(s.Root, id) }

// Create initialises and saves a new run.
func (s *Store) Create(it *item.Item, runner string) (*Run, error) {
	now := s.now()
	r := &Run{
		ID: idAt(it, now), ItemID: it.ID, Item: it, Phase: PhaseQueued, Runner: runner,
		Created: now, Updated: now,
	}
	// Ids carry the second, so a second run of the same item within that
	// second gets a suffix instead of overwriting the first.
	base := r.ID
	for i := 2; ; i++ {
		if _, err := os.Stat(s.Dir(r.ID)); errors.Is(err, os.ErrNotExist) {
			break
		}
		r.ID = fmt.Sprintf("%s-%d", base, i)
	}
	r.dir = s.Dir(r.ID)
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return nil, err
	}
	return r, s.Save(r)
}

// FileName is the run state file inside the run folder.
const FileName = "run.yaml"

// Save writes run.yaml atomically and refreshes the listing cache.
func (s *Store) Save(r *Run) error {
	if r.dir == "" {
		r.dir = s.Dir(r.ID)
	}
	r.Updated = s.now()
	b, err := yaml.Marshal(r)
	if err != nil {
		return err
	}
	tmp := filepath.Join(r.dir, FileName+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	path := filepath.Join(r.dir, FileName)
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if st, err := os.Stat(path); err == nil {
		s.remember(r.ID, st, r)
	}
	return nil
}

// Load reads one run from disk.
func (s *Store) Load(id string) (*Run, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir(id), FileName))
	if err != nil {
		return nil, err
	}
	r := &Run{}
	if err := yaml.Unmarshal(b, r); err != nil {
		return nil, fmt.Errorf("run %s: %w", id, err)
	}
	r.dir = s.Dir(id)
	return r, nil
}

// remember puts a copy of the run into the cache under the file state it
// was written or read with.
func (s *Store) remember(id string, st os.FileInfo, r *Run) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		s.cache = map[string]cached{}
	}
	s.cache[id] = cached{mod: st.ModTime(), size: st.Size(), run: r.clone()}
}

// List returns every run, newest first. Runs whose run.yaml did not
// change since the last listing come from the cache.
func (s *Store) List() ([]*Run, error) {
	entries, err := os.ReadDir(s.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		s.cache = map[string]cached{}
	}
	seen := make(map[string]bool, len(entries))
	var out []*Run
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		st, err := os.Stat(filepath.Join(s.Root, id, FileName))
		if err != nil {
			continue
		}
		c, ok := s.cache[id]
		if !ok || !c.mod.Equal(st.ModTime()) || c.size != st.Size() {
			r, err := s.Load(id)
			if err != nil {
				continue
			}
			c = cached{mod: st.ModTime(), size: st.Size(), run: r}
			s.cache[id] = c
		}
		seen[id] = true
		out = append(out, c.run.clone())
	}
	for id := range s.cache {
		if !seen[id] {
			delete(s.cache, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// clone copies the run so that a caller can change it without touching
// the cache or another caller's copy.
func (r *Run) clone() *Run {
	c := *r
	c.Sessions = append([]Session(nil), r.Sessions...)
	c.HandledComments = append([]int64(nil), r.HandledComments...)
	c.HandledReviews = append([]int64(nil), r.HandledReviews...)
	c.Events = append([]Event(nil), r.Events...)
	if r.PR != nil {
		pr := *r.PR
		c.PR = &pr
	}
	if r.Item != nil {
		it := *r.Item
		it.Labels = append([]string(nil), r.Item.Labels...)
		it.DependsOn = append([]string(nil), r.Item.DependsOn...)
		it.Comments = append([]item.Comment(nil), r.Item.Comments...)
		if r.Item.Extra != nil {
			it.Extra = make(map[string]string, len(r.Item.Extra))
			for k, v := range r.Item.Extra {
				it.Extra[k] = v
			}
		}
		c.Item = &it
	}
	return &c
}

// Active returns runs that are not finished.
func (s *Store) Active() ([]*Run, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	var out []*Run
	for _, r := range all {
		if r.Phase.Active() {
			out = append(out, r)
		}
	}
	return out, nil
}

// ForItem returns the newest active run for an item, or nil.
func (s *Store) ForItem(itemID string) (*Run, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	for _, r := range all {
		if r.ItemID == itemID && r.Phase.Active() {
			return r, nil
		}
	}
	return nil, nil
}

// LastForItem returns the newest run for an item in any phase, or nil.
// Recurring items are due again an interval after this run started.
func (s *Store) LastForItem(itemID string) (*Run, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	for _, r := range all {
		if r.ItemID == itemID {
			return r, nil
		}
	}
	return nil, nil
}

// OpenForItem returns the newest run for an item that is still active or
// that parked with its pull request still open, or nil. A recurring item
// is not started again while such a run exists, so a parked PR does not
// get a sibling.
func (s *Store) OpenForItem(itemID string) (*Run, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	for _, r := range all {
		if r.ItemID == itemID && (r.Phase.Active() || r.PR.Open()) {
			return r, nil
		}
	}
	return nil, nil
}

// Find resolves a run by id, id prefix, or item id.
func (s *Store) Find(ref string) (*Run, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	var candidates []*Run
	for _, r := range all {
		if r.ID == ref {
			return r, nil
		}
		if strings.HasPrefix(r.ID, ref) || r.ItemID == ref || r.Item != nil && r.Item.NativeID == ref {
			candidates = append(candidates, r)
		}
	}
	for _, r := range candidates {
		if r.Phase.Active() {
			return r, nil
		}
	}
	if len(candidates) > 0 {
		return candidates[0], nil
	}
	return nil, fmt.Errorf("no run matches %q", ref)
}

// Dir returns the run folder.
func (r *Run) Dir() string { return r.dir }

// HasWorkdir reports whether the run's checkout exists on disk. It is
// gone after the cleanup phase, loop clean, or retention.
func (r *Run) HasWorkdir() bool {
	if r.Workdir == "" {
		return false
	}
	st, err := os.Stat(r.Workdir)
	return err == nil && st.IsDir()
}

// SummaryFile is where the agent writes the PR summary.
func (r *Run) SummaryFile() string { return filepath.Join(r.dir, "summary.md") }

// FindingsFile is where a self-review session writes its findings.
func (r *Run) FindingsFile() string { return filepath.Join(r.dir, "findings.md") }

// PlanFile is where a plan session writes its plan.
func (r *Run) PlanFile() string { return filepath.Join(r.dir, "plan.md") }

// LogFile is the run-level log.
func (r *Run) LogFile() string { return filepath.Join(r.dir, "run.log") }

// Cost is what the run's sessions cost so far, as reported by the harness.
func (r *Run) Cost() float64 {
	var c float64
	for _, s := range r.Sessions {
		c += s.CostUSD
	}
	return c
}

// AgentTime is the wall clock the run's sessions used so far.
func (r *Run) AgentTime() time.Duration {
	var d time.Duration
	for _, s := range r.Sessions {
		if !s.Ended.IsZero() {
			d += s.Ended.Sub(s.Started)
		}
	}
	return d
}

// Log appends an event.
func (r *Run) Log(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	r.Events = append(r.Events, Event{Time: time.Now(), Phase: r.Phase, Message: msg})
	if len(r.Events) > 500 {
		r.Events = r.Events[len(r.Events)-500:]
	}
	if f, err := os.OpenFile(r.LogFile(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintf(f, "%s [%s] %s\n", time.Now().Format(time.RFC3339), r.Phase, msg)
		f.Close()
	}
}

// SetPhase moves the run and records the transition.
func (r *Run) SetPhase(p Phase, msg string) {
	r.Log("%s -> %s%s", r.Phase, p, suffix(msg))
	r.Phase = p
}

func suffix(s string) string {
	if s == "" {
		return ""
	}
	return ": " + s
}

// Fail marks the run failed.
func (r *Run) Fail(err error) {
	r.Error = err.Error()
	r.SetPhase(PhaseFailed, err.Error())
}

// Block marks the run blocked (needs a human).
func (r *Run) Block(reason string) {
	r.Error = reason
	r.SetPhase(PhaseBlocked, reason)
}

// Lock takes an exclusive advisory lock on the run folder so two loop
// processes never drive the same run. Returns an unlock func.
func (r *Run) Lock() (func(), error) {
	f, err := os.OpenFile(filepath.Join(r.dir, "lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("run %s is being driven by another loop process", r.ID)
	}
	return func() {
		_ = unlockFile(f)
		f.Close()
	}, nil
}

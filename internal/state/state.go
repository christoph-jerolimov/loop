// Package state persists runs as run.yaml under <project>/.loop/runs/<id>/.
package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// PR is the pull request of a run.
type PR struct {
	Number  int    `json:"number" yaml:"number"`
	NodeID  string `json:"node_id" yaml:"node_id"`
	URL     string `json:"url" yaml:"url"`
	HeadSHA string `json:"head_sha" yaml:"head_sha"`
	Draft   bool   `json:"draft" yaml:"draft"`
	Merged  bool   `json:"merged" yaml:"merged"`
}

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
type Store struct {
	Root string // <project>/.loop/runs
}

// NewStore creates the store under the given .loop folder.
func NewStore(loopDir string) *Store { return &Store{Root: filepath.Join(loopDir, "runs")} }

// NewID builds a run id from the item and the current time.
func NewID(it *item.Item) string {
	return fmt.Sprintf("%s-%s", time.Now().Format("20060102-150405"), item.Slug(it.Source+"-"+it.NativeID, 40))
}

// Dir returns a run's folder.
func (s *Store) Dir(id string) string { return filepath.Join(s.Root, id) }

// Create initialises and saves a new run.
func (s *Store) Create(it *item.Item, runner string) (*Run, error) {
	r := &Run{
		ID: NewID(it), ItemID: it.ID, Item: it, Phase: PhaseQueued, Runner: runner,
		Created: time.Now(), Updated: time.Now(),
	}
	r.dir = s.Dir(r.ID)
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return nil, err
	}
	return r, s.Save(r)
}

// FileName is the run state file inside the run folder.
const FileName = "run.yaml"

// Save writes run.yaml atomically.
func (s *Store) Save(r *Run) error {
	if r.dir == "" {
		r.dir = s.Dir(r.ID)
	}
	r.Updated = time.Now()
	b, err := yaml.Marshal(r)
	if err != nil {
		return err
	}
	tmp := filepath.Join(r.dir, FileName+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(r.dir, FileName))
}

// Load reads one run.
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

// List returns every run, newest first.
func (s *Store) List() ([]*Run, error) {
	entries, err := os.ReadDir(s.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Run
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		r, err := s.Load(e.Name())
		if err != nil {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
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

package ghapi

import "testing"

func TestJobID(t *testing.T) {
	c := CheckRun{HTMLURL: "https://github.com/o/r/actions/runs/34716030419/job/103613312518"}
	if got := c.JobID(); got != 103613312518 {
		t.Errorf("JobID = %d", got)
	}
	if got := (CheckRun{DetailsURL: "https://example.com/other"}).JobID(); got != 0 {
		t.Errorf("non-Actions check should have no job id, got %d", got)
	}
}

func TestTailLog(t *testing.T) {
	log := "2026-09-12T16:44:04.1234567Z first\n2026-09-12T16:44:05.0000000Z \x1b[31mFAIL\x1b[0m pkg\n2026-09-12T16:44:06.0000000Z last\n"
	got := TailLog(log, 2)
	want := "FAIL pkg\nlast"
	if got != want {
		t.Errorf("TailLog = %q, want %q", got, want)
	}
	if TailLog(log, 0) == "" {
		t.Error("n=0 should keep everything")
	}
}

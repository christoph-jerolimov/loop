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

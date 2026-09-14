package host

import "testing"

func TestTailLog(t *testing.T) {
	log := "2026-09-12T16:44:04.0000000Z first\n2026-09-12T16:44:05.0000000Z \x1b[31mFAIL\x1b[0m pkg\n2026-09-12T16:44:06.0000000Z last  \n"
	got := TailLog(log, 2)
	if want := "FAIL pkg\nlast"; got != want {
		t.Errorf("TailLog = %q, want %q", got, want)
	}
	if TailLog(log, 0) == "" {
		t.Error("0 keeps every line")
	}
}

func TestCheckFailedAndCanPush(t *testing.T) {
	for _, c := range []string{"failure", "timed_out", "cancelled", "action_required", "startup_failure"} {
		if !(Check{Conclusion: c}).Failed() {
			t.Errorf("%s should count as failed", c)
		}
	}
	for _, c := range []string{"success", "neutral", "skipped", ""} {
		if (Check{Conclusion: c}).Failed() {
			t.Errorf("%s should not count as failed", c)
		}
	}
	for p, want := range map[string]bool{"admin": true, "maintain": true, "write": true, "read": false, "none": false, "": false} {
		if CanPush(p) != want {
			t.Errorf("CanPush(%q) = %v", p, !want)
		}
	}
	for lvl, want := range map[int]string{50: "admin", 40: "admin", 30: "write", 20: "read", 10: "read", 0: "none"} {
		if got := AccessName(lvl); got != want {
			t.Errorf("AccessName(%d) = %q, want %q", lvl, got, want)
		}
	}
}

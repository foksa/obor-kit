package run

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"obor/internal/testkit"
)

// bash slots.test.sh (lib/slots.sh, checkSlots.ts's protocol): claims, waits, the merge marker's age cap, foreign and
// legacy markers, stale reclaim, release only by the holder.
func TestSlotsTry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := Slots{Dir: dir, Max: 2, BlockMax: 900 * time.Second, Marker: "merge-running-proj"}
	write := func(name, content string) { os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644) }
	live, _ := testkit.Sleeper(t)
	dead := testkit.DeadPID(t)
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	old := strconv.FormatInt(time.Now().Add(-time.Hour).UnixMilli(), 10)

	if got, _, _ := s.Try("worker", 1); got != at(dir, "slot-0") {
		t.Fatalf("first claim: %q", got)
	}
	if b, _ := os.ReadFile(at(dir, "slot-0")); len(b) == 0 || string(b[:2]) != "1 " {
		t.Errorf("slot content %q", b)
	}
	Release(at(dir, "slot-0"), 2)
	if _, err := os.Stat(at(dir, "slot-0")); err != nil {
		t.Error("released by a pid that does not hold it")
	}
	Release(at(dir, "slot-0"), 1)

	write("slot-0", strconv.Itoa(dead)+" "+now+"\n")
	if got, _, _ := s.Try("worker", 3); got != at(dir, "slot-0") {
		t.Errorf("dead holder reclaimed: %q", got)
	}
	if _, err := os.Stat(at(dir, "slot-0.reclaim")); err == nil {
		t.Error("reclaim guard left")
	}
	os.Remove(at(dir, "slot-0"))

	write("merge-running-proj", strconv.Itoa(live)+" "+now+"\n")
	if got, wait, _ := s.Try("worker", 4); got != "" || wait != "waiting: a merge check is running (pid "+strconv.Itoa(live)+")" {
		t.Errorf("young own marker: %q %q", got, wait)
	}
	write("merge-running-proj", strconv.Itoa(live)+" "+old+"\n")
	write("merge-running", strconv.Itoa(live)+"\n")
	if got, wait, _ := s.Try("worker", 4); got != "" || wait != "waiting for a check slot (2 running)" {
		t.Errorf("old own marker + legacy marker take both slots: %q %q", got, wait)
	}
	os.Remove(at(dir, "merge-running"))
	write("merge-running-other", strconv.Itoa(live)+" "+now+"\n")
	write("merge-running-proj", strconv.Itoa(live)+" "+old+"\n")
	if got, _, _ := s.Try("worker", 4); got != "" {
		t.Errorf("a foreign marker never blocks but takes a slot: claimed %q", got)
	}
	os.Remove(at(dir, "merge-running-other"))
	if got, wait, _ := s.Try("merge", 5); got != "" || wait != "waiting for the other merge check (pid "+strconv.Itoa(live)+")" {
		t.Errorf("merge priority, live own marker: %q %q", got, wait)
	}
	write("merge-running-proj", strconv.Itoa(dead)+" "+now+"\n")
	if got, _, _ := s.Try("merge", 5); got != at(dir, "merge-running-proj") {
		t.Errorf("merge priority, dead own marker reclaimed: %q", got)
	}
	s.Dir = filepath.Join(dir, "merge-running-proj", "x") // a file on the way: no dir
	if _, _, err := s.Try("worker", 6); err == nil {
		t.Error("no slot dir: no error")
	}
}

func at(dir, name string) string { return filepath.Join(dir, name) }

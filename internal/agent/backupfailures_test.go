package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

func TestFailureLogRoundTrip(t *testing.T) {
	l := newFailureLog(t.TempDir())
	l.record("srv1", failureRecord{ID: "1__a", Name: "a", CreatedUnixMs: 10, Error: "boom"})
	l.record("srv1", failureRecord{ID: "2__b", Name: "b", CreatedUnixMs: 20, Error: "bang"})
	l.record("srv2", failureRecord{ID: "3__c", Name: "c", CreatedUnixMs: 30, Error: "pow"})

	jobs := map[string]*agentpb.BackupInfo{}
	l.loadInto(jobs)
	if len(jobs) != 3 {
		t.Fatalf("loaded %d jobs, want 3", len(jobs))
	}
	b := jobs["srv1/2__b"]
	if b == nil || b.State != agentpb.BackupState_BACKUP_STATE_FAILED || b.Error != "bang" || b.CreatedUnixMs != 20 {
		t.Fatalf("record did not round-trip: %+v", b)
	}
}

func TestFailureLogCapsNewestFirst(t *testing.T) {
	l := newFailureLog(t.TempDir())
	for i := 0; i < maxFailureRecords+5; i++ {
		l.record("srv", failureRecord{ID: fmt.Sprintf("%d__x", i), CreatedUnixMs: int64(i)})
	}
	recs := l.read()["srv"]
	if len(recs) != maxFailureRecords {
		t.Fatalf("kept %d records, want %d", len(recs), maxFailureRecords)
	}
	if recs[0].CreatedUnixMs != int64(maxFailureRecords+4) {
		t.Fatalf("newest record not first: %+v", recs[0])
	}
}

func TestFailureLogForget(t *testing.T) {
	l := newFailureLog(t.TempDir())
	l.record("srv", failureRecord{ID: "1__a"})
	l.record("srv", failureRecord{ID: "2__b"})
	l.record("other", failureRecord{ID: "3__c"})

	l.forget("srv", "1__a")
	if recs := l.read()["srv"]; len(recs) != 1 || recs[0].ID != "2__b" {
		t.Fatalf("forget left %+v", recs)
	}
	l.forgetServer("srv")
	m := l.read()
	if _, ok := m["srv"]; ok {
		t.Fatal("forgetServer left records")
	}
	if len(m["other"]) != 1 {
		t.Fatal("forgetServer touched another server's records")
	}
}

func TestFailureLogToleratesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, backupFailuresName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := newFailureLog(dir)
	if got := l.read(); len(got) != 0 {
		t.Fatalf("corrupt file parsed to %v", got)
	}
	// A record over a corrupt file starts a fresh, valid one.
	l.record("srv", failureRecord{ID: "1__a"})
	if recs := l.read()["srv"]; len(recs) != 1 {
		t.Fatalf("record over corrupt file: %v", recs)
	}
}

// The end-to-end shape a restart takes: a failure recorded through the runtime's
// tracker survives into a fresh tracker seeded by loadInto.
func TestFailBackupPersistsAcrossRestart(t *testing.T) {
	stateDir := t.TempDir()
	d := &DockerRuntime{backupJobs: map[string]*agentpb.BackupInfo{}, failures: newFailureLog(stateDir)}
	d.putBackupJob("srv", &agentpb.BackupInfo{Id: "9__nightly", Name: "nightly", CreatedUnixMs: 99,
		State: agentpb.BackupState_BACKUP_STATE_PENDING})
	d.failBackup("srv", "9__nightly", fmt.Errorf("store backup (local): mkdir Z:\\games: not found"))

	// "Restart": a new runtime over the same state dir.
	fresh := &DockerRuntime{backupJobs: map[string]*agentpb.BackupInfo{}, failures: newFailureLog(stateDir)}
	fresh.failures.loadInto(fresh.backupJobs)
	b := fresh.backupJobs["srv/9__nightly"]
	if b == nil || b.State != agentpb.BackupState_BACKUP_STATE_FAILED || b.Error == "" || b.Name != "nightly" {
		t.Fatalf("failure did not survive the restart: %+v", b)
	}

	// The operator deletes the failed row: gone from tracker AND disk.
	fresh.forgetBackupJob("srv", "9__nightly")
	if len(fresh.failures.read()) != 0 {
		t.Fatal("forgetBackupJob left the persisted record")
	}

	// And a removed server clears everything it had.
	d.forgetServerBackupJobs("srv")
	if len(d.failures.read()) != 0 || len(d.backupJobs) != 0 {
		t.Fatal("forgetServerBackupJobs left records")
	}
}

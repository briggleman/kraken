package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// A failed backup used to exist only in the in-memory job tracker: restart the
// agent and the FAILED row vanished from ListBackups, so the operator saw a
// backup that simply never appeared (#221 — plausibly why the enshrouded
// failures went unnoticed for days). failureLog persists those records in the
// agent's state dir — node-local on purpose: the backup target is the thing
// that may be failing, so the record of its failure cannot live there.

// maxFailureRecords caps the retained failures per server, newest first. Ten is
// enough to show a pattern (a week of nightly failures plus manual attempts)
// without the file becoming a log.
const maxFailureRecords = 10

// backupFailuresName is the file under the state dir; one JSON object mapping
// server id → newest-first failure records.
const backupFailuresName = "backup-failures.json"

type failureRecord struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	CreatedUnixMs int64  `json:"created_unix_ms"`
	Error         string `json:"error"`
}

type failureLog struct {
	mu   sync.Mutex
	path string
}

func newFailureLog(stateDir string) *failureLog {
	return &failureLog{path: filepath.Join(stateDir, backupFailuresName)}
}

// read parses the file; a missing or corrupt file is an empty log — failure
// history is diagnostic data, never worth failing a boot over.
func (l *failureLog) read() map[string][]failureRecord {
	out := map[string][]failureRecord{}
	data, err := os.ReadFile(l.path)
	if err != nil {
		return out
	}
	_ = json.Unmarshal(data, &out)
	return out
}

// write persists atomically (tmp + rename) so a crash mid-write can't leave a
// torn file that read() would then discard wholesale.
func (l *failureLog) write(m map[string][]failureRecord) {
	data, err := json.Marshal(m)
	if err != nil {
		return
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, l.path); err != nil {
		_ = os.Remove(tmp)
	}
}

// record prepends a failure for the server, capped at maxFailureRecords.
func (l *failureLog) record(serverID string, r failureRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	m := l.read()
	recs := append([]failureRecord{r}, m[serverID]...)
	if len(recs) > maxFailureRecords {
		recs = recs[:maxFailureRecords]
	}
	m[serverID] = recs
	l.write(m)
}

// forget drops one record (the operator deleted the failed row).
func (l *failureLog) forget(serverID, id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	m := l.read()
	recs := m[serverID][:0]
	for _, r := range m[serverID] {
		if r.ID != id {
			recs = append(recs, r)
		}
	}
	if len(recs) == 0 {
		delete(m, serverID)
	} else {
		m[serverID] = recs
	}
	l.write(m)
}

// forgetServer drops every record for a removed server.
func (l *failureLog) forgetServer(serverID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	m := l.read()
	if _, ok := m[serverID]; !ok {
		return
	}
	delete(m, serverID)
	l.write(m)
}

// loadInto seeds a boot-fresh job tracker with the persisted failures, so
// ListBackups overlays them exactly like live jobs and the Panel keeps showing
// the failed rows (with their reasons) across agent restarts.
func (l *failureLog) loadInto(jobs map[string]*agentpb.BackupInfo) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for serverID, recs := range l.read() {
		for _, r := range recs {
			jobs[backupJobKey(serverID, r.ID)] = &agentpb.BackupInfo{
				Id: r.ID, Name: r.Name, CreatedUnixMs: r.CreatedUnixMs,
				State: agentpb.BackupState_BACKUP_STATE_FAILED,
				Error: r.Error,
			}
		}
	}
}

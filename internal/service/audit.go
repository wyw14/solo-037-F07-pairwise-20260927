package service

import (
    "sort"
    "time"

    "github.com/wyw14/solo-037/internal/journal"
    "github.com/wyw14/solo-037/internal/model"
)

type AuditCheck struct {
    Name string `json:"name"`
    Passed bool `json:"passed"`
    Detail string `json:"detail"`
}

const (
    PersistenceVerified = "verified"
    PersistenceUnverified = "unverified"
)

// PersistenceRow reports one category of run records: how many were expected,
// how many are verified in the journal file, and how many exist only in
// memory or nowhere at all.
type PersistenceRow struct {
    Category string `json:"category"`
    Expected int `json:"expected"`
    Persisted int `json:"persisted"`
    MemoryOnly int `json:"memory_only"`
    Missing int `json:"missing"`
    Source string `json:"source"`
    Detail string `json:"detail"`
}

// PersistenceAudit lists, for one run, which expected records are verified in
// the journal file and which are only held in memory. Status is "unverified"
// whenever the journal file cannot be read or disagrees with memory, so
// in-memory state is never presented as persistent evidence.
type PersistenceAudit struct {
    Run model.RunID `json:"run"`
    Status string `json:"status"`
    Journal string `json:"journal"`
    Issue string `json:"issue,omitempty"`
    Rows []PersistenceRow `json:"rows"`
}

func (p PersistenceAudit) Verified() bool { return p.Status == PersistenceVerified }

func (p PersistenceAudit) Row(category string) PersistenceRow {
    for _, row := range p.Rows {
        if row.Category == category {
            return row
        }
    }
    return PersistenceRow{Category: category}
}

type AuditReport struct {
    Run model.RunID `json:"run"`
    Fence uint64 `json:"fence"`
    CheckedAt time.Time `json:"checked_at"`
    Passed bool `json:"passed"`
    Checks []AuditCheck `json:"checks"`
    Persistence PersistenceAudit `json:"persistence"`
}

func AuditLaboratory(laboratory *Laboratory, run model.RunID, fence uint64, at time.Time) AuditReport {
    report := AuditReport{Run: run, Fence: fence, CheckedAt: at.UTC(), Passed: true}
    report.Add("identity", run != "", string(run))
    owner, currentFence, generation := laboratory.TrackState.Snapshot()
    report.Add("track_generation", generation > 0, unsignedDetail(generation))
    report.Add("track_owner_retired", owner == "", string(owner))
    report.Add("track_fence_retired", currentFence == 0, unsignedDetail(currentFence))
    occupancy := laboratory.Tank.Snapshot()
    report.Add("tank_section_released", len(occupancy) == 0, countDetail(len(occupancy)))
    samples := laboratory.Probes.Samples(run)
    report.Add("wave_samples_present", len(samples) > 0, countDetail(len(samples)))
    report.Add("wave_revision_current", sameRevision(samples), revisionDetail(samples))
    forces := laboratory.Forces.Values(run)
    report.Add("force_samples_present", len(forces) > 0, countDetail(len(forces)))
    persistence := AuditPersistence(laboratory, run)
    report.Persistence = persistence
    consistencyDetail := persistence.Journal
    if persistence.Issue != "" {
        consistencyDetail = persistence.Issue
    }
    report.Add("journal_file_consistent", persistence.Verified(), consistencyDetail)
    waveRow := persistence.Row("wave")
    report.Add("wave_evidence_durable", persistence.Verified() && waveRow.Expected > 0 && waveRow.Persisted >= waveRow.Expected, waveRow.Detail)
    forceRow := persistence.Row("force")
    report.Add("force_evidence_durable", persistence.Verified() && forceRow.Expected > 0 && forceRow.Persisted >= forceRow.Expected, forceRow.Detail)
    brakeRow := persistence.Row("brake")
    report.Add("brake_record_persisted", brakeRow.Expected == 0 || (persistence.Verified() && brakeRow.Persisted >= brakeRow.Expected), brakeRow.Detail)
    stageRow := persistence.Row("stage")
    report.Add("stage_record_persisted", stageRow.Expected == 0 || (persistence.Verified() && stageRow.Persisted >= stageRow.Expected), stageRow.Detail)
    records := journal.RecordsForRun(laboratory.Store.Records(), run)
    report.Add("journal_records_present", len(records) > 0, countDetail(len(records)))
    recovery := journal.Recover(records, run)
    report.Add("recovery_fence_nonnegative", recovery.Fence <= fence, unsignedDetail(recovery.Fence))
    emergencyResult, emergencyFound := laboratory.Emergency.Result(run)
    report.Add("emergency_result_present", emergencyFound, boolDetail(emergencyFound))
    report.Add("emergency_secured", emergencyFound && emergencyResult.Secured, boolDetail(emergencyResult.Secured))
    report.Add("interlocks_ready", laboratory.Interlocks.Ready(), boolDetail(laboratory.Interlocks.Ready()))
    report.Sort()
    return report
}

// AuditPersistence reconciles one run's expected records against the journal
// file on disk. Expected counts come from live run state (probe and force
// collections, emergency result); persisted counts come only from re-reading
// the journal file, never from the store's in-memory copy.
func AuditPersistence(laboratory *Laboratory, run model.RunID) PersistenceAudit {
    audit := PersistenceAudit{Run: run, Status: PersistenceVerified, Journal: laboratory.Store.Path()}
    diskRecords, diskErr := laboratory.Store.PersistedRecords()
    diskReadable := diskErr == nil
    if !diskReadable {
        audit.Status = PersistenceUnverified
        audit.Issue = "journal unreadable: " + diskErr.Error()
    }
    diskRun := journal.RecordsForRun(diskRecords, run)
    memoryRun := journal.RecordsForRun(laboratory.Store.Records(), run)
    if diskReadable && !sameRecordSet(diskRun, memoryRun) {
        audit.Status = PersistenceUnverified
        audit.Issue = "journal differs from memory: disk=" + countDetail(len(diskRun)) + " memory=" + countDetail(len(memoryRun))
    }
    diskCounts := journal.CountKinds(diskRun)
    waveExpected := len(laboratory.Probes.Samples(run))
    forceExpected := len(laboratory.Forces.Values(run))
    brakeExpected := 0
    if _, found := laboratory.Emergency.Result(run); found {
        brakeExpected = 1
    }
    stageExpected := 0
    if waveExpected+forceExpected+brakeExpected > 0 {
        stageExpected = 1
    }
    audit.Rows = []PersistenceRow{
        persistenceRow("wave", waveExpected, waveExpected, diskCounts["wave_trace"], diskReadable, audit.Verified()),
        persistenceRow("force", forceExpected, forceExpected, diskCounts["force_trace"], diskReadable, audit.Verified()),
        persistenceRow("brake", brakeExpected, brakeExpected, diskCounts["brake"], diskReadable, audit.Verified()),
        persistenceRow("stage", stageExpected, 0, diskCounts["stage"], diskReadable, audit.Verified()),
    }
    return audit
}

func persistenceRow(category string, expected int, memoryHeld int, persisted int, diskReadable bool, verified bool) PersistenceRow {
    memoryOnly := memoryHeld
    if diskReadable {
        memoryOnly = memoryHeld - persisted
        if memoryOnly < 0 {
            memoryOnly = 0
        }
    }
    missing := expected - persisted - memoryOnly
    if missing < 0 {
        missing = 0
    }
    row := PersistenceRow{
        Category: category,
        Expected: expected,
        Persisted: persisted,
        MemoryOnly: memoryOnly,
        Missing: missing,
        Detail: countDetail(persisted) + "/" + countDetail(expected) + " on disk",
    }
    row.Source = persistenceSource(row, verified)
    return row
}

func persistenceSource(row PersistenceRow, verified bool) string {
    if !verified {
        return PersistenceUnverified
    }
    switch {
    case row.Expected == 0 && row.Persisted == 0:
        return "none"
    case row.Persisted > 0 && row.MemoryOnly > 0:
        return "journal_file+memory"
    case row.Persisted > 0:
        return "journal_file"
    case row.MemoryOnly > 0:
        return "memory_only"
    default:
        return "missing"
    }
}

func sameRecordSet(disk []journal.Record, memory []journal.Record) bool {
    if len(disk) != len(memory) {
        return false
    }
    counts := make(map[model.EventID]int, len(disk))
    for _, record := range disk {
        counts[record.ID]++
    }
    for _, record := range memory {
        counts[record.ID]--
        if counts[record.ID] < 0 {
            return false
        }
    }
    return true
}

func (r *AuditReport) Add(name string, passed bool, detail string) {
    r.Checks = append(r.Checks, AuditCheck{Name: name, Passed: passed, Detail: detail})
    r.Passed = r.Passed && passed
}

func (r *AuditReport) Sort() {
    sort.SliceStable(r.Checks, func(i int, j int) bool {
        return r.Checks[i].Name < r.Checks[j].Name
    })
}

func (r AuditReport) FailedChecks() []AuditCheck {
    result := make([]AuditCheck, 0)
    for _, check := range r.Checks {
        if !check.Passed {
            result = append(result, check)
        }
    }
    return result
}

func (r AuditReport) CheckMap() map[string]bool {
    result := make(map[string]bool, len(r.Checks))
    for _, check := range r.Checks {
        result[check.Name] = check.Passed
    }
    return result
}

func sameRevision(evidence []model.Evidence) bool {
    if len(evidence) == 0 {
        return false
    }
    revision := evidence[0].Revision
    for _, item := range evidence[1:] {
        if item.Revision != revision {
            return false
        }
    }
    return true
}

func revisionDetail(evidence []model.Evidence) string {
    if len(evidence) == 0 {
        return "none"
    }
    revisions := make(map[model.RevisionID]struct{})
    for _, item := range evidence {
        revisions[item.Revision] = struct{}{}
    }
    return countDetail(len(revisions))
}

func countDetail(value int) string {
    if value == 0 {
        return "0"
    }
    result := ""
    for value > 0 {
        result = string(rune('0'+value%10)) + result
        value /= 10
    }
    return result
}

func unsignedDetail(value uint64) string {
    if value == 0 {
        return "0"
    }
    result := ""
    for value > 0 {
        result = string(rune('0'+value%10)) + result
        value /= 10
    }
    return result
}

func boolDetail(value bool) string {
    if value {
        return "true"
    }
    return "false"
}

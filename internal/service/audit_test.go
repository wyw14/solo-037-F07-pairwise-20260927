package service

import (
    "os"
    "strings"
    "testing"
    "time"
)

func exerciseLaboratory(t *testing.T) (*Laboratory, AuditReport) {
    t.Helper()
    laboratory, err := NewLaboratory(t.TempDir())
    if err != nil {
        t.Fatal(err)
    }
    diagnostics := laboratory.Exercise(time.Unix(1700000000, 0))
    audit, ok := diagnostics["audit"].(AuditReport)
    if !ok {
        t.Fatalf("diagnostics missing audit: %T", diagnostics["audit"])
    }
    return laboratory, audit
}

func TestAuditListsPersistedSourcesAndGapsPerRun(t *testing.T) {
    _, audit := exerciseLaboratory(t)
    persistence := audit.Persistence
    if persistence.Status != PersistenceVerified {
        t.Fatalf("expected verified persistence, got %q: %s", persistence.Status, persistence.Issue)
    }
    wave := persistence.Row("wave")
    if wave.Expected == 0 || wave.Persisted != wave.Expected || wave.Source != "journal_file" {
        t.Fatalf("wave row does not show frames verified on disk: %+v", wave)
    }
    force := persistence.Row("force")
    if force.Expected != 1 || force.Persisted != 1 || force.Source != "journal_file" {
        t.Fatalf("force row does not show frame verified on disk: %+v", force)
    }
    brake := persistence.Row("brake")
    if brake.Expected != 1 || brake.Persisted != 0 || brake.MemoryOnly != 1 || brake.Source != "memory_only" {
        t.Fatalf("brake row does not show memory-only gap: %+v", brake)
    }
    stage := persistence.Row("stage")
    if stage.Expected != 1 || stage.Persisted != 0 || stage.Missing != 1 || stage.Source != "missing" {
        t.Fatalf("stage row does not show missing record: %+v", stage)
    }
    checks := audit.CheckMap()
    if !checks["journal_file_consistent"] || !checks["wave_evidence_durable"] || !checks["force_evidence_durable"] {
        t.Fatalf("persisted frames not certified from disk: %+v", checks)
    }
    if checks["brake_record_persisted"] || checks["stage_record_persisted"] {
        t.Fatalf("unpersisted brake/stage records certified: %+v", checks)
    }
    if audit.Passed {
        t.Fatal("audit passed despite brake and stage records never landing on disk")
    }
}

func TestAuditMarksUnverifiedWhenJournalFileCorrupt(t *testing.T) {
    laboratory, audit := exerciseLaboratory(t)
    if err := os.WriteFile(laboratory.Store.Path(), []byte("{not json"), 0o644); err != nil {
        t.Fatal(err)
    }
    reaudited := AuditLaboratory(laboratory, audit.Run, audit.Fence, time.Now())
    if reaudited.Persistence.Status != PersistenceUnverified {
        t.Fatalf("corrupt journal not marked unverified: %+v", reaudited.Persistence)
    }
    checks := reaudited.CheckMap()
    if checks["journal_file_consistent"] {
        t.Fatal("corrupt journal reported consistent")
    }
    if checks["wave_evidence_durable"] || checks["force_evidence_durable"] {
        t.Fatal("in-memory flags certified as durable while journal unreadable")
    }
    for _, row := range reaudited.Persistence.Rows {
        if row.Source == "journal_file" {
            t.Fatalf("row %s claims journal source while unreadable: %+v", row.Category, row)
        }
    }
}

func TestAuditMarksUnverifiedWhenJournalFileMissing(t *testing.T) {
    laboratory, audit := exerciseLaboratory(t)
    if err := os.Remove(laboratory.Store.Path()); err != nil {
        t.Fatal(err)
    }
    reaudited := AuditLaboratory(laboratory, audit.Run, audit.Fence, time.Now())
    if reaudited.Persistence.Status != PersistenceUnverified {
        t.Fatalf("missing journal not marked unverified: %+v", reaudited.Persistence)
    }
    if !strings.Contains(reaudited.Persistence.Issue, "differs from memory") {
        t.Fatalf("issue does not describe divergence: %q", reaudited.Persistence.Issue)
    }
    if reaudited.CheckMap()["wave_evidence_durable"] {
        t.Fatal("memory state certified as durable without journal file")
    }
}

func TestAuditDetectsWaveFrameThatNeverLanded(t *testing.T) {
    laboratory, err := NewLaboratory(t.TempDir())
    if err != nil {
        t.Fatal(err)
    }
    laboratory.Store.FailNext("wave_trace")
    diagnostics := laboratory.Exercise(time.Unix(1700000000, 0))
    audit, ok := diagnostics["audit"].(AuditReport)
    if !ok {
        t.Fatalf("diagnostics missing audit: %T", diagnostics["audit"])
    }
    if audit.Persistence.Status != PersistenceVerified {
        t.Fatalf("dropped append should not break file consistency: %+v", audit.Persistence)
    }
    wave := audit.Persistence.Row("wave")
    if wave.Expected < 2 || wave.Persisted != wave.Expected-1 || wave.MemoryOnly != 1 {
        t.Fatalf("dropped frame not surfaced as memory-only gap: %+v", wave)
    }
    if audit.CheckMap()["wave_evidence_durable"] {
        t.Fatal("memory-only frame certified as durable")
    }
}

func TestAuditReadsPersistedFramesAfterRestart(t *testing.T) {
    directory := t.TempDir()
    laboratory, err := NewLaboratory(directory)
    if err != nil {
        t.Fatal(err)
    }
    diagnostics := laboratory.Exercise(time.Unix(1700000000, 0))
    run := diagnostics["audit"].(AuditReport).Run
    restarted, err := NewLaboratory(directory)
    if err != nil {
        t.Fatal(err)
    }
    persistence := AuditPersistence(restarted, run)
    if persistence.Status != PersistenceVerified {
        t.Fatalf("restart rendered journal unverified: %+v", persistence)
    }
    wave := persistence.Row("wave")
    if wave.Persisted == 0 || wave.Source != "journal_file" {
        t.Fatalf("persisted frames not attributed to journal after restart: %+v", wave)
    }
    if wave.MemoryOnly != 0 {
        t.Fatalf("restarted service reported live memory for old run: %+v", wave)
    }
}

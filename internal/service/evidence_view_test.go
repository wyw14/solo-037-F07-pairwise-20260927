package service

import (
    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"

    "github.com/wyw14/solo-037/internal/journal"
    "github.com/wyw14/solo-037/internal/model"
)

func ledgerFor(view EvidenceView, run model.RunID) *RunEvidence {
    for index := range view.Runs {
        if view.Runs[index].Run == run {
            return &view.Runs[index]
        }
    }
    return nil
}

func category(run RunEvidence, name string) CategoryLedger {
    for _, category := range run.Categories {
        if category.Category == name {
            return category
        }
    }
    return CategoryLedger{Category: name, Status: "missing"}
}

func TestExerciseLedgerVerifiesAllExpectedRecordsFromDisk(t *testing.T) {
    laboratory, err := NewLaboratory(t.TempDir())
    if err != nil {
        t.Fatal(err)
    }
    diagnostics := laboratory.Exercise(time.Unix(1700000100, 0))
    operation, _ := diagnostics["operation"].(model.Operation)

    view := BuildLaboratoryEvidenceView(laboratory, laboratory.Plans, time.Unix(1700000101, 0))
    run := ledgerFor(view, operation.ID)
    if run == nil {
        t.Fatal("exercised run missing from ledger")
    }
    if run.Status != EvidenceVerified {
        t.Fatalf("run not verified: %s gaps=%v", run.Status, run.Gaps)
    }
    for _, name := range []string{"wave", "force", "brake", "stage"} {
        ledger := category(*run, name)
        if ledger.Status != EvidenceVerified {
            t.Fatalf("%s ledger = %s, gaps=%v", name, ledger.Status, ledger.Gaps)
        }
        if ledger.OnDisk != ledger.Expected {
            t.Fatalf("%s on disk %d != expected %d", name, ledger.OnDisk, ledger.Expected)
        }
    }
    if category(*run, "stage").Expected != len(model.StageSequence()) {
        t.Fatalf("expected full stage sequence, got %d", category(*run, "stage").Expected)
    }

    audit, _ := diagnostics["audit"].(AuditReport)
    if !audit.Passed {
        t.Fatalf("audit failed on fully durable run: %+v", audit.FailedChecks())
    }
}

func TestLedgerReportsMissingForceFramesWhenAppendFails(t *testing.T) {
    laboratory, err := NewLaboratory(t.TempDir())
    if err != nil {
        t.Fatal(err)
    }
    laboratory.Store.FailNext("force_trace")
    diagnostics := laboratory.Exercise(time.Unix(1700000200, 0))
    operation, _ := diagnostics["operation"].(model.Operation)
    if diagnostics["force_persist_error"] == "" {
        t.Fatal("expected force persist error to be surfaced")
    }

    view := BuildLaboratoryEvidenceView(laboratory, laboratory.Plans, time.Unix(1700000201, 0))
    run := ledgerFor(view, operation.ID)
    force := category(*run, "force")
    if force.Status != EvidenceMissing || force.OnDisk != 0 || force.Expected != 1 {
        t.Fatalf("force ledger = %+v", force)
    }
    if run.Status != EvidenceMissing {
        t.Fatalf("run status = %s, want missing", run.Status)
    }
    if len(laboratory.Forces.Values(operation.ID)) != 0 {
        t.Fatal("failed durable write must not publish frames into memory")
    }
}

func TestLedgerTreatsMemoryOnlyFramesAsUnverified(t *testing.T) {
    laboratory, err := NewLaboratory(t.TempDir())
    if err != nil {
        t.Fatal(err)
    }
    run := model.RunID("run-memory-only")
    plan := laboratory.Plans.Register(run, "rev-1", model.StageSequence())
    frame := model.NewEvidence(run, plan.Revision, "probe-a", model.EvidenceWaveTime, 0.2, time.Unix(1700000300, 0))
    laboratory.Plans.ExpectWaves(run, []model.Evidence{frame})
    // Publish straight to memory without touching the journal.
    laboratory.Probes.Add(frame)

    view := BuildLaboratoryEvidenceView(laboratory, laboratory.Plans, time.Unix(1700000301, 0))
    reconciled := ledgerFor(view, run)
    wave := category(*reconciled, "wave")
    if wave.Status != EvidenceUnverified {
        t.Fatalf("memory-only frame status = %s, want unverified", wave.Status)
    }
    found := false
    for _, gap := range reconciled.Gaps {
        if strings.Contains(gap, "only in memory") {
            found = true
        }
    }
    if !found {
        t.Fatalf("memory-only gap not reported: %v", reconciled.Gaps)
    }
}

func TestLedgerUnverifiedWhenJournalFileCorrupt(t *testing.T) {
    directory := t.TempDir()
    laboratory, err := NewLaboratory(directory)
    if err != nil {
        t.Fatal(err)
    }
    diagnostics := laboratory.Exercise(time.Unix(1700000400, 0))
    operation, _ := diagnostics["operation"].(model.Operation)

    if err := os.WriteFile(filepath.Join(directory, "journal", "events.jsonl"), []byte("{not json\n"), 0o644); err != nil {
        t.Fatal(err)
    }
    view := BuildLaboratoryEvidenceView(laboratory, laboratory.Plans, time.Unix(1700000401, 0))
    run := ledgerFor(view, operation.ID)
    if run.FileReadable {
        t.Fatal("corrupt journal reported readable")
    }
    if run.Status != EvidenceUnverified {
        t.Fatalf("corrupt journal run status = %s, want unverified", run.Status)
    }
    for _, name := range []string{"wave", "force", "brake", "stage"} {
        if ledger := category(*run, name); ledger.Status != EvidenceUnverified {
            t.Fatalf("%s status = %s, want unverified", name, ledger.Status)
        }
    }
}

func TestLedgerUnverifiedForBrakeHeldOnlyInMemory(t *testing.T) {
    laboratory, err := NewLaboratory(t.TempDir())
    if err != nil {
        t.Fatal(err)
    }
    run := model.RunID("run-brake-memory")
    laboratory.Plans.Register(run, "rev-1", model.StageSequence())
    laboratory.Plans.ExpectBrake(run)
    command := laboratory.Carriage.Accelerate(run, 1, 7, 1.0, time.Unix(1700000500, 0))
    // Plain Execute publishes a secured result but writes nothing durable.
    result := laboratory.Emergency.Execute(run, command.ID, command.Epoch, nil, time.Unix(1700000501, 0))
    if !result.Secured {
        t.Fatal("test setup needs a secured in-memory brake")
    }

    view := BuildLaboratoryEvidenceView(laboratory, laboratory.Plans, time.Unix(1700000502, 0))
    reconciled := ledgerFor(view, run)
    brake := category(*reconciled, "brake")
    if brake.Status != EvidenceUnverified {
        t.Fatalf("in-memory brake status = %s, want unverified; gaps=%v", brake.Status, brake.Gaps)
    }
    if brake.OnDisk != 0 {
        t.Fatalf("brake on disk = %d, want 0", brake.OnDisk)
    }
}

func TestLedgerUnverifiedForUnplannedJournalRun(t *testing.T) {
    laboratory, err := NewLaboratory(t.TempDir())
    if err != nil {
        t.Fatal(err)
    }
    run := model.RunID("run-orphan")
    record, err := journal.NewRecord(run, "wave_trace", model.Evidence{ID: model.NewEventID(), Run: run, Kind: model.EvidenceWaveTime}, time.Unix(1700000600, 0))
    if err != nil {
        t.Fatal(err)
    }
    if err := laboratory.Store.Append(record); err != nil {
        t.Fatal(err)
    }
    view := BuildLaboratoryEvidenceView(laboratory, laboratory.Plans, time.Unix(1700000600, 0))
    if len(view.UnplannedRuns) != 1 || view.UnplannedRuns[0].Run != run {
        t.Fatalf("unplanned runs = %+v", view.UnplannedRuns)
    }
    if view.UnplannedRuns[0].Status != EvidenceUnverified {
        t.Fatalf("orphan run status = %s, want unverified", view.UnplannedRuns[0].Status)
    }
}

func TestArchiveLedgerVerifiedAfterCommitAndUnverifiedWhenMemoryOnly(t *testing.T) {
    directory := t.TempDir()
    archive, err := OpenMeasurementArchive(directory)
    if err != nil {
        t.Fatal(err)
    }
    run, err := archive.Create("tow", "hull", "section-a", "carriage-1")
    if err != nil {
        t.Fatal(err)
    }
    at := time.Now().Add(-time.Minute).UTC()
    if _, err := archive.Record(run.Operation.ID, MeasurementInput{Kind: model.EvidenceWaveTime, SourceID: "probe-a", Value: 0.3, RecordedAt: at}); err != nil {
        t.Fatal(err)
    }
    view := BuildArchiveEvidenceView(archive, time.Now().UTC())
    reconciled := ledgerFor(view, run.Operation.ID)
    if reconciled == nil || reconciled.Status != EvidenceVerified {
        t.Fatalf("durable archive run = %+v", reconciled)
    }

    memoryOnly, err := OpenMeasurementArchive("")
    if err != nil {
        t.Fatal(err)
    }
    run2, err := memoryOnly.Create("tow", "hull", "section-a", "carriage-1")
    if err != nil {
        t.Fatal(err)
    }
    if _, err := memoryOnly.Record(run2.Operation.ID, MeasurementInput{Kind: model.EvidenceForce, SourceID: "cell", Value: 9.0, RecordedAt: at}); err != nil {
        t.Fatal(err)
    }
    view = BuildArchiveEvidenceView(memoryOnly, time.Now().UTC())
    reconciled = ledgerFor(view, run2.Operation.ID)
    if reconciled.Status != EvidenceUnverified || reconciled.Path != "" {
        t.Fatalf("memory-only archive should be unverified: %+v", reconciled)
    }
}

func TestArchiveLedgerUnverifiedWhenFileTampered(t *testing.T) {
    directory := t.TempDir()
    archive, err := OpenMeasurementArchive(directory)
    if err != nil {
        t.Fatal(err)
    }
    run, err := archive.Create("tow", "hull", "section-a", "carriage-1")
    if err != nil {
        t.Fatal(err)
    }
    if _, err := archive.Record(run.Operation.ID, MeasurementInput{Kind: model.EvidenceWaveTime, SourceID: "probe-a", Value: 0.3, RecordedAt: time.Now().Add(-time.Minute).UTC()}); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(archive.Path(), []byte("not json"), 0o640); err != nil {
        t.Fatal(err)
    }
    view := BuildArchiveEvidenceView(archive, time.Now().UTC())
    reconciled := ledgerFor(view, run.Operation.ID)
    if reconciled == nil || reconciled.Status != EvidenceUnverified {
        t.Fatalf("tampered archive run = %+v", reconciled)
    }
}

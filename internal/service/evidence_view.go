package service

import (
    "encoding/json"
    "errors"
    "os"
    "path/filepath"
    "sort"
    "time"

    "github.com/wyw14/solo-037/internal/journal"
    "github.com/wyw14/solo-037/internal/model"
)

// EvidenceStatus classifies what the audit can prove from the files on disk.
// Memory state never upgrades a status on its own.
type EvidenceStatus string

const (
    // EvidenceVerified: every expected item was found in the durable file.
    EvidenceVerified EvidenceStatus = "verified"
    // EvidenceMissing: the file was readable, but expected items were absent.
    EvidenceMissing EvidenceStatus = "missing"
    // EvidenceUnverified: file and memory disagree or the file is unreadable;
    // no durable claim can be made either way.
    EvidenceUnverified EvidenceStatus = "unverified"
)

type EvidenceFrame struct {
    ID model.EventID `json:"id"`
    SourceID string `json:"source_id"`
    Kind model.EvidenceKind `json:"kind"`
    Revision model.RevisionID `json:"revision"`
    Value float64 `json:"value"`
    RecordedAt time.Time `json:"recorded_at"`
}

type CategoryLedger struct {
    Category string `json:"category"`
    Expected int `json:"expected"`
    OnDisk int `json:"on_disk"`
    InMemory int `json:"in_memory"`
    Sources []EvidenceFrame `json:"sources"`
    Gaps []string `json:"gaps"`
    Status EvidenceStatus `json:"status"`
    Detail string `json:"detail"`
}

type RunEvidence struct {
    Run model.RunID `json:"run"`
    Name string `json:"name"`
    Origin string `json:"origin"`
    Revision model.RevisionID `json:"revision"`
    Path string `json:"path"`
    FileReadable bool `json:"file_readable"`
    FileError string `json:"file_error,omitempty"`
    Categories []CategoryLedger `json:"categories"`
    Gaps []string `json:"gaps"`
    Status EvidenceStatus `json:"status"`
    ExpectedKnown bool `json:"expected_known"`
}

type EvidenceView struct {
    CheckedAt time.Time `json:"checked_at"`
    JournalPath string `json:"journal_path,omitempty"`
    ArchivePath string `json:"archive_path,omitempty"`
    SnapshotPresent bool `json:"snapshot_present"`
    Runs []RunEvidence `json:"runs"`
    UnplannedRuns []RunEvidence `json:"unplanned_runs"`
    Summary map[string]int `json:"summary"`
}

const (
    categoryWave = "wave"
    categoryForce = "force"
    categoryBrake = "brake"
    categoryStage = "stage"

    originJournal = "journal"
    originArchive = "operator_archive"
)

func worseStatus(current EvidenceStatus, next EvidenceStatus) EvidenceStatus {
    rank := map[EvidenceStatus]int{EvidenceVerified: 0, EvidenceMissing: 1, EvidenceUnverified: 2}
    if rank[next] > rank[current] {
        return next
    }
    return current
}

// BuildLaboratoryEvidenceView reconciles the run plans against the journal
// file on disk. The file is re-read for every build so the view cannot be
// fooled by in-memory state. Probe, force receiver and emergency state are
// used only to flag memory-only claims.
func BuildLaboratoryEvidenceView(laboratory *Laboratory, plans *RunPlans, at time.Time) EvidenceView {
    view := EvidenceView{
        CheckedAt: at.UTC(),
        JournalPath: journal.FilePath(laboratory.Store.Dir()),
        Summary: map[string]int{},
        Runs: []RunEvidence{},
        UnplannedRuns: []RunEvidence{},
    }
    if _, err := os.Stat(filepath.Join(laboratory.Snapshots.Dir(), "snapshot.json")); err == nil {
        view.SnapshotPresent = true
    }

    diskRecords, readErr := journal.ReadFile(laboratory.Store.Dir())
    fileExists := !errors.Is(readErr, os.ErrNotExist)
    fileReadable := readErr == nil
    diskByRun := make(map[model.RunID][]journal.Record)
    if fileReadable {
        for _, record := range diskRecords {
            diskByRun[record.Run] = append(diskByRun[record.Run], record)
        }
    }

    planned := plans.Runs()
    sort.Slice(planned, func(i, j int) bool { return planned[i] < planned[j] })
    for _, run := range planned {
        plan, _ := plans.Get(run)
        operation, found := laboratory.Tests.Get(run)
        name := ""
        if found {
            name = operation.Name
        }
        evidence := reconcileJournalRun(run, name, plan, laboratory, diskByRun[run], fileExists, fileReadable, readErr)
        view.Runs = append(view.Runs, evidence)
        view.Summary[string(evidence.Status)]++
    }

    // Runs with durable records but no registered plan: expectations are
    // unknown, so their ledger stays unverified rather than passing silently.
    handled := make(map[model.RunID]struct{}, len(planned))
    for _, run := range planned {
        handled[run] = struct{}{}
    }
    unplanned := make([]model.RunID, 0)
    for run := range diskByRun {
        if _, ok := handled[run]; !ok {
            unplanned = append(unplanned, run)
        }
    }
    sort.Slice(unplanned, func(i, j int) bool { return unplanned[i] < unplanned[j] })
    unplannedEvidence := make([]RunEvidence, 0, len(unplanned))
    for _, run := range unplanned {
        unplannedEvidence = append(unplannedEvidence, reconcileUnplannedRun(run, diskByRun[run], view.JournalPath))
        view.Summary[string(unplannedEvidence[len(unplannedEvidence)-1].Status)]++
    }
    view.UnplannedRuns = unplannedEvidence
    normalizeEvidenceView(&view)
    return view
}

// normalizeEvidenceView replaces nil slices with empty ones so the JSON view
// renders "[]" instead of null for missing collections.
func normalizeEvidenceView(view *EvidenceView) {
    if view.Runs == nil {
        view.Runs = []RunEvidence{}
    }
    if view.UnplannedRuns == nil {
        view.UnplannedRuns = []RunEvidence{}
    }
    if view.Summary == nil {
        view.Summary = map[string]int{}
    }
    normalize := func(runs []RunEvidence) {
        for index := range runs {
            if runs[index].Categories == nil {
                runs[index].Categories = []CategoryLedger{}
            }
            if runs[index].Gaps == nil {
                runs[index].Gaps = []string{}
            }
            for categoryIndex := range runs[index].Categories {
                if runs[index].Categories[categoryIndex].Sources == nil {
                    runs[index].Categories[categoryIndex].Sources = []EvidenceFrame{}
                }
                if runs[index].Categories[categoryIndex].Gaps == nil {
                    runs[index].Categories[categoryIndex].Gaps = []string{}
                }
            }
        }
    }
    normalize(view.Runs)
    normalize(view.UnplannedRuns)
}

func reconcileJournalRun(run model.RunID, name string, plan RunPlan, laboratory *Laboratory, diskRecords []journal.Record, fileExists, fileReadable bool, readErr error) RunEvidence {
    evidence := RunEvidence{
        Run: run,
        Name: name,
        Origin: originJournal,
        Revision: plan.Revision,
        Path: journal.FilePath(laboratory.Store.Dir()),
        ExpectedKnown: true,
        Status: EvidenceVerified,
    }
    if !fileExists {
        evidence.FileError = "journal file not found"
    } else if !fileReadable {
        evidence.FileReadable = false
        evidence.FileError = "journal unreadable: " + readErr.Error()
    } else {
        evidence.FileReadable = true
    }

    waveDisk, forceDisk, brakeDisk, stageDisk := indexRunRecords(diskRecords)

    // A missing file is an empty-but-usable source of evidence (expected
    // records simply never landed); only a corrupt/existing file forces
    // unverified across the board.
    fileUsable := fileReadable || !fileExists
    evidence.Categories = append(evidence.Categories,
        reconcileFrameCategory(categoryWave, plan.Waves, laboratory.Probes.Samples(run), waveDisk, plan.Revision, fileUsable),
        reconcileFrameCategory(categoryForce, plan.Forces, laboratory.Forces.Values(run), forceDisk, plan.Revision, fileUsable),
        reconcileBrakeCategory(plan, laboratory, brakeDisk, fileUsable),
        reconcileStageCategory(plan.Stages, stageDisk, fileUsable),
    )

    for _, category := range evidence.Categories {
        evidence.Status = worseStatus(evidence.Status, category.Status)
        evidence.Gaps = append(evidence.Gaps, category.Gaps...)
    }
    if fileExists && !fileReadable {
        evidence.Gaps = append(evidence.Gaps, evidence.FileError)
    }
    return evidence
}

func indexRunRecords(records []journal.Record) (waves, forces, brakes []journal.Record, stages []journal.Record) {
    ordered := append([]journal.Record{}, records...)
    sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].At.Before(ordered[j].At) })
    for _, record := range ordered {
        switch record.Kind {
        case "wave_trace":
            waves = append(waves, record)
        case "force_trace":
            forces = append(forces, record)
        case "brake_trace":
            brakes = append(brakes, record)
        case "stage":
            stages = append(stages, record)
        }
    }
    return waves, forces, brakes, stages
}

func decodeFrames(records []journal.Record) ([]EvidenceFrame, map[model.EventID]model.Evidence) {
    sources := make([]EvidenceFrame, 0, len(records))
    byID := make(map[model.EventID]model.Evidence, len(records))
    for _, record := range records {
        var item model.Evidence
        if err := json.Unmarshal(record.Payload, &item); err != nil {
            continue
        }
        byID[item.ID] = item
        sources = append(sources, EvidenceFrame{ID: item.ID, SourceID: item.SourceID, Kind: item.Kind, Revision: item.Revision, Value: item.Value, RecordedAt: item.RecordedAt})
    }
    return sources, byID
}

func reconcileFrameCategory(category string, expected, memory []model.Evidence, diskRecords []journal.Record, revision model.RevisionID, fileReadable bool) CategoryLedger {
    sources, diskByID := decodeFrames(diskRecords)
    ledger := CategoryLedger{
        Category: category,
        Expected: len(expected),
        OnDisk: len(sources),
        InMemory: len(memory),
        Sources: sources,
        Status: EvidenceVerified,
    }
    if !fileReadable {
        ledger.Status = EvidenceUnverified
        ledger.Detail = "journal unreadable; counts cannot be trusted"
        ledger.Gaps = append(ledger.Gaps, category+": journal unreadable")
        return ledger
    }
    for _, frame := range expected {
        onDisk, ok := diskByID[frame.ID]
        if !ok {
            ledger.Status = worseStatus(ledger.Status, EvidenceMissing)
            ledger.Gaps = append(ledger.Gaps, category+" frame "+string(frame.ID)+" from "+frame.SourceID+" never landed")
            continue
        }
        if onDisk.Revision != revision {
            ledger.Status = worseStatus(ledger.Status, EvidenceUnverified)
            ledger.Gaps = append(ledger.Gaps, category+" frame "+string(frame.ID)+" revision drift on disk")
        }
    }
    for _, frame := range memory {
        if _, onDisk := diskByID[frame.ID]; !onDisk {
            ledger.Status = worseStatus(ledger.Status, EvidenceUnverified)
            ledger.Gaps = append(ledger.Gaps, category+" frame "+string(frame.ID)+" exists only in memory, not on disk")
        }
    }
    if len(ledger.Gaps) == 0 {
        ledger.Detail = "all frames durable"
    }
    return ledger
}

func reconcileBrakeCategory(plan RunPlan, laboratory *Laboratory, diskRecords []journal.Record, fileReadable bool) CategoryLedger {
    sources, _ := decodeFrames(diskRecords)
    ledger := CategoryLedger{
        Category: categoryBrake,
        OnDisk: len(sources),
        Sources: sources,
        Status: EvidenceVerified,
    }
    result, memoryFound := laboratory.Emergency.Result(plan.Run)
    expected := plan.Brake || (memoryFound && result.Secured)
    if expected {
        ledger.Expected = 1
    }
    if memoryFound && result.Secured {
        ledger.InMemory = 1
    }
    if !fileReadable {
        ledger.Status = EvidenceUnverified
        ledger.Detail = "journal unreadable; brake proof cannot be trusted"
        ledger.Gaps = append(ledger.Gaps, "brake: journal unreadable")
        return ledger
    }
    if expected && len(sources) == 0 {
        ledger.Status = EvidenceMissing
        if memoryFound && result.Secured {
            ledger.Gaps = append(ledger.Gaps, "brake secured in memory but no brake_trace on disk")
        } else {
            ledger.Gaps = append(ledger.Gaps, "brake proof never landed")
        }
    }
    if memoryFound && result.Secured && !result.Persisted {
        ledger.Status = worseStatus(ledger.Status, EvidenceUnverified)
        detail := "brake result held only in memory"
        if result.PersistError != "" {
            detail += ": " + result.PersistError
        }
        ledger.Gaps = append(ledger.Gaps, detail)
    }
    if len(ledger.Gaps) == 0 {
        if expected {
            ledger.Detail = "brake proof durable"
        } else {
            ledger.Detail = "no brake expected"
        }
    }
    return ledger
}

func reconcileStageCategory(expected []model.RunStage, diskRecords []journal.Record, fileReadable bool) CategoryLedger {
    ledger := CategoryLedger{
        Category: categoryStage,
        Expected: len(expected),
        Sources: []EvidenceFrame{},
        Status: EvidenceVerified,
    }
    diskStages := make([]model.RunStage, 0, len(diskRecords))
    for _, record := range diskRecords {
        var stage model.RunStage
        if err := json.Unmarshal(record.Payload, &stage); err != nil {
            ledger.Status = worseStatus(ledger.Status, EvidenceUnverified)
            ledger.Gaps = append(ledger.Gaps, "stage record "+string(record.ID)+" corrupt on disk")
            continue
        }
        diskStages = append(diskStages, stage)
    }
    ledger.OnDisk = len(diskStages)
    if !fileReadable {
        ledger.Status = EvidenceUnverified
        ledger.Detail = "journal unreadable; stage records cannot be trusted"
        ledger.Gaps = append(ledger.Gaps, "stage: journal unreadable")
        return ledger
    }
    remaining := append([]model.RunStage{}, diskStages...)
    for _, stage := range expected {
        matched := -1
        for index, candidate := range remaining {
            if candidate == stage {
                matched = index
                break
            }
        }
        if matched < 0 {
            ledger.Status = worseStatus(ledger.Status, EvidenceMissing)
            ledger.Gaps = append(ledger.Gaps, "stage "+string(stage)+" never landed")
            continue
        }
        remaining = append(remaining[:matched], remaining[matched+1:]...)
    }
    for _, extra := range remaining {
        ledger.Status = worseStatus(ledger.Status, EvidenceUnverified)
        ledger.Gaps = append(ledger.Gaps, "unplanned stage "+string(extra)+" present on disk")
    }
    if len(ledger.Gaps) == 0 {
        ledger.Detail = "all stage records durable"
    }
    return ledger
}

func reconcileUnplannedRun(run model.RunID, records []journal.Record, path string) RunEvidence {
    waves, forces, brakes, stages := indexRunRecords(records)
    evidence := RunEvidence{
        Run: run,
        Origin: originJournal,
        Path: path,
        FileReadable: true,
        ExpectedKnown: false,
        Status: EvidenceUnverified,
        Gaps: []string{"no expectation registered for this run; durability cannot be judged"},
    }
    waveSources, _ := decodeFrames(waves)
    forceSources, _ := decodeFrames(forces)
    brakeSources, _ := decodeFrames(brakes)
    evidence.Categories = []CategoryLedger{
        {Category: categoryWave, OnDisk: len(waveSources), Sources: waveSources, Status: EvidenceUnverified, Detail: "expected frame count unknown"},
        {Category: categoryForce, OnDisk: len(forceSources), Sources: forceSources, Status: EvidenceUnverified, Detail: "expected frame count unknown"},
        {Category: categoryBrake, OnDisk: len(brakeSources), Sources: brakeSources, Status: EvidenceUnverified, Detail: "expected brake proof unknown"},
        {Category: categoryStage, OnDisk: len(stages), Status: EvidenceUnverified, Detail: "expected stage sequence unknown"},
    }
    return evidence
}

// BuildArchiveEvidenceView reconciles operator-measurement runs against the
// operator-measurements.json file. In-memory runs describe what was expected
// to land; the file is re-read and treated as the only durable evidence. A
// memory-only archive (no path configured) can never verify anything.
func BuildArchiveEvidenceView(archive *MeasurementArchive, at time.Time) EvidenceView {
    view := EvidenceView{CheckedAt: at.UTC(), Summary: map[string]int{}}
    if archive == nil {
        return view
    }
    view.ArchivePath = archive.Path()
    memoryRuns := archive.List()

    fileExists, fileReadable := true, true
    if view.ArchivePath == "" {
        fileExists = false
        fileReadable = false
    } else {
        switch _, err := archive.ReadFile(); {
        case err == nil:
        case errors.Is(err, os.ErrNotExist):
            fileExists = false
            fileReadable = false
        default:
            fileExists = true
            fileReadable = false
            view.Summary["archive_error"]++
        }
    }

    diskRuns := map[model.RunID]MeasuredRun{}
    if fileReadable {
        diskRuns, _ = archive.ReadFile()
    }
    for _, run := range memoryRuns {
        diskRun, onDisk := diskRuns[run.Operation.ID]
        evidence := reconcileArchiveRun(run, diskRun, onDisk, view.ArchivePath, fileExists, fileReadable)
        view.Runs = append(view.Runs, evidence)
        view.Summary[string(evidence.Status)]++
    }
    handled := make(map[model.RunID]struct{}, len(memoryRuns))
    for _, run := range memoryRuns {
        handled[run.Operation.ID] = struct{}{}
    }
    for id, run := range diskRuns {
        if _, ok := handled[id]; ok {
            continue
        }
        evidence := RunEvidence{
            Run: id,
            Name: run.Operation.Name,
            Origin: originArchive,
            Path: view.ArchivePath,
            FileReadable: true,
            ExpectedKnown: false,
            Status: EvidenceUnverified,
            Gaps: []string{"run present in archive file but unknown to the running service"},
        }
        view.UnplannedRuns = append(view.UnplannedRuns, evidence)
        view.Summary[string(evidence.Status)]++
    }
    sort.Slice(view.Runs, func(i, j int) bool { return view.Runs[i].Run < view.Runs[j].Run })
    sort.Slice(view.UnplannedRuns, func(i, j int) bool { return view.UnplannedRuns[i].Run < view.UnplannedRuns[j].Run })
    normalizeEvidenceView(&view)
    return view
}

func reconcileArchiveRun(memory MeasuredRun, disk MeasuredRun, onDisk bool, path string, fileExists, fileReadable bool) RunEvidence {
    evidence := RunEvidence{
        Run: memory.Operation.ID,
        Name: memory.Operation.Name,
        Origin: originArchive,
        Revision: memory.Operation.Revision,
        Path: path,
        ExpectedKnown: true,
        Status: EvidenceVerified,
    }
    switch {
    case fileReadable:
        evidence.FileReadable = true
    case !fileExists && path == "":
        evidence.FileError = "archive is memory-only; no durable file configured"
    case !fileExists:
        evidence.FileError = "archive file not found"
    default:
        evidence.FileError = "archive unreadable"
    }

    expectedWaves := filterEvidence(memory.Evidence, model.EvidenceWaveTime, model.EvidenceWaveSpectrum)
    expectedForces := filterEvidence(memory.Evidence, model.EvidenceForce)
    var diskWaves, diskForces []model.Evidence
    if onDisk {
        diskWaves = filterEvidence(disk.Evidence, model.EvidenceWaveTime, model.EvidenceWaveSpectrum)
        diskForces = filterEvidence(disk.Evidence, model.EvidenceForce)
        if disk.Operation.Revision != memory.Operation.Revision {
            evidence.Status = EvidenceUnverified
            evidence.Gaps = append(evidence.Gaps, "revision drift between archive file and running state")
        }
    }
    evidence.Categories = append(evidence.Categories,
        reconcileArchiveFrames(categoryWave, expectedWaves, diskWaves, fileReadable),
        reconcileArchiveFrames(categoryForce, expectedForces, diskForces, fileReadable),
        notApplicableCategory(categoryBrake, "brake proofs are not captured for operator measurements"),
        notApplicableCategory(categoryStage, "stage records are not captured for operator measurements"),
    )
    for _, category := range evidence.Categories {
        evidence.Status = worseStatus(evidence.Status, category.Status)
        evidence.Gaps = append(evidence.Gaps, category.Gaps...)
    }
    return evidence
}

func filterEvidence(evidence []model.Evidence, kinds ...model.EvidenceKind) []model.Evidence {
    wanted := make(map[model.EvidenceKind]struct{}, len(kinds))
    for _, kind := range kinds {
        wanted[kind] = struct{}{}
    }
    result := make([]model.Evidence, 0)
    for _, item := range evidence {
        if _, ok := wanted[item.Kind]; ok {
            result = append(result, item)
        }
    }
    return result
}

func reconcileArchiveFrames(category string, expected, disk []model.Evidence, fileReadable bool) CategoryLedger {
    sources := make([]EvidenceFrame, 0, len(disk))
    diskByID := make(map[model.EventID]model.Evidence, len(disk))
    for _, item := range disk {
        diskByID[item.ID] = item
        sources = append(sources, EvidenceFrame{ID: item.ID, SourceID: item.SourceID, Kind: item.Kind, Revision: item.Revision, Value: item.Value, RecordedAt: item.RecordedAt})
    }
    ledger := CategoryLedger{Category: category, Expected: len(expected), OnDisk: len(sources), InMemory: len(expected), Sources: sources, Status: EvidenceVerified}
    if !fileReadable {
        ledger.Status = EvidenceUnverified
        ledger.Detail = "archive unreadable or absent; frames cannot be verified"
        if len(expected) == 0 {
            ledger.Gaps = append(ledger.Gaps, category+": archive unreadable")
        }
        for range expected {
            ledger.Gaps = append(ledger.Gaps, category+": frame cannot be verified against disk")
        }
        return ledger
    }
    for _, frame := range expected {
        if _, ok := diskByID[frame.ID]; !ok {
            ledger.Status = worseStatus(ledger.Status, EvidenceMissing)
            ledger.Gaps = append(ledger.Gaps, category+" frame "+string(frame.ID)+" from "+frame.SourceID+" is held in memory but absent from archive file")
        }
    }
    if len(ledger.Gaps) == 0 {
        ledger.Detail = "all frames durable"
    }
    return ledger
}

func notApplicableCategory(category, detail string) CategoryLedger {
    return CategoryLedger{Category: category, Sources: []EvidenceFrame{}, Status: EvidenceVerified, Detail: detail}
}

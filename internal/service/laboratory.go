package service

import (
    "os"
    "path/filepath"
    "sync"
    "time"

    "github.com/wyw14/solo-037/internal/absorber"
    "github.com/wyw14/solo-037/internal/carriage"
    "github.com/wyw14/solo-037/internal/emergency"
    "github.com/wyw14/solo-037/internal/force"
    "github.com/wyw14/solo-037/internal/journal"
    "github.com/wyw14/solo-037/internal/model"
    "github.com/wyw14/solo-037/internal/permit"
    "github.com/wyw14/solo-037/internal/probe"
    "github.com/wyw14/solo-037/internal/safety"
    "github.com/wyw14/solo-037/internal/tank"
    "github.com/wyw14/solo-037/internal/telemetry"
    testpkg "github.com/wyw14/solo-037/internal/test"
    "github.com/wyw14/solo-037/internal/track"
    "github.com/wyw14/solo-037/internal/wave"
)

type Laboratory struct {
    Store *journal.Store
    Snapshots *journal.SnapshotStore
    Tank *tank.Registry
    Sections *tank.Sections
    Boundary *tank.Boundary
    Tracks *track.Coordinator
    TrackState *track.State
    Generation *track.Generation
    Carriage *carriage.Controller
    Position *carriage.PositionState
    Wave *wave.Controller
    Probes *probe.Collection
    Forces *force.Receiver
    Permits *permit.Evaluator
    PermitCoordinator *permit.Coordinator
    Absorber *absorber.Controller
    Emergency *emergency.Brake
    Interlocks *safety.Interlocks
    Metrics *telemetry.Metrics
    Tracer *telemetry.Tracer
    Tests *testpkg.Registry
    Plans *RunPlans
}

func NewLaboratory(directory string) (*Laboratory, error) {
    store, err := journal.NewStore(filepath.Join(directory, "journal"))
    if err != nil {
        return nil, err
    }
    snapshots, err := journal.NewSnapshotStore(filepath.Join(directory, "snapshots"))
    if err != nil {
        return nil, err
    }
    tankRegistry := tank.NewRegistry(2.4)
    sections := tank.NewSections([]string{"section-a", "section-b"}, 2.4)
    boundary := tank.NewBoundary()
    tracks := track.NewCoordinator(tankRegistry)
    trackState := &track.State{}
    generation := track.NewGeneration()
    carriageController := carriage.NewController()
    waveController := wave.NewController()
    evaluator := permit.NewEvaluator()
    absorberController := absorber.NewController(boundary)
    return &Laboratory{
        Store: store,
        Snapshots: snapshots,
        Tank: tankRegistry,
        Sections: sections,
        Boundary: boundary,
        Tracks: tracks,
        TrackState: trackState,
        Generation: generation,
        Carriage: carriageController,
        Position: carriage.NewPositionState(generation.Revision()),
        Wave: waveController,
        Probes: probe.NewCollection(),
        Forces: force.NewReceiver(),
        Permits: evaluator,
        PermitCoordinator: permit.NewCoordinator(evaluator),
        Absorber: absorberController,
        Emergency: emergency.NewBrake(carriageController, waveController),
        Interlocks: safety.NewInterlocks(),
        Metrics: &telemetry.Metrics{},
        Tracer: telemetry.NewTracer(),
        Tests: testpkg.NewRegistry(),
        Plans: NewRunPlans(),
    }, nil
}

func (l *Laboratory) Exercise(at time.Time) map[string]any {
    span := l.Tracer.Start("laboratory.exercise", at)
    operation := model.NewOperation("commissioning", "benchmark-hull", "section-a", "carriage-1", at)
    l.Tests.Put(operation)
    l.Plans.Register(operation.ID, operation.Revision, model.StageSequence())
    startup := testpkg.Startup{Tests: l.Tests, Tracks: l.Tracks, Carriage: l.Carriage}
    lease, leaseErr := startup.Arm(operation, operation.Section, at)
    l.Metrics.Request()
    if leaseErr != nil {
        l.Metrics.Fault()
        l.Tracer.End(span, at)
        return map[string]any{"error": leaseErr.Error()}
    }
    l.TrackState.Assign(operation.ID, lease.Fence)
    command := l.Carriage.Accelerate(operation.ID, 1, lease.Fence, 1.4, at.Add(time.Millisecond))
    l.Metrics.Event()
    l.Position.SetWindow(5, 15)
    positionWindow := l.Position.Observe(10)

    // Stage records are journaled as the run moves through them; any append
    // failure shows up as a stage gap in the evidence view instead of being
    // hidden behind in-memory operation state.
    stageErr := l.persistStages(operation.ID,
        timedStage{model.StageConfigured, at},
        timedStage{model.StageArming, at.Add(time.Millisecond)},
        timedStage{model.StageAccelerating, at.Add(2 * time.Millisecond)},
        timedStage{model.StageMeasuring, at.Add(2 * time.Millisecond)},
    )

    profile := wave.NewProfile(0.8, 0.01, 30*time.Millisecond)
    profileEvidence := wave.ProfileEvidence(operation.ID, operation.Revision, profile, at.Add(2*time.Millisecond))
    for index := range profileEvidence {
        profileEvidence[index].SourceID = "probe-" + string(rune('a'+index%2))
    }
    l.Plans.ExpectWaves(operation.ID, profileEvidence)
    waveErr := probe.PersistFrames(l.Probes, l.Store, profileEvidence, at.Add(3*time.Millisecond))
    for _, evidence := range profileEvidence {
        l.Sections.RecordWave(operation.Section, evidence.Value, evidence.RecordedAt)
    }

    forceFrames := []model.Evidence{model.NewEvidence(operation.ID, operation.Revision, "force-frame", model.EvidenceForce, 12.5, at.Add(3*time.Millisecond))}
    l.Plans.ExpectForces(operation.ID, forceFrames)
    forceErr := force.PersistFrames(l.Forces, l.Store, forceFrames, at.Add(3*time.Millisecond))

    analysis := probe.Analyze(l.Probes.Samples(operation.ID))
    policy := permit.DefaultPolicy()
    policyDecision := policy.Evaluate(operation.ID, l.Probes.Samples(operation.ID), true, false, at.Add(4*time.Millisecond))
    l.Permits.SetSources(operation.ID, len(analysis.Sources))
    l.Permits.SetCalm(operation.ID, analysis.RMS <= 0.03)
    permitResult := l.PermitCoordinator.Update(operation.ID)
    _ = l.Absorber.SwitchMode("active", at.Add(5*time.Millisecond))
    reflectionReady := absorber.CalibrateFromProbe(l.Absorber, l.Probes.Samples(operation.ID), operation.Revision)

    l.Plans.ExpectBrake(operation.ID)
    receipt, brakeErr := l.Carriage.Brake(operation.ID, command.ID, command.Epoch, at.Add(6*time.Millisecond))
    if brakeErr == nil {
        l.Permits.ObserveBrake(operation.ID, receipt)
    }
    emergencyResult := l.Emergency.ExecutePersisted(operation.ID, command.ID, command.Epoch, brakeErr, l.Store, at.Add(7*time.Millisecond))

    decelErr := l.persistStages(operation.ID,
        timedStage{model.StageDecelerating, at.Add(6 * time.Millisecond)},
        timedStage{model.StageSettling, at.Add(7 * time.Millisecond)},
        timedStage{model.StageComplete, at.Add(8 * time.Millisecond)},
    )
    l.TrackState.Clear(operation.ID, lease.Fence)
    _ = l.Tracks.Release(operation.ID)
    records := l.Store.Records()
    snapshot, snapshotErr := l.Snapshots.SaveRecords(records, at.Add(8*time.Millisecond))
    scenario := StandardScenario(operation.ID, operation.Revision, at)
    merged := MergeScenarios("commissioning evidence", scenario)
    evidenceView := BuildLaboratoryEvidenceView(l, l.Plans, at.Add(9*time.Millisecond))
    audit := AuditLaboratory(l, operation.ID, lease.Fence, at.Add(9*time.Millisecond))
    reachability := ExerciseReachability(l, at.Add(10*time.Millisecond))
    l.Tracer.End(span, at.Add(9*time.Millisecond))
    return map[string]any{
        "operation": operation,
        "lease": track.LeaseDescription(lease),
        "position": positionWindow,
        "analysis": analysis,
        "policy": policyDecision,
        "permit": permitResult,
        "missing": permit.Missing(policyDecision),
        "requirements": permit.RequirementMap(policyDecision),
        "reflection_ready": reflectionReady,
        "emergency": emergencyResult,
        "journal": journal.Summarize(records),
        "snapshot_runs": journal.SnapshotRuns(snapshot),
        "snapshot_error": errorText(snapshotErr),
        "stage_error": errorText(stageErr),
        "wave_persist_error": errorText(waveErr),
        "force_persist_error": errorText(forceErr),
        "decel_stage_error": errorText(decelErr),
        "scenario": merged.Summary(),
        "evidence_view": evidenceView,
        "audit": audit,
        "audit_failures": audit.FailedChecks(),
        "audit_map": audit.CheckMap(),
        "reachability": reachability,
        "interlocks": l.Interlocks.Snapshot(),
        "metrics": l.Metrics.Snapshot(),
        "spans": l.Tracer.Spans(),
    }
}

type timedStage struct {
    stage model.RunStage
    at time.Time
}

// persistStages appends stage records in one durable batch.
func (l *Laboratory) persistStages(run model.RunID, stages ...timedStage) error {
    records := make([]journal.Record, 0, len(stages))
    for _, item := range stages {
        record, err := journal.StageRecord(run, item.stage, item.at)
        if err != nil {
            return err
        }
        records = append(records, record)
    }
    return l.Store.AppendBatch(records)
}

func errorText(err error) string {
    if err == nil {
        return ""
    }
    return err.Error()
}

var diagnosticsOnce sync.Once
var diagnosticsValue map[string]any

func BuildDiagnostics() map[string]any {
    diagnosticsOnce.Do(func() {
        directory := filepath.Join(os.TempDir(), "wavelab-runtime")
        laboratory, err := NewLaboratory(directory)
        if err != nil {
            diagnosticsValue = map[string]any{"error": err.Error()}
            return
        }
        diagnosticsValue = laboratory.Exercise(time.Unix(1700000000, 0))
    })
    return diagnosticsValue
}

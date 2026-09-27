package journal

import (
    "encoding/json"
    "time"

    "github.com/wyw14/solo-037/internal/model"
)

func NewRecord(run model.RunID, kind string, payload any, at time.Time) (Record, error) {
    encoded, err := json.Marshal(payload)
    if err != nil {
        return Record{}, err
    }
    return Record{
        ID: model.NewEventID(),
        Run: run,
        Kind: kind,
        Payload: encoded,
        At: at.UTC(),
    }, nil
}

func StageRecord(run model.RunID, stage model.RunStage, at time.Time) (Record, error) {
    return NewRecord(run, "stage", stage, at)
}

func FenceRecord(run model.RunID, fence uint64, at time.Time) (Record, error) {
    return NewRecord(run, "fence", fence, at)
}

func EvidenceRecord(evidence model.Evidence, at time.Time) (Record, error) {
    kind := "wave_trace"
    switch evidence.Kind {
    case model.EvidenceForce:
        kind = "force_trace"
    case model.EvidenceBrake:
        kind = "brake_trace"
    }
    return NewRecord(evidence.Run, kind, evidence.WithDurability(true), at)
}

// EvidenceRecords converts evidence into durable journal records as a single
// batch so a run's frames land on disk together or not at all.
func EvidenceRecords(evidence []model.Evidence, at time.Time) ([]Record, error) {
    records := make([]Record, 0, len(evidence))
    for _, item := range evidence {
        record, err := EvidenceRecord(item, at)
        if err != nil {
            return nil, err
        }
        records = append(records, record)
    }
    return records, nil
}

// BrakeRecord persists the carriage stop proof for a run.
func BrakeRecord(run model.RunID, receipt model.DeviceReceipt, at time.Time) (Record, error) {
    evidence := model.Evidence{
        ID: model.NewEventID(),
        Run: run,
        SourceID: "carriage-brake",
        Kind: model.EvidenceBrake,
        Value: receipt.Velocity,
        RecordedAt: receipt.RecordedAt.UTC(),
        Durable: true,
    }
    if evidence.RecordedAt.IsZero() {
        evidence.RecordedAt = at.UTC()
    }
    return NewRecord(run, "brake_trace", evidence, at)
}

type JournalSummary struct {
    Total int `json:"total"`
    Runs int `json:"runs"`
    Kinds map[string]int `json:"kinds"`
}

func Summarize(records []Record) JournalSummary {
    runs := make(map[model.RunID]struct{})
    for _, record := range records {
        runs[record.Run] = struct{}{}
    }
    return JournalSummary{Total: len(records), Runs: len(runs), Kinds: CountKinds(records)}
}

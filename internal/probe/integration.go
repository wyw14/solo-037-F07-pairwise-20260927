package probe

import (
    "time"

    "github.com/wyw14/solo-037/internal/journal"
    "github.com/wyw14/solo-037/internal/model"
    "github.com/wyw14/solo-037/internal/tank"
)

func RecordToSection(coordinator *Coordinator, sections *tank.Sections, section string, run model.RunID, revision model.RevisionID, source string, height float64, at time.Time) model.Evidence {
    evidence := coordinator.Record(run, revision, source, height, at)
    sections.RecordWave(section, height, at)
    return evidence
}

func ResetAfterGap(coordinator *Coordinator, revision model.RevisionID) {
    coordinator.Window.Reset(revision)
}

func SourceIDs(samples []model.Evidence) []string {
    values := make([]string, 0, len(samples))
    seen := make(map[string]struct{})
    for _, sample := range samples {
        if _, ok := seen[sample.SourceID]; !ok {
            values = append(values, sample.SourceID)
            seen[sample.SourceID] = struct{}{}
        }
    }
    return values
}

// PersistFrames appends wave frames to the journal in one batch and only
// publishes them to the in-memory collection after the write is durable, so a
// failed append can never leave frames looking recorded without an on-disk
// trace.
func PersistFrames(collection *Collection, store *journal.Store, frames []model.Evidence, at time.Time) error {
    records, err := journal.EvidenceRecords(frames, at)
    if err != nil {
        return err
    }
    if err := store.AppendBatch(records); err != nil {
        return err
    }
    for _, frame := range frames {
        collection.Add(frame.WithDurability(true))
    }
    return nil
}

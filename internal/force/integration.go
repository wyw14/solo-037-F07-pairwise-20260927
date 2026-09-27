package force

import (
    "time"

    "github.com/wyw14/solo-037/internal/journal"
    "github.com/wyw14/solo-037/internal/model"
)
func Persist(receiver *Receiver, store *journal.Store, run model.RunID, revision model.RevisionID, value float64, at time.Time) error {
    evidence := receiver.Record(run, revision, value, at)
    record, err := journal.EvidenceRecord(evidence, at)
    if err != nil {
        return err
    }
    return store.Append(record)
}

// PersistFrames records force frames in memory only after the batch has been
// appended to the journal, keeping durable state and receiver state aligned.
func PersistFrames(receiver *Receiver, store *journal.Store, frames []model.Evidence, at time.Time) error {
    records, err := journal.EvidenceRecords(frames, at)
    if err != nil {
        return err
    }
    if err := store.AppendBatch(records); err != nil {
        return err
    }
    for _, frame := range frames {
        receiver.Keep(frame.WithDurability(true))
    }
    return nil
}

func Complete(receiver *Receiver, run model.RunID) bool { return receiver.Durable(run) }

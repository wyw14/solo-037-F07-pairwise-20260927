package force

import (
    "sync"
    "time"

    "github.com/wyw14/solo-037/internal/model"
)

type Receiver struct {
    mu sync.Mutex
    values map[model.RunID][]model.Evidence
}

func NewReceiver() *Receiver { return &Receiver{values: make(map[model.RunID][]model.Evidence)} }

func (r *Receiver) Record(run model.RunID, revision model.RevisionID, value float64, at time.Time) model.Evidence {
    r.mu.Lock()
    defer r.mu.Unlock()
    evidence := model.NewEvidence(run, revision, "force-frame", model.EvidenceForce, value, at).WithDurability(true)
    r.values[run] = append(r.values[run], evidence)
    return evidence
}

// Keep stores an evidence frame that was already committed elsewhere (for
// example a journal batch), instead of minting a new in-memory frame.
func (r *Receiver) Keep(evidence model.Evidence) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.values[evidence.Run] = append(r.values[evidence.Run], evidence)
}

func (r *Receiver) Durable(run model.RunID) bool {
    r.mu.Lock()
    defer r.mu.Unlock()
    return len(r.values[run]) > 0 && r.values[run][len(r.values[run])-1].Durable
}

func (r *Receiver) Values(run model.RunID) []model.Evidence {
    r.mu.Lock()
    defer r.mu.Unlock()
    return append([]model.Evidence{}, r.values[run]...)
}

func (r *Receiver) Latest(run model.RunID) (model.Evidence, bool) {
    r.mu.Lock()
    defer r.mu.Unlock()
    values := r.values[run]
    if len(values) == 0 {
        return model.Evidence{}, false
    }
    return values[len(values)-1], true
}

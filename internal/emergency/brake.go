package emergency

import (
    "errors"
    "sync"
    "time"

    "github.com/wyw14/solo-037/internal/carriage"
    "github.com/wyw14/solo-037/internal/journal"
    "github.com/wyw14/solo-037/internal/model"
    "github.com/wyw14/solo-037/internal/wave"
)

type Result struct {
    Run model.RunID `json:"run"`
    Receipt model.DeviceReceipt `json:"receipt"`
    CarriageError string `json:"carriage_error"`
    WaveStopped bool `json:"wave_stopped"`
    Secured bool `json:"secured"`
    Persisted bool `json:"persisted"`
    PersistError string `json:"persist_error,omitempty"`
}

type Brake struct {
    mu sync.Mutex
    carriage *carriage.Controller
    wave *wave.Controller
    results map[model.RunID]Result
}

func NewBrake(carriageController *carriage.Controller, waveController *wave.Controller) *Brake {
    return &Brake{carriage: carriageController, wave: waveController, results: make(map[model.RunID]Result)}
}

func (b *Brake) Execute(run model.RunID, commandID string, epoch uint64, carriageFailure error, at time.Time) Result {
    b.mu.Lock()
    defer b.mu.Unlock()
    result := b.stop(run, commandID, epoch, carriageFailure, at)
    b.results[run] = result
    return result
}

// ExecutePersisted performs the stop sequence and appends the brake proof to
// the journal before publishing the result. A failed durable write leaves
// Persisted false (and names the error): the stop itself is never hidden, but
// the in-memory result must not be presented as durable evidence.
func (b *Brake) ExecutePersisted(run model.RunID, commandID string, epoch uint64, carriageFailure error, store *journal.Store, at time.Time) Result {
    b.mu.Lock()
    defer b.mu.Unlock()
    result := b.stop(run, commandID, epoch, carriageFailure, at)
    if result.Secured {
        record, err := journal.BrakeRecord(run, result.Receipt, at)
        if err == nil {
            err = store.Append(record)
        }
        if err != nil {
            result.PersistError = err.Error()
        } else {
            result.Persisted = true
        }
    }
    b.results[run] = result
    return result
}

// stop must be called with b.mu held.
func (b *Brake) stop(run model.RunID, commandID string, epoch uint64, carriageFailure error, at time.Time) Result {
    result := Result{Run: run}
    if carriageFailure != nil {
        result.CarriageError = carriageFailure.Error()
    } else {
        receipt, err := b.carriage.Brake(run, commandID, epoch, at)
        result.Receipt = receipt
        if err != nil {
            result.CarriageError = err.Error()
        }
    }
    b.wave.Stop()
    result.WaveStopped = !b.wave.Active()
    result.Secured = result.CarriageError == "" && result.WaveStopped
    return result
}

func (b *Brake) Result(run model.RunID) (Result, bool) {
    b.mu.Lock()
    defer b.mu.Unlock()
    result, ok := b.results[run]
    return result, ok
}

func (r Result) Error() error {
    if r.CarriageError != "" {
        return errors.New(r.CarriageError)
    }
    if !r.Secured {
        return errors.New("emergency not secured")
    }
    return nil
}

package journal

import (
    "encoding/json"
    "errors"
    "os"
    "path/filepath"
    "sort"
    "sync"
    "time"

    "github.com/wyw14/solo-037/internal/model"
)

type Snapshot struct {
    Version int `json:"version"`
    CreatedAt time.Time `json:"created_at"`
    Runs map[model.RunID]ReplayState `json:"runs"`
    Summary JournalSummary `json:"summary"`
}

type SnapshotStore struct {
    mu sync.Mutex
    directory string
}

func NewSnapshotStore(directory string) (*SnapshotStore, error) {
    if directory == "" {
        return nil, errors.New("snapshot directory is empty")
    }
    if err := os.MkdirAll(directory, 0755); err != nil {
        return nil, err
    }
    return &SnapshotStore{directory: directory}, nil
}

func (s *SnapshotStore) path() string {
    return filepath.Join(s.directory, "snapshot.json")
}

// Dir is the directory that holds the durable snapshot.json file.
func (s *SnapshotStore) Dir() string { return s.directory }

func (s *SnapshotStore) Build(records []Record, at time.Time) Snapshot {
    runs := make(map[model.RunID]struct{})
    for _, record := range records {
        runs[record.Run] = struct{}{}
    }
    states := make(map[model.RunID]ReplayState, len(runs))
    for run := range runs {
        states[run] = Replay(records, run)
    }
    return Snapshot{
        Version: 1,
        CreatedAt: at.UTC(),
        Runs: states,
        Summary: Summarize(records),
    }
}

func (s *SnapshotStore) Save(snapshot Snapshot) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    encoded, err := json.MarshalIndent(snapshot, "", "  ")
    if err != nil {
        return err
    }
    temporary := s.path() + ".pending"
    file, err := os.OpenFile(temporary, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
    if err != nil {
        return err
    }
    if _, err := file.Write(append(encoded, '\n')); err != nil {
        file.Close()
        return err
    }
    if err := file.Sync(); err != nil {
        file.Close()
        return err
    }
    if err := file.Close(); err != nil {
        return err
    }
    return os.Rename(temporary, s.path())
}

func (s *SnapshotStore) Load() (Snapshot, error) {
    s.mu.Lock()
    defer s.mu.Unlock()
    encoded, err := os.ReadFile(s.path())
    if err != nil {
        return Snapshot{}, err
    }
    var snapshot Snapshot
    if err := json.Unmarshal(encoded, &snapshot); err != nil {
        return Snapshot{}, err
    }
    return snapshot, nil
}

func (s *SnapshotStore) SaveRecords(records []Record, at time.Time) (Snapshot, error) {
    snapshot := s.Build(records, at)
    return snapshot, s.Save(snapshot)
}

func SnapshotRuns(snapshot Snapshot) []model.RunID {
    runs := make([]model.RunID, 0, len(snapshot.Runs))
    for run := range snapshot.Runs {
        runs = append(runs, run)
    }
    sort.Slice(runs, func(i int, j int) bool {
        return runs[i] < runs[j]
    })
    return runs
}

func SnapshotDecision(snapshot Snapshot, run model.RunID) RecoveryDecision {
    state, ok := snapshot.Runs[run]
    if !ok {
        return RecoveryDecision{Run: run, Stage: model.StageConfigured}
    }
    evidenceReady := state.Evidence >= 2
    return RecoveryDecision{
        Run: run,
        Stage: state.Stage,
        Fence: state.Fence,
        EvidenceReady: evidenceReady,
        CanClear: state.Stage == model.StageComplete && evidenceReady,
    }
}

func MergeSnapshots(left Snapshot, right Snapshot) Snapshot {
    if left.Runs == nil {
        left.Runs = make(map[model.RunID]ReplayState)
    }
    for run, state := range right.Runs {
        current, ok := left.Runs[run]
        if !ok || state.Fence >= current.Fence {
            left.Runs[run] = state
        }
    }
    left.Version++
    if right.CreatedAt.After(left.CreatedAt) {
        left.CreatedAt = right.CreatedAt
    }
    return left
}

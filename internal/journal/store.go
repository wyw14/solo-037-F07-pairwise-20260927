package journal

import (
    "bufio"
    "encoding/json"
    "errors"
    "os"
    "path/filepath"
    "sync"
    "time"

    "github.com/wyw14/solo-037/internal/model"
)

type Record struct {
    ID model.EventID `json:"id"`
    Run model.RunID `json:"run"`
    Kind string `json:"kind"`
    Payload json.RawMessage `json:"payload"`
    At time.Time `json:"at"`
}

type Store struct {
    mu sync.Mutex
    directory string
    failKind string
    records []Record
}

func NewStore(directory string) (*Store, error) {
    if err := os.MkdirAll(directory, 0755); err != nil {
        return nil, err
    }
    store := &Store{directory: directory}
    if err := store.load(); err != nil {
        return nil, err
    }
    return store, nil
}

func (s *Store) path() string {
    return filepath.Join(s.directory, "events.jsonl")
}

func (s *Store) load() error {
    file, err := os.Open(s.path())
    if errors.Is(err, os.ErrNotExist) {
        return nil
    }
    if err != nil {
        return err
    }
    defer file.Close()
    scanner := bufio.NewScanner(file)
    for scanner.Scan() {
        var record Record
        if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
            return err
        }
        s.records = append(s.records, record)
    }
    return scanner.Err()
}

// ReadFile reads records directly from the journal file on disk, bypassing
// in-memory state. A missing file returns os.ErrNotExist so callers can
// distinguish "nothing ever landed" from an empty read.
func ReadFile(directory string) ([]Record, error) {
    path := filepath.Join(directory, "events.jsonl")
    file, err := os.Open(path)
    if err != nil {
        return nil, err
    }
    defer file.Close()
    records := make([]Record, 0)
    scanner := bufio.NewScanner(file)
    for scanner.Scan() {
        var record Record
        if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
            return nil, err
        }
        records = append(records, record)
    }
    if err := scanner.Err(); err != nil {
        return nil, err
    }
    return records, nil
}

// FilePath is the expected location of the durable journal.
func FilePath(directory string) string { return filepath.Join(directory, "events.jsonl") }

func (s *Store) FailNext(kind string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.failKind = kind
}

func (s *Store) Append(record Record) error {
    return s.AppendBatch([]Record{record})
}

func (s *Store) AppendBatch(records []Record) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    for _, record := range records {
        if record.Kind == s.failKind {
            s.failKind = ""
            return errors.New("incomplete write")
        }
    }
    pending := append(append([]Record{}, s.records...), records...)
    temporary := s.path() + ".pending"
    file, err := os.Create(temporary)
    if err != nil {
        return err
    }
    encoder := json.NewEncoder(file)
    for _, record := range pending {
        if err := encoder.Encode(record); err != nil {
            file.Close()
            return err
        }
    }
    if err := file.Sync(); err != nil {
        file.Close()
        return err
    }
    if err := file.Close(); err != nil {
        return err
    }
    if err := os.Rename(temporary, s.path()); err != nil {
        return err
    }
    s.records = pending
    return nil
}

func (s *Store) Records() []Record {
    s.mu.Lock()
    defer s.mu.Unlock()
    result := make([]Record, len(s.records))
    copy(result, s.records)
    return result
}

// Dir is the directory that holds the durable events.jsonl journal.
func (s *Store) Dir() string { return s.directory }

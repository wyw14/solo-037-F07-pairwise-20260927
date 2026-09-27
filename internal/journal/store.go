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
    records, err := s.readFile()
    if err != nil {
        return err
    }
    s.records = records
    return nil
}

func (s *Store) readFile() ([]Record, error) {
    file, err := os.Open(s.path())
    if errors.Is(err, os.ErrNotExist) {
        return nil, nil
    }
    if err != nil {
        return nil, err
    }
    defer file.Close()
    var records []Record
    scanner := bufio.NewScanner(file)
    for scanner.Scan() {
        var record Record
        if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
            return nil, err
        }
        records = append(records, record)
    }
    return records, scanner.Err()
}

// Path names the journal file, so audit output can cite the on-disk source.
func (s *Store) Path() string {
    return s.path()
}

// PersistedRecords re-reads the journal file from disk instead of serving the
// in-memory copy, so audits verify what actually landed on disk.
func (s *Store) PersistedRecords() ([]Record, error) {
    s.mu.Lock()
    defer s.mu.Unlock()
    return s.readFile()
}

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

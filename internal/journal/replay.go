package journal

import (
    "encoding/json"
    "sort"

    "github.com/wyw14/solo-037/internal/model"
)

type ReplayState struct {
    Run model.RunID `json:"run"`
    Stage model.RunStage `json:"stage"`
    Fence uint64 `json:"fence"`
    Evidence int `json:"evidence"`
    LastKind string `json:"last_kind"`
}

func Replay(records []Record, run model.RunID) ReplayState {
    ordered := append([]Record{}, records...)
    sort.SliceStable(ordered, func(i int, j int) bool {
        return ordered[i].At.Before(ordered[j].At)
    })
    state := ReplayState{Run: run, Stage: model.StageConfigured}
    for _, record := range ordered {
        if record.Run != run {
            continue
        }
        state.LastKind = record.Kind
        switch record.Kind {
        case "stage":
            var stage model.RunStage
            if json.Unmarshal(record.Payload, &stage) == nil {
                state.Stage = stage
            }
        case "fence":
            var fence uint64
            if json.Unmarshal(record.Payload, &fence) == nil && fence > state.Fence {
                state.Fence = fence
            }
        case "wave_trace", "force_trace", "brake_trace":
            state.Evidence++
        }
    }
    return state
}

func RecordsForRun(records []Record, run model.RunID) []Record {
    result := make([]Record, 0)
    for _, record := range records {
        if record.Run == run {
            result = append(result, record)
        }
    }
    sort.SliceStable(result, func(i int, j int) bool {
        return result[i].At.Before(result[j].At)
    })
    return result
}

func CountKinds(records []Record) map[string]int {
    counts := make(map[string]int)
    for _, record := range records {
        counts[record.Kind]++
    }
    return counts
}

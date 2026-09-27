package service

import (
    "sort"
    "time"

    "github.com/wyw14/solo-037/internal/journal"
    "github.com/wyw14/solo-037/internal/model"
)

type AuditCheck struct {
    Name string `json:"name"`
    Passed bool `json:"passed"`
    Detail string `json:"detail"`
}

type AuditReport struct {
    Run model.RunID `json:"run"`
    Fence uint64 `json:"fence"`
    CheckedAt time.Time `json:"checked_at"`
    Passed bool `json:"passed"`
    Checks []AuditCheck `json:"checks"`
    EvidenceRun *RunEvidence `json:"evidence_run,omitempty"`
}

func AuditLaboratory(laboratory *Laboratory, run model.RunID, fence uint64, at time.Time) AuditReport {
    report := AuditReport{Run: run, Fence: fence, CheckedAt: at.UTC(), Passed: true}
    report.Add("identity", run != "", string(run))
    owner, currentFence, generation := laboratory.TrackState.Snapshot()
    report.Add("track_generation", generation > 0, unsignedDetail(generation))
    report.Add("track_owner_retired", owner == "", string(owner))
    report.Add("track_fence_retired", currentFence == 0, unsignedDetail(currentFence))
    occupancy := laboratory.Tank.Snapshot()
    report.Add("tank_section_released", len(occupancy) == 0, countDetail(len(occupancy)))

    // Evidence checks read the journal file through the ledger: anything the
    // ledger cannot confirm from disk is reported missing or unverified, even
    // when probes, force receiver or the emergency state remember it.
    view := BuildLaboratoryEvidenceView(laboratory, laboratory.Plans, at)
    ledger := ledgerForRun(view, run)
    report.EvidenceRun = ledger
    if ledger == nil {
        report.Add("run_plan_registered", false, "no expectation registered for run")
    } else {
        report.Add("run_plan_registered", true, string(run))
        for _, category := range ledger.Categories {
            name := "evidence_" + category.Category + "_durable"
            if category.Category == categoryStage {
                name = "stage_records_durable"
            }
            passed := category.Status == EvidenceVerified
            report.Add(name, passed, categoryLedgerDetail(category))
        }
    }

    recovery := journal.Recover(laboratory.Store.Records(), run)
    report.Add("recovery_fence_nonnegative", recovery.Fence <= fence, unsignedDetail(recovery.Fence))
    emergencyResult, emergencyFound := laboratory.Emergency.Result(run)
    report.Add("emergency_result_present", emergencyFound, boolDetail(emergencyFound))
    report.Add("emergency_secured", emergencyFound && emergencyResult.Secured, boolDetail(emergencyResult.Secured))
    report.Add("interlocks_ready", laboratory.Interlocks.Ready(), boolDetail(laboratory.Interlocks.Ready()))
    report.Sort()
    return report
}

func ledgerForRun(view EvidenceView, run model.RunID) *RunEvidence {
    for index := range view.Runs {
        if view.Runs[index].Run == run {
            return &view.Runs[index]
        }
    }
    return nil
}

func categoryLedgerDetail(category CategoryLedger) string {
    detail := countDetail(category.OnDisk) + "/" + countDetail(category.Expected) + " on disk"
    if category.Detail != "" {
        detail += "; " + category.Detail
    }
    if len(category.Gaps) > 0 {
        detail += "; " + category.Gaps[0]
    }
    return detail
}

func (r *AuditReport) Add(name string, passed bool, detail string) {
    r.Checks = append(r.Checks, AuditCheck{Name: name, Passed: passed, Detail: detail})
    r.Passed = r.Passed && passed
}

func (r *AuditReport) Sort() {
    sort.SliceStable(r.Checks, func(i int, j int) bool {
        return r.Checks[i].Name < r.Checks[j].Name
    })
}

func (r AuditReport) FailedChecks() []AuditCheck {
    result := make([]AuditCheck, 0)
    for _, check := range r.Checks {
        if !check.Passed {
            result = append(result, check)
        }
    }
    return result
}

func (r AuditReport) CheckMap() map[string]bool {
    result := make(map[string]bool, len(r.Checks))
    for _, check := range r.Checks {
        result[check.Name] = check.Passed
    }
    return result
}

func countDetail(value int) string {
    if value == 0 {
        return "0"
    }
    result := ""
    for value > 0 {
        result = string(rune('0'+value%10)) + result
        value /= 10
    }
    return result
}

func unsignedDetail(value uint64) string {
    if value == 0 {
        return "0"
    }
    result := ""
    for value > 0 {
        result = string(rune('0'+value%10)) + result
        value /= 10
    }
    return result
}

func boolDetail(value bool) string {
    if value {
        return "true"
    }
    return "false"
}

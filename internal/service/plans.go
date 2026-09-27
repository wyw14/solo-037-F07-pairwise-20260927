package service

import (
    "sync"

    "github.com/wyw14/solo-037/internal/model"
)

// RunPlan records what a run was expected to persist: the planned stage
// sequence and the wave/force/brake frames generated during the trial. The
// audit view treats this as the expectation and the journal (or measurement
// archive file) as the evidence; in-memory state alone is never proof.
type RunPlan struct {
    Run model.RunID `json:"run"`
    Revision model.RevisionID `json:"revision"`
    Stages []model.RunStage `json:"stages"`
    Waves []model.Evidence `json:"waves"`
    Forces []model.Evidence `json:"forces"`
    Brake bool `json:"brake"`
}

func (p RunPlan) ExpectedEvidenceCount() int { return len(p.Waves) + len(p.Forces) }

type RunPlans struct {
    mu sync.Mutex
    plans map[model.RunID]RunPlan
}

func NewRunPlans() *RunPlans {
    return &RunPlans{plans: make(map[model.RunID]RunPlan)}
}

func (p *RunPlans) Register(run model.RunID, revision model.RevisionID, stages []model.RunStage) RunPlan {
    p.mu.Lock()
    defer p.mu.Unlock()
    plan := RunPlan{Run: run, Revision: revision, Stages: append([]model.RunStage{}, stages...)}
    p.plans[run] = plan
    return plan
}

func (p *RunPlans) ExpectWaves(run model.RunID, frames []model.Evidence) {
    p.mu.Lock()
    defer p.mu.Unlock()
    plan := p.plans[run]
    plan.Waves = append(plan.Waves, frames...)
    p.plans[run] = plan
}

func (p *RunPlans) ExpectForces(run model.RunID, frames []model.Evidence) {
    p.mu.Lock()
    defer p.mu.Unlock()
    plan := p.plans[run]
    plan.Forces = append(plan.Forces, frames...)
    p.plans[run] = plan
}

func (p *RunPlans) ExpectBrake(run model.RunID) {
    p.mu.Lock()
    defer p.mu.Unlock()
    plan := p.plans[run]
    plan.Brake = true
    p.plans[run] = plan
}

func (p *RunPlans) Get(run model.RunID) (RunPlan, bool) {
    p.mu.Lock()
    defer p.mu.Unlock()
    plan, ok := p.plans[run]
    return plan, ok
}

func (p *RunPlans) Runs() []model.RunID {
    p.mu.Lock()
    defer p.mu.Unlock()
    runs := make([]model.RunID, 0, len(p.plans))
    for run := range p.plans {
        runs = append(runs, run)
    }
    return runs
}

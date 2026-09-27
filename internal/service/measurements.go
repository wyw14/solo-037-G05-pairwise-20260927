package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wyw14/solo-037/internal/model"
	"github.com/wyw14/solo-037/internal/probe"
)

// MeasuredRun contains observations supplied by an operator, unlike the
// deterministic diagnostic scenario used by the legacy equipment page.
type MeasuredRun struct {
	Operation model.Operation           `json:"operation"`
	Evidence  []model.Evidence          `json:"evidence"`
	Layout    probe.Layout              `json:"layout,omitempty"`
	TimeBases map[string]probe.TimeBase `json:"time_bases,omitempty"`
}

type MeasurementInput struct {
	Kind       model.EvidenceKind `json:"kind"`
	SourceID   string             `json:"source_id"`
	Value      float64            `json:"value"`
	RecordedAt time.Time          `json:"recorded_at"`
	Batch      string             `json:"batch,omitempty"`
}

type MeasurementAnalysis struct {
	Operation   model.Operation   `json:"operation"`
	Wave        probe.Analysis    `json:"wave"`
	Propagation probe.Propagation `json:"propagation"`
	ForceCount  int               `json:"force_count"`
	ForceMean   float64           `json:"force_mean"`
	Evaluated   time.Time         `json:"evaluated_at"`
}

type MeasurementArchive struct {
	mu   sync.Mutex
	path string
	runs map[model.RunID]MeasuredRun
}

func OpenMeasurementArchive(directory string) (*MeasurementArchive, error) {
	a := &MeasurementArchive{runs: make(map[model.RunID]MeasuredRun)}
	if directory == "" {
		return a, nil
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return nil, err
	}
	a.path = filepath.Join(directory, "operator-measurements.json")
	data, err := os.ReadFile(a.path)
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &a.runs); err != nil {
		return nil, fmt.Errorf("read measurement archive: %w", err)
	}
	if a.runs == nil {
		return nil, errors.New("measurement archive must contain a run map")
	}
	return a, nil
}

func cloneMeasuredRun(value MeasuredRun) MeasuredRun {
	value.Evidence = append([]model.Evidence(nil), value.Evidence...)
	value.Layout.Probes = append([]probe.Installation(nil), value.Layout.Probes...)
	if value.TimeBases != nil {
		bases := make(map[string]probe.TimeBase, len(value.TimeBases))
		for batch, base := range value.TimeBases {
			bases[batch] = base
		}
		value.TimeBases = bases
	}
	return value
}

func (a *MeasurementArchive) commit(next map[model.RunID]MeasuredRun) error {
	if a.path != "" {
		data, err := json.MarshalIndent(next, "", "  ")
		if err != nil {
			return err
		}
		file, err := os.CreateTemp(filepath.Dir(a.path), "measurements-*.tmp")
		if err != nil {
			return err
		}
		name := file.Name()
		defer os.Remove(name)
		if _, err = file.Write(data); err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err := os.Rename(name, a.path); err != nil {
			return err
		}
	}
	a.runs = next
	return nil
}

func (a *MeasurementArchive) Create(name, modelName, section, carriage string) (MeasuredRun, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(modelName) == "" || strings.TrimSpace(section) == "" || strings.TrimSpace(carriage) == "" {
		return MeasuredRun{}, errors.New("name, model, section and carriage are required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	operation := model.NewOperation(name, modelName, section, carriage, time.Now().UTC())
	value := MeasuredRun{Operation: operation, Evidence: []model.Evidence{}}
	next := make(map[model.RunID]MeasuredRun, len(a.runs)+1)
	for id, run := range a.runs {
		next[id] = run
	}
	next[operation.ID] = value
	if err := a.commit(next); err != nil {
		return MeasuredRun{}, err
	}
	return value, nil
}

func (a *MeasurementArchive) Record(id model.RunID, input MeasurementInput) (model.Evidence, error) {
	if input.Kind != model.EvidenceWaveTime && input.Kind != model.EvidenceForce {
		return model.Evidence{}, errors.New("measurement kind must be wave_time or force")
	}
	if strings.TrimSpace(input.SourceID) == "" || math.IsNaN(input.Value) || math.IsInf(input.Value, 0) || input.RecordedAt.IsZero() || input.RecordedAt.After(time.Now().Add(time.Minute)) {
		return model.Evidence{}, errors.New("source, finite value and valid observation time are required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	run, exists := a.runs[id]
	if !exists {
		return model.Evidence{}, errors.New("run not found")
	}
	evidence := model.NewEvidence(id, run.Operation.Revision, input.SourceID, input.Kind, input.Value, input.RecordedAt).WithDurability(a.path != "").WithBatch(strings.TrimSpace(input.Batch))
	run = cloneMeasuredRun(run)
	run.Evidence = append(run.Evidence, evidence)
	next := make(map[model.RunID]MeasuredRun, len(a.runs))
	for key, value := range a.runs {
		next[key] = value
	}
	next[id] = run
	if err := a.commit(next); err != nil {
		return model.Evidence{}, err
	}
	return evidence, nil
}

// SetProbeLayout replaces the probe installation coordinates of a run. The
// flume length is optional and only bounds the uncovered region reporting.
func (a *MeasurementArchive) SetProbeLayout(id model.RunID, layout probe.Layout) (MeasuredRun, error) {
	probes := make([]probe.Installation, 0, len(layout.Probes))
	seen := make(map[string]struct{}, len(layout.Probes))
	for _, installation := range layout.Probes {
		source := strings.TrimSpace(installation.SourceID)
		if source == "" || math.IsNaN(installation.Position) || math.IsInf(installation.Position, 0) || installation.Position < 0 {
			return MeasuredRun{}, errors.New("probe source and finite non-negative position are required")
		}
		if _, duplicate := seen[source]; duplicate {
			return MeasuredRun{}, errors.New("probe sources must be unique")
		}
		seen[source] = struct{}{}
		probes = append(probes, probe.Installation{SourceID: source, Position: installation.Position})
	}
	if math.IsNaN(layout.FlumeLength) || math.IsInf(layout.FlumeLength, 0) || layout.FlumeLength < 0 {
		return MeasuredRun{}, errors.New("flume length must be finite and non-negative")
	}
	if layout.FlumeLength > 0 {
		for _, installation := range probes {
			if installation.Position > layout.FlumeLength {
				return MeasuredRun{}, errors.New("probe position beyond flume length")
			}
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	run, exists := a.runs[id]
	if !exists {
		return MeasuredRun{}, errors.New("run not found")
	}
	run = cloneMeasuredRun(run)
	run.Layout = probe.Layout{Probes: probes, FlumeLength: layout.FlumeLength}
	next := make(map[model.RunID]MeasuredRun, len(a.runs))
	for key, value := range a.runs {
		next[key] = value
	}
	next[id] = run
	if err := a.commit(next); err != nil {
		return MeasuredRun{}, err
	}
	return run, nil
}

// SetBatchTimeBase marks one sampling batch as confirmed or unknown. Batches
// stay unknown until explicitly confirmed, so unverifiable samples never
// enter the propagation mapping.
func (a *MeasurementArchive) SetBatchTimeBase(id model.RunID, batch string, timeBase probe.TimeBase) (MeasuredRun, error) {
	batch = strings.TrimSpace(batch)
	if batch == "" {
		return MeasuredRun{}, errors.New("batch is required")
	}
	if timeBase != probe.TimeBaseConfirmed && timeBase != probe.TimeBaseUnknown {
		return MeasuredRun{}, errors.New("time base must be confirmed or unknown")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	run, exists := a.runs[id]
	if !exists {
		return MeasuredRun{}, errors.New("run not found")
	}
	run = cloneMeasuredRun(run)
	if run.TimeBases == nil {
		run.TimeBases = make(map[string]probe.TimeBase)
	}
	run.TimeBases[batch] = timeBase
	next := make(map[model.RunID]MeasuredRun, len(a.runs))
	for key, value := range a.runs {
		next[key] = value
	}
	next[id] = run
	if err := a.commit(next); err != nil {
		return MeasuredRun{}, err
	}
	return run, nil
}

func (a *MeasurementArchive) Get(id model.RunID) (MeasuredRun, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	run, exists := a.runs[id]
	return cloneMeasuredRun(run), exists
}

func (a *MeasurementArchive) List() []MeasuredRun {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]MeasuredRun, 0, len(a.runs))
	for _, run := range a.runs {
		result = append(result, cloneMeasuredRun(run))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Operation.CreatedAt.Before(result[j].Operation.CreatedAt) })
	return result
}

func (a *MeasurementArchive) Analyze(id model.RunID) (MeasurementAnalysis, error) {
	run, exists := a.Get(id)
	if !exists {
		return MeasurementAnalysis{}, errors.New("run not found")
	}
	waves := make([]model.Evidence, 0)
	result := MeasurementAnalysis{Operation: run.Operation, Evaluated: time.Now().UTC()}
	for _, evidence := range run.Evidence {
		if evidence.Kind == model.EvidenceWaveTime {
			waves = append(waves, evidence)
		} else if evidence.Kind == model.EvidenceForce {
			result.ForceCount++
			result.ForceMean += evidence.Value
		}
	}
	result.Wave = probe.Analyze(waves)
	result.Propagation = probe.AnalyzePropagation(waves, run.Layout, run.TimeBases)
	if result.ForceCount > 0 {
		result.ForceMean /= float64(result.ForceCount)
	}
	return result, nil
}

func (r *Runtime) CreateMeasuredRun(name, modelName, section, carriage string) (MeasuredRun, error) {
	return r.Measurements.Create(name, modelName, section, carriage)
}

func (r *Runtime) RecordMeasurement(id model.RunID, input MeasurementInput) (model.Evidence, error) {
	return r.Measurements.Record(id, input)
}

func (r *Runtime) SetProbeLayout(id model.RunID, layout probe.Layout) (MeasuredRun, error) {
	return r.Measurements.SetProbeLayout(id, layout)
}

func (r *Runtime) SetBatchTimeBase(id model.RunID, batch string, timeBase probe.TimeBase) (MeasuredRun, error) {
	return r.Measurements.SetBatchTimeBase(id, batch, timeBase)
}

func (r *Runtime) MeasuredRuns() []MeasuredRun { return r.Measurements.List() }

func (r *Runtime) MeasuredRun(id model.RunID) (MeasuredRun, bool) { return r.Measurements.Get(id) }

func (r *Runtime) AnalyzeMeasuredRun(id model.RunID) (MeasurementAnalysis, error) {
	return r.Measurements.Analyze(id)
}

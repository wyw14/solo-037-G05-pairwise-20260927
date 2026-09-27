package probe

import (
    "testing"
    "time"

    "github.com/wyw14/solo-037/internal/model"
)

func waveSample(source string, height float64, at time.Time, batch string) model.Evidence {
    return model.NewEvidence("run-1", "rev-1", source, model.EvidenceWaveTime, height, at).WithBatch(batch)
}

func TestAnalyzePropagationMapsConfirmedBatchesToPositions(t *testing.T) {
    base := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
    layout := Layout{Probes: []Installation{
        {SourceID: "probe-near", Position: 1},
        {SourceID: "probe-far", Position: 3},
    }}
    samples := []model.Evidence{
        waveSample("probe-near", 0.10, base, "b1"),
        waveSample("probe-far", 0.05, base, "b1"),
        waveSample("probe-near", 0.40, base.Add(2*time.Second), "b2"),
        waveSample("probe-far", 0.35, base.Add(4*time.Second), "b2"),
    }
    bases := map[string]TimeBase{"b1": TimeBaseConfirmed, "b2": TimeBaseConfirmed}

    result := AnalyzePropagation(samples, layout, bases)

    if len(result.Windows) != 2 {
        t.Fatalf("expected two confirmed windows, got %+v", result.Windows)
    }
    for _, window := range result.Windows {
        if len(window.Points) != 2 || window.Points[0].Position != 1 || window.Points[1].Position != 3 {
            t.Fatalf("window %s not mapped to positions: %+v", window.Batch, window.Points)
        }
    }
    // Crest reaches probe-near (1 m) at base+2s and probe-far (3 m) at base+4s.
    if len(result.Delays) != 1 {
        t.Fatalf("expected one delay, got %+v", result.Delays)
    }
    delay := result.Delays[0]
    if delay.FromPosition != 1 || delay.ToPosition != 3 || delay.Distance != 2 || delay.Delay != 2*time.Second {
        t.Fatalf("unexpected delay: %+v", delay)
    }
    if len(result.UnknownBatches) != 0 || result.SamplesWithoutBatch != 0 {
        t.Fatalf("nothing should be unknown here: %+v", result)
    }
}

func TestAnalyzePropagationKeepsUnconfirmedBatchesUnknown(t *testing.T) {
    base := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
    layout := Layout{Probes: []Installation{{SourceID: "probe-near", Position: 1}, {SourceID: "probe-far", Position: 3}}}
    samples := []model.Evidence{
        waveSample("probe-near", 0.10, base, "b1"),
        waveSample("probe-far", 0.20, base, "b-unverified"),
        waveSample("probe-far", 0.30, base.Add(time.Second), "b-explicit-unknown"),
        waveSample("probe-near", 0.50, base.Add(2*time.Second), ""),
    }
    bases := map[string]TimeBase{"b1": TimeBaseConfirmed, "b-explicit-unknown": TimeBaseUnknown}

    result := AnalyzePropagation(samples, layout, bases)

    if len(result.Windows) != 1 || result.Windows[0].Batch != "b1" || len(result.Windows[0].Points) != 1 {
        t.Fatalf("only the confirmed batch may be mapped: %+v", result.Windows)
    }
    if len(result.UnknownBatches) != 2 || result.UnknownBatches[0] != "b-explicit-unknown" || result.UnknownBatches[1] != "b-unverified" {
        t.Fatalf("unconfirmed batches must stay unknown: %+v", result.UnknownBatches)
    }
    if result.SamplesWithoutBatch != 1 {
        t.Fatalf("unbatched sample not counted: %+v", result)
    }
    if len(result.Delays) != 0 {
        t.Fatalf("no delay may be derived from a single mapped probe: %+v", result.Delays)
    }
}

func TestAnalyzePropagationReportsUnmappedSourcesAndUncoveredRegions(t *testing.T) {
    base := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
    layout := Layout{
        FlumeLength: 12,
        Probes: []Installation{
            {SourceID: "probe-a", Position: 2},
            {SourceID: "probe-b", Position: 5},
            {SourceID: "probe-c", Position: 5},
        },
    }
    samples := []model.Evidence{
        waveSample("probe-a", 0.10, base, "b1"),
        waveSample("probe-b", 0.20, base, "b1"),
        waveSample("probe-rogue", 0.99, base, "b1"),
    }
    result := AnalyzePropagation(samples, layout, map[string]TimeBase{"b1": TimeBaseConfirmed})

    if len(result.UnmappedSources) != 1 || result.UnmappedSources[0] != "probe-rogue" {
        t.Fatalf("source without coordinates must be reported: %+v", result.UnmappedSources)
    }
    want := []UncoveredRegion{{Start: 0, End: 2}, {Start: 2, End: 5}, {Start: 5, End: 12}}
    if len(result.Uncovered) != len(want) {
        t.Fatalf("uncovered regions: got %+v want %+v", result.Uncovered, want)
    }
    for index := range want {
        if result.Uncovered[index] != want[index] {
            t.Fatalf("uncovered regions: got %+v want %+v", result.Uncovered, want)
        }
    }
    // probe-b and probe-c share position 5: no delay across zero distance.
    for _, delay := range result.Delays {
        if delay.Distance == 0 {
            t.Fatalf("zero-distance delay reported: %+v", delay)
        }
    }
}

func TestAnalyzePropagationWithoutLayoutOrBatches(t *testing.T) {
    base := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
    empty := AnalyzePropagation(nil, Layout{}, nil)
    if len(empty.Windows) != 0 || len(empty.Delays) != 0 || len(empty.Uncovered) != 0 {
        t.Fatalf("empty input must stay empty: %+v", empty)
    }

    bareFlume := AnalyzePropagation(nil, Layout{FlumeLength: 8}, nil)
    if len(bareFlume.Uncovered) != 1 || bareFlume.Uncovered[0] != (UncoveredRegion{Start: 0, End: 8}) {
        t.Fatalf("whole flume is uncovered without probes: %+v", bareFlume.Uncovered)
    }

    forceOnly := AnalyzePropagation([]model.Evidence{
        model.NewEvidence("run-1", "rev-1", "load-cell", model.EvidenceForce, 12, base).WithBatch("b1"),
    }, Layout{FlumeLength: 8}, map[string]TimeBase{"b1": TimeBaseConfirmed})
    if len(forceOnly.Windows) != 0 || len(forceOnly.UnknownBatches) != 0 {
        t.Fatalf("force samples must not enter propagation: %+v", forceOnly)
    }
}

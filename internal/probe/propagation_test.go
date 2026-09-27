package probe

import (
    "math"
    "testing"
    "time"
)

func propagationFixture() ([]Installation, []SampleBatch, time.Time) {
    base := time.Unix(1700000000, 0).UTC()
    installations := []Installation{
        {SourceID: "probe-a", Position: 1},
        {SourceID: "probe-b", Position: 3},
        {SourceID: "probe-c", Position: 6},
    }
    batches := []SampleBatch{
        {ID: "b1", TimeBase: "ptp", Confirmed: true, Samples: []Sample{
            {Source: "probe-a", Height: 0.5, At: base},
            {Source: "probe-b", Height: 0.1, At: base.Add(10 * time.Millisecond)},
            {Source: "probe-c", Height: 0.05, At: base.Add(20 * time.Millisecond)},
        }},
        {ID: "b2", TimeBase: "ptp", Confirmed: true, Samples: []Sample{
            {Source: "probe-a", Height: 0.1, At: base.Add(490 * time.Millisecond)},
            {Source: "probe-b", Height: 0.6, At: base.Add(500 * time.Millisecond)},
            {Source: "probe-c", Height: 0.1, At: base.Add(510 * time.Millisecond)},
        }},
        {ID: "b3", TimeBase: "ptp", Confirmed: true, Samples: []Sample{
            {Source: "probe-a", Height: 0.08, At: base.Add(980 * time.Millisecond)},
            {Source: "probe-b", Height: 0.1, At: base.Add(990 * time.Millisecond)},
            {Source: "probe-c", Height: 0.55, At: base.Add(1000 * time.Millisecond)},
        }},
    }
    return installations, batches, base
}

func TestMapPropagationMapsWindowVerifiedHeightsToPositions(t *testing.T) {
    installations, batches, base := propagationFixture()
    result := MapPropagation(installations, batches, 100*time.Millisecond)
    if len(result.Mapped) != 9 {
        t.Fatalf("expected 9 mapped heights, got %+v", result.Mapped)
    }
    first := result.Mapped[0]
    if first.Batch != "b1" || first.SourceID != "probe-a" || first.Position != 1 || first.Height != 0.5 || !first.At.Equal(base) {
        t.Fatalf("first mapped height wrong: %+v", first)
    }
    for index := 1; index < len(result.Mapped); index++ {
        if result.Mapped[index].Position < result.Mapped[index-1].Position {
            t.Fatalf("mapped heights not ordered by position: %+v", result.Mapped)
        }
    }
    if len(result.Unknown) != 0 {
        t.Fatalf("confirmed batches reported unknown: %+v", result.Unknown)
    }
}

func TestMapPropagationReportsPropagationDelayAndSpeed(t *testing.T) {
    installations, batches, _ := propagationFixture()
    result := MapPropagation(installations, batches, 100*time.Millisecond)
    if len(result.Delays) != 2 {
        t.Fatalf("expected 2 delays, got %+v", result.Delays)
    }
    upstream := result.Delays[0]
    if upstream.FromSource != "probe-a" || upstream.ToSource != "probe-b" || upstream.Delay != 500*time.Millisecond {
        t.Fatalf("upstream delay wrong: %+v", upstream)
    }
    if math.Abs(upstream.Speed-4) > 1e-9 {
        t.Fatalf("upstream speed wrong: %+v", upstream)
    }
    downstream := result.Delays[1]
    if downstream.FromSource != "probe-b" || downstream.ToSource != "probe-c" || downstream.Delay != 500*time.Millisecond {
        t.Fatalf("downstream delay wrong: %+v", downstream)
    }
    if math.Abs(downstream.Speed-6) > 1e-9 {
        t.Fatalf("downstream speed wrong: %+v", downstream)
    }
}

func TestMapPropagationReportsUncoveredRegions(t *testing.T) {
    installations, batches, _ := propagationFixture()
    result := MapPropagation(installations, batches, 100*time.Millisecond)
    expected := []UncoveredRegion{{From: 0, To: 1}, {From: 1, To: 3}, {From: 3, To: 6}}
    if len(result.Uncovered) != len(expected) {
        t.Fatalf("uncovered regions wrong: %+v", result.Uncovered)
    }
    for index, region := range expected {
        if result.Uncovered[index] != region {
            t.Fatalf("uncovered region %d wrong: %+v", index, result.Uncovered)
        }
    }
}

func TestMapPropagationKeepsUnconfirmableBatchesUnknown(t *testing.T) {
    base := time.Unix(1700000000, 0).UTC()
    installations := []Installation{{SourceID: "probe-a", Position: 1}}
    batches := []SampleBatch{
        {ID: "unconfirmed", TimeBase: "ptp", Confirmed: false, Samples: []Sample{
            {Source: "probe-a", Height: 9, At: base},
        }},
        {ID: "no-time-base", TimeBase: "", Confirmed: true, Samples: []Sample{
            {Source: "probe-a", Height: 9, At: base},
        }},
        {ID: "mapped", TimeBase: "ptp", Confirmed: true, Samples: []Sample{
            {Source: "probe-a", Height: 0.2, At: base},
        }},
    }
    result := MapPropagation(installations, batches, 100*time.Millisecond)
    if len(result.Unknown) != 2 || result.Unknown[0] != "unconfirmed" || result.Unknown[1] != "no-time-base" {
        t.Fatalf("unconfirmable batches not kept unknown: %+v", result.Unknown)
    }
    if len(result.Mapped) != 1 || result.Mapped[0].Batch != "mapped" {
        t.Fatalf("unknown batches leaked into mapped heights: %+v", result.Mapped)
    }
}

func TestMapPropagationDropsSamplesOutsideWindowOrWithoutPosition(t *testing.T) {
    base := time.Unix(1700000000, 0).UTC()
    installations := []Installation{{SourceID: "probe-a", Position: 1}}
    batches := []SampleBatch{
        {ID: "b1", TimeBase: "ptp", Confirmed: true, Samples: []Sample{
            {Source: "probe-a", Height: 0.2, At: base},
            {Source: "probe-a", Height: 0.9, At: base.Add(500 * time.Millisecond)},
            {Source: "probe-x", Height: 0.9, At: base},
            {Source: "probe-a", Height: 0.9},
        }},
    }
    result := MapPropagation(installations, batches, 100*time.Millisecond)
    if len(result.Mapped) != 1 || result.Mapped[0].Height != 0.2 {
        t.Fatalf("unverifiable samples mapped: %+v", result.Mapped)
    }
}

func TestMapPropagationHandlesEmptyInput(t *testing.T) {
    result := MapPropagation(nil, nil, time.Second)
    if len(result.Mapped) != 0 || len(result.Delays) != 0 || len(result.Uncovered) != 0 || len(result.Unknown) != 0 {
        t.Fatalf("empty input produced output: %+v", result)
    }
    installations := []Installation{{SourceID: "probe-a", Position: 2}}
    result = MapPropagation(installations, nil, time.Second)
    if len(result.Uncovered) != 1 || result.Uncovered[0] != (UncoveredRegion{From: 0, To: 2}) {
        t.Fatalf("installations without batches wrong: %+v", result)
    }
    if len(result.Mapped) != 0 || len(result.Delays) != 0 {
        t.Fatalf("installations without batches mapped: %+v", result)
    }
}

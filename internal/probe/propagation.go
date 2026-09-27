package probe

import (
    "math"
    "sort"
    "time"
)

// Installation records where a probe is mounted along the tank as a
// distance in meters from the wave maker.
type Installation struct {
    SourceID string  `json:"source_id"`
    Position float64 `json:"position"`
}

// SampleBatch groups samples acquired against a shared time reference.
// Confirmed reports whether that reference has been verified; a batch
// whose time base cannot be confirmed is kept unknown and its samples
// are never mapped to tank positions.
type SampleBatch struct {
    ID        string   `json:"id"`
    TimeBase  string   `json:"time_base"`
    Confirmed bool     `json:"confirmed"`
    Samples   []Sample `json:"samples"`
}

// PositionedHeight is a wave height verified against the batch time
// window and mapped to the probe's position along the tank.
type PositionedHeight struct {
    Batch    string    `json:"batch"`
    SourceID string    `json:"source_id"`
    Position float64   `json:"position"`
    Height   float64   `json:"height"`
    At       time.Time `json:"at"`
}

// PropagationDelay describes the travel time of a crest between two
// installed probes. Speed is meters per second and is reported only
// when the delay is positive.
type PropagationDelay struct {
    FromSource   string        `json:"from_source"`
    ToSource     string        `json:"to_source"`
    FromPosition float64       `json:"from_position"`
    ToPosition   float64       `json:"to_position"`
    Delay        time.Duration `json:"delay"`
    Speed        float64       `json:"speed"`
}

// UncoveredRegion is a stretch of tank with no installed probe,
// expressed in meters from the wave maker.
type UncoveredRegion struct {
    From float64 `json:"from"`
    To   float64 `json:"to"`
}

// Propagation maps window-verified wave heights to tank positions and
// summarizes how the wave travels along the tank.
type Propagation struct {
    Mapped    []PositionedHeight `json:"mapped"`
    Delays    []PropagationDelay `json:"delays"`
    Uncovered []UncoveredRegion  `json:"uncovered"`
    Unknown   []string           `json:"unknown"`
}

// confirmable reports whether the batch time base can be trusted: the
// acquirer must have confirmed it and named the reference clock.
func (b SampleBatch) confirmable() bool {
    return b.Confirmed && b.TimeBase != ""
}

// MapPropagation maps wave heights to tank positions for batches whose
// time base is confirmable. Within a confirmed batch, only samples
// falling inside window of the batch's first sample are treated as
// simultaneous; later samples are left unmapped. Batches whose time
// base cannot be confirmed are reported as unknown and contribute
// nothing. Uncovered regions are derived from the installed probe
// positions alone; the stretch downstream of the last probe cannot be
// reported because the tank length is not known here.
func MapPropagation(installations []Installation, batches []SampleBatch, window time.Duration) Propagation {
    positions := make(map[string]float64, len(installations))
    for _, installation := range installations {
        if installation.SourceID == "" || math.IsNaN(installation.Position) || math.IsInf(installation.Position, 0) {
            continue
        }
        positions[installation.SourceID] = installation.Position
    }
    result := Propagation{}
    crests := make(map[string]time.Time)
    crestHeights := make(map[string]float64)
    for _, batch := range batches {
        if !batch.confirmable() {
            result.Unknown = append(result.Unknown, batch.ID)
            continue
        }
        reference, ok := batchReference(batch.Samples)
        if !ok {
            continue
        }
        for _, sample := range batch.Samples {
            position, installed := positions[sample.Source]
            if !installed || sample.At.IsZero() || sample.At.Sub(reference) > window {
                continue
            }
            result.Mapped = append(result.Mapped, PositionedHeight{
                Batch:    batch.ID,
                SourceID: sample.Source,
                Position: position,
                Height:   sample.Height,
                At:       sample.At,
            })
            height, seen := crestHeights[sample.Source]
            if !seen || sample.Height > height || (sample.Height == height && sample.At.Before(crests[sample.Source])) {
                crestHeights[sample.Source] = sample.Height
                crests[sample.Source] = sample.At
            }
        }
    }
    sort.SliceStable(result.Mapped, func(i int, j int) bool {
        if result.Mapped[i].Position != result.Mapped[j].Position {
            return result.Mapped[i].Position < result.Mapped[j].Position
        }
        return result.Mapped[i].At.Before(result.Mapped[j].At)
    })
    result.Delays = propagationDelays(positions, crests)
    result.Uncovered = uncoveredRegions(positions)
    return result
}

// batchReference anchors a batch at its earliest timestamped sample so
// that the same-window check has a concrete instant to compare against.
func batchReference(samples []Sample) (time.Time, bool) {
    var reference time.Time
    for _, sample := range samples {
        if sample.At.IsZero() {
            continue
        }
        if reference.IsZero() || sample.At.Before(reference) {
            reference = sample.At
        }
    }
    return reference, !reference.IsZero()
}

// propagationDelays chains the probes that observed a crest, ordered by
// position, and reports the crest travel time between neighbours.
func propagationDelays(positions map[string]float64, crests map[string]time.Time) []PropagationDelay {
    type installedProbe struct {
        source   string
        position float64
    }
    observed := make([]installedProbe, 0, len(positions))
    for source, position := range positions {
        if _, ok := crests[source]; !ok {
            continue
        }
        observed = append(observed, installedProbe{source: source, position: position})
    }
    sort.SliceStable(observed, func(i int, j int) bool {
        if observed[i].position != observed[j].position {
            return observed[i].position < observed[j].position
        }
        return observed[i].source < observed[j].source
    })
    delays := make([]PropagationDelay, 0, len(observed))
    for index := 1; index < len(observed); index++ {
        from := observed[index-1]
        to := observed[index]
        delay := crests[to.source].Sub(crests[from.source])
        entry := PropagationDelay{
            FromSource:   from.source,
            ToSource:     to.source,
            FromPosition: from.position,
            ToPosition:   to.position,
            Delay:        delay,
        }
        if distance := to.position - from.position; delay > 0 && distance > 0 {
            entry.Speed = distance / delay.Seconds()
        }
        delays = append(delays, entry)
    }
    return delays
}

// uncoveredRegions reports the stretches between the wave maker and the
// installed probes that no probe observes directly.
func uncoveredRegions(positions map[string]float64) []UncoveredRegion {
    ordered := make([]float64, 0, len(positions))
    for _, position := range positions {
        ordered = append(ordered, position)
    }
    sort.Float64s(ordered)
    regions := make([]UncoveredRegion, 0, len(ordered))
    coveredFrom := 0.0
    for _, position := range ordered {
        if position <= coveredFrom {
            continue
        }
        regions = append(regions, UncoveredRegion{From: coveredFrom, To: position})
        coveredFrom = position
    }
    return regions
}

package probe

import (
    "sort"
    "time"

    "github.com/wyw14/solo-037/internal/model"
)

// Installation records where one probe is mounted along the flume, in meters
// from the wave-maker datum.
type Installation struct {
    SourceID string  `json:"source_id"`
    Position float64 `json:"position"`
}

// Layout describes the probe installation coordinates for a run. FlumeLength
// is optional; when set it bounds the uncovered region beyond the last probe.
type Layout struct {
    Probes      []Installation `json:"probes,omitempty"`
    FlumeLength float64        `json:"flume_length,omitempty"`
}

// TimeBase states whether the samples of a batch share a confirmed common
// clock. Batches default to TimeBaseUnknown: only confirmed batches may be
// mapped onto flume positions, everything else stays unknown.
type TimeBase string

const (
    TimeBaseUnknown   TimeBase = "unknown"
    TimeBaseConfirmed TimeBase = "confirmed"
)

// PositionHeight maps one wave height sample to its probe position.
type PositionHeight struct {
    Position float64   `json:"position"`
    SourceID string    `json:"source_id"`
    Height   float64   `json:"height"`
    At       time.Time `json:"at"`
}

// PropagationWindow is one confirmed batch: wave heights that share a
// verifiable common time base, mapped to flume positions.
type PropagationWindow struct {
    Batch  string           `json:"batch"`
    First  time.Time        `json:"first"`
    Last   time.Time        `json:"last"`
    Points []PositionHeight `json:"points"`
}

// PropagationDelay is the crest arrival delay between two adjacent probe
// positions, derived from each probe's maximum-height sample across the
// confirmed batches.
type PropagationDelay struct {
    FromPosition float64       `json:"from_position"`
    ToPosition   float64       `json:"to_position"`
    Distance     float64       `json:"distance"`
    Delay        time.Duration `json:"delay"`
}

// UncoveredRegion is a stretch of flume without any installed probe.
type UncoveredRegion struct {
    Start float64 `json:"start"`
    End   float64 `json:"end"`
}

// Propagation maps confirmed-batch wave heights onto flume positions and
// reports crest delays between adjacent probes. Batches whose time base
// cannot be confirmed stay unknown: they are listed, never mapped.
type Propagation struct {
    Windows             []PropagationWindow `json:"windows"`
    Delays              []PropagationDelay  `json:"delays"`
    Uncovered           []UncoveredRegion   `json:"uncovered"`
    UnknownBatches      []string            `json:"unknown_batches"`
    UnmappedSources     []string            `json:"unmapped_sources"`
    SamplesWithoutBatch int                 `json:"samples_without_batch"`
}

// AnalyzePropagation is self-contained: it only needs the recorded samples,
// the probe layout and the per-batch time base confirmations.
func AnalyzePropagation(samples []model.Evidence, layout Layout, timeBases map[string]TimeBase) Propagation {
    result := Propagation{
        Windows:         []PropagationWindow{},
        Delays:          []PropagationDelay{},
        Uncovered:       []UncoveredRegion{},
        UnknownBatches:  []string{},
        UnmappedSources: []string{},
    }
    positions := make(map[string]float64, len(layout.Probes))
    for _, installation := range layout.Probes {
        positions[installation.SourceID] = installation.Position
    }

    type batchGroup struct {
        points []PositionHeight
        first  time.Time
        last   time.Time
    }
    batches := make(map[string]*batchGroup)
    unknown := make(map[string]struct{})
    unmapped := make(map[string]struct{})
    crest := make(map[string]model.Evidence)

    for _, sample := range samples {
        if sample.Kind != model.EvidenceWaveTime {
            continue
        }
        if sample.Batch == "" {
            result.SamplesWithoutBatch++
            continue
        }
        if timeBases[sample.Batch] != TimeBaseConfirmed {
            unknown[sample.Batch] = struct{}{}
            continue
        }
        position, ok := positions[sample.SourceID]
        if !ok {
            unmapped[sample.SourceID] = struct{}{}
            continue
        }
        group := batches[sample.Batch]
        if group == nil {
            group = &batchGroup{}
            batches[sample.Batch] = group
        }
        group.points = append(group.points, PositionHeight{Position: position, SourceID: sample.SourceID, Height: sample.Value, At: sample.RecordedAt})
        if group.first.IsZero() || sample.RecordedAt.Before(group.first) {
            group.first = sample.RecordedAt
        }
        if sample.RecordedAt.After(group.last) {
            group.last = sample.RecordedAt
        }
        if current, ok := crest[sample.SourceID]; !ok || sample.Value > current.Value || (sample.Value == current.Value && sample.RecordedAt.Before(current.RecordedAt)) {
            crest[sample.SourceID] = sample
        }
    }

    batchIDs := make([]string, 0, len(batches))
    for id := range batches {
        batchIDs = append(batchIDs, id)
    }
    sort.Strings(batchIDs)
    for _, id := range batchIDs {
        group := batches[id]
        sort.SliceStable(group.points, func(i, j int) bool {
            if group.points[i].Position != group.points[j].Position {
                return group.points[i].Position < group.points[j].Position
            }
            if !group.points[i].At.Equal(group.points[j].At) {
                return group.points[i].At.Before(group.points[j].At)
            }
            return group.points[i].SourceID < group.points[j].SourceID
        })
        result.Windows = append(result.Windows, PropagationWindow{Batch: id, First: group.first, Last: group.last, Points: group.points})
    }
    sort.SliceStable(result.Windows, func(i, j int) bool {
        if !result.Windows[i].First.Equal(result.Windows[j].First) {
            return result.Windows[i].First.Before(result.Windows[j].First)
        }
        return result.Windows[i].Batch < result.Windows[j].Batch
    })

    for id := range unknown {
        result.UnknownBatches = append(result.UnknownBatches, id)
    }
    sort.Strings(result.UnknownBatches)
    for source := range unmapped {
        result.UnmappedSources = append(result.UnmappedSources, source)
    }
    sort.Strings(result.UnmappedSources)

    // Crest delays between adjacent probe positions, from the confirmed
    // samples only. Probes sharing a position carry no propagation signal.
    crested := make([]model.Evidence, 0, len(crest))
    for _, sample := range crest {
        crested = append(crested, sample)
    }
    sort.SliceStable(crested, func(i, j int) bool {
        if positions[crested[i].SourceID] != positions[crested[j].SourceID] {
            return positions[crested[i].SourceID] < positions[crested[j].SourceID]
        }
        return crested[i].SourceID < crested[j].SourceID
    })
    for index := 1; index < len(crested); index++ {
        from := positions[crested[index-1].SourceID]
        to := positions[crested[index].SourceID]
        if to == from {
            continue
        }
        result.Delays = append(result.Delays, PropagationDelay{
            FromPosition: from,
            ToPosition:   to,
            Distance:     to - from,
            Delay:        crested[index].RecordedAt.Sub(crested[index-1].RecordedAt),
        })
    }

    result.Uncovered = uncoverRegions(layout)
    return result
}

// uncoverRegions reports the flume stretches without any probe: before the
// first probe, between adjacent probe positions, and past the last probe
// when the flume length is known.
func uncoverRegions(layout Layout) []UncoveredRegion {
    positions := make([]float64, 0, len(layout.Probes))
    seen := make(map[float64]struct{}, len(layout.Probes))
    for _, installation := range layout.Probes {
        if _, ok := seen[installation.Position]; !ok {
            seen[installation.Position] = struct{}{}
            positions = append(positions, installation.Position)
        }
    }
    sort.Float64s(positions)
    regions := []UncoveredRegion{}
    if len(positions) == 0 {
        if layout.FlumeLength > 0 {
            regions = append(regions, UncoveredRegion{Start: 0, End: layout.FlumeLength})
        }
        return regions
    }
    if positions[0] > 0 {
        regions = append(regions, UncoveredRegion{Start: 0, End: positions[0]})
    }
    for index := 1; index < len(positions); index++ {
        if positions[index] > positions[index-1] {
            regions = append(regions, UncoveredRegion{Start: positions[index-1], End: positions[index]})
        }
    }
    if last := positions[len(positions)-1]; layout.FlumeLength > last {
        regions = append(regions, UncoveredRegion{Start: last, End: layout.FlumeLength})
    }
    return regions
}

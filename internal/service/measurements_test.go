package service

import (
	"math"
	"testing"
	"time"

	"github.com/wyw14/solo-037/internal/model"
	"github.com/wyw14/solo-037/internal/probe"
)

func TestOperatorMeasurementsPersistAndAnalyzeSuppliedValues(t *testing.T) {
	directory := t.TempDir()
	a, err := OpenMeasurementArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Create("test A", "hull X", "section-a", "carriage-1")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute).UTC()
	for _, input := range []MeasurementInput{
		{Kind: model.EvidenceWaveTime, SourceID: "left", Value: 0.1, RecordedAt: at},
		{Kind: model.EvidenceWaveTime, SourceID: "right", Value: 0.3, RecordedAt: at.Add(time.Second)},
		{Kind: model.EvidenceForce, SourceID: "load-cell", Value: 12, RecordedAt: at},
	} {
		if _, err := a.Record(run.Operation.ID, input); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := OpenMeasurementArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := loaded.Analyze(run.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Wave.Count != 2 || math.Abs(analysis.Wave.Mean-0.2) > 1e-9 || analysis.ForceCount != 1 || analysis.ForceMean != 12 {
		t.Fatalf("analysis did not reflect supplied values after reload: %+v", analysis)
	}
	if len(loaded.List()) != 1 || len(loaded.List()[0].Evidence) != 3 || !loaded.List()[0].Evidence[0].Durable {
		t.Fatalf("persisted evidence missing: %+v", loaded.List())
	}
}

func TestOperatorMeasurementRejectsInvalidAndUnknownRun(t *testing.T) {
	a, err := OpenMeasurementArchive(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Create("test B", "hull X", "section-a", "carriage-1")
	if err != nil {
		t.Fatal(err)
	}
	input := MeasurementInput{Kind: model.EvidenceWaveTime, SourceID: "left", Value: 1, RecordedAt: time.Now().UTC()}
	if _, err := a.Record("missing", input); err == nil {
		t.Fatal("unknown run accepted")
	}
	input.Value = math.NaN()
	if _, err := a.Record(run.Operation.ID, input); err == nil {
		t.Fatal("NaN accepted")
	}
	if len(a.List()[0].Evidence) != 0 {
		t.Fatal("rejected values changed archive")
	}
}

func TestProbeLayoutAndTimeBasesPersistAndDrivePropagation(t *testing.T) {
	directory := t.TempDir()
	a, err := OpenMeasurementArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Create("tow C", "hull Y", "section-a", "carriage-1")
	if err != nil {
		t.Fatal(err)
	}
	id := run.Operation.ID
	layout := probe.Layout{FlumeLength: 10, Probes: []probe.Installation{
		{SourceID: "probe-near", Position: 1},
		{SourceID: "probe-far", Position: 4},
	}}
	if _, err := a.SetProbeLayout(id, layout); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetBatchTimeBase(id, "b1", probe.TimeBaseConfirmed); err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	for _, input := range []MeasurementInput{
		{Kind: model.EvidenceWaveTime, SourceID: "probe-near", Value: 0.4, RecordedAt: base, Batch: "b1"},
		{Kind: model.EvidenceWaveTime, SourceID: "probe-far", Value: 0.3, RecordedAt: base.Add(1500 * time.Millisecond), Batch: "b1"},
		{Kind: model.EvidenceWaveTime, SourceID: "probe-far", Value: 0.2, RecordedAt: base, Batch: "b-drifted"},
	} {
		if _, err := a.Record(id, input); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := OpenMeasurementArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := loaded.Analyze(id)
	if err != nil {
		t.Fatal(err)
	}
	propagation := analysis.Propagation
	if len(propagation.Windows) != 1 || propagation.Windows[0].Batch != "b1" || len(propagation.Windows[0].Points) != 2 {
		t.Fatalf("confirmed batch not mapped after reload: %+v", propagation.Windows)
	}
	if propagation.Windows[0].Points[0].Position != 1 || propagation.Windows[0].Points[1].Position != 4 {
		t.Fatalf("heights not mapped to probe positions: %+v", propagation.Windows[0].Points)
	}
	if len(propagation.Delays) != 1 || propagation.Delays[0].Delay != 1500*time.Millisecond || propagation.Delays[0].Distance != 3 {
		t.Fatalf("unexpected propagation delay: %+v", propagation.Delays)
	}
	if len(propagation.UnknownBatches) != 1 || propagation.UnknownBatches[0] != "b-drifted" {
		t.Fatalf("unconfirmed batch must stay unknown: %+v", propagation.UnknownBatches)
	}
	wantUncovered := []probe.UncoveredRegion{{Start: 0, End: 1}, {Start: 1, End: 4}, {Start: 4, End: 10}}
	if len(propagation.Uncovered) != len(wantUncovered) {
		t.Fatalf("uncovered regions: got %+v want %+v", propagation.Uncovered, wantUncovered)
	}
	for index := range wantUncovered {
		if propagation.Uncovered[index] != wantUncovered[index] {
			t.Fatalf("uncovered regions: got %+v want %+v", propagation.Uncovered, wantUncovered)
		}
	}
}

func TestProbeLayoutAndTimeBaseValidation(t *testing.T) {
	a, err := OpenMeasurementArchive(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Create("tow D", "hull Y", "section-a", "carriage-1")
	if err != nil {
		t.Fatal(err)
	}
	id := run.Operation.ID
	if _, err := a.SetProbeLayout("missing", probe.Layout{}); err == nil {
		t.Fatal("unknown run accepted")
	}
	if _, err := a.SetProbeLayout(id, probe.Layout{Probes: []probe.Installation{{SourceID: "p", Position: -1}}}); err == nil {
		t.Fatal("negative position accepted")
	}
	if _, err := a.SetProbeLayout(id, probe.Layout{Probes: []probe.Installation{{SourceID: "p", Position: 1}, {SourceID: "p", Position: 2}}}); err == nil {
		t.Fatal("duplicate probe source accepted")
	}
	if _, err := a.SetProbeLayout(id, probe.Layout{FlumeLength: 2, Probes: []probe.Installation{{SourceID: "p", Position: 3}}}); err == nil {
		t.Fatal("probe beyond flume length accepted")
	}
	if _, err := a.SetBatchTimeBase(id, "b1", probe.TimeBase("maybe")); err == nil {
		t.Fatal("unrecognized time base accepted")
	}
	if _, err := a.SetBatchTimeBase(id, " ", probe.TimeBaseConfirmed); err == nil {
		t.Fatal("blank batch accepted")
	}
	if got := a.List()[0]; len(got.Layout.Probes) != 0 || len(got.TimeBases) != 0 {
		t.Fatalf("rejected input changed archive: %+v", got)
	}
}

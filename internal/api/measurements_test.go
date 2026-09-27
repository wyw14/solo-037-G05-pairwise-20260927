package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wyw14/solo-037/internal/service"
)

func TestMeasuredRunHTTPUsesOperatorEvidence(t *testing.T) {
	runtime, err := service.OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := NewRouter(runtime).Handler()
	created := httptest.NewRecorder()
	server.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/api/measured-runs", strings.NewReader(`{"name":"tow A","model_name":"hull A","section":"section-a","carriage":"carriage-1"}`)))
	if created.Code != http.StatusOK {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	var response struct {
		Data service.MeasuredRun `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	id := response.Data.Operation.ID
	if id == "" {
		t.Fatal("missing ID")
	}
	stamp := time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	input := `{"kind":"wave_time","source_id":"probe-a","value":0.24,"recorded_at":"` + stamp + `"}`
	recorded := httptest.NewRecorder()
	server.ServeHTTP(recorded, httptest.NewRequest(http.MethodPost, "/api/measured-runs/"+string(id)+"/evidence", strings.NewReader(input)))
	if recorded.Code != http.StatusOK {
		t.Fatalf("record: %d %s", recorded.Code, recorded.Body)
	}
	analysis := httptest.NewRecorder()
	server.ServeHTTP(analysis, httptest.NewRequest(http.MethodGet, "/api/measured-runs/"+string(id)+"/analysis", nil))
	if analysis.Code != http.StatusOK || !strings.Contains(analysis.Body.String(), `"mean":0.24`) {
		t.Fatalf("analysis: %d %s", analysis.Code, analysis.Body)
	}
	reloaded, err := service.OpenRuntime(t.TempDir())
	if err != nil || len(reloaded.MeasuredRuns()) != 0 {
		t.Fatal("archive separation failed", err)
	}
}

func TestMeasuredRunPropagationOverHTTP(t *testing.T) {
	runtime, err := service.OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := NewRouter(runtime).Handler()
	created := httptest.NewRecorder()
	server.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/api/measured-runs", strings.NewReader(`{"name":"tow B","model_name":"hull B","section":"section-a","carriage":"carriage-1"}`)))
	if created.Code != http.StatusOK {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	var response struct {
		Data service.MeasuredRun `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	id := string(response.Data.Operation.ID)

	layout := httptest.NewRecorder()
	server.ServeHTTP(layout, httptest.NewRequest(http.MethodPut, "/api/measured-runs/"+id+"/probes", strings.NewReader(`{"flume_length":10,"probes":[{"source_id":"probe-near","position":1},{"source_id":"probe-far","position":4}]}`)))
	if layout.Code != http.StatusOK {
		t.Fatalf("layout: %d %s", layout.Code, layout.Body)
	}
	confirm := httptest.NewRecorder()
	server.ServeHTTP(confirm, httptest.NewRequest(http.MethodPost, "/api/measured-runs/"+id+"/batches", strings.NewReader(`{"batch":"b1","time_base":"confirmed"}`)))
	if confirm.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", confirm.Code, confirm.Body)
	}

	near := time.Now().Add(-time.Second).UTC()
	for _, sample := range []map[string]any{
		{"kind": "wave_time", "source_id": "probe-near", "value": 0.4, "recorded_at": near.Format(time.RFC3339Nano), "batch": "b1"},
		{"kind": "wave_time", "source_id": "probe-far", "value": 0.3, "recorded_at": near.Add(1500 * time.Millisecond).Format(time.RFC3339Nano), "batch": "b1"},
		{"kind": "wave_time", "source_id": "probe-far", "value": 0.2, "recorded_at": near.Format(time.RFC3339Nano), "batch": "b-drifted"},
	} {
		body, err := json.Marshal(sample)
		if err != nil {
			t.Fatal(err)
		}
		recorded := httptest.NewRecorder()
		server.ServeHTTP(recorded, httptest.NewRequest(http.MethodPost, "/api/measured-runs/"+id+"/evidence", strings.NewReader(string(body))))
		if recorded.Code != http.StatusOK {
			t.Fatalf("record: %d %s", recorded.Code, recorded.Body)
		}
	}

	analysis := httptest.NewRecorder()
	server.ServeHTTP(analysis, httptest.NewRequest(http.MethodGet, "/api/measured-runs/"+id+"/analysis", nil))
	if analysis.Code != http.StatusOK {
		t.Fatalf("analysis: %d %s", analysis.Code, analysis.Body)
	}
	var parsed struct {
		Data service.MeasurementAnalysis `json:"data"`
	}
	if err := json.Unmarshal(analysis.Body.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	propagation := parsed.Data.Propagation
	if len(propagation.Windows) != 1 || len(propagation.Windows[0].Points) != 2 {
		t.Fatalf("confirmed batch not mapped: %+v", propagation.Windows)
	}
	if propagation.Windows[0].Points[0].Position != 1 || propagation.Windows[0].Points[1].Position != 4 {
		t.Fatalf("heights not mapped to positions: %+v", propagation.Windows[0].Points)
	}
	if len(propagation.Delays) != 1 || propagation.Delays[0].Delay != 1500*time.Millisecond {
		t.Fatalf("unexpected delay: %+v", propagation.Delays)
	}
	if len(propagation.UnknownBatches) != 1 || propagation.UnknownBatches[0] != "b-drifted" {
		t.Fatalf("unconfirmed batch must stay unknown: %+v", propagation.UnknownBatches)
	}
	if len(propagation.Uncovered) != 3 {
		t.Fatalf("uncovered regions missing: %+v", propagation.Uncovered)
	}

	rejected := httptest.NewRecorder()
	server.ServeHTTP(rejected, httptest.NewRequest(http.MethodPost, "/api/measured-runs/"+id+"/batches", strings.NewReader(`{"batch":"b2","time_base":"maybe"}`)))
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("invalid time base accepted: %d %s", rejected.Code, rejected.Body)
	}
}

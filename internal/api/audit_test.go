package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wyw14/solo-037/internal/service"
)

func TestAuditEndpointListsRunLedgers(t *testing.T) {
	runtime, err := service.OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	NewRouter(runtime).Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/audit", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	var response struct {
		Data struct {
			Journal struct {
				Runs []struct {
					Run        string `json:"run"`
					Status     string `json:"status"`
					Categories []struct {
						Category string `json:"category"`
						Status   string `json:"status"`
						Expected int    `json:"expected"`
						OnDisk   int    `json:"on_disk"`
					} `json:"categories"`
				} `json:"runs"`
			} `json:"journal"`
			OperatorArchive struct {
				ArchivePath string `json:"archive_path"`
			} `json:"operator_archive"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data.Journal.Runs) == 0 {
		t.Fatal("expected the commissioning scenario run in the audit view")
	}
	verified := false
	for _, run := range response.Data.Journal.Runs {
		if run.Status != "verified" {
			t.Fatalf("run %s status = %s, want verified", run.Run, run.Status)
		}
		for _, category := range run.Categories {
			if category.Category == "wave" && category.OnDisk >= category.Expected && category.Expected > 0 {
				verified = true
			}
			if category.Status != "verified" {
				t.Fatalf("category %s = %s", category.Category, category.Status)
			}
		}
	}
	if !verified {
		t.Fatal("no run showed durable wave frames")
	}
}

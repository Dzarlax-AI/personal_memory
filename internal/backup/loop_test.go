package backup

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

func TestDoBackupSurfacesSnapshotErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    string
		keep    int
	}{
		{
			name: "create", want: "create snapshot", keep: 1,
			handler: func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusBadGateway) },
		},
		{
			name: "list", want: "list snapshots", keep: 1,
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					_, _ = w.Write([]byte(`{"status":"ok","result":{"name":"new"}}`))
					return
				}
				http.Error(w, "down", http.StatusServiceUnavailable)
			},
		},
		{
			name: "delete", want: `delete snapshot "001-old"`, keep: 1,
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodPost:
					_, _ = w.Write([]byte(`{"status":"ok","result":{"name":"new"}}`))
				case http.MethodGet:
					_, _ = w.Write([]byte(`{"result":[{"name":"001-old"},{"name":"002-new"}]}`))
				case http.MethodDelete:
					http.Error(w, "cannot delete", http.StatusInternalServerError)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(tt.handler)
			defer ts.Close()
			loop := NewLoop(qdrant.NewClient(ts.URL, "memory"), time.Hour, tt.keep)
			err := loop.DoBackup(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("DoBackup error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

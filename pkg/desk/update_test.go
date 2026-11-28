package desk

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/app"
	"github.com/Metixpro/macron-agent/pkg/updater"
)

func TestUpdateCheckCannotOverwriteInstallStatus(t *testing.T) {
	s := &Server{}
	s.upd.rel = &updater.Release{TagName: "v" + app.Version}
	started, release := make(chan struct{}), make(chan struct{})
	result := make(chan UpdateStatus, 1)
	go func() {
		result <- s.checkUpdateWith(context.Background(), true, func(context.Context, string) (*updater.Release, error) {
			close(started)
			<-release
			return &updater.Release{TagName: "v99.0.0"}, nil
		})
	}()
	<-started
	rec := httptest.NewRecorder()
	s.updateInstallAPI(rec, httptest.NewRequest(http.MethodPost, "/api/update/install", nil))
	close(release)
	got := <-result
	if rec.Code != http.StatusOK || got.Available || got.Latest != app.Version {
		t.Fatalf("completed install status replaced by a stale check: HTTP %d, %+v", rec.Code, got)
	}
}

func TestInstalledUpdateRemainsReadyToRestart(t *testing.T) {
	s := &Server{}
	s.upd.status = UpdateStatus{Installed: true, Phase: "done", Percent: 100}
	got := s.checkUpdateWith(context.Background(), true, func(context.Context, string) (*updater.Release, error) {
		t.Fatal("checked for updates while waiting for restart")
		return nil, nil
	})
	if !got.Installed || got.Phase != "done" || got.Percent != 100 {
		t.Fatalf("lost ready-to-restart state: %+v", got)
	}
}

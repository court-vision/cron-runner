package task

import "testing"

func TestJobNameFromEndpoint(t *testing.T) {
	cases := map[string]string{
		"/v1/internal/pipelines/pre-game":                    "pre-game",
		"/v1/internal/pipelines/game-start-times?source=cdn": "game-start-times",
		"/v1/internal/pipelines/deploy/":                     "deploy",
		"/v1/internal/pipelines/playoffs#frag":               "playoffs",
		"live-stats":                                         "live-stats",
	}
	for in, want := range cases {
		if got := jobNameFromEndpoint(in); got != want {
			t.Errorf("jobNameFromEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReportNamePrefersJobName(t *testing.T) {
	tt := &TriggerTask{Endpoint: "/v1/internal/pipelines/game-start-times?source=cdn"}
	if got := tt.ReportName(); got != "game-start-times" {
		t.Errorf("ReportName() without JobName = %q, want %q", got, "game-start-times")
	}
	tt.JobName = "schedule-sync"
	if got := tt.ReportName(); got != "schedule-sync" {
		t.Errorf("ReportName() with JobName = %q, want %q", got, "schedule-sync")
	}
}

package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/httpapi"
	"invoice-system/backend/internal/postgresstore"
)

func TestReadinessPhasedReportNeverInventsUnexecutedGates(t *testing.T) {
	module := newRuntime(appRuntime{AuthMode: "session", SourceMode: "agent"})
	body, err := json.Marshal(module.Readiness(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Checks map[string]struct {
			Status string `json:"status"`
		} `json:"checks"`
		NonFreshness string `json:"source_non_freshness_status"`
		Freshness    struct {
			Status  string   `json:"status"`
			Reasons []string `json:"reasons"`
		} `json:"source_freshness"`
	}
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Checks) != 11 || report.NonFreshness != "not_evaluated" || report.Freshness.Status != "not_evaluated" {
		t.Fatalf("unconfigured runtime must explicitly report all eleven gates as unexecuted: %s", body)
	}
	for _, name := range []string{"database", "admin_settings", "invoice_issuer", "clamav_daemon", "clamav_signatures", "pdf_scanner", "source_health_query", "source_ingest", "eligibility_health_query", "eligibility_projection", "source_streams"} {
		if report.Checks[name].Status != "not_evaluated" {
			t.Fatalf("gate %s was invented: %s", name, body)
		}
	}
}

func phaseSourceHealth(now time.Time) postgresstore.SourceReadinessHealth {
	health := healthySourceReadiness()
	for i := range health.Report.Items {
		item := &health.Report.Items[i]
		item.ApprovedRuntimeVersion, item.ObservedRuntimeVersion = "private-runtime-version", "private-runtime-version"
		item.SourceName = "private-source-name"
		item.ProjectionStatus = "healthy"
		item.LastAcceptedAt, item.EconomicWatermarkAt = now.Add(-time.Minute), now.Add(-time.Minute)
	}
	refreshPhaseSourceHealth(&health, now)
	return health
}

func refreshPhaseSourceHealth(health *postgresstore.SourceReadinessHealth, now time.Time) {
	policy := phaseSourcePolicy(now)
	health.Report.Ready = true
	for i := range health.Report.Items {
		postgresstore.EvaluateSourceStreamHealth(&health.Report.Items[i], policy)
		if health.Report.Items[i].SourceEnabled && !health.Report.Items[i].Ready {
			health.Report.Ready = false
		}
	}
}

func phaseSourcePolicy(now time.Time) postgresstore.SourceFreshnessPolicy {
	return postgresstore.SourceFreshnessPolicy{EconomicHeartbeatMaxAge: 5 * time.Minute, EconomicWatermarkMaxAge: 15 * time.Minute, IdentitiesMaxAge: 15 * time.Minute, EconomicRescanActivityMaxAge: 10 * time.Minute, Now: now}
}

func TestReadinessPhasedReportSeparatesOnlyActualSourceExpiry(t *testing.T) {
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name          string
		mutate        func(*postgresstore.SourceReadinessHealth)
		nonFresh      GateStatus
		fresh         GateStatus
		reasons       []string
		originalReady bool
	}{
		{"healthy", func(*postgresstore.SourceReadinessHealth) {}, GateReady, GateReady, []string{}, true},
		{"both expired", func(h *postgresstore.SourceReadinessHealth) {
			h.Report.Items[0].LastAcceptedAt = now.Add(-6 * time.Minute)
			h.Report.Items[0].EconomicWatermarkAt = now.Add(-16 * time.Minute)
			refreshPhaseSourceHealth(h, now)
		}, GateReady, GateNotReady, []string{sourceHeartbeatExpired, sourceWatermarkExpired}, false},
		{"both future", func(h *postgresstore.SourceReadinessHealth) {
			h.Report.Items[0].LastAcceptedAt = now.Add(6 * time.Minute)
			h.Report.Items[0].EconomicWatermarkAt = now.Add(6 * time.Minute)
			refreshPhaseSourceHealth(h, now)
		}, GateNotReady, GateNotReady, []string{sourceHeartbeatFuture, sourceWatermarkFuture}, false},
		{"both missing", func(h *postgresstore.SourceReadinessHealth) {
			h.Report.Items[0].LastAcceptedAt = time.Time{}
			h.Report.Items[0].EconomicWatermarkAt = time.Time{}
			refreshPhaseSourceHealth(h, now)
		}, GateNotReady, GateNotReady, []string{sourceHeartbeatMissing, sourceWatermarkMissing}, false},
		{"rescan keeps old readiness but watermark still expired", func(h *postgresstore.SourceReadinessHealth) {
			h.Report.Items[0].EconomicWatermarkAt = now.Add(-16 * time.Minute)
			h.Report.Items[0].ActiveRescanUpdatedAt = now.Add(-time.Minute)
			refreshPhaseSourceHealth(h, now)
		}, GateReady, GateNotReady, []string{sourceWatermarkExpired}, true},
		{"identity never needs a watermark", func(h *postgresstore.SourceReadinessHealth) {
			h.Report.Items[1].EconomicWatermarkAt = time.Time{}
			refreshPhaseSourceHealth(h, now)
		}, GateReady, GateReady, []string{}, true},
		{"bounded pending remains allowed", func(h *postgresstore.SourceReadinessHealth) {
			h.Ingest.Pending = 1
			h.Ingest.OldestPending = now.Add(-time.Minute)
			h.Report.Items[0].PendingEvents = 1
			refreshPhaseSourceHealth(h, now)
		}, GateReady, GateReady, []string{}, true},
		{"contained dead remains allowed", func(h *postgresstore.SourceReadinessHealth) {
			h.Ingest.Dead = 1
			h.Ingest.DeadContained = 1
			h.Report.Items[0].DeadEvents = 1
			h.Report.Items[0].ContainedDeadEvents = 1
			refreshPhaseSourceHealth(h, now)
		}, GateReady, GateReady, []string{}, true},
		{"later blocked cannot hide behind first stale stream", func(h *postgresstore.SourceReadinessHealth) {
			h.Report.Items[0].LastAcceptedAt = now.Add(-6 * time.Minute)
			h.Report.Items[9].ProjectionStatus = "blocked"
			refreshPhaseSourceHealth(h, now)
		}, GateNotReady, GateNotReady, []string{sourceHeartbeatExpired}, false},
		{"later dead cannot hide behind first stale stream", func(h *postgresstore.SourceReadinessHealth) {
			h.Report.Items[0].LastAcceptedAt = now.Add(-6 * time.Minute)
			h.Report.Items[9].DeadEvents = 1
			refreshPhaseSourceHealth(h, now)
		}, GateNotReady, GateNotReady, []string{sourceHeartbeatExpired}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			health := phaseSourceHealth(now)
			tc.mutate(&health)
			p := healthyReadinessProbe(now)
			p.sourcePolicy = phaseSourcePolicy(now)
			calls := 0
			p.sourceHealth = func(context.Context) (postgresstore.SourceReadinessHealth, error) { calls++; return health, nil }
			module := newRuntime(appRuntime{AuthMode: "session", SourceMode: "agent", readiness: p.evaluate, readinessWithReport: p.evaluateWithReport})
			report := module.Readiness(context.Background())
			if calls != 1 || report.Ready != tc.originalReady || report.SourceNonFreshnessStatus != tc.nonFresh || report.SourceFreshness.Status != tc.fresh || !reflect.DeepEqual(report.SourceFreshness.Reasons, tc.reasons) {
				t.Fatalf("calls=%d report=%+v want original=%t nonfresh=%s freshness=%s reasons=%v", calls, report, tc.originalReady, tc.nonFresh, tc.fresh, tc.reasons)
			}
			for _, name := range readinessGates[:10] {
				if report.Checks[name].Status != GateReady {
					t.Fatalf("prior gate %s not proven: %+v", name, report)
				}
			}
			wantLast := GateNotReady
			if tc.originalReady {
				wantLast = GateReady
			}
			if report.Checks[readinessCheckSourceStreams].Status != wantLast {
				t.Fatalf("original source-stream gate changed: %+v", report)
			}
			body, _ := json.Marshal(report)
			for _, private := range []string{"private-runtime-version", "private-source-name", "source_instance_id", "last_accepted_at"} {
				if strings.Contains(string(body), private) {
					t.Fatalf("public report leaked %s: %s", private, body)
				}
			}
		})
	}
}

func TestReadinessPhasedReportRejectsInconsistentAndIncompleteSourceEvidence(t *testing.T) {
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(*postgresstore.SourceReadinessHealth){
		"empty":                   func(h *postgresstore.SourceReadinessHealth) { h.Report.Items = nil; h.Report.Ready = false },
		"missing last stream":     func(h *postgresstore.SourceReadinessHealth) { h.Report.Items = h.Report.Items[:9] },
		"duplicate last stream":   func(h *postgresstore.SourceReadinessHealth) { h.Report.Items[9] = h.Report.Items[8] },
		"unsupported source":      func(h *postgresstore.SourceReadinessHealth) { h.Report.Items[9].SourceType = "unknown" },
		"missing source identity": func(h *postgresstore.SourceReadinessHealth) { h.Report.Items[9].SourceInstanceID = "" },
		"source identity changes type": func(h *postgresstore.SourceReadinessHealth) {
			h.Report.Items[9].SourceInstanceID = h.Report.Items[0].SourceInstanceID
		},
		"report flag inconsistent": func(h *postgresstore.SourceReadinessHealth) { h.Report.Ready = true },
		"item flag inconsistent":   func(h *postgresstore.SourceReadinessHealth) { h.Report.Items[0].Ready = true },
		"hidden blocked flag":      func(h *postgresstore.SourceReadinessHealth) { h.Report.Items[9].ProjectionStatus = "blocked" },
		"hidden version mismatch": func(h *postgresstore.SourceReadinessHealth) {
			h.Report.Items[9].ObservedRuntimeVersion = "other-private-version"
		},
		"hidden dead count":   func(h *postgresstore.SourceReadinessHealth) { h.Report.Items[9].DeadEvents = 1 },
		"invalid containment": func(h *postgresstore.SourceReadinessHealth) { h.Report.Items[9].ContainedDeadEvents = 1 },
		"negative pending":    func(h *postgresstore.SourceReadinessHealth) { h.Report.Items[9].PendingEvents = -1 },
		"unknown reason": func(h *postgresstore.SourceReadinessHealth) {
			h.Report.Items[0].Reasons = append(h.Report.Items[0].Reasons, "private-host.invalid:5432")
		},
		"duplicate reason": func(h *postgresstore.SourceReadinessHealth) {
			h.Report.Items[0].Reasons = append(h.Report.Items[0].Reasons, "STREAM_STALE")
		},
		"missing threshold": func(h *postgresstore.SourceReadinessHealth) { h.Report.Items[0].MaximumAgeSeconds = 0 },
		"missing enabled source type": func(h *postgresstore.SourceReadinessHealth) {
			for i := 5; i < len(h.Report.Items); i++ {
				h.Report.Items[i].SourceEnabled = false
			}
		},
		"invented rescan grace": func(h *postgresstore.SourceReadinessHealth) {
			h.Report.Items[9].Reasons = []string{"ECONOMIC_RESCAN_ACTIVE"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			health := phaseSourceHealth(now)
			health.Report.Items[0].LastAcceptedAt = now.Add(-6 * time.Minute)
			refreshPhaseSourceHealth(&health, now)
			mutate(&health)
			nonFresh, _ := sourceReadinessDiagnostics(health.Report, phaseSourcePolicy(now))
			if nonFresh == GateReady {
				t.Fatalf("expiry hid inconsistent source evidence: %+v", health.Report)
			}
		})
	}
}

func TestReadinessPhasedReportTimestampBoundariesUseOriginalPolicy(t *testing.T) {
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name           string
		offset, maxAge time.Duration
		reason         string
	}{
		{"at heartbeat expiry boundary", -5 * time.Minute, 5 * time.Minute, ""},
		{"one nanosecond expired", -5*time.Minute - time.Nanosecond, 5 * time.Minute, sourceHeartbeatExpired},
		{"at allowed future skew", 5 * time.Minute, 5 * time.Minute, ""},
		{"one nanosecond beyond future skew", 5*time.Minute + time.Nanosecond, 5 * time.Minute, sourceHeartbeatFuture},
		{"fractional policy must not be rounded down", -30200 * time.Millisecond, 30500 * time.Millisecond, ""},
		{"fractional policy exceeded", -30500*time.Millisecond - time.Nanosecond, 30500 * time.Millisecond, sourceHeartbeatExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			health := phaseSourceHealth(now)
			policy := phaseSourcePolicy(now)
			policy.EconomicHeartbeatMaxAge = tc.maxAge
			health.Report.Ready = true
			for i := range health.Report.Items {
				health.Report.Items[i].LastAcceptedAt = now
				if i == 0 {
					health.Report.Items[i].LastAcceptedAt = now.Add(tc.offset)
				}
				postgresstore.EvaluateSourceStreamHealth(&health.Report.Items[i], policy)
				health.Report.Ready = health.Report.Ready && health.Report.Items[i].Ready
			}
			nonFresh, fresh := sourceReadinessDiagnostics(health.Report, policy)
			want := []string{}
			wantStatus := GateReady
			wantNonFresh := GateReady
			if tc.reason != "" {
				want = []string{tc.reason}
				wantStatus = GateNotReady
			}
			if tc.reason == sourceHeartbeatFuture {
				wantNonFresh = GateNotReady
			}
			if nonFresh != wantNonFresh || fresh.Status != wantStatus || !reflect.DeepEqual(fresh.Reasons, want) {
				t.Fatalf("nonfresh=%s freshness=%+v want nonfresh=%s status=%s reasons=%v", nonFresh, fresh, wantNonFresh, wantStatus, want)
			}
		})
	}
}

func TestReadinessPhasedReportPreservesShortCircuitAndSubFailureNames(t *testing.T) {
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		failAt int
		mutate func(*readinessProbe)
	}{
		{"clamav", 3, func(p *readinessProbe) {
			p.pingClamAV = func(context.Context) error { return errors.New("private dial detail") }
			p.pingPDFScanner = func(context.Context) error { t.Fatal("later PDF query ran"); return nil }
			p.sourceHealth = func(context.Context) (postgresstore.SourceReadinessHealth, error) {
				t.Fatal("later source query ran")
				return postgresstore.SourceReadinessHealth{}, nil
			}
		}},
		{"source query", 6, func(p *readinessProbe) {
			p.sourceHealth = func(context.Context) (postgresstore.SourceReadinessHealth, error) {
				return postgresstore.SourceReadinessHealth{}, errors.New("private database detail")
			}
			p.eligibilityHealth = func(context.Context) (postgresstore.EligibilityProjectionHealth, error) {
				t.Fatal("later projection query ran")
				return postgresstore.EligibilityProjectionHealth{}, nil
			}
		}},
		{"ingest dead", 7, func(p *readinessProbe) {
			p.sourceHealth = func(context.Context) (postgresstore.SourceReadinessHealth, error) {
				h := phaseSourceHealth(now)
				h.Ingest.Dead = 1
				return h, nil
			}
		}},
		{"projection dead", 9, func(p *readinessProbe) {
			p.eligibilityHealth = func(context.Context) (postgresstore.EligibilityProjectionHealth, error) {
				return postgresstore.EligibilityProjectionHealth{Dead: 1}, nil
			}
		}},
		{"projection stuck", 9, func(p *readinessProbe) {
			p.eligibilityHealth = func(context.Context) (postgresstore.EligibilityProjectionHealth, error) {
				return postgresstore.EligibilityProjectionHealth{OldestPending: now.Add(-16 * time.Minute)}, nil
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := healthyReadinessProbe(now)
			tc.mutate(&p)
			_, report, err := p.evaluateWithReport(context.Background())
			if err == nil || report.SourceNonFreshnessStatus != GateNotEvaluated || report.SourceFreshness.Status != GateNotEvaluated {
				t.Fatalf("short circuit lost: %v %+v", err, report)
			}
			for i, name := range readinessGates {
				want := GateNotEvaluated
				if i < tc.failAt {
					want = GateReady
				}
				if i == tc.failAt {
					want = GateNotReady
				}
				if report.Checks[name].Status != want {
					t.Fatalf("gate=%s got=%s want=%s", name, report.Checks[name].Status, want)
				}
			}
		})
	}
}

func TestReadinessPhasedReportFiltersAllPublicDiagnosticVocabulary(t *testing.T) {
	for name, mutate := range map[string]func(*ReadinessReport){
		"unknown gate": func(r *ReadinessReport) {
			delete(r.Checks, "database")
			r.Checks["private-host.invalid:5432"] = GateReport{Status: GateReady}
		},
		"unknown status":          func(r *ReadinessReport) { r.Checks["database"] = GateReport{Status: "private-host.invalid:5432"} },
		"unknown nonfresh status": func(r *ReadinessReport) { r.SourceNonFreshnessStatus = "private-host.invalid:5432" },
		"unknown freshness reason": func(r *ReadinessReport) {
			r.SourceFreshness = SourceFreshnessReport{Status: GateNotReady, Reasons: []string{"private-host.invalid:5432"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			detailed := unevaluatedReadinessReport()
			mutate(&detailed)
			m := newRuntime(appRuntime{AuthMode: "session", SourceMode: "agent", readinessWithReport: func(context.Context) (httpapi.ReadinessOutcome, ReadinessReport, error) {
				return httpapi.ReadinessOutcome{}, detailed, nil
			}})
			got := m.Readiness(context.Background())
			body, _ := json.Marshal(got)
			if strings.Contains(string(body), "private") || got.SourceNonFreshnessStatus != GateNotEvaluated || len(got.Checks) != 11 {
				t.Fatalf("invalid public diagnostic escaped: %s", body)
			}
		})
	}
}

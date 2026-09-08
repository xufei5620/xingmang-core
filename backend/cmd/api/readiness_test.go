package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/adminsettings"
	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/httpapi"
	"invoice-system/backend/internal/postgresstore"
)

// healthySourceReadiness is the fixture every readinessProbe test starts from:
// a source health report that passes both validateSourceIngestRuntimeReadiness
// and validateSourceRuntimeReadiness. Its shape (two enabled source types,
// each carrying all five required streams, all Ready, report.Ready=true)
// mirrors the healthy fixtures in main_test.go's own validator tests.
func healthySourceReadiness() postgresstore.SourceReadinessHealth {
	report := postgresstore.SourceHealthReport{Ready: true}
	for _, source := range []struct {
		id         string
		sourceType domain.SourceType
	}{{"sub2", domain.SourceSub2API}, {"new", domain.SourceNewAPI}} {
		for _, stream := range []string{"payments", "identities", "usage", "credits", "balances"} {
			report.Items = append(report.Items, postgresstore.SourceStreamHealth{
				SourceInstanceID: source.id, SourceType: source.sourceType, SourceEnabled: true,
				StreamID: stream, Ready: true,
			})
		}
	}
	return postgresstore.SourceReadinessHealth{Report: report}
}

// healthyReadinessProbe returns a probe on which every one of the eleven
// checks passes. Each test breaks exactly one dependency, so any check name
// other than the expected one means the wrong check reported -- which is the
// whole point of the slice.
func healthyReadinessProbe(now time.Time) readinessProbe {
	return readinessProbe{
		pingDatabase: func(context.Context) error { return nil },
		loadSettings: func(context.Context) (adminsettings.Settings, error) {
			return adminsettings.Settings{IssuerName: "某某科技有限公司"}, nil
		},
		pingClamAV:       func(context.Context) error { return nil },
		clamAVSignatures: func(time.Time) error { return nil },
		pingPDFScanner:   func(context.Context) error { return nil },
		sourceHealth: func(context.Context) (postgresstore.SourceReadinessHealth, error) {
			return healthySourceReadiness(), nil
		},
		eligibilityHealth: func(context.Context) (postgresstore.EligibilityProjectionHealth, error) {
			return postgresstore.EligibilityProjectionHealth{}, nil
		},
		proofPending:  &eligibilityProofPendingWarner{},
		containedDead: &containedDeadWarner{},
		now:           func() time.Time { return now },
	}
}

// TestReadinessProbeNamesTheCheckThatFailed is the control-and-mutation pair
// the whole slice rests on. The control is the untouched healthy probe, which
// must return nil: without it, every "wrong check name" assertion below could
// pass for the trivial reason that the fixture is broken to begin with and
// some earlier check is failing in every subtree.
//
// Each subtest then breaks exactly one dependency and requires the *exact*
// check name and the *exact* operator sentence -- not merely that some name
// came back. An assertion of "a check name is present" would have been
// satisfied by the pre-slice behaviour of naming everything identically,
// which is precisely the bug.
func TestReadinessProbeNamesTheCheckThatFailed(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	if _, err := healthyReadinessProbe(now).evaluate(context.Background()); err != nil {
		t.Fatalf("control: the healthy fixture must be ready, else every case below is vacuous: %v", err)
	}

	for name, fixture := range map[string]struct {
		break_      func(*readinessProbe)
		wantCheck   string
		wantSummary string
		wantCause   string
	}{
		"database ping": {
			break_: func(p *readinessProbe) {
				p.pingDatabase = func(context.Context) error {
					return errors.New("dial tcp 10.0.0.7:5432: connect: connection refused")
				}
			},
			wantCheck:   "database",
			wantSummary: "the invoice database is unreachable",
			wantCause:   "dial tcp 10.0.0.7:5432: connect: connection refused",
		},
		"admin settings load": {
			break_: func(p *readinessProbe) {
				p.loadSettings = func(context.Context) (adminsettings.Settings, error) {
					return adminsettings.Settings{}, errors.New("decrypt settings row")
				}
			},
			wantCheck:   "admin_settings",
			wantSummary: "administrator settings cannot be loaded",
			wantCause:   "decrypt settings row",
		},
		"issuer not configured": {
			break_: func(p *readinessProbe) {
				p.loadSettings = func(context.Context) (adminsettings.Settings, error) {
					return adminsettings.Settings{IssuerName: adminsettings.UnconfiguredIssuerName}, nil
				}
			},
			wantCheck:   "invoice_issuer",
			wantSummary: "the invoice issuer is not configured",
			wantCause:   "invoice issuer is not configured",
		},
		"clamav daemon": {
			break_: func(p *readinessProbe) {
				p.pingClamAV = func(context.Context) error { return errors.New("clamd PING timed out") }
			},
			wantCheck:   "clamav_daemon",
			wantSummary: "the antivirus scanner daemon is unreachable",
			wantCause:   "clamd PING timed out",
		},
		"clamav signatures": {
			break_: func(p *readinessProbe) {
				p.clamAVSignatures = func(time.Time) error { return errors.New("daily.cvd is 9 days old") }
			},
			wantCheck:   "clamav_signatures",
			wantSummary: "the antivirus signature database is stale",
			wantCause:   "daily.cvd is 9 days old",
		},
		"pdf scanner": {
			break_: func(p *readinessProbe) {
				p.pingPDFScanner = func(context.Context) error {
					return errors.New("dial unix /run/pdf-scanner.sock: no such file")
				}
			},
			wantCheck:   "pdf_scanner",
			wantSummary: "the document scanner sidecar is unreachable",
			wantCause:   "dial unix /run/pdf-scanner.sock: no such file",
		},
		"source health query": {
			break_: func(p *readinessProbe) {
				p.sourceHealth = func(context.Context) (postgresstore.SourceReadinessHealth, error) {
					return postgresstore.SourceReadinessHealth{}, errors.New("statement timeout")
				}
			},
			wantCheck:   "source_health_query",
			wantSummary: "source health cannot be queried",
			wantCause:   "statement timeout",
		},
		// The incident's own check. It and the two below are the three
		// separate conditions that were all spelled `Dead > 0` in the source
		// and all reported as the same sentence.
		"source ingest dead events": {
			break_: func(p *readinessProbe) {
				p.sourceHealth = func(context.Context) (postgresstore.SourceReadinessHealth, error) {
					health := healthySourceReadiness()
					health.Ingest.Dead = 1
					// Written out rather than left at the zero value: this
					// case only means "an uncontained dead event" because
					// nothing contains it (XM-INV-DEAD-CONTAINMENT).
					health.Ingest.DeadContained = 0
					return health, nil
				}
			},
			wantCheck:   "source_ingest_dead_events",
			wantSummary: "source ingestion has dead events requiring operator repair",
			wantCause:   "source ingestion contains dead events",
		},
		"source ingest backlog is not dead events": {
			break_: func(p *readinessProbe) {
				p.sourceHealth = func(context.Context) (postgresstore.SourceReadinessHealth, error) {
					health := healthySourceReadiness()
					health.Ingest.Pending = 1
					health.Ingest.OldestPending = now.Add(-16 * time.Minute)
					return health, nil
				}
			},
			wantCheck:   "source_ingest",
			wantSummary: "source ingestion is not processing its backlog",
			wantCause:   "source ingestion processing is unhealthy",
		},
		"eligibility health query": {
			break_: func(p *readinessProbe) {
				p.eligibilityHealth = func(context.Context) (postgresstore.EligibilityProjectionHealth, error) {
					return postgresstore.EligibilityProjectionHealth{}, errors.New("lock timeout")
				}
			},
			wantCheck:   "eligibility_health_query",
			wantSummary: "eligibility projection health cannot be queried",
			wantCause:   "lock timeout",
		},
		"eligibility projection dead jobs": {
			break_: func(p *readinessProbe) {
				p.eligibilityHealth = func(context.Context) (postgresstore.EligibilityProjectionHealth, error) {
					return postgresstore.EligibilityProjectionHealth{Dead: 1}, nil
				}
			},
			wantCheck:   "eligibility_projection_dead_jobs",
			wantSummary: "invoice eligibility projection has dead jobs requiring operator repair",
			wantCause:   "invoice eligibility projection has dead jobs requiring operator repair",
		},
		"eligibility projection stuck is not dead jobs": {
			break_: func(p *readinessProbe) {
				p.eligibilityHealth = func(context.Context) (postgresstore.EligibilityProjectionHealth, error) {
					return postgresstore.EligibilityProjectionHealth{OldestPending: now.Add(-16 * time.Minute)}, nil
				}
			},
			wantCheck:   "eligibility_projection_stuck",
			wantSummary: "invoice eligibility projection is not making progress",
			wantCause:   "invoice eligibility projection is not making progress",
		},
		"source stream dead events": {
			break_: func(p *readinessProbe) {
				p.sourceHealth = func(context.Context) (postgresstore.SourceReadinessHealth, error) {
					health := healthySourceReadiness()
					health.Report.Ready = false
					health.Report.Items[0].Ready = false
					health.Report.Items[0].DeadEvents = 1
					health.Report.Items[0].ContainedDeadEvents = 0
					health.Report.Items[0].Reasons = []string{"EVENTS_DEAD"}
					return health, nil
				}
			},
			wantCheck:   "source_stream_dead_events",
			wantSummary: "a required source stream has dead events requiring operator repair",
			wantCause:   "required source stream contains dead events",
		},
		// XM-INV-DEAD-CONTAINMENT. These two are not regression proofs -- both
		// were red before the slice for the same reason they are red after it.
		// They guard the opposite direction: over-correcting containment so
		// that *any* contained dead event excuses the rest. The ingest one in
		// particular is the case that the "{Dead:1,DeadContained:1} is
		// tolerated" assertion in main_test.go cannot see, and vice versa;
		// neither test can be dropped in favour of the other.
		"partly contained ingest dead events still fail closed": {
			break_: func(p *readinessProbe) {
				p.sourceHealth = func(context.Context) (postgresstore.SourceReadinessHealth, error) {
					health := healthySourceReadiness()
					health.Ingest.Dead = 2
					health.Ingest.DeadContained = 1
					return health, nil
				}
			},
			wantCheck:   "source_ingest_dead_events",
			wantSummary: "source ingestion has dead events requiring operator repair",
			wantCause:   "source ingestion contains dead events",
		},
		"an ingest report claiming more contained than dead is rejected": {
			break_: func(p *readinessProbe) {
				p.sourceHealth = func(context.Context) (postgresstore.SourceReadinessHealth, error) {
					health := healthySourceReadiness()
					health.Ingest.Dead = 1
					health.Ingest.DeadContained = 2
					return health, nil
				}
			},
			wantCheck:   "source_ingest",
			wantSummary: "source ingestion is not processing its backlog",
			wantCause:   "source ingestion dead-event evidence is inconsistent",
		},
		"an uncontained stream dead event beside a contained one still fails closed": {
			break_: func(p *readinessProbe) {
				p.sourceHealth = func(context.Context) (postgresstore.SourceReadinessHealth, error) {
					health := healthySourceReadiness()
					health.Report.Ready = false
					health.Report.Items[0].Ready = false
					health.Report.Items[0].DeadEvents = 2
					health.Report.Items[0].ContainedDeadEvents = 1
					health.Report.Items[0].Reasons = []string{"EVENTS_DEAD", "EVENTS_DEAD_CONTAINED"}
					return health, nil
				}
			},
			wantCheck:   "source_stream_dead_events",
			wantSummary: "a required source stream has dead events requiring operator repair",
			wantCause:   "required source stream contains dead events",
		},
		"other source stream failures share the coarse bucket": {
			break_: func(p *readinessProbe) {
				p.sourceHealth = func(context.Context) (postgresstore.SourceReadinessHealth, error) {
					health := healthySourceReadiness()
					health.Report.Items = health.Report.Items[:len(health.Report.Items)-1]
					return health, nil
				}
			},
			wantCheck:   "source_streams",
			wantSummary: "required source streams are not healthy",
			wantCause:   "required source stream set is incomplete",
		},
	} {
		t.Run(name, func(t *testing.T) {
			probe := healthyReadinessProbe(now)
			fixture.break_(&probe)
			_, err := probe.evaluate(context.Background())
			if err == nil {
				t.Fatal("a broken dependency was reported ready")
			}
			var named *httpapi.ReadinessCheckError
			if !errors.As(err, &named) {
				t.Fatalf("readiness error is not classified, so /readyz cannot name it: %v", err)
			}
			if named.Check != fixture.wantCheck {
				t.Fatalf("check=%q want %q -- a different check reported than the one that failed", named.Check, fixture.wantCheck)
			}
			if named.Summary != fixture.wantSummary {
				t.Fatalf("summary=%q want %q", named.Summary, fixture.wantSummary)
			}
			// The real cause must survive to the log even though it never
			// reaches the response body.
			if named.Err == nil || named.Err.Error() != fixture.wantCause {
				t.Fatalf("underlying cause=%v want %q", named.Err, fixture.wantCause)
			}
		})
	}
}

// TestReadinessProbeCheckNamesAreUnique guards the vocabulary itself: two
// checks sharing a name would silently re-create the failure this slice
// exists to fix, and the table above would not catch it because each subtest
// only ever looks at its own expected name.
func TestReadinessProbeCheckNamesAreUnique(t *testing.T) {
	names := []string{
		readinessCheckDatabase, readinessCheckAdminSettings, readinessCheckInvoiceIssuer,
		readinessCheckClamAVDaemon, readinessCheckClamAVSignatures, readinessCheckPDFScanner,
		readinessCheckSourceHealthQuery, readinessCheckSourceIngestDead, readinessCheckSourceIngest,
		readinessCheckEligibilityQuery, readinessCheckEligibilityDead, readinessCheckEligibilityStuck,
		readinessCheckEligibility, readinessCheckSourceStreamDead, readinessCheckSourceStreams,
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			t.Fatalf("duplicate readiness check name %q", name)
		}
		seen[name] = true
	}
}

// TestReadinessProbePublishedStringsCarryNothingIdentifying is the standing
// guard on the security decision (see docs/handoffs/XM-INV-READYZ-DETAIL.md):
// /readyz is unauthenticated and internet-facing, so both halves of what it
// publishes are checked here against the same character rules httpapi
// enforces at the boundary. A future check whose name or sentence embeds a
// host, a port, a path or an id fails this test rather than shipping.
func TestReadinessProbePublishedStringsCarryNothingIdentifying(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	forbidden := ".:/@%0123456789"
	for _, breaker := range []func(*readinessProbe){
		func(p *readinessProbe) {
			p.pingDatabase = func(context.Context) error { return errors.New("x") }
		},
		func(p *readinessProbe) {
			p.loadSettings = func(context.Context) (adminsettings.Settings, error) {
				return adminsettings.Settings{}, errors.New("x")
			}
		},
		func(p *readinessProbe) {
			p.loadSettings = func(context.Context) (adminsettings.Settings, error) {
				return adminsettings.Settings{IssuerName: adminsettings.UnconfiguredIssuerName}, nil
			}
		},
		func(p *readinessProbe) { p.pingClamAV = func(context.Context) error { return errors.New("x") } },
		func(p *readinessProbe) { p.clamAVSignatures = func(time.Time) error { return errors.New("x") } },
		func(p *readinessProbe) { p.pingPDFScanner = func(context.Context) error { return errors.New("x") } },
		func(p *readinessProbe) {
			p.sourceHealth = func(context.Context) (postgresstore.SourceReadinessHealth, error) {
				return postgresstore.SourceReadinessHealth{}, errors.New("x")
			}
		},
		func(p *readinessProbe) {
			p.sourceHealth = func(context.Context) (postgresstore.SourceReadinessHealth, error) {
				health := healthySourceReadiness()
				health.Ingest.Dead = 1
				return health, nil
			}
		},
		func(p *readinessProbe) {
			p.sourceHealth = func(context.Context) (postgresstore.SourceReadinessHealth, error) {
				health := healthySourceReadiness()
				health.Ingest.Pending = 1
				health.Ingest.OldestPending = now.Add(-16 * time.Minute)
				return health, nil
			}
		},
		func(p *readinessProbe) {
			p.eligibilityHealth = func(context.Context) (postgresstore.EligibilityProjectionHealth, error) {
				return postgresstore.EligibilityProjectionHealth{}, errors.New("x")
			}
		},
		func(p *readinessProbe) {
			p.eligibilityHealth = func(context.Context) (postgresstore.EligibilityProjectionHealth, error) {
				return postgresstore.EligibilityProjectionHealth{Dead: 1}, nil
			}
		},
		func(p *readinessProbe) {
			p.eligibilityHealth = func(context.Context) (postgresstore.EligibilityProjectionHealth, error) {
				return postgresstore.EligibilityProjectionHealth{OldestPending: now.Add(-16 * time.Minute)}, nil
			}
		},
		func(p *readinessProbe) {
			p.sourceHealth = func(context.Context) (postgresstore.SourceReadinessHealth, error) {
				health := healthySourceReadiness()
				health.Report.Ready = false
				health.Report.Items[0].Ready = false
				health.Report.Items[0].DeadEvents = 1
				return health, nil
			}
		},
		func(p *readinessProbe) {
			p.sourceHealth = func(context.Context) (postgresstore.SourceReadinessHealth, error) {
				health := healthySourceReadiness()
				health.Report.Items = health.Report.Items[:len(health.Report.Items)-1]
				return health, nil
			}
		},
	} {
		probe := healthyReadinessProbe(now)
		breaker(&probe)
		var named *httpapi.ReadinessCheckError
		_, probeErr := probe.evaluate(context.Background())
		if !errors.As(probeErr, &named) {
			t.Fatal("readiness failure was not classified")
		}
		if strings.ContainsAny(named.Check, forbidden) || strings.ContainsAny(named.Summary, forbidden) {
			t.Fatalf("published readiness strings may not contain %q: check=%q summary=%q",
				forbidden, named.Check, named.Summary)
		}
	}
}

// TestReadinessProbeShortCircuitsOnTheFirstFailure pins the ordering
// decision. With two checks failing at once, the earlier one is what an
// operator is told about: the later failure is very often a consequence of
// the earlier one (a source health query cannot succeed while the database is
// down), so reporting the later one would send them the wrong way.
func TestReadinessProbeShortCircuitsOnTheFirstFailure(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	probe := healthyReadinessProbe(now)
	pdfScannerPinged := false
	probe.pingClamAV = func(context.Context) error { return errors.New("clamd down") }
	probe.pingPDFScanner = func(context.Context) error {
		pdfScannerPinged = true
		return errors.New("sidecar down")
	}
	probe.eligibilityHealth = func(context.Context) (postgresstore.EligibilityProjectionHealth, error) {
		return postgresstore.EligibilityProjectionHealth{Dead: 4}, nil
	}
	var named *httpapi.ReadinessCheckError
	_, probeErr := probe.evaluate(context.Background())
	if !errors.As(probeErr, &named) {
		t.Fatal("readiness failure was not classified")
	}
	if named.Check != readinessCheckClamAVDaemon {
		t.Fatalf("check=%q want the first failing check %q", named.Check, readinessCheckClamAVDaemon)
	}
	if pdfScannerPinged {
		t.Fatal("a later check ran after an earlier one had already failed; the 3s budget assumes it does not")
	}
}

// TestReadinessProbeStillWarnsAboutProofPendingJobs is a non-regression guard.
// The proof-pending warning is not a readiness gate and never was; lifting the
// closure into readinessProbe must not have quietly dropped it, and it has to
// keep firing on an evaluation that goes on to succeed.
func TestReadinessProbeStillWarnsAboutProofPendingJobs(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	probe := healthyReadinessProbe(now)
	probe.eligibilityHealth = func(context.Context) (postgresstore.EligibilityProjectionHealth, error) {
		return postgresstore.EligibilityProjectionHealth{
			ProofPending: 2, OldestProofPending: now.Add(-3 * time.Hour),
		}, nil
	}
	if _, err := probe.evaluate(context.Background()); err != nil {
		t.Fatalf("balance-proof-pending jobs must not make the API not-ready: %v", err)
	}
	if probe.proofPending.lastAt.IsZero() {
		t.Fatal("warnIfStale was not reached from the extracted probe")
	}
}

// TestShouldWarnContainedDeadRateLimits pins the boundary of the
// contained-dead Warn (XM-INV-DEAD-CONTAINMENT). The /readyz probe fires
// every ten seconds in production, so an unlimited line would write ~2,600
// entries a day about one unrepaired event -- which is the "one account's
// problem becomes everybody's noise" shape this slice exists to end, merely
// moved from the endpoint to the log.
func TestShouldWarnContainedDeadRateLimits(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	for name, fixture := range map[string]struct {
		health       postgresstore.SourceIngestHealth
		lastWarnedAt time.Time
		want         bool
	}{
		"nothing contained says nothing": {
			health: postgresstore.SourceIngestHealth{Dead: 1}, want: false,
		},
		"the first contained dead event warns": {
			health: postgresstore.SourceIngestHealth{Dead: 1, DeadContained: 1}, want: true,
		},
		"one second inside the interval stays quiet": {
			health:       postgresstore.SourceIngestHealth{Dead: 1, DeadContained: 1},
			lastWarnedAt: now.Add(-containedDeadWarnInterval + time.Second), want: false,
		},
		"exactly at the interval warns again": {
			health:       postgresstore.SourceIngestHealth{Dead: 1, DeadContained: 1},
			lastWarnedAt: now.Add(-containedDeadWarnInterval), want: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := shouldWarnContainedDead(fixture.health, now, fixture.lastWarnedAt); got != fixture.want {
				t.Fatalf("shouldWarnContainedDead=%t want %t", got, fixture.want)
			}
		})
	}
}

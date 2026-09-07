package main

import (
	"context"
	"errors"
	"time"

	"invoice-system/backend/internal/adminsettings"
	"invoice-system/backend/internal/httpapi"
	"invoice-system/backend/internal/postgresstore"
)

// The readinessCheck* constants are /readyz's closed vocabulary
// (XM-INV-READYZ-DETAIL). Every one of them is published verbatim on an
// unauthenticated, internet-facing endpoint, so the rules for adding one are:
// name a subsystem, never an instance; never interpolate anything; and keep
// it inside httpapi.readinessCheckNamePattern, which rejects digits and every
// character a hostname, address, path or id would need.
//
// The vocabulary is deliberately coarser than the set of conditions the
// validators distinguish. Sub-conditions that actually occur in production
// get their own name -- above all the two independent `Dead > 0` gates, which
// live in different tables, need different repairs, and were previously
// indistinguishable from each other and from the other nine checks. The
// structural-consistency assertions (a malformed health report, a duplicated
// stream) share a bucket: they mean the report itself is broken rather than a
// dependency being down, and the exact text of those is in the log line
// anyway. Public output is coarse and stable; the log is exact.
const (
	readinessCheckDatabase          = "database"
	readinessCheckAdminSettings     = "admin_settings"
	readinessCheckInvoiceIssuer     = "invoice_issuer"
	readinessCheckClamAVDaemon      = "clamav_daemon"
	readinessCheckClamAVSignatures  = "clamav_signatures"
	readinessCheckPDFScanner        = "pdf_scanner"
	readinessCheckSourceHealthQuery = "source_health_query"
	readinessCheckSourceIngestDead  = "source_ingest_dead_events"
	readinessCheckSourceIngest      = "source_ingest"
	readinessCheckEligibilityQuery  = "eligibility_health_query"
	readinessCheckEligibilityDead   = "eligibility_projection_dead_jobs"
	readinessCheckEligibilityStuck  = "eligibility_projection_stuck"
	readinessCheckEligibility       = "eligibility_projection"
	readinessCheckSourceStreamDead  = "source_stream_dead_events"
	readinessCheckSourceStreams     = "source_streams"
)

// The readinessSummary* constants are the one operator-facing sentence that
// accompanies each check name in the response body. Same rules, plus
// httpapi.readinessSummaryPattern's tighter character set (lowercase letters,
// spaces and hyphens only). The two eligibility ones reuse the existing
// eligibilityProjection*Reason constants verbatim rather than restating them,
// so the sentence an operator reads on /readyz is character-for-character the
// one XM-INV-PROJECTION-FAILURE-GRADING already put in the error text.
const (
	readinessSummaryDatabase          = "the invoice database is unreachable"
	readinessSummaryAdminSettings     = "administrator settings cannot be loaded"
	readinessSummaryInvoiceIssuer     = "the invoice issuer is not configured"
	readinessSummaryClamAVDaemon      = "the antivirus scanner daemon is unreachable"
	readinessSummaryClamAVSignatures  = "the antivirus signature database is stale"
	readinessSummaryPDFScanner        = "the document scanner sidecar is unreachable"
	readinessSummarySourceHealthQuery = "source health cannot be queried"
	readinessSummarySourceIngestDead  = "source ingestion has dead events requiring operator repair"
	readinessSummarySourceIngest      = "source ingestion is not processing its backlog"
	readinessSummaryEligibilityQuery  = "eligibility projection health cannot be queried"
	readinessSummaryEligibility       = "invoice eligibility projection is not healthy"
	readinessSummarySourceStreamDead  = "a required source stream has dead events requiring operator repair"
	readinessSummarySourceStreams     = "required source streams are not healthy"
)

// readinessProbe is the /readyz decision, lifted out of the closure that used
// to live inline in buildProductionRuntime. Every dependency arrives as a
// function value so each of the eleven checks can be failed on its own in a
// unit test -- previously the only way to exercise, say, the PDF-scanner
// check was to have a real sidecar socket refuse a connection, which is why
// none of them were covered individually.
//
// Order and short-circuiting are unchanged from the original closure and must
// stay that way. The checks are in dependency order (settings need the
// database, the ingest verdict needs the health query) and the handler gives
// the whole probe three seconds, while the PDF sidecar alone carries a
// 20-second timeout of its own. Running the remaining checks after one has
// already failed would report cascading consequences of the first failure and
// could spend the entire budget before reporting anything -- strictly worse
// for the operator than the first real cause, named.
type readinessProbe struct {
	pingDatabase      func(context.Context) error
	loadSettings      func(context.Context) (adminsettings.Settings, error)
	pingClamAV        func(context.Context) error
	clamAVSignatures  func(time.Time) error
	pingPDFScanner    func(context.Context) error
	sourceHealth      func(context.Context) (postgresstore.SourceReadinessHealth, error)
	eligibilityHealth func(context.Context) (postgresstore.EligibilityProjectionHealth, error)
	proofPending      *eligibilityProofPendingWarner
	now               func() time.Time
}

func (p readinessProbe) evaluate(ctx context.Context) error {
	if pingErr := p.pingDatabase(ctx); pingErr != nil {
		return httpapi.NotReady(readinessCheckDatabase, readinessSummaryDatabase, pingErr)
	}
	currentSettings, settingsErr := p.loadSettings(ctx)
	if settingsErr != nil {
		return httpapi.NotReady(readinessCheckAdminSettings, readinessSummaryAdminSettings, settingsErr)
	}
	if issuerErr := validateIssuerReadiness(currentSettings); issuerErr != nil {
		return httpapi.NotReady(readinessCheckInvoiceIssuer, readinessSummaryInvoiceIssuer, issuerErr)
	}
	if clamErr := p.pingClamAV(ctx); clamErr != nil {
		return httpapi.NotReady(readinessCheckClamAVDaemon, readinessSummaryClamAVDaemon, clamErr)
	}
	if signatureErr := p.clamAVSignatures(p.now()); signatureErr != nil {
		return httpapi.NotReady(readinessCheckClamAVSignatures, readinessSummaryClamAVSignatures, signatureErr)
	}
	if pdfScannerErr := p.pingPDFScanner(ctx); pdfScannerErr != nil {
		return httpapi.NotReady(readinessCheckPDFScanner, readinessSummaryPDFScanner, pdfScannerErr)
	}
	sourceHealth, healthErr := p.sourceHealth(ctx)
	if healthErr != nil {
		return httpapi.NotReady(readinessCheckSourceHealthQuery, readinessSummarySourceHealthQuery, healthErr)
	}
	if ingestErr := validateSourceIngestRuntimeReadiness(sourceHealth.Ingest, p.now()); ingestErr != nil {
		if errors.Is(ingestErr, errSourceIngestDeadEvents) {
			return httpapi.NotReady(readinessCheckSourceIngestDead, readinessSummarySourceIngestDead, ingestErr)
		}
		return httpapi.NotReady(readinessCheckSourceIngest, readinessSummarySourceIngest, ingestErr)
	}
	eligibilityHealth, healthErr := p.eligibilityHealth(ctx)
	if healthErr != nil {
		return httpapi.NotReady(readinessCheckEligibilityQuery, readinessSummaryEligibilityQuery, healthErr)
	}
	readinessNow := p.now()
	p.proofPending.warnIfStale(eligibilityHealth, readinessNow)
	if readinessErr := eligibilityProjectionReady(eligibilityHealth, readinessNow); readinessErr != nil {
		switch {
		case errors.Is(readinessErr, errEligibilityProjectionDead):
			return httpapi.NotReady(readinessCheckEligibilityDead, eligibilityProjectionDeadReason, readinessErr)
		case errors.Is(readinessErr, errEligibilityProjectionStuck):
			return httpapi.NotReady(readinessCheckEligibilityStuck, eligibilityProjectionStuckReason, readinessErr)
		}
		return httpapi.NotReady(readinessCheckEligibility, readinessSummaryEligibility, readinessErr)
	}
	if readinessErr := validateSourceRuntimeReadiness(sourceHealth.Report); readinessErr != nil {
		if errors.Is(readinessErr, errSourceStreamDeadEvents) {
			return httpapi.NotReady(readinessCheckSourceStreamDead, readinessSummarySourceStreamDead, readinessErr)
		}
		return httpapi.NotReady(readinessCheckSourceStreams, readinessSummarySourceStreams, readinessErr)
	}
	return nil
}

package main

import (
	"flag"
	"fmt"
	"log"

	"invoice-system/agents/sourceagent"
)

func main() {
	batchPath := flag.String("batch", "sourceagent/testdata/source-agent-batch.mock.json", "local mock batch JSON path")
	expectedSource := flag.String("source-id", "sub2api-mock", "expected source instance ID")
	flag.Parse()

	raw, err := sourceagent.ReadMockBatch(*batchPath)
	if err != nil {
		log.Fatal(err)
	}

	// This small entrypoint remains an offline mock verifier. Production launchers
	// compose the read-only DB/API connectors, durable cursor/sequence stores and
	// signed mTLS ingestion client from sourceagent; they must not silently fall
	// back to this in-memory mock path.
	bodyHash := sourceagent.SHA256Hex(raw)
	validated, err := sourceagent.NewValidator(*expectedSource).ValidateAndCommit(raw, bodyHash)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("validated mock batch source=%s sequence=%d records=%d body_sha256=%s\n",
		validated.Batch.SourceInstanceID,
		validated.Batch.Sequence,
		len(validated.Batch.Records),
		validated.BodyHash,
	)
}

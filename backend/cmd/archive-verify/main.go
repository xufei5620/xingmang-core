package main

import (
	"flag"
	"fmt"
	"os"

	"invoice-system/backend/internal/backuparchive"
)

func main() {
	archive := flag.String("archive", "", "regular tar archive to validate")
	maxEntries := flag.Int("max-entries", backuparchive.DefaultMaxEntries, "maximum archive entries")
	maxTotal := flag.Int64("max-total-bytes", backuparchive.DefaultMaxTotalSize, "maximum sum of regular file sizes")
	maxFile := flag.Int64("max-file-bytes", backuparchive.DefaultMaxFileSize, "maximum single regular file size")
	flag.Parse()
	if *archive == "" {
		fmt.Fprintln(os.Stderr, "archive-verify: --archive is required")
		os.Exit(2)
	}
	report, err := backuparchive.VerifyTarFile(*archive, backuparchive.Limits{
		MaxEntries: *maxEntries, MaxTotalSize: *maxTotal, MaxFileSize: *maxFile,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "archive-verify: archive rejected")
		os.Exit(1)
	}
	fmt.Printf("archive verified: entries=%d files=%d total_bytes=%d\n", report.Entries, report.Files, report.TotalSize)
}

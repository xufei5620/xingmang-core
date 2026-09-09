package backuparchive

import (
	"archive/tar"
	"bytes"
	"fmt"
	"strings"
	"testing"
)

type archiveEntry struct {
	name       string
	typeflag   byte
	body       string
	link       string
	paxRecords map[string]string
}

func TestVerifyTarAcceptsOnlyBoundedDirectoriesAndRegularFiles(t *testing.T) {
	archive := buildArchive(t,
		archiveEntry{name: "./", typeflag: tar.TypeDir},
		archiveEntry{name: "./issued/", typeflag: tar.TypeDir},
		archiveEntry{name: "./issued/a.pdf.enc", typeflag: tar.TypeReg, body: "ciphertext"},
	)
	report, err := VerifyTar(bytes.NewReader(archive), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Entries != 3 || report.Files != 1 || report.TotalSize != 10 {
		t.Fatalf("report=%+v", report)
	}
}

func TestVerifyTarRejectsUnsafeTypesPAXPathsAndDuplicates(t *testing.T) {
	tests := map[string][]archiveEntry{
		"symlink":          {{name: "link", typeflag: tar.TypeSymlink, link: "/etc/passwd"}},
		"hardlink":         {{name: "link", typeflag: tar.TypeLink, link: "target"}},
		"fifo":             {{name: "pipe", typeflag: tar.TypeFifo}},
		"device":           {{name: "device", typeflag: tar.TypeChar}},
		"pax":              {{name: "file", typeflag: tar.TypeReg, paxRecords: map[string]string{"path": "file"}}},
		"traversal":        {{name: "../escape", typeflag: tar.TypeReg}},
		"absolute":         {{name: "/escape", typeflag: tar.TypeReg}},
		"backslash":        {{name: `..\escape`, typeflag: tar.TypeReg}},
		"control":          {{name: "bad\nname", typeflag: tar.TypeReg}},
		"duplicate":        {{name: "./same", typeflag: tar.TypeReg}, {name: "same", typeflag: tar.TypeReg}},
		"hidden_traversal": {{name: ".//../escape", typeflag: tar.TypeReg}},
	}
	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			archive := buildArchive(t, entries...)
			if _, err := VerifyTar(bytes.NewReader(archive), Limits{}); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
	t.Run("socket", func(t *testing.T) {
		archive := buildArchive(t, archiveEntry{name: "socket", typeflag: tar.TypeReg})
		archive[156] = 's'
		for index := 148; index < 156; index++ {
			archive[index] = ' '
		}
		checksum := 0
		for _, value := range archive[:512] {
			checksum += int(value)
		}
		copy(archive[148:156], []byte(fmt.Sprintf("%06o\x00 ", checksum)))
		if _, err := VerifyTar(bytes.NewReader(archive), Limits{}); err == nil {
			t.Fatal("socket archive accepted")
		}
	})
}

func TestVerifyTarEnforcesResourceLimits(t *testing.T) {
	archive := buildArchive(t, archiveEntry{name: "large", typeflag: tar.TypeReg, body: strings.Repeat("x", 5)})
	if _, err := VerifyTar(bytes.NewReader(archive), Limits{MaxEntries: 1, MaxFileSize: 4, MaxTotalSize: 10}); err == nil {
		t.Fatal("oversized member accepted")
	}
	archive = buildArchive(t,
		archiveEntry{name: "a", typeflag: tar.TypeReg, body: "a"},
		archiveEntry{name: "b", typeflag: tar.TypeReg, body: "b"},
	)
	if _, err := VerifyTar(bytes.NewReader(archive), Limits{MaxEntries: 1}); err == nil {
		t.Fatal("entry limit was not enforced")
	}
}

func buildArchive(t *testing.T, entries ...archiveEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	for _, entry := range entries {
		header := &tar.Header{
			Name: entry.name, Typeflag: entry.typeflag, Mode: 0o600,
			Size: int64(len(entry.body)), Linkname: entry.link, PAXRecords: entry.paxRecords,
		}
		if entry.typeflag == tar.TypeDir {
			header.Mode = 0o700
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if entry.body != "" {
			if _, err := writer.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

package sourceagent

import (
	"fmt"
	"io"
	"os"
)

const MaxMockBatchBytes int64 = 4 << 20

// ReadMockBatch reads a bounded local JSON file. It performs no network access.
func ReadMockBatch(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open mock batch: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, MaxMockBatchBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read mock batch: %w", err)
	}
	if int64(len(data)) > MaxMockBatchBytes {
		return nil, fmt.Errorf("mock batch exceeds %d bytes", MaxMockBatchBytes)
	}
	return data, nil
}

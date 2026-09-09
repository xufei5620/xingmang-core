//go:build windows

package document

// Windows rename/delete durability is provided by the filesystem API. Opening
// a directory with os.Open and Sync is not supported consistently on Windows.
func syncDocumentDirectory(string) error { return nil }

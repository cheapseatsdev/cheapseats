package grade

import (
	"embed"
	"fmt"
	"strings"
)

// The same public fixtures ship with the reviewer and the golden tests. Keep one
// source of truth; importing these never promotes their model labels to human.
//
//go:embed testdata/golden/*.jsonl
var goldenFixtures embed.FS

func BundledGolden() ([]byte, error) {
	files, err := goldenFixtures.ReadDir("testdata/golden")
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
			continue
		}
		data, err := goldenFixtures.ReadFile("testdata/golden/" + f.Name())
		if err != nil {
			return nil, fmt.Errorf("bundled golden: %w", err)
		}
		b.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			b.WriteByte('\n')
		}
	}
	return []byte(b.String()), nil
}

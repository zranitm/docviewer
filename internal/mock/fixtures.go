package mock

import _ "embed"

// Fixtures, embedded so the mock binaries need no files next to them.
var (
	//go:embed fixtures/sales.json
	SalesFixture []byte

	//go:embed fixtures/service.json
	ServiceFixture []byte

	//go:embed fixtures/placeholder.pdf
	PlaceholderPDF []byte
)

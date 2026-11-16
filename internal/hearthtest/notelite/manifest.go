package notelite

import _ "embed"

// Manifest is the app's hearth manifest, as hearth's generator writes it:
// format major 1, 2 capabilities, 1 selector and the 4 operations.
//
//go:embed manifest.json
var Manifest []byte

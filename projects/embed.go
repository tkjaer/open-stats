// SPDX-License-Identifier: AGPL-3.0-or-later

// Package projects embeds projects/*.yml, so the open-stats binary carries
// the configuration it was built with.
package projects

import "embed"

//go:embed *.yml
var FS embed.FS

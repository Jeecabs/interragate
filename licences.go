// Copyright (C) 2026 Jeecabs and interragate contributors
// SPDX-License-Identifier: AGPL-3.0-only

// Package interragate holds the licence notices embedded in standalone reports.
package interragate

import _ "embed"

var (
	//go:embed COPYRIGHT
	copyrightNotice string
	//go:embed LICENSE
	projectLicence string
	//go:embed third_party/geist/LICENSE.txt
	fontLicence string
	//go:embed third_party/phosphor/LICENSE
	iconLicence string
)

// LicenceNotices returns the full notices for the report's code, fonts and icons.
func LicenceNotices() string {
	return copyrightNotice + "\n\n" + projectLicence +
		"\n\nGeist fonts\n\n" + fontLicence +
		"\n\nPhosphor icons\n\n" + iconLicence
}

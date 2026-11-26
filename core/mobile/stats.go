package mobile

import (
	"sync"

	"canalpeer.com/core/route"
	"canalpeer.com/core/stats"
)

func init() { stats.Roots = roots }

var openOnce sync.Once

// ReportOpen tells canalpeer.com the app was opened, once per process, and
// returns at once. It configures the route first, as Start does; dataDir
// keeps the install id ("" reports nothing).
func ReportOpen(platform, version, vpnMode, dataDir string) {
	openOnce.Do(func() {
		route.Configure(vpnMode, dataDir)
		stats.Open(platform, version, dataDir)
	})
}

// nlm-agent is the NLM probing agent (spec §3.1). The full mode wiring
// (spoke/hub/listener) lands in Phase 3; this binary currently exists so
// the build covers the cmd/ tree.
package main

import (
	"github.com/jscobbie73/netlatencymonitor/internal/logging"
)

func main() {
	log := logging.Init()
	log.Info().Msg("nlm-agent: stub binary; modes wired in Phase 3")
}

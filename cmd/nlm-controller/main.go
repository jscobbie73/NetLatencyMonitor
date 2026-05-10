// nlm-controller is the central NLM API + UI server (spec §3.2). The HTTP
// surface and DB land in Phase 2; this binary currently exists so the build
// covers the cmd/ tree.
package main

import (
	"github.com/jscobbie73/netlatencymonitor/internal/logging"
)

func main() {
	log := logging.Init()
	log.Info().Msg("nlm-controller: stub binary; HTTP + DB wired in Phase 2")
}

package proxy

import (
	"github.com/regolo-ai/brick-SR1/apps/router/src/spatial-router/pkg/brickrouting"
	"github.com/regolo-ai/brick-SR1/apps/router/src/spatial-router/pkg/observability/logging"
)

// logShadowOrchestrator is the RoutingModeOrchestrator shadow path. It records,
// per request, what the inverted orchestrator+subagent architecture WOULD do,
// WITHOUT changing what is served: the request is still handled by the normal
// passthrough (the candidate model stands). It exists so the mode can be turned
// on in dev to observe intent before any behavior change, per the design's
// ordering (sticky mode A must produce real numbers before B is served).
//
// This is deliberately a stub, not a running orchestrator. The load-bearing v2
// piece — the classifier-as-extractor that emits the minimal context slice for a
// stateless subagent call — is intentionally NOT built here; it is the shared
// net-new component the design gates behind evaluation of mode A. Until it lands,
// this logs the candidate route and the prompt size so a shadow analysis can be
// assembled from logs, and nothing is served differently.
//
// It does NOT emit a fabricated est_saved_usd: without the extractor there is no
// honest slice-reduction figure to report. The promotion gate metrics
// (est_saved_usd median, p95 latency) attach when the extractor + subagent path
// is implemented.
func logShadowOrchestrator(bodyBytes int, route *brickrouting.Result, servedModel string) {
	candidate := ""
	if route != nil {
		candidate = route.Model
	}
	logging.Infof("orchestrator_shadow: served=%s candidate=%s prompt_bytes=%d note=shadow_only_not_served_extractor_pending",
		servedModel, candidate, bodyBytes)
}

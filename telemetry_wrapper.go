package libXray

import (
	"github.com/xtls/libxray/nodep"
	"github.com/xtls/libxray/telemetry"
)

// ConfigureTelemetry enables opt-in OTLP trace export for this libXray process.
// Call only after the user has enabled diagnostics; callers must keep the
// short-lived ingest token in memory and never persist or log it.
func ConfigureTelemetry(endpoint, ingestToken, serviceVersion string) string {
	var response nodep.CallResponse[string]
	return response.EncodeToBase64("", telemetry.Configure(endpoint, ingestToken, serviceVersion))
}

// ShutdownTelemetry stops trace export and releases the configured provider.
func ShutdownTelemetry() string {
	var response nodep.CallResponse[string]
	return response.EncodeToBase64("", telemetry.Shutdown(nil))
}

package nodep

import (
	"context"
	"crypto/tls"
	"errors"
	"math"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const (
	PingDelayTimeout int64 = 11000
	PingDelayError   int64 = 10000
)

// get the delay of some outbound.
// timeout means how long the http request will be cancelled if no response, in units of seconds.
// url means the website we use to test speed. "https://www.google.com" is a good choice for most cases.
// proxy means the local http/socks5 proxy, like "socks5://[::1]:1080". If proxy is empty, it means no proxy.
func MeasureDelay(timeout int, url string, proxy string) (int64, error) {
	httpTimeout := time.Second * time.Duration(timeout)
	c, err := CoreHTTPClient(httpTimeout, proxy)
	if err != nil {
		return PingDelayError, err
	}
	delay, err := PingHTTPRequest(c, url, timeout)
	if err != nil {
		return delay, err
	}

	return delay, nil
}

func CoreHTTPClient(timeout time.Duration, proxy string) (*http.Client, error) {
	tr := &http.Transport{
		DisableKeepAlives: true,
	}

	if len(proxy) > 0 {
		tr.Proxy = func(r *http.Request) (*url.URL, error) {
			return url.Parse(proxy)
		}
	}

	c := &http.Client{
		Transport: tr,
		Timeout:   timeout,
	}

	return c, nil
}

func PingHTTPRequest(c *http.Client, url string, timeout int) (int64, error) {
	start := time.Now()
	ctx, span := otel.Tracer("github.com/xtls/libxray/nodep").Start(
		context.Background(), "xconnect.client.probe.http_request",
		trace.WithSpanKind(trace.SpanKindClient),
	)
	defer span.End()
	var phaseMu sync.Mutex
	phaseStart := make(map[string]time.Time)
	phaseDurations := make(map[string]time.Duration)
	begin := func(name string) {
		phaseMu.Lock()
		phaseStart[name] = time.Now()
		phaseMu.Unlock()
	}
	finish := func(name string) {
		phaseMu.Lock()
		if at, ok := phaseStart[name]; ok {
			phaseDurations[name] = time.Since(at)
			delete(phaseStart, name)
		}
		phaseMu.Unlock()
	}
	traceHooks := &httptrace.ClientTrace{
		DNSStart:          func(httptrace.DNSStartInfo) { begin("dns") },
		DNSDone:           func(httptrace.DNSDoneInfo) { finish("dns") },
		ConnectStart:      func(_, _ string) { begin("connect") },
		ConnectDone:       func(_, _ string, _ error) { finish("connect") },
		TLSHandshakeStart: func() { begin("tls") },
		TLSHandshakeDone: func(state tls.ConnectionState, err error) {
			finish("tls")
			if state.NegotiatedProtocol != "" {
				span.SetAttributes(attribute.String("network.protocol.version", state.NegotiatedProtocol))
			}
			span.SetAttributes(attribute.Bool("tls.resumed", state.DidResume))
			if err != nil {
				span.AddEvent("tls.handshake.error")
			}
		},
		GotConn: func(info httptrace.GotConnInfo) {
			span.SetAttributes(attribute.Bool("network.connection.reused", info.Reused))
		},
		GotFirstResponseByte: func() {
			span.AddEvent("http.first_response_byte")
			span.SetAttributes(attribute.Int64("xconnect.client.probe.first_response_wait_ms", time.Since(start).Milliseconds()))
		},
	}
	ctx = httptrace.WithClientTrace(ctx, traceHooks)
	span.SetAttributes(attribute.String("xconnect.client.probe.transport_scope", "http_client_to_configured_proxy_or_target"))
	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "request creation failed")
		return PingDelayError, err
	}
	req = req.WithContext(ctx)
	response, err := c.Do(req)
	delay := time.Since(start).Milliseconds()
	phaseMu.Lock()
	for name, duration := range phaseDurations {
		span.SetAttributes(attribute.Int64("xconnect.client.probe."+name+"_duration_ms", duration.Milliseconds()))
	}
	phaseMu.Unlock()
	span.SetAttributes(attribute.Int64("xconnect.client.probe.duration_ms", delay))
	if err != nil {
		span.RecordError(errors.New("probe HTTP request failed"))
		span.SetStatus(codes.Error, "probe HTTP request failed")
		precision := delay - int64(timeout)*1000
		if math.Abs(float64(precision)) < 50 {
			return PingDelayTimeout, err
		}
		return PingDelayError, err
	}
	response.Body.Close()
	span.SetAttributes(attribute.Int("http.response.status_code", response.StatusCode))
	return delay, nil
}

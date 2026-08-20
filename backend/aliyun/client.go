package aliyun

import (
	"fmt"
	"strings"
	"sync"
	"time"

	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	util "github.com/alibabacloud-go/tea-utils/v2/service"
	"github.com/alibabacloud-go/tea/tea"
)

// Client is a thin wrapper over the generic Aliyun OpenAPI client. We use the
// generic RPC caller (rather than the product-specific typed SDK) so that the
// exact request parameters match what we verified via the `aliyun mse` CLI,
// with no struct-name guesswork across SDK major versions.
type Client struct {
	c          *openapi.Client
	InstanceID string

	mu       sync.Mutex
	lastCall time.Time
	minGap   time.Duration // throttle: minimum spacing between MSE calls
}

func New(ak, sk, token, endpoint, instanceID string) (*Client, error) {
	cfg := &openapi.Config{
		AccessKeyId:     tea.String(ak),
		AccessKeySecret: tea.String(sk),
		Endpoint:        tea.String(endpoint),
	}
	if token != "" {
		cfg.SecurityToken = tea.String(token)
		cfg.Type = tea.String("sts")
	}
	c, err := openapi.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{c: c, InstanceID: instanceID, minGap: 150 * time.Millisecond}, nil
}

// throttle spaces out calls to avoid MSE server-side rate limiting (503).
func (cl *Client) throttle() {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if wait := cl.minGap - time.Since(cl.lastCall); wait > 0 {
		time.Sleep(wait)
	}
	cl.lastCall = time.Now()
}

func isRetryable(err error) bool {
	s := err.Error()
	return strings.Contains(s, "ServiceUnavailable") ||
		strings.Contains(s, "Throttling") ||
		strings.Contains(s, "503") ||
		strings.Contains(s, "500")
}

// call invokes an MSE RPC action (version 2019-05-31) with the given query
// parameters and returns the decoded response body. It throttles and retries
// transient failures itself; the SDK's own aggressive auto-retry is disabled to
// avoid multi-second backoff storms.
func (cl *Client) call(action string, query map[string]any) (map[string]any, error) {
	params := &openapi.Params{
		Action:      tea.String(action),
		Version:     tea.String("2019-05-31"),
		Protocol:    tea.String("HTTPS"),
		Method:      tea.String("POST"),
		AuthType:    tea.String("AK"),
		Style:       tea.String("RPC"),
		Pathname:    tea.String("/"),
		ReqBodyType: tea.String("formData"),
		BodyType:    tea.String("json"),
	}
	q := map[string]*string{}
	for k, v := range query {
		if v == nil {
			continue
		}
		q[k] = tea.String(fmt.Sprint(v))
	}
	req := &openapi.OpenApiRequest{Query: q}
	runtime := &util.RuntimeOptions{Autoretry: tea.Bool(false)}

	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 400 * time.Millisecond)
		}
		cl.throttle()
		resp, err := cl.c.CallApi(params, req, runtime)
		if err != nil {
			lastErr = err
			if isRetryable(err) {
				continue
			}
			return nil, err
		}
		body, _ := resp["body"].(map[string]any)
		if body == nil {
			return nil, fmt.Errorf("action %s: empty body", action)
		}
		// MSE returns Success:true with ErrorCode:"Success" on success; only
		// treat a real error code as a failure.
		if ok, present := body["Success"].(bool); present && !ok {
			return body, fmt.Errorf("action %s: %v: %v", action, body["ErrorCode"], body["Message"])
		}
		if ec, ok := body["ErrorCode"].(string); ok && ec != "" && ec != "Success" {
			return body, fmt.Errorf("action %s: %s: %v", action, ec, body["Message"])
		}
		return body, nil
	}
	return nil, fmt.Errorf("action %s: exhausted retries: %w", action, lastErr)
}

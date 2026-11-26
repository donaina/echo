// Package walletclient is the transfer service's client for the wallet's
// internal API. Every call is idempotent at the wallet, which is why a
// transport error is retried rather than parked.
package walletclient

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/donaina/echo/internal/transfer"
)

// Client calls the wallet's internal API with the shared service token.
type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

// New returns a Client for baseURL.
func New(baseURL, token string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("wallet client: %q is not a URL", baseURL)
	}
	return &Client{base: u, token: token, http: &http.Client{Timeout: 5 * time.Second}}, nil
}

// Hold reserves money in the source wallet.
func (c *Client) Hold(ctx context.Context, holdID, accountID string, amount int64, currency, reference string, ttl time.Duration) error {
	return c.do(ctx, http.MethodPost, "/holds", map[string]any{
		"hold_id":      holdID,
		"account_id":   accountID,
		"amount_minor": amount,
		"currency":     currency,
		"reference":    reference,
		"ttl_seconds":  int64(ttl.Seconds()),
	}, nil)
}

// Capture turns the hold into money movement and returns the transaction id
// compensation will reverse.
func (c *Client) Capture(ctx context.Context, holdID string) (string, error) {
	var out struct {
		Transaction struct {
			TransactionID string `json:"transaction_id"`
		} `json:"transaction"`
	}
	if err := c.do(ctx, http.MethodPost, "/holds/"+holdID+"/capture", nil, &out); err != nil {
		return "", err
	}
	if out.Transaction.TransactionID == "" {
		return "", fmt.Errorf("wallet: capture returned no transaction")
	}
	return out.Transaction.TransactionID, nil
}

// Release returns the reservation when the transfer fails before capture.
func (c *Client) Release(ctx context.Context, holdID string) error {
	return c.do(ctx, http.MethodPost, "/holds/"+holdID+"/release",
		map[string]string{"reason": "transfer failed"}, nil)
}

// Reverse compensates a capture. An already-reversed transaction is success:
// the reversal is idempotent by the original's unique reversal link.
func (c *Client) Reverse(ctx context.Context, transactionID, reason string) error {
	err := c.do(ctx, http.MethodPost, "/transactions/"+transactionID+"/reverse",
		map[string]string{"reason": reason}, nil)
	var rej *transfer.Rejection
	if stderrors.As(err, &rej) && rej.Code == "conflict" {
		return nil
	}
	return err
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("wallet: encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, reader)
	if err != nil {
		return fmt.Errorf("wallet: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Echo-Service-Token", c.token)
	req.Header.Set("X-Echo-Caller", "transfer")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("wallet: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("wallet: read response: %w", err)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out != nil && len(data) > 0 {
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("wallet: decode response: %w", err)
			}
		}
		return nil
	}

	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(data, &problem)
	switch {
	case resp.StatusCode >= 500:
		// The wallet is idempotent; a retry is safe.
		return fmt.Errorf("wallet: %s %s: status %d", method, path, resp.StatusCode)
	case problem.Code == "request_in_progress":
		return fmt.Errorf("wallet: %s %s: request in progress", method, path)
	default:
		return &transfer.Rejection{Code: problem.Code, Detail: problem.Detail}
	}
}

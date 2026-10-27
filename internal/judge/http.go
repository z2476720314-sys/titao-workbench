package judge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"
)

// DoVisionRequest completes a bounded reply before decoding, retrying transient failures once.
// It is used for read-only visual judgments, never image generation.
func DoVisionRequest(req *http.Request, timeout time.Duration) (*http.Response, error) {
	ctx := req.Context()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	req = req.Clone(ctx)
	client := &http.Client{}
	for attempt := 0; ; attempt++ {
		resp, err := client.Do(req)
		if err == nil {
			// Four MiB matches the largest existing vision consumer limit.
			data, readErr := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
			resp.Body.Close()
			resp.Body = http.NoBody
			if readErr != nil {
				err = readErr
				if resp.StatusCode >= 400 && resp.StatusCode < 500 {
					err = fmt.Errorf("HTTP %d: %w", resp.StatusCode, readErr)
				}
			} else if len(data) > 4<<20 {
				return nil, fmt.Errorf("视觉模型响应超过 4 MiB 限制")
			} else {
				resp.Body = io.NopCloser(bytes.NewReader(data))
			}
		}
		var networkError net.Error
		retry := errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
		retry = retry || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED)
		// Winsock uses 10054/10053, distinct from Go's portable errno values on Windows.
		retry = retry || errors.Is(err, syscall.Errno(10054)) || errors.Is(err, syscall.Errno(10053))
		retry = retry || (errors.As(err, &networkError) && networkError.Timeout())
		if resp != nil {
			if (resp.StatusCode >= 400 && resp.StatusCode < 500) || resp.Header.Get("Retry-After") != "" {
				retry = false
			} else if resp.StatusCode >= 500 && resp.StatusCode <= 599 {
				retry = true
			}
		}
		if attempt == 1 || !retry || req.GetBody == nil || ctx.Err() != nil {
			if err != nil {
				if resp != nil {
					resp.Body.Close()
				}
				return nil, err
			}
			return resp, nil
		}
		body, bodyErr := req.GetBody()
		if bodyErr != nil {
			if err != nil {
				return nil, err
			}
			return resp, nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			body.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
		req = req.Clone(ctx)
		req.Body = body
	}
}

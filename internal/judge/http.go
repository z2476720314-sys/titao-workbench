package judge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"
)

// DoVisionRequest completes a bounded reply before decoding, retrying transient failures once.
// It is used for read-only visual judgments, never image generation.
func DoVisionRequest(req *http.Request, timeout time.Duration, beforeRequest ...func() error) (*http.Response, error) {
	ctx := req.Context()
	remaining := timeout
	client := &http.Client{Transport: visionTransport()}
	for attempt := 0; ; attempt++ {
		if len(beforeRequest) > 0 && beforeRequest[0] != nil {
			if err := beforeRequest[0](); err != nil {
				if req.Body != nil {
					req.Body.Close()
				}
				return nil, err
			}
		}
		requestCtx := ctx
		if timeout > 0 {
			if remaining <= 0 {
				return nil, context.DeadlineExceeded
			}
			var cancel context.CancelFunc
			requestCtx, cancel = context.WithTimeout(ctx, remaining)
			defer cancel()
		}
		req = req.Clone(requestCtx)
		started := time.Now()
		resp, err := client.Do(req)
		if err == nil {
			if resp.StatusCode >= 200 && resp.StatusCode < 300 && strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
				data, streamErr := readChatStream(resp.Body)
				resp.Body.Close()
				if streamErr != nil {
					return nil, streamErr
				}
				resp.Body = io.NopCloser(bytes.NewReader(data))
				resp.Header.Set("Content-Type", "application/json")
				resp.ContentLength = int64(len(data))
				return resp, nil
			}
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
		remaining -= time.Since(started)
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
		if attempt == 1 || !retry || req.GetBody == nil || requestCtx.Err() != nil {
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
		started = time.Now()
		select {
		case <-requestCtx.Done():
			timer.Stop()
			body.Close()
			return nil, requestCtx.Err()
		case <-timer.C:
		}
		remaining -= time.Since(started)
		req = req.Clone(ctx)
		req.Body = body
	}
}

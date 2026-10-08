package gpu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type scriptedRunner struct {
	values  []uint32
	err     error
	block   chan struct{}
	started chan struct{}
}

func (s *scriptedRunner) Compute(ctx context.Context, values []uint32) ([]uint32, error) {
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.values != nil {
		return s.values, nil
	}
	out := append([]uint32(nil), values...)
	for i := range out {
		out[i]++
	}
	return out, nil
}

func TestComputeHoldsShares(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	b := &Broker{
		Device: Device{Name: "Apple M4", Present: true},
		Shares: 1,
		Token:  "secret-token-secret-token-secret",
		Runner: &scriptedRunner{block: block, started: started},
	}
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()

	done := make(chan int, 1)
	go func() {
		done <- postCompute(t, srv.URL, b.Token, "1", `{"values":[1,2]}`)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first compute did not start")
	}
	if got := postCompute(t, srv.URL, b.Token, "1", `{"values":[4]}`); got != http.StatusTooManyRequests {
		t.Fatalf("second status = %d", got)
	}
	close(block)
	if got := <-done; got != http.StatusOK {
		t.Fatalf("first status = %d", got)
	}
}

func TestComputeRequiresToken(t *testing.T) {
	b := &Broker{
		Device: Device{Name: "Apple M4", Present: true},
		Shares: 4,
		Token:  "secret-token-secret-token-secret",
		Runner: &scriptedRunner{},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/compute", strings.NewReader(`{"values":[1]}`))
	rec := httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestComputeResult(t *testing.T) {
	b := &Broker{
		Device: Device{Name: "Apple M4", Present: true},
		Shares: 4,
		Token:  "secret-token-secret-token-secret",
		Runner: &scriptedRunner{},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/compute", strings.NewReader(`{"values":[1,2,3,4]}`))
	req.Header.Set("Authorization", "Bearer "+b.Token)
	req.Header.Set(SharesHeader, "2")
	rec := httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Device string   `json:"device"`
		Shares int      `json:"shares"`
		Values []uint32 `json:"values"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Device != "Apple M4" || body.Shares != 2 || len(body.Values) != 4 || body.Values[0] != 2 || body.Values[3] != 5 {
		t.Fatalf("body = %+v", body)
	}
	if b.used != 0 {
		t.Fatalf("shares still held: %d", b.used)
	}
}

func TestAdvertisedSharesWaitsForGateway(t *testing.T) {
	b := &Broker{Device: Device{Present: true}, Shares: 4}
	if b.AdvertisedShares() != 0 {
		t.Fatal("expected zero before the gateway listens")
	}
	b.gatewayUp.Store(true)
	if b.AdvertisedShares() != 4 {
		t.Fatalf("shares = %d", b.AdvertisedShares())
	}
	b.Device.Present = false
	if b.AdvertisedShares() != 0 {
		t.Fatal("expected zero without a device")
	}
}

func postCompute(t *testing.T, url, token, shares, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+"/v1/compute", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(SharesHeader, shares)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	return res.StatusCode
}

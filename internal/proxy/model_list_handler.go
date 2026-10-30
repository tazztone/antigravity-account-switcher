package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/quota"
)

const (
	// DefaultModelListTimeout bounds each per-account model list fan-out request.
	DefaultModelListTimeout = 5 * time.Second
	// DefaultModelListCacheTTL is how long a merged model list is reused before the
	// pool is queried again.
	DefaultModelListCacheTTL = 60 * time.Second
	// modelListFanOutConcurrency caps concurrent per-account upstream requests.
	modelListFanOutConcurrency = 4
	// modelListMaxBodyBytes bounds a single upstream model list response.
	modelListMaxBodyBytes = 8 << 20
)

// modelListCache stores the last merged model list along with the pool composition
// it was built from, so adding or removing an account invalidates it immediately.
type modelListCache struct {
	mu          sync.Mutex
	data        []byte
	accountHash string
	expiresAt   time.Time
}

// get returns the cached model list for the given pool composition, if still fresh.
func (c *modelListCache) get(accountHash string, now time.Time) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data == nil || c.accountHash != accountHash || !now.Before(c.expiresAt) {
		return nil
	}
	return c.data
}

// put stores a merged model list for the given pool composition.
func (c *modelListCache) put(accountHash string, data []byte, now time.Time, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data = data
	c.accountHash = accountHash
	c.expiresAt = now.Add(ttl)
}

// isModelListRequest reports whether path is a model catalog endpoint whose answer
// should describe the whole pool instead of a single account.
func isModelListRequest(path string) bool {
	return strings.HasSuffix(path, ":fetchAvailableModels") ||
		strings.HasSuffix(path, "/fetchAvailableModels")
}

// serveMergedModelList answers a model catalog request with the union of the models
// every enabled account in the pool can serve, so the editor picker exposes the full
// entitlement of the pool rather than only the active account's.
//
// It returns false when the request must fall back to the regular single-account
// proxy path, e.g. when the pool is empty or no account answered upstream.
func (h *ProxyHandler) serveMergedModelList(w http.ResponseWriter, r *http.Request, buffered *BufferedRequest) bool {
	if r.Method != http.MethodPost || h.accountRepo == nil || buffered == nil {
		return false
	}

	ctx := r.Context()
	accounts, err := h.accountRepo.List(ctx)
	if err != nil {
		return false
	}
	candidates := enabledModelListAccounts(accounts)
	if len(candidates) == 0 {
		return false
	}

	accountHash := accountSetHash(candidates)
	if cached := h.modelListCache.get(accountHash, time.Now()); cached != nil {
		writeModelListResponse(w, cached)
		return true
	}

	type accountModels struct {
		accountID string
		body      []byte
	}
	results := make([]accountModels, len(candidates))
	found := make([]bool, len(candidates))

	var wg sync.WaitGroup
	sem := make(chan struct{}, modelListFanOutConcurrency)
	for i, acc := range candidates {
		wg.Add(1)
		go func(idx int, account *domain.Account) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			reqCtx, cancel := context.WithTimeout(ctx, h.cfg.ModelListTimeout)
			defer cancel()

			body, fetchErr := h.fetchModelListForAccount(reqCtx, r, buffered, h.ensureFreshToken(reqCtx, account))
			if fetchErr != nil {
				return
			}
			results[idx] = accountModels{accountID: account.ID, body: body}
			found[idx] = true
		}(i, acc)
	}
	wg.Wait()

	var bodies [][]byte
	for i := range results {
		if !found[i] {
			continue
		}
		bodies = append(bodies, results[i].body)
	}
	if len(bodies) == 0 {
		return false
	}

	merged, mergeErr := MergeModelListResponses(bodies)
	if mergeErr != nil {
		return false
	}

	// Keep per-model eligibility routing fresh without waiting for the dashboard poller.
	persistCtx := context.WithoutCancel(ctx)
	for i := range results {
		if found[i] {
			h.persistAccountModels(persistCtx, results[i].accountID, results[i].body)
		}
	}

	h.modelListCache.put(accountHash, merged, time.Now(), DefaultModelListCacheTTL)
	writeModelListResponse(w, merged)
	return true
}

// enabledModelListAccounts returns the accounts whose entitlements belong in the
// merged list, with the active account first so its defaults win the merge.
func enabledModelListAccounts(accounts []*domain.Account) []*domain.Account {
	enabled := make([]*domain.Account, 0, len(accounts))
	var active *domain.Account
	for _, acc := range accounts {
		if acc == nil || acc.Status == domain.AccountStatusDisabled {
			continue
		}
		if acc.IsActive && active == nil {
			active = acc
			continue
		}
		enabled = append(enabled, acc)
	}
	if active != nil {
		enabled = append([]*domain.Account{active}, enabled...)
	}
	return enabled
}

// accountSetHash identifies a pool composition by its sorted account IDs.
func accountSetHash(accounts []*domain.Account) string {
	ids := make([]string, 0, len(accounts))
	for _, acc := range accounts {
		ids = append(ids, acc.ID)
	}
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// ensureFreshToken refreshes acc's access token when it is expired or close to it.
// It returns a copy carrying the renewed credentials, leaving the repository value
// untouched when no refresh is needed or the refresh fails.
func (h *ProxyHandler) ensureFreshToken(ctx context.Context, acc *domain.Account) *domain.Account {
	if acc == nil || h.tokenRefresher == nil || acc.RefreshToken == "" || !acc.IsTokenExpired(h.cfg.TokenExpiryMargin) {
		return acc
	}

	access, expiry, err := h.tokenRefresher.RefreshToken(ctx, acc.RefreshToken)
	if err != nil || access == "" {
		return acc
	}
	_ = h.accountRepo.UpdateToken(ctx, acc.ID, access, expiry)

	refreshed := *acc
	refreshed.AccessToken = access
	refreshed.TokenExpiry = expiry
	return &refreshed
}

// fetchModelListForAccount replays the buffered model catalog request upstream with
// one account's bearer token and returns the raw response body.
func (h *ProxyHandler) fetchModelListForAccount(
	ctx context.Context,
	r *http.Request,
	buffered *BufferedRequest,
	acc *domain.Account,
) ([]byte, error) {
	if acc == nil || acc.AccessToken == "" {
		return nil, errors.New("account has no access token")
	}

	body := buffered.Bytes()
	destURL := h.upstreamURL(r, r.URL.Path, r.URL.RawQuery)

	outReq, err := http.NewRequestWithContext(ctx, r.Method, destURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	outReq.ContentLength = int64(len(body))
	outReq.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}

	copyRequestHeaders(outReq.Header, r.Header)
	if len(body) > 0 {
		outReq.Header.Set("Content-Length", strconv.Itoa(len(body)))
	}
	outReq.Host = h.targetURL.Host
	setForwardingHeaders(outReq, r)
	outReq.Header.Set("Authorization", "Bearer "+acc.AccessToken)
	// The response must be readable to be merged, so ask for plaintext explicitly.
	outReq.Header.Set("Accept-Encoding", "identity")

	resp, err := h.client.Do(outReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		return nil, fmt.Errorf("model list request for account %s returned status %d", acc.ID, resp.StatusCode)
	}
	return decodeModelListBody(resp)
}

// decodeModelListBody reads a model list response body, transparently undoing gzip
// compression in case the upstream ignored the identity encoding request.
func decodeModelListBody(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, errors.New("model list response has no body")
	}
	var reader io.Reader = io.LimitReader(resp.Body, modelListMaxBodyBytes)
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("Content-Encoding")), "gzip") {
		gzipReader, err := gzip.NewReader(reader)
		if err != nil {
			return nil, err
		}
		defer gzipReader.Close()
		reader = io.LimitReader(gzipReader, modelListMaxBodyBytes)
	}
	return io.ReadAll(reader)
}

// persistAccountModels stores the models one account reported, so per-model
// eligibility routing can use them immediately.
func (h *ProxyHandler) persistAccountModels(ctx context.Context, accountID string, body []byte) {
	models, err := quota.ParseAvailableModelsResponse(body)
	if err != nil || len(models) == 0 {
		return
	}
	_ = h.accountRepo.UpsertAccountModels(ctx, accountID, models)
}

// writeModelListResponse emits a JSON model catalog response.
func writeModelListResponse(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

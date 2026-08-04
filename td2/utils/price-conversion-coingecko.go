package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultCoinGeckoApiEndpoint = "https://api.coingecko.com"
	coinGeckoDemoKeyHeader      = "x-cg-demo-api-key"
	coinGeckoCacheKey           = "crypto_price_coingecko"
)

// CoinGeckoClient handles API requests to CoinGecko's /simple/price endpoint.
// Unlike CoinMarketCap, a single request covers every configured id, and CoinGecko
// silently omits unknown/unlisted ids from the response instead of erroring, so no
// per-id retry/skip loop is needed.
type CoinGeckoClient struct {
	apiKey          string
	currency        string
	cacheExpiration int
	ids             []string
	apiEndpoint     string
	httpClient      *http.Client
	cacheClient     *TenderdutyCache
}

// NewCoinGeckoClient creates a new client. apiKey may be empty: CoinGecko's
// /simple/price endpoint works without a key, just at a lower rate limit.
func NewCoinGeckoClient(apiKey string, currency string, cacheClient *TenderdutyCache, cacheExpiration int, ids []string) *CoinGeckoClient {
	return &CoinGeckoClient{
		apiKey:          apiKey,
		currency:        currency,
		cacheExpiration: cacheExpiration,
		cacheClient:     cacheClient,
		ids:             ids,
		apiEndpoint:     defaultCoinGeckoApiEndpoint,
		httpClient:      &http.Client{Timeout: defaultRequestTimeout},
	}
}

var _ PriceConverter = (*CoinGeckoClient)(nil)

// GetPrices fetches cryptocurrency prices, using cache when available
func (c *CoinGeckoClient) GetPrices(ctx context.Context) (map[string]CryptoPrice, error) {
	cache, ok1 := c.cacheClient.Get(coinGeckoCacheKey)
	prices, ok2 := cache.(map[string]CryptoPrice)

	if !ok1 || !ok2 {
		var err error
		prices, err = c.fetchPricesFromAPI(ctx, c.ids, c.currency)
		if err != nil {
			return nil, err
		}
		c.cacheClient.Set(coinGeckoCacheKey, prices, time.Duration(c.cacheExpiration)*time.Hour)
	}

	return prices, nil
}

// GetPrice fetches the price for a specific id, using cache when available
func (c *CoinGeckoClient) GetPrice(ctx context.Context, slug string) (*CryptoPrice, error) {
	prices, err := c.GetPrices(ctx)
	if err != nil {
		return nil, err
	}

	if prices != nil {
		if price, exists := prices[strings.ToLower(slug)]; exists {
			return &price, nil
		}
	}

	return nil, fmt.Errorf("slug '%s' not found", slug)
}

// fetchPricesFromAPI makes a single bulk request to CoinGecko for all configured ids.
func (c *CoinGeckoClient) fetchPricesFromAPI(ctx context.Context, ids []string, currency string) (map[string]CryptoPrice, error) {
	if len(ids) == 0 {
		return map[string]CryptoPrice{}, nil
	}

	req, err := http.NewRequestWithContext(ctx, "GET", c.apiEndpoint+"/api/v3/simple/price", nil)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		req.Header.Add(coinGeckoDemoKeyHeader, c.apiKey)
	}
	req.Header.Add("Accept", "application/json")

	q := req.URL.Query()
	q.Add("ids", joinStrings(ids, ","))
	q.Add("vs_currencies", strings.ToLower(currency))
	q.Add("include_last_updated_at", "true")
	req.URL.RawQuery = q.Encode()

	resp, err := c.httpClient.Do(req) //#nosec G704 -- URL is from operator-supplied config
	if err != nil {
		return nil, fmt.Errorf("error fetching prices from CoinGecko: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			fmt.Printf("Error closing CoinGecko response body: %v\n", cerr)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading CoinGecko response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CoinGecko API error (status %d): %s", resp.StatusCode, string(body))
	}

	return parseCoinGeckoResponse(body, currency)
}

// parseCoinGeckoResponse normalizes a raw CoinGecko /simple/price JSON body (e.g.
// {"cosmos":{"usd":4.32,"last_updated_at":1735856789}}) into the shared CryptoPrice
// map. It is a pure function with no network/cache dependency so it can be unit
// tested directly against literal JSON fixtures. Ids present in the response but
// missing a quote for the requested currency are silently skipped, matching
// CoinGecko's own behavior of silently omitting unlisted ids from the response.
func parseCoinGeckoResponse(body []byte, currency string) (map[string]CryptoPrice, error) {
	var raw map[string]map[string]float64
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse CoinGecko response: %w", err)
	}

	currencyKey := strings.ToLower(currency)
	result := make(map[string]CryptoPrice, len(raw))
	for id, values := range raw {
		price, ok := values[currencyKey]
		if !ok {
			continue
		}
		lastUpdated := time.Now()
		if ts, ok := values["last_updated_at"]; ok {
			lastUpdated = time.Unix(int64(ts), 0)
		}
		result[id] = CryptoPrice{
			Slug:        id,
			Currency:    currency,
			Price:       price,
			LastUpdated: lastUpdated,
		}
	}
	return result, nil
}

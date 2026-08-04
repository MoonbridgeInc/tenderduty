package utils

import (
	"testing"
	"time"
)

func TestParseCoinGeckoResponse(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		currency string
		want     map[string]float64 // slug -> expected price
		wantErr  bool
	}{
		{
			name:     "multi-id happy path",
			body:     `{"cosmos":{"usd":4.32,"last_updated_at":1735856789},"osmosis":{"usd":0.21,"last_updated_at":1735856790}}`,
			currency: "USD",
			want:     map[string]float64{"cosmos": 4.32, "osmosis": 0.21},
		},
		{
			name:     "currency lookup is case-insensitive against the response",
			body:     `{"cosmos":{"usd":4.32}}`,
			currency: "USD",
			want:     map[string]float64{"cosmos": 4.32},
		},
		{
			name:     "id missing the requested currency is skipped, not an error",
			body:     `{"cosmos":{"usd":4.32},"arkeo":{"eur":0.5}}`,
			currency: "USD",
			want:     map[string]float64{"cosmos": 4.32},
		},
		{
			name:     "missing last_updated_at falls back to now",
			body:     `{"cosmos":{"usd":4.32}}`,
			currency: "USD",
			want:     map[string]float64{"cosmos": 4.32},
		},
		{
			name:     "empty body returns empty map, no error",
			body:     `{}`,
			currency: "USD",
			want:     map[string]float64{},
		},
		{
			name:     "malformed JSON errors",
			body:     `not json`,
			currency: "USD",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCoinGeckoResponse([]byte(tt.body), tt.currency)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d entries, want %d (%+v)", len(got), len(tt.want), got)
			}
			for slug, wantPrice := range tt.want {
				entry, ok := got[slug]
				if !ok {
					t.Fatalf("missing expected slug %q in result %+v", slug, got)
				}
				if entry.Price != wantPrice {
					t.Errorf("slug %q: got price %v, want %v", slug, entry.Price, wantPrice)
				}
				if entry.Slug != slug {
					t.Errorf("slug %q: entry.Slug = %q, want %q", slug, entry.Slug, slug)
				}
				if entry.Currency != tt.currency {
					t.Errorf("slug %q: entry.Currency = %q, want %q", slug, entry.Currency, tt.currency)
				}
				if entry.LastUpdated.IsZero() {
					t.Errorf("slug %q: LastUpdated should never be zero", slug)
				}
			}
		})
	}
}

func TestParseCoinGeckoResponseUsesResponseTimestamp(t *testing.T) {
	body := `{"cosmos":{"usd":4.32,"last_updated_at":1735856789}}`
	got, err := parseCoinGeckoResponse([]byte(body), "USD")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entry, ok := got["cosmos"]
	if !ok {
		t.Fatalf("missing expected slug \"cosmos\" in result %+v", got)
	}
	want := time.Unix(1735856789, 0)
	if !entry.LastUpdated.Equal(want) {
		t.Errorf("LastUpdated = %v, want %v", entry.LastUpdated, want)
	}
}

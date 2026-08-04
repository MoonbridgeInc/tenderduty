# Setting up CoinGecko (fiat conversion & unclaimed rewards alerts)

Tenderduty can convert on-chain values into a fiat currency (e.g. USD) for display
on the dashboard and for the `unclaimed_rewards_alerts` feature. CoinGecko is an
alternative to CoinMarketCap for this — useful if a chain you're monitoring isn't
listed on CoinMarketCap (e.g. Arkeo) but is listed on CoinGecko, and CoinGecko has
a free tier.

#### 1. Create a CoinGecko account
Go to https://www.coingecko.com/en/api/pricing and sign up for a free account.

#### 2. Get a free Demo API key (optional)
CoinGecko's `/simple/price` endpoint works without a key. Generating a free "Demo"
API key from your account's Developer Dashboard is optional but recommended — it
raises your rate limit and requires no payment method.

#### 3. Add it to config.yml
```yaml
coin_gecko_api_token: <your key here> # optional; omit to use CoinGecko without a key, at a lower rate limit
convert_to_fiat:
  enabled: yes
  provider: coingecko
  currency: USD
```

#### 4. Chain `slug`
Each chain also needs its CoinGecko id set in the same `slug:` field used for
CoinMarketCap (e.g. `cosmos` for ATOM, `osmosis` for OSMO) for price lookups to
resolve — without it, fiat conversion silently returns no data even with a valid
API key. CoinGecko's id usually, but not always, matches CoinMarketCap's slug for
the same coin — if you're switching an existing config from CoinMarketCap to
CoinGecko, double check each chain's `slug` still resolves (the id is visible in
the URL of the coin's page on coingecko.com, e.g. coingecko.com/en/coins/cosmos).
Not every chain is listed on CoinGecko either (this is common for newer or testnet
chains), in which case fiat conversion simply won't be available for that chain.

NOTE: unknown/unlisted ids are silently omitted from CoinGecko's response (no error
is returned), so `convert_to_fiat`/`unclaimed_rewards_alerts` will just have no data
for that chain rather than failing loudly.

See [Setting up CoinMarketCap](coinmarketcap.md) for the other supported provider.

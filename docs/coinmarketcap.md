# Setting up CoinMarketCap (fiat conversion & unclaimed rewards alerts)

Tenderduty can convert on-chain values into a fiat currency (e.g. USD) for display
on the dashboard and for the `unclaimed_rewards_alerts` feature. This requires a
CoinMarketCap API key.

#### 1. Create a CoinMarketCap account
Go to https://coinmarketcap.com/api/ and click "Get Your API Key Now" / Sign Up.
Register with an email and password, then confirm your email.

#### 2. Pick a plan
The **Basic (Free)** plan is enough for this use case — 10,000 credits/month, which
comfortably covers periodic price lookups for a handful of chains.

#### 3. Copy your API key
Once registered, go to your account dashboard:<br />
https://pro.coinmarketcap.com/account<br />
Find the **API Key** section and copy the key shown there.

#### 4. Add it to config.yml
```yaml
coin_market_cap_api_token: <your key here>
convert_to_fiat:
  enabled: yes
  provider: coinmarketcap
  currency: USD
```

Each chain also needs its CoinMarketCap `slug` set (the URL slug CoinMarketCap uses
for that coin, e.g. `cosmos` for ATOM) for price lookups to resolve — without it,
fiat conversion silently returns no data even with a valid API key. Not every chain
is listed on CoinMarketCap (this is common for newer or testnet chains), in which
case fiat conversion simply won't be available for that chain.

NOTE: without a valid key, requests fail with an HTTP 401 in the logs, and
`convert_to_fiat`/`unclaimed_rewards_alerts` won't produce usable data.

See [Setting up CoinGecko](coingecko.md) for an alternative provider — useful for
chains not listed on CoinMarketCap.

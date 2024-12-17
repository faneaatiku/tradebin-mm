
# BeeZee DEX Market Making BOT

## Details
This trading bot can be used to fill market orders on BeeZee DEX and create small volume orders.

### Config file
```yaml
logging:
  level: "debug" # the logging level
orders:
  buy_no: 10 # the number of buy orders to place
  sell_no: 10 # the number of sell orders to place
  step: 0.1 # the amount between orders prices
  spread_steps: 5 # the number of steps to leave empty in the spread
  start_price: 100 # the price to start trading at - ignored if order book spread already exists
  min_amount: 500000
  max_amount: 2000000
market:
  base_denom: "factory/bze13gzq40che93tgfm9kzmkpjamah5nj0j73pyhqk/uvdl" # the base currency
  quote_denom: "ubze" # the quote currency
volume:
  strategy: "carousel" # the volume strategy: carousel - random up and down in order list ; spread - trade in spread
  min: 500000 # the minimum volume to trade
  max: 1000000 # the maximum volume to trade
  trade_interval: 60 # the number of seconds between orders
  extra_min: 2000000 # the minimum amount to extra trade for higher volume
  extra_max: 4000000 # the maximum amount to extra trade for higher volume
  extra_every: 3 # how often to add the maximum amount
wallet:
  mnemonic: "your SUPER SECRET mnemonic" # the mnemonic of the wallet
client:
  grpc: "grpc:443" # the host of the gRPC server
  tls: false
transaction:
  gas_adjustment: 1.5 # the gas adjustment to use for transactions
  gas_prices: "0.1ubze" # the gas prices to use for transactions
  chain_id: "beezee-1"
```

### Market making command  
This command will start the market and volume making with the provided config file
```shell
./tradebin PATH_TO_CONFIG.yml 
```

### Cancel all orders
```shell
./tradebin PATH_TO_CONFIG.yml cancel
```

## Build
```shell
GOOS=linux GOARCH=amd64 go build -o tradebin
```

### **This is experimental software, use at your own risk.**

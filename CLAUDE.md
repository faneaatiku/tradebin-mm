# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**tradebin-mm** is an automated market-making bot for the BeeZee DEX (Cosmos SDK chain). It fills order books with buy/sell orders and generates trading volume using configurable strategies.

## Build & Run

```bash
# Build
go build -o tradebin

# Cross-compile for Linux
GOOS=linux GOARCH=amd64 go build -o tradebin

# Run market making (default mode)
./tradebin config.yml

# Cancel all active orders
./tradebin config.yml cancel

# Withdraw all balances to an address
./tradebin config.yml withdraw-all <destination_address>

# Run tests (none currently exist)
go test ./...
```

## Configuration

YAML config files (see `config.yml.dist` for template, `prod_configs/` for examples). The runtime `config.yml` is gitignored since it contains the wallet mnemonic. Key sections: `orders`, `market`, `volume`, `wallet`, `client`, `transaction`, `logging`.

## Architecture

The application uses a layered architecture:

**Entry point** (`main.go`) — Parses CLI args, loads config, initializes dependencies, dispatches to one of three actions: market-making (default), cancel, or withdraw.

**Orchestrator** (`app.go`) — `App.Start()` runs OrdersFiller then VolumeMaker in a 30-second loop.

**Action layer** (`app/`):
- `OrdersFiller` (`orders.go`) — Places symmetric buy/sell orders around the market spread using configurable price steps, amounts, and hold-back intervals.
- `VolumeMaker` (`volume.go`) — Executes volume trading with two strategies: **carousel** (random trades against existing orders) and **spread** (trades within order book spread). Supports extra-volume trades every N intervals.
- `CancelAction` (`cancel.go`) and `WithdrawAction` (`withdraw.go`) — One-shot operations for cleanup.

**Service layer** (`app/service/`):
- `Broadcaster` (`broadcaster.go`) — Core transaction signing and broadcasting via Cosmos SDK. Caches account sequence numbers with mutex for throughput. Handles TLS/insecure gRPC, custom Bech32 prefixes, and sequence validation on simulation errors.
- `Orders` / `Sender` — Thin wrappers that construct and broadcast `MsgCreateOrder`, `MsgCancelOrder`, and `MsgSend`.

**Data provider layer** (`app/data_provider/`) — Queries blockchain state (balances, orders, markets, history) via gRPC. Order provider uses an in-memory cache with 240-minute TTL.

**Client layer** (`app/client/`) — Thread-safe gRPC connection management with TLS support and connection pooling.

**Support packages**:
- `app/wallet/` — BIP39 mnemonic to private key derivation (BIP44 path `m/44'/118'/0'/0/0`), custom Bech32 address encoding.
- `app/cache/` — Singleton in-memory cache with TTL expiration.
- `app/lock/` — Per-key mutex locking via `sync.Map`.
- `app/internal/` — Amount formatting (trailing zero trimming, step truncation), slice utilities, dependency validation errors.

## Key Dependencies

- `github.com/bze-alphateam/bze` — BZE chain types and message definitions
- `github.com/cosmos/cosmos-sdk v0.50.14` — Cosmos SDK for transaction building/signing
- `cosmossdk.io/math` — Decimal math for price/amount calculations
- `google.golang.org/grpc` — Blockchain node communication
- `github.com/sirupsen/logrus` — Structured logging

## Important Patterns

- All config structs have `.Validate()` methods called at startup; add validation for any new config fields.
- The `Broadcaster` manages sequence numbers with a mutex (`seqMtx`); `sequenceValid` flag controls when to re-fetch from chain vs use cached value.
- `go.mod` has a replace directive: `github.com/gogo/protobuf => github.com/regen-network/protobuf` — required for Cosmos SDK compatibility.
- The `config.yml` file contains wallet mnemonics and must never be committed.

## BFTScout -  Consensus Monitoring

A simple Go utility that subscribes to CometBFT consensus events and stores information about blocks and proposers per round in a database (GORM).

### Features
- Subscribes to `NewRound` and `NewBlock` events via WebSocket (`/websocket`).
- Optionally stores data in PostgreSQL (if `DATABASE_URL` is provided):
  - `blocks` table: height, hash, time, proposer address, proposer moniker, commit success flag.
  - `round_proposers` table: height, round, proposer address, proposer moniker, success flag.
- If multiple rounds occur, all are recorded; the round that finalized into a block is marked with `succeeded = true`.
- Automatic moniker resolution: fetches validator monikers from Cosmos REST API and matches them by consensus address.
- Real-time logging of processed blocks.

### Quick Start
```bash
export RPC_URL="http://localhost:26657"
export WS_PATH="/websocket"  # optional, default is /websocket
# export DATABASE_URL="postgres://postgres:postgres@localhost:5432/consensus?sslmode=disable"  # optional
export APP_API_URL="http://localhost:1317"  # optional, for moniker resolution

go run ./cmd/monitor
```

### Configuration

Environment variables:
- `RPC_URL` - CometBFT RPC endpoint (default: `http://localhost:26657`)
- `WS_PATH` - WebSocket path (default: `/websocket`)
- `APP_API_URL` - Cosmos SDK REST API base URL for moniker resolution (optional, default port: 1317; can point to the validator host if the port is exposed)
- `DATABASE_URL` - PostgreSQL connection URL (optional; if omitted, data is not persisted)

If `DATABASE_URL` is not provided, the application still runs fully (including TUI updates and logging) but skips all persistence.

The application automatically loads variables from `.env` file if present.

### Project Structure
- `cmd/monitor`: application entry point
- `internal/config`: configuration loaded from environment variables
- `internal/db`: database connection and migrations
- `internal/models`: GORM models (`Block`, `RoundProposer`)
- `internal/collector`: event subscription and data persistence
- `internal/moniker`: validator moniker resolution via RPC + REST API

### Moniker Resolution

The application automatically resolves validator monikers by:
1. Fetching validators from CometBFT RPC (`/validators`)
2. Fetching validators from Cosmos REST API (`/cosmos/staking/v1beta1/validators`)
3. Matching validators by `pub_key` to build consensus address → moniker mapping
4. Caching the mapping for 30 minutes

Monikers are resolved for all statuses (bonded, unbonding, unbonded) to maximize coverage.

### Building and Running

#### Using Make
```bash
make build    # Build binary to bin/bftscout
make run      # Run with environment variables (from .env if present)
make clean    # Remove build artifacts
```

#### Manual
```bash
# Build
go build -o bin/bftscout ./cmd/monitor

# Run
./bin/bftscout
```

#### Docker
```bash
# Build the container image locally
docker build -t bftscout:0.0.1 .

# Run the container with TUI attached to the current terminal
docker run --rm -it \
  --name bftscout \
  -e TERM="${TERM:-xterm-256color}" \
  -e RPC_URL="http://validator.bft:26657" \
  -e WS_PATH="/websocket" \
  -e APP_API_URL="http://node.bft:1317" \
  bftscout:0.0.1

```

### Database Schema

#### `blocks`
- `id` - Primary key
- `height` - Block height (unique index)
- `hash` - Block hash
- `time` - Block timestamp
- `proposer_address` - Consensus address of block proposer
- `proposer_moniker` - Moniker of proposer (if resolved)
- `commit_succeeded` - Whether block was successfully committed
- `created_at`, `updated_at` - Timestamps

#### `round_proposers`
- `id` - Primary key
- `height` - Block height (indexed, unique with round)
- `round` - Consensus round number (indexed, unique with height)
- `proposer_address` - Consensus address of proposer for this round
- `proposer_moniker` - Moniker of proposer (if resolved)
- `succeeded` - Whether this round succeeded (finalized into a block)
- `created_at`, `updated_at` - Timestamps

### Notes
- To determine the round that finalized into a block, the system marks the maximum observed round for that height as succeeded. If `NewRound` events were not received (e.g., after restart), a synthetic record is created for round `0` with the block's proposer.
- Proposer addresses are extracted from `NewRound` events, which reliably include the proposer information.
- Code comments are in English.

### Example: Docker Compose for Local Postgres
```yaml
version: '3.8'
services:
  postgres:
    image: postgres:16
    environment:
      POSTGRES_PASSWORD: postgres
      POSTGRES_USER: postgres
      POSTGRES_DB: consensus
    ports:
      - "5432:5432"
    volumes:
      - pgdata:/var/lib/postgresql/data
volumes:
  pgdata: {}
```

### License
See [LICENSE](LICENSE) file for details.

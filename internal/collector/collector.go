package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"consensus-monitoring/internal/config"
	"consensus-monitoring/internal/logger"
	"consensus-monitoring/internal/models"
	"consensus-monitoring/internal/moniker"
	"consensus-monitoring/internal/tui"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	rpccoretypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"gorm.io/gorm"
)

const (
	// Vote state constants
	voteStateNone      = 0 // No vote
	voteStateNilHash   = 1 // Vote with nil hash
	voteStateValidHash = 2 // Vote with valid hash

	// Vote type constants
	voteTypePrevote   = 1 // Prevote type
	voteTypePrecommit = 2 // Precommit type

	// Percentage calculation
	percentMultiplier = 100.0

	// Timeouts and intervals
	reconnectDelay           = 3 * time.Second
	unsubscribeTimeout       = 2 * time.Second
	cleanupDelay             = 500 * time.Millisecond
	chainInfoInterval        = 5 * time.Minute
	validatorsUpdateInterval = 30 * time.Second
	watchdogInterval         = 30 * time.Second
	resolveProposerTimeout   = 2 * time.Second

	// Buffer and batch sizes
	TUIChannelBufferSize = 100
	TUICloseDelay        = 200 * time.Millisecond
	votesBatchSize       = 1000

	// History and cache sizes
	maxBlockHistorySize = 20

	// HTTP client timeouts
	httpClientTimeout     = 5 * time.Second
	resolverClientTimeout = 10 * time.Second

	// UI layout constants
	minUIWidth        = 2
	hashDisplayLen    = 16
	maxPercentValue   = 100
	uiBorderWidth     = 2
	uiColumnCount     = 3
	uiColumnSpacing   = 4
	uiMinColWidth     = 24
	uiHeaderHeight    = 6
	uiBorderCharWidth = 2
)

type Collector struct {
	cfg             config.Config
	db              *gorm.DB
	client          *rpchttp.HTTP
	monres          *moniker.Resolver
	log             *logger.Logger
	lastBlockTime   time.Time
	lastBlockTimeMu sync.RWMutex
	prevBlockTime   time.Time // Time of previous block for block time calculation
	prevBlockTimeMu sync.RWMutex
	// Average block time calculation (sliding window of last N blocks)
	blockTimeHistory   []time.Duration // History of block times
	blockTimeHistoryMu sync.RWMutex
	maxHistorySize     int // Maximum number of blocks to keep in history
	// Vote accumulation: map[height][]*models.RoundVote
	pendingVotes map[int64][]*models.RoundVote
	votesMu      sync.Mutex // Protects pendingVotes
	// Current height vote states: map[validatorAddress]voteState
	currentVoteStates map[string]validatorVoteState
	voteStatesMu      sync.RWMutex // Protects currentVoteStates
	currentHeight     int64
	currentHeightMu   sync.RWMutex
	currentConsensus  int64
	currentRound      int32
	consensusStateMu  sync.RWMutex
	// TUI update channel
	tuiUpdateCh chan<- interface{} // Will send tui.BlockInfo
	// Cached validators ordering to avoid per-update sorting
	validatorsCache   []moniker.ValidatorInfo
	validatorsCacheMu sync.RWMutex
	// Last block information snapshot for consensus updates
	lastBlockInfo   blockInfoSnapshot
	lastBlockInfoMu sync.RWMutex
	chainInfo       chainMetadata
	chainInfoMu     sync.RWMutex
	httpClient      *http.Client
	metricsMu       sync.RWMutex
	proposerStats   map[string]localProposerStats
	voteStats       map[string]localVoteStats
}

// validatorVoteState tracks vote status for a validator
type validatorVoteState struct {
	PreVote   int // 0 = none, 1 = nil hash, 2 = valid hash
	PreCommit int // 0 = none, 1 = nil hash, 2 = valid hash
}

// blockInfoSnapshot mirrors tui.BlockInfo without importing the package
type blockInfoSnapshot struct {
	Height                   int64
	Hash                     string
	Time                     time.Time
	Proposer                 string
	Moniker                  string
	BlockTime                time.Duration
	AvgBlockTime             time.Duration
	ConsensusHeight          int64
	Round                    int32
	ChainID                  string
	CometBFT                 string
	PreVoteTotalPercent      float64
	PreVoteWithHashPercent   float64
	PreCommitTotalPercent    float64
	PreCommitWithHashPercent float64
}

type chainMetadata struct {
	ChainID  string
	CometBFT string
	LastSync time.Time
}

type validatorMetricsSnapshot struct {
	ProposerSuccessRate float64
	NonNilVoteRate      float64
	HasProposerStats    bool
	HasVoteStats        bool
}

type localProposerStats struct {
	total           int64
	success         int64
	lastBlockHeight int64 // Track last block height to avoid double-counting
}

type localVoteStats struct {
	total  int64
	nonNil int64
}

func (c *Collector) calculateVotePercentages() (float64, float64, float64, float64) {
	// Get total number of validators
	validators := c.getValidatorCacheSnapshot()
	totalValidators := len(validators)
	if totalValidators == 0 {
		return 0.0, 0.0, 0.0, 0.0
	}

	c.voteStatesMu.RLock()
	defer c.voteStatesMu.RUnlock()

	prevoteTotal := 0      // All prevotes (including nil)
	prevoteWithHash := 0   // Prevotes with non-nil hash
	precommitTotal := 0    // All precommits (including nil)
	precommitWithHash := 0 // Precommits with non-nil hash

	for _, state := range c.currentVoteStates {
		if state.PreVote > voteStateNone { // Any prevote (1 = nil, 2 = valid hash)
			prevoteTotal++
			if state.PreVote == voteStateValidHash { // valid hash
				prevoteWithHash++
			}
		}
		if state.PreCommit > voteStateNone { // Any precommit (1 = nil, 2 = valid hash)
			precommitTotal++
			if state.PreCommit == voteStateValidHash { // valid hash
				precommitWithHash++
			}
		}
	}

	// Calculate percentages based on total validators
	prevoteTotalPercent := float64(prevoteTotal) / float64(totalValidators) * percentMultiplier
	prevoteWithHashPercent := float64(prevoteWithHash) / float64(totalValidators) * percentMultiplier
	precommitTotalPercent := float64(precommitTotal) / float64(totalValidators) * percentMultiplier
	precommitWithHashPercent := float64(precommitWithHash) / float64(totalValidators) * percentMultiplier

	return prevoteTotalPercent, prevoteWithHashPercent, precommitTotalPercent, precommitWithHashPercent
}

func (c *Collector) snapshotToBlockInfo(snapshot blockInfoSnapshot) tui.BlockInfo {
	return tui.BlockInfo{
		Height:                   snapshot.Height,
		Hash:                     snapshot.Hash,
		Time:                     snapshot.Time,
		Proposer:                 snapshot.Proposer,
		Moniker:                  snapshot.Moniker,
		BlockTime:                snapshot.BlockTime,
		AvgBlockTime:             snapshot.AvgBlockTime,
		ConsensusHeight:          snapshot.ConsensusHeight,
		Round:                    snapshot.Round,
		ChainID:                  snapshot.ChainID,
		CometBFT:                 snapshot.CometBFT,
		PreVoteTotalPercent:      snapshot.PreVoteTotalPercent,
		PreVoteWithHashPercent:   snapshot.PreVoteWithHashPercent,
		PreCommitTotalPercent:    snapshot.PreCommitTotalPercent,
		PreCommitWithHashPercent: snapshot.PreCommitWithHashPercent,
	}
}

func (c *Collector) recordProposerRound(address string) {
	if address == "" {
		return
	}
	c.metricsMu.Lock()
	stats := c.proposerStats[address]
	stats.total++
	c.proposerStats[address] = stats
	c.metricsMu.Unlock()
}

func (c *Collector) recordProposerSuccess(address string, blockHeight int64) {
	if address == "" {
		return
	}
	c.metricsMu.Lock()
	stats := c.proposerStats[address]
	// Only record success if:
	// 1. The validator was already recorded as a proposer (total > 0)
	// 2. This block height hasn't been counted yet (to avoid double-counting)
	if stats.total > 0 && stats.lastBlockHeight != blockHeight {
		stats.success++
		stats.lastBlockHeight = blockHeight
		c.proposerStats[address] = stats
	}
	c.metricsMu.Unlock()
}

func (c *Collector) recordVote(address string, hasHash bool) {
	if address == "" {
		return
	}
	c.metricsMu.Lock()
	stats := c.voteStats[address]
	stats.total++
	if hasHash {
		stats.nonNil++
	}
	c.voteStats[address] = stats
	c.metricsMu.Unlock()
}

func (c *Collector) getChainInfo() chainMetadata {
	c.chainInfoMu.RLock()
	defer c.chainInfoMu.RUnlock()
	return c.chainInfo
}

func (c *Collector) updateSnapshotChainInfo(meta chainMetadata) {
	c.lastBlockInfoMu.Lock()
	snapshot := c.lastBlockInfo
	updated := false
	if meta.ChainID != "" && snapshot.ChainID != meta.ChainID {
		snapshot.ChainID = meta.ChainID
		updated = true
	}
	if meta.CometBFT != "" && snapshot.CometBFT != meta.CometBFT {
		snapshot.CometBFT = meta.CometBFT
		updated = true
	}
	if updated {
		c.lastBlockInfo = snapshot
	}
	c.lastBlockInfoMu.Unlock()

	if updated && snapshot.Height > 0 && c.tuiUpdateCh != nil {
		c.enqueueTUI(c.snapshotToBlockInfo(snapshot))
	}
}

func (c *Collector) updateSnapshotProposer(proposer, moniker string, broadcast bool) {
	if proposer == "" {
		return
	}
	if moniker == "" {
		moniker = c.resolveMoniker(proposer)
	}

	c.lastBlockInfoMu.Lock()
	snapshot := c.lastBlockInfo
	changed := snapshot.Proposer != proposer || snapshot.Moniker != moniker
	if changed {
		snapshot.Proposer = proposer
		snapshot.Moniker = moniker
		c.lastBlockInfo = snapshot
	}
	c.lastBlockInfoMu.Unlock()

	if changed && broadcast {
		c.enqueueTUI(c.snapshotToBlockInfo(snapshot))
	}
}

func (c *Collector) getCurrentProposer() string {
	c.lastBlockInfoMu.RLock()
	defer c.lastBlockInfoMu.RUnlock()
	return c.lastBlockInfo.Proposer
}

func NewCollector(cfg config.Config, db *gorm.DB, tuiUpdateCh chan<- interface{}, log *logger.Logger) (*Collector, error) {
	// rpchttp.New takes RPC base URL and WS path separately
	c, err := rpchttp.New(cfg.RPCURL, cfg.WSURL())
	if err != nil {
		return nil, err
	}
	collector := &Collector{
		cfg:               cfg,
		db:                db,
		client:            c,
		monres:            moniker.NewResolver(cfg.RPCURL, cfg.AppAPIURL, log),
		log:               log,
		pendingVotes:      make(map[int64][]*models.RoundVote),
		currentVoteStates: make(map[string]validatorVoteState),
		tuiUpdateCh:       tuiUpdateCh,
		blockTimeHistory:  make([]time.Duration, 0),
		maxHistorySize:    maxBlockHistorySize, // Keep last N blocks for average calculation
		httpClient:        &http.Client{Timeout: httpClientTimeout},
		proposerStats:     make(map[string]localProposerStats),
		voteStats:         make(map[string]localVoteStats),
	}

	return collector, nil
}

func (c *Collector) startChainInfoLoop(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(chainInfoInterval)
		defer ticker.Stop()

		_ = c.ensureChainInfo(ctx)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = c.ensureChainInfo(ctx)
			}
		}
	}()
}

func (c *Collector) ensureChainInfo(ctx context.Context) error {
	if c.cfg.RPCURL == "" || c.httpClient == nil {
		return nil
	}

	c.chainInfoMu.RLock()
	lastSync := c.chainInfo.LastSync
	c.chainInfoMu.RUnlock()
	if time.Since(lastSync) < 5*time.Minute {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.cfg.RPCURL, "/")+"/status", nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status http %d", resp.StatusCode)
	}

	var payload struct {
		Result struct {
			NodeInfo struct {
				Network string `json:"network"`
				Version string `json:"version"`
			} `json:"node_info"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}

	c.chainInfoMu.Lock()
	c.chainInfo = chainMetadata{
		ChainID:  payload.Result.NodeInfo.Network,
		CometBFT: payload.Result.NodeInfo.Version,
		LastSync: time.Now(),
	}
	c.chainInfoMu.Unlock()

	c.updateSnapshotChainInfo(chainMetadata{
		ChainID:  payload.Result.NodeInfo.Network,
		CometBFT: payload.Result.NodeInfo.Version,
	})

	return nil
}

func (c *Collector) Run(ctx context.Context) error {
	c.startChainInfoLoop(ctx)

	for {
		if err := c.ensureChainInfo(ctx); err != nil && c.log != nil {
			c.log.Printf("warn: failed to refresh chain info: %v", err)
		}
		if err := c.runLoop(ctx); err != nil {
			if ctx.Err() != nil {
				return nil // Context canceled, normal shutdown
			}
			// Only log actual errors, not planned reconnects
			if !strings.Contains(err.Error(), "reconnect:") {
				c.log.Printf("Run loop error: %v, reconnecting...", err)
			}

			// Check context before sleeping
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(reconnectDelay):
				// Continue to reconnect
			}
		} else if ctx.Err() != nil {
			// runLoop returned nil, check if context was canceled
			return nil
		}
	}
}

func (c *Collector) runLoop(ctx context.Context) error {
	// Create a cancellable context for this connection cycle
	// This ensures that when we reconnect, all old goroutines are properly stopped
	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel() // Ensure cleanup on exit

	// Cleanup existing client if present (reconnect case)
	if err := c.cleanupClient(loopCtx); err != nil {
		c.log.Printf("warning: error during client cleanup: %v", err)
	}

	// Create and start new client
	if err := c.initClient(); err != nil {
		return err
	}

	// Subscribe to all events
	subscriptions, err := c.subscribeToEvents(loopCtx)
	if err != nil {
		return err
	}

	// Initialize last block time
	c.updateLastBlockTime()

	// Start event handlers in separate goroutines
	// These will be stopped when loopCtx is canceled on reconnect
	c.startEventHandlers(loopCtx, subscriptions)

	// Start validators update goroutine for TUI
	c.startValidatorsUpdater(loopCtx)

	// Main loop: only handles context cancellation and watchdog
	return c.watchdogLoop(loopCtx)
}

// cleanupClient stops and cleans up existing client
func (c *Collector) cleanupClient(ctx context.Context) error {
	if c.client == nil {
		return nil
	}

	unsubCtx, cancel := context.WithTimeout(ctx, unsubscribeTimeout)
	defer cancel()

	_ = c.client.UnsubscribeAll(unsubCtx, "consmon")
	_ = c.client.Stop()
	c.client = nil

	time.Sleep(cleanupDelay) // Brief pause for cleanup
	return nil
}

// initClient creates and starts a new RPC client
func (c *Collector) initClient() error {
	client, err := rpchttp.New(c.cfg.RPCURL, c.cfg.WSURL())
	if err != nil {
		return fmt.Errorf("create rpc client: %w", err)
	}

	if err := client.Start(); err != nil {
		return fmt.Errorf("start rpc client: %w", err)
	}

	c.client = client
	return nil
}

// eventSubscriptions holds all event channel subscriptions
type eventSubscriptions struct {
	roundCh    <-chan rpccoretypes.ResultEvent
	completeCh <-chan rpccoretypes.ResultEvent
	proposeCh  <-chan rpccoretypes.ResultEvent
	blockCh    <-chan rpccoretypes.ResultEvent
	voteCh     <-chan rpccoretypes.ResultEvent
}

// subscribeToEvents subscribes to all CometBFT events
func (c *Collector) subscribeToEvents(ctx context.Context) (*eventSubscriptions, error) {
	subs := &eventSubscriptions{}

	// Required subscriptions
	roundCh, err := c.client.Subscribe(ctx, "consmon", "tm.event = 'NewRound'")
	if err != nil {
		return nil, fmt.Errorf("subscribe NewRound: %w", err)
	}
	subs.roundCh = roundCh

	blockCh, err := c.client.Subscribe(ctx, "consmon", "tm.event = 'NewBlock'")
	if err != nil {
		return nil, fmt.Errorf("subscribe NewBlock: %w", err)
	}
	subs.blockCh = blockCh

	voteCh, err := c.client.Subscribe(ctx, "consmon", "tm.event = 'Vote'")
	if err != nil {
		return nil, fmt.Errorf("subscribe Vote: %w", err)
	}
	subs.voteCh = voteCh

	// Optional subscriptions (may fail in some CometBFT versions)
	if completeCh, err := c.client.Subscribe(ctx, "consmon", "tm.event = 'CompleteProposal'"); err == nil {
		subs.completeCh = completeCh
	} else {
		c.log.Printf("warn: subscribe CompleteProposal failed: %v", err)
	}

	if proposeCh, err := c.client.Subscribe(ctx, "consmon", "tm.event = 'Propose'"); err == nil {
		subs.proposeCh = proposeCh
	} else {
		c.log.Printf("warn: subscribe Propose failed: %v", err)
	}

	c.log.Printf("Subscribed to events: NewRound, NewBlock, Vote, CompleteProposal?, Propose?")
	return subs, nil
}

// startEventHandlers starts goroutines for each event type
func (c *Collector) startEventHandlers(ctx context.Context, subs *eventSubscriptions) {
	// NewRound events
	c.startEventHandler(ctx, "NewRound", subs.roundCh, func(ev rpccoretypes.ResultEvent) {
		if ev.Data != nil {
			c.handleNewRound(ev)
		}
	})

	// CompleteProposal events
	if subs.completeCh != nil {
		c.startEventHandler(ctx, "CompleteProposal", subs.completeCh, func(ev rpccoretypes.ResultEvent) {
			if ev.Data != nil {
				c.handleProposerFromAttributes(ev)
			}
		})
	}

	// Propose events
	if subs.proposeCh != nil {
		c.startEventHandler(ctx, "Propose", subs.proposeCh, func(ev rpccoretypes.ResultEvent) {
			if ev.Data != nil {
				c.handleProposerFromAttributes(ev)
			}
		})
	}

	// NewBlock events
	c.startEventHandler(ctx, "NewBlock", subs.blockCh, func(ev rpccoretypes.ResultEvent) {
		if ev.Data == nil {
			return
		}
		c.updateLastBlockTime()
		c.handleNewBlock(ev)
	})

	// Vote events
	c.startEventHandler(ctx, "Vote", subs.voteCh, func(ev rpccoretypes.ResultEvent) {
		if ev.Data != nil {
			c.handleVote(ev)
		} else {
			c.log.Printf("Vote event received with nil Data")
		}
	})
}

// startEventHandler starts a goroutine to handle events from a channel
func (c *Collector) startEventHandler(ctx context.Context, name string, ch <-chan rpccoretypes.ResultEvent, handler func(rpccoretypes.ResultEvent)) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					c.log.Printf("%s event channel closed", name)
					return
				}
				handler(ev)
			}
		}
	}()
}

// updateLastBlockTime updates the last block time (thread-safe)
func (c *Collector) updateLastBlockTime() {
	c.lastBlockTimeMu.Lock()
	c.lastBlockTime = time.Now()
	c.lastBlockTimeMu.Unlock()
}

// startValidatorsUpdater periodically sends validators list to TUI
func (c *Collector) startValidatorsUpdater(ctx context.Context) {
	if c.tuiUpdateCh == nil || c.monres == nil {
		return
	}

	go func() {
		c.refreshValidatorCache()
		// Send initial validators list
		c.sendValidatorsToTUI()

		// Update every N seconds (same as validator cache TTL)
		ticker := time.NewTicker(validatorsUpdateInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.refreshValidatorCache()
				c.sendValidatorsToTUI()
			}
		}
	}()
}

// sendValidatorsToTUI sends current validators list to TUI
func (c *Collector) sendValidatorsToTUI() {
	if c.tuiUpdateCh == nil || c.monres == nil {
		return
	}

	validators := c.getValidatorCacheSnapshot()
	if len(validators) == 0 {
		return
	}

	metrics := c.getValidatorMetricsSnapshot()
	currentProposer := c.getCurrentProposer()

	// Get current consensus height
	consensusHeight, _ := c.getConsensusState()

	c.voteStatesMu.RLock()
	voteStates := make(map[string]validatorVoteState)
	for addr, state := range c.currentVoteStates {
		voteStates[addr] = state
	}
	c.voteStatesMu.RUnlock()

	// Only send if we have a current consensus height
	if consensusHeight == 0 {
		return
	}

	// Convert moniker.ValidatorInfo to tui.ValidatorInfo with vote states
	tuiValidators := make([]struct {
		Address             string
		Moniker             string
		VotingPower         int64
		PowerPercent        float64
		ProposerSuccessRate float64
		NonNilVoteRate      float64
		HasProposerStats    bool
		HasVoteStats        bool
		IsCurrentProposer   bool
		PreVote             int
		PreCommit           int
	}, len(validators))
	for i, v := range validators {
		state := voteStates[v.Address]
		stats := metrics[v.Address]
		tuiValidators[i] = struct {
			Address             string
			Moniker             string
			VotingPower         int64
			PowerPercent        float64
			ProposerSuccessRate float64
			NonNilVoteRate      float64
			HasProposerStats    bool
			HasVoteStats        bool
			IsCurrentProposer   bool
			PreVote             int
			PreCommit           int
		}{
			Address:             v.Address,
			Moniker:             v.Moniker,
			VotingPower:         v.VotingPower,
			PowerPercent:        v.PowerPercent,
			ProposerSuccessRate: stats.ProposerSuccessRate,
			NonNilVoteRate:      stats.NonNilVoteRate,
			HasProposerStats:    stats.HasProposerStats,
			HasVoteStats:        stats.HasVoteStats,
			IsCurrentProposer:   strings.EqualFold(v.Address, currentProposer),
			PreVote:             state.PreVote,
			PreCommit:           state.PreCommit,
		}
	}

	c.enqueueTUI(tuiValidators)
}

// sendVoteStatesUpdate sends updated vote states to TUI
func (c *Collector) sendVoteStatesUpdate() {
	// Reuse sendValidatorsToTUI which includes vote states
	c.sendValidatorsToTUI()
}

// watchdogLoop runs the main loop checking for missing blocks
func (c *Collector) watchdogLoop(ctx context.Context) error {
	watchdog := time.NewTicker(watchdogInterval)
	defer watchdog.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-watchdog.C:
			if c.shouldReconnect() {
				c.log.Printf("No blocks received for 30+ seconds, reconnecting WebSocket...")
				c.client = nil
				c.updateLastBlockTime()
				return fmt.Errorf("reconnect: no blocks for 30s")
			}
		}
	}
}

// shouldReconnect checks if we should reconnect due to missing blocks
func (c *Collector) shouldReconnect() bool {
	c.lastBlockTimeMu.RLock()
	defer c.lastBlockTimeMu.RUnlock()
	return time.Since(c.lastBlockTime) > 30*time.Second
}

/*{"jsonrpc":"2.0","method":"subscribe","id":1,"params":{"query":"tm.event='Vote'"}}*/

func (c *Collector) Close() error {
	if c.client != nil {
		// Stop the client (this will close connections and stop goroutines)
		_ = c.client.Stop()
		c.client = nil
	}
	return nil
}

func (c *Collector) handleNewRound(ev rpccoretypes.ResultEvent) {
	// Expect cmttypes.EventDataNewRound
	data, ok := ev.Data.(cmttypes.EventDataNewRound)
	if !ok {
		// some versions may wrap pointers
		if d2, ok2 := ev.Data.(*cmttypes.EventDataNewRound); ok2 && d2 != nil {
			data = *d2
			ok = true
		}
	}
	if !ok {
		c.log.Printf("unknown NewRound event data type: %T", ev.Data)
		return
	}

	height := data.Height
	round := data.Round

	heightChanged, roundChanged := c.updateConsensusState(height, round)
	if heightChanged {
		c.resetVoteStates()
		c.sendVoteStatesUpdate()
	}

	// 1) Try from event payload (stable across versions): value.proposer.address
	proposer := ""
	// CometBFT types typically expose Proposer with Address bytes
	// Guarded access to avoid panics on zero-values
	// NOTE: in some versions Proposer could be nil, so check and format accordingly
	if len(data.Proposer.Address) > 0 {
		proposer = fmt.Sprintf("%X", data.Proposer.Address)
	}
	// 2) Try from event attributes (tags)
	if proposer == "" {
		proposer = extractProposerFromAttributes(ev.Events)
	}
	// 2) If still empty, try to resolve via RPC consensus endpoints (best-effort)
	if proposer == "" {
		proposer = c.tryResolveProposerAddress(context.Background(), round)
	}

	proposerMoniker := c.resolveMoniker(proposer)

	c.recordProposerRound(proposer)
	c.updateSnapshotProposer(proposer, proposerMoniker, false)

	if heightChanged || roundChanged || proposer != "" {
		c.sendConsensusUpdate()
	}

	rec := models.RoundProposer{
		Height:          height,
		Round:           round,
		ProposerAddress: proposer,
		ProposerMoniker: proposerMoniker,
		Succeeded:       false,
	}
	if c.db == nil {
		return
	}
	// Try create; if exists, ignore
	_ = c.db.Create(&rec).Error

	// If create failed due to conflict, try to ensure proposer is updated if empty
	var existing models.RoundProposer
	if err := c.db.Where("height = ? AND round = ?", height, round).First(&existing).Error; err == nil {
		if existing.ProposerAddress == "" && proposer != "" {
			existing.ProposerAddress = proposer
			if existing.ProposerMoniker == "" {
				existing.ProposerMoniker = proposerMoniker
			}
			_ = c.db.Save(&existing).Error
		}
	}
}

// Removed CompleteProposal handler due to type differences across versions

func (c *Collector) handleNewBlock(ev rpccoretypes.ResultEvent) {
	// Expect cmttypes.EventDataNewBlock
	data, ok := ev.Data.(cmttypes.EventDataNewBlock)
	if !ok {
		if d2, ok2 := ev.Data.(*cmttypes.EventDataNewBlock); ok2 && d2 != nil {
			data = *d2
			ok = true
		}
	}
	if !ok {
		c.log.Printf("unknown NewBlock event data type: %T", ev.Data)
		return
	}

	blk := data.Block
	if blk == nil || blk.Header.Height == 0 {
		return
	}

	// Persist block basic info
	// Normalize address to uppercase hex (same format as in moniker resolver)
	proposerAddr := strings.TrimPrefix(strings.ToUpper(fmt.Sprintf("%X", blk.ProposerAddress)), "0X")
	proposerMoniker := c.resolveMoniker(proposerAddr)
	b := models.Block{
		Height:          blk.Header.Height,
		Hash:            fmt.Sprintf("%X", blk.Hash()),
		Time:            blk.Header.Time,
		ProposerAddress: proposerAddr,
		ProposerMoniker: proposerMoniker,
		CommitSucceeded: true,
	}

	c.recordProposerSuccess(proposerAddr, b.Height)

	if c.db != nil {
		_ = c.db.Where(models.Block{Height: b.Height}).Assign(b).FirstOrCreate(&b).Error
	}

	// Log processed block
	if proposerMoniker != "" {
		c.log.Printf("Block processed: height=%d hash=%s proposer=%s (%s)", b.Height, b.Hash[:16], proposerAddr[:16], proposerMoniker)
	} else {
		c.log.Printf("Block processed: height=%d hash=%s proposer=%s", b.Height, b.Hash[:16], proposerAddr[:16])
	}

	// Calculate block time (time since previous block)
	c.prevBlockTimeMu.RLock()
	prevTime := c.prevBlockTime
	c.prevBlockTimeMu.RUnlock()

	blockTime := time.Duration(0)
	if !prevTime.IsZero() {
		blockTime = blk.Header.Time.Sub(prevTime)
	}

	// Update previous block time
	c.prevBlockTimeMu.Lock()
	c.prevBlockTime = blk.Header.Time
	c.prevBlockTimeMu.Unlock()

	// Update block time history and calculate average
	var avgBlockTime time.Duration
	if blockTime > 0 {
		c.blockTimeHistoryMu.Lock()
		c.blockTimeHistory = append(c.blockTimeHistory, blockTime)
		// Keep only last N blocks
		if len(c.blockTimeHistory) > c.maxHistorySize {
			c.blockTimeHistory = c.blockTimeHistory[len(c.blockTimeHistory)-c.maxHistorySize:]
		}
		// Calculate average
		if len(c.blockTimeHistory) > 0 {
			var total time.Duration
			for _, bt := range c.blockTimeHistory {
				total += bt
			}
			avgBlockTime = total / time.Duration(len(c.blockTimeHistory))
		}
		c.blockTimeHistoryMu.Unlock()
	} else {
		// If no block time yet, get current average
		c.blockTimeHistoryMu.RLock()
		if len(c.blockTimeHistory) > 0 {
			var total time.Duration
			for _, bt := range c.blockTimeHistory {
				total += bt
			}
			avgBlockTime = total / time.Duration(len(c.blockTimeHistory))
		}
		c.blockTimeHistoryMu.RUnlock()
	}

	heightChanged, roundChanged := c.updateConsensusState(b.Height, 0)
	if heightChanged {
		c.resetVoteStates()
		c.sendVoteStatesUpdate()
	}

	// Send update to TUI if channel is available
	if c.tuiUpdateCh != nil {
		defer func(hc, rc bool) {
			if hc || rc {
				c.sendConsensusUpdate()
			}
		}(heightChanged, roundChanged)

		consensusHeight, consensusRound := c.getConsensusState()
		if consensusHeight < b.Height {
			consensusHeight = b.Height
		}

		meta := c.getChainInfo()
		prevoteTotal, prevoteWithHash, precommitTotal, precommitWithHash := c.calculateVotePercentages()

		snapshot := blockInfoSnapshot{
			Height:                   b.Height,
			Hash:                     b.Hash,
			Time:                     blk.Header.Time,
			Proposer:                 proposerAddr,
			Moniker:                  proposerMoniker,
			BlockTime:                blockTime,
			AvgBlockTime:             avgBlockTime,
			ConsensusHeight:          consensusHeight,
			Round:                    consensusRound,
			ChainID:                  meta.ChainID,
			CometBFT:                 meta.CometBFT,
			PreVoteTotalPercent:      prevoteTotal,
			PreVoteWithHashPercent:   prevoteWithHash,
			PreCommitTotalPercent:    precommitTotal,
			PreCommitWithHashPercent: precommitWithHash,
		}

		c.lastBlockInfoMu.Lock()
		c.lastBlockInfo = snapshot
		c.lastBlockInfoMu.Unlock()

		c.enqueueTUI(c.snapshotToBlockInfo(snapshot))
	}

	// Update current height and reset vote states for new block
	c.currentHeightMu.Lock()
	c.currentHeight = b.Height
	c.currentHeightMu.Unlock()

	// Flush votes for previous height (height - 1) as a batch
	prevHeight := b.Height - 1
	if prevHeight > 0 {
		c.flushVotesForHeight(prevHeight)
	}

	// Mark succeeded round for this height.
	// We may not know exact round from event; heuristic: mark max(round) for height.
	if c.db != nil {
		var r models.RoundProposer
		tx := c.db.Where("height = ?", b.Height).Order("round DESC").First(&r)
		if tx.Error == nil {
			if !r.Succeeded {
				r.Succeeded = true
				if r.ProposerAddress == "" {
					r.ProposerAddress = b.ProposerAddress
				}
				_ = c.db.Save(&r).Error
			}
		}

		if tx.Error != nil {
			// No round observed; create a synthetic round 0 as succeeded
			_ = c.db.Create(&models.RoundProposer{
				Height:          b.Height,
				Round:           0,
				ProposerAddress: b.ProposerAddress,
				Succeeded:       true,
			}).Error
		}
	}
}

// tryResolveProposerAddress best-effort fetch of proposer address for a given round
func (c *Collector) tryResolveProposerAddress(ctx context.Context, round int32) string {
	// short timeout to avoid blocking event loop
	tctx, cancel := context.WithTimeout(ctx, resolveProposerTimeout)
	defer cancel()

	endpoints := []string{"/consensus_state", "/dump_consensus_state"}
	for _, ep := range endpoints {
		url := c.cfg.RPCURL + ep
		req, err := http.NewRequestWithContext(tctx, http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			continue
		}
		defer func() { _ = resp.Body.Close() }()
		var payload interface{}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			continue
		}
		if addr := findProposerAddress(payload); addr != "" {
			addr = strings.TrimPrefix(strings.ToUpper(addr), "0X")
			return addr
		}
	}
	return ""
}

// findProposerAddress walks arbitrary JSON and returns first field value for keys like proposer/proposer_address
func findProposerAddress(v interface{}) string {
	switch val := v.(type) {
	case map[string]interface{}:
		for k, vv := range val {
			kl := strings.ToLower(k)
			if kl == "proposer" || kl == "proposer_address" || kl == "proposeraddress" {
				if s, ok := vv.(string); ok && s != "" {
					return s
				}
			}
			if s := findProposerAddress(vv); s != "" {
				return s
			}
		}
	case []interface{}:
		for _, it := range val {
			if s := findProposerAddress(it); s != "" {
				return s
			}
		}
	}
	return ""
}

// extractProposerFromAttributes checks event attributes for proposer/proposer_address key
func extractProposerFromAttributes(attrs map[string][]string) string {
	if attrs == nil {
		return ""
	}
	// Common keys could be "proposer", "proposer_address"
	for k, vals := range attrs {
		kl := strings.ToLower(k)
		if kl == "proposer" || kl == "proposer_address" || kl == "proposeraddress" {
			if len(vals) > 0 {
				v := strings.TrimSpace(vals[0])
				if v != "" {
					return strings.TrimPrefix(strings.ToUpper(v), "0X")
				}
			}
		}
	}
	return ""
}

// handleProposerFromAttributes updates RoundProposer using generic event attributes
func (c *Collector) handleProposerFromAttributes(ev rpccoretypes.ResultEvent) {
	attrs := ev.Events
	if attrs == nil {
		return
	}
	proposer := extractProposerFromAttributes(attrs)
	if proposer == "" {
		return
	}
	// Parse height and round from attributes (keys may vary in case)
	heightStr := firstAttr(attrs, []string{"height"})
	roundStr := firstAttr(attrs, []string{"round", "proposal_round"})
	if heightStr == "" || roundStr == "" {
		return
	}
	height, err1 := strconv.ParseInt(heightStr, 10, 64)
	round64, err2 := strconv.ParseInt(roundStr, 10, 32)
	if err1 != nil || err2 != nil {
		return
	}
	round := int32(round64)

	proposerMoniker := c.resolveMoniker(proposer)

	// Upsert into DB
	if c.db != nil {
		var rp models.RoundProposer
		if err := c.db.Where("height = ? AND round = ?", height, round).First(&rp).Error; err == nil {
			if rp.ProposerAddress == "" {
				rp.ProposerAddress = proposer
				if rp.ProposerMoniker == "" {
					rp.ProposerMoniker = proposerMoniker
				}
				_ = c.db.Save(&rp).Error
			}
		} else {
			_ = c.db.Create(&models.RoundProposer{
				Height:          height,
				Round:           round,
				ProposerAddress: proposer,
				ProposerMoniker: proposerMoniker,
				Succeeded:       false,
			}).Error
		}
	}

	c.recordProposerRound(proposer)
	c.updateSnapshotProposer(proposer, proposerMoniker, true)
}

func firstAttr(attrs map[string][]string, keys []string) string {
	for _, k := range keys {
		for ak, vals := range attrs {
			if strings.EqualFold(ak, k) && len(vals) > 0 {
				v := strings.TrimSpace(vals[0])
				if v != "" {
					return v
				}
			}
		}
	}
	return ""
}

func (c *Collector) resolveMoniker(consAddrHex string) string {
	if c.monres == nil || consAddrHex == "" {
		return ""
	}
	return c.monres.Resolve(consAddrHex)
}

func (c *Collector) enqueueTUI(msg interface{}) {
	if c.tuiUpdateCh == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			if c.log != nil {
				c.log.Printf("tui update send failed: %v", r)
			}
		}
	}()
	c.tuiUpdateCh <- msg
}

func (c *Collector) refreshValidatorCache() {
	if c.monres == nil {
		return
	}
	validators := c.monres.GetValidators()
	if len(validators) == 0 {
		return
	}

	c.validatorsCacheMu.Lock()
	c.validatorsCache = validators
	c.validatorsCacheMu.Unlock()
}

func (c *Collector) getValidatorCacheSnapshot() []moniker.ValidatorInfo {
	c.validatorsCacheMu.RLock()
	if len(c.validatorsCache) == 0 {
		c.validatorsCacheMu.RUnlock()
		c.refreshValidatorCache()
		c.validatorsCacheMu.RLock()
	}
	defer c.validatorsCacheMu.RUnlock()

	if len(c.validatorsCache) == 0 {
		return nil
	}

	snapshot := make([]moniker.ValidatorInfo, len(c.validatorsCache))
	copy(snapshot, c.validatorsCache)
	return snapshot
}

func (c *Collector) getValidatorMetricsSnapshot() map[string]validatorMetricsSnapshot {
	c.metricsMu.RLock()
	defer c.metricsMu.RUnlock()

	result := make(map[string]validatorMetricsSnapshot, len(c.proposerStats)+len(c.voteStats))

	for addr, stat := range c.proposerStats {
		if stat.total == 0 {
			continue
		}
		entry := result[addr]
		// Calculate percentage, ensuring it doesn't exceed 100%
		rate := float64(stat.success) / float64(stat.total) * percentMultiplier
		if rate > percentMultiplier {
			rate = percentMultiplier
		}
		entry.ProposerSuccessRate = rate
		entry.HasProposerStats = true
		result[addr] = entry
	}

	for addr, stat := range c.voteStats {
		if stat.total == 0 {
			continue
		}
		entry := result[addr]
		entry.NonNilVoteRate = float64(stat.nonNil) / float64(stat.total) * percentMultiplier
		entry.HasVoteStats = true
		result[addr] = entry
	}

	return result
}

func (c *Collector) updateConsensusState(height int64, round int32) (bool, bool) {
	heightChanged := false
	roundChanged := false

	c.consensusStateMu.Lock()
	defer c.consensusStateMu.Unlock()

	if height > c.currentConsensus {
		c.currentConsensus = height
		c.currentRound = round
		heightChanged = true
		roundChanged = true
	} else if height == c.currentConsensus && round > c.currentRound {
		c.currentRound = round
		roundChanged = true
	}

	return heightChanged, roundChanged
}

func (c *Collector) getConsensusState() (int64, int32) {
	c.consensusStateMu.RLock()
	defer c.consensusStateMu.RUnlock()
	return c.currentConsensus, c.currentRound
}

func (c *Collector) resetVoteStates() {
	c.voteStatesMu.Lock()
	c.currentVoteStates = make(map[string]validatorVoteState)
	c.voteStatesMu.Unlock()
}

func (c *Collector) sendConsensusUpdate() {
	consensusHeight, consensusRound := c.getConsensusState()

	c.lastBlockInfoMu.Lock()
	snapshot := c.lastBlockInfo

	if snapshot.Height == 0 {
		c.currentHeightMu.RLock()
		snapshot.Height = c.currentHeight
		c.currentHeightMu.RUnlock()
	}

	if snapshot.Height == 0 && consensusHeight == 0 {
		c.lastBlockInfoMu.Unlock()
		return
	}

	meta := c.getChainInfo()
	if snapshot.ChainID == "" {
		snapshot.ChainID = meta.ChainID
	}
	if snapshot.CometBFT == "" {
		snapshot.CometBFT = meta.CometBFT
	}

	snapshot.ConsensusHeight = consensusHeight
	snapshot.Round = consensusRound
	prevoteTotal, prevoteWithHash, precommitTotal, precommitWithHash := c.calculateVotePercentages()
	snapshot.PreVoteTotalPercent = prevoteTotal
	snapshot.PreVoteWithHashPercent = prevoteWithHash
	snapshot.PreCommitTotalPercent = precommitTotal
	snapshot.PreCommitWithHashPercent = precommitWithHash
	c.lastBlockInfo = snapshot
	c.lastBlockInfoMu.Unlock()

	c.enqueueTUI(c.snapshotToBlockInfo(snapshot))
}

// handleVote processes Vote events from WebSocket (includes both Prevote and Precommit)
// This function is called from a separate goroutine, so it needs to be thread-safe
// Votes are accumulated in memory and will be written to DB in batches when a new block arrives
func (c *Collector) handleVote(ev rpccoretypes.ResultEvent) {
	data, ok := ev.Data.(cmttypes.EventDataVote)
	if !ok {
		c.log.Printf("unknown Vote event data type: %T, data: %+v", ev.Data, ev.Data)
		return
	}
	if data.Vote == nil {
		c.log.Printf("Vote event has nil Vote field: %+v", data)
		return
	}
	vote := data.Vote

	heightChanged, roundChanged := c.updateConsensusState(vote.Height, vote.Round)
	if heightChanged {
		c.resetVoteStates()
		c.sendVoteStatesUpdate()
	}

	if heightChanged || roundChanged {
		c.sendConsensusUpdate()
	}

	// Determine vote type (1 = Prevote, 2 = Precommit)
	voteType := "prevote"
	if vote.Type == voteTypePrecommit { // VoteTypePrecommit
		voteType = "precommit"
	}

	// Extract block hash if present
	blockHash := ""
	hasHash := false
	if len(vote.BlockID.Hash) > 0 {
		blockHash = fmt.Sprintf("%X", vote.BlockID.Hash)
		hasHash = true
	}

	// Create RoundVote record (proposer will be filled later from RoundProposer)
	// Normalize address to uppercase hex (same format as in moniker resolver)
	validatorAddr := strings.TrimPrefix(strings.ToUpper(fmt.Sprintf("%X", vote.ValidatorAddress)), "0X")
	roundVote := &models.RoundVote{
		Height:           vote.Height,
		Round:            vote.Round,
		ValidatorAddress: validatorAddr,
		ValidatorMoniker: c.resolveMoniker(validatorAddr),
		ProposerAddress:  "", // Will be filled from RoundProposer when flushing
		VoteType:         voteType,
		BlockHash:        blockHash,
		Timestamp:        vote.Timestamp,
	}

	// Accumulate vote by height
	c.votesMu.Lock()
	c.pendingVotes[vote.Height] = append(c.pendingVotes[vote.Height], roundVote)
	c.votesMu.Unlock()

	c.recordVote(validatorAddr, hasHash)

	consensusHeight, _ := c.getConsensusState()
	if consensusHeight > 0 && vote.Height == consensusHeight {
		c.voteStatesMu.Lock()
		state := c.currentVoteStates[validatorAddr]
		if voteType == "prevote" {
			if hasHash {
				state.PreVote = 2 // valid hash
			} else {
				state.PreVote = 1 // nil hash
			}
		} else if voteType == "precommit" {
			if hasHash {
				state.PreCommit = 2 // valid hash
			} else {
				state.PreCommit = 1 // nil hash
			}
		}
		c.currentVoteStates[validatorAddr] = state
		c.voteStatesMu.Unlock()

		c.sendVoteStatesUpdate()
		// Also update consensus info to refresh progress bars
		c.sendConsensusUpdate()
	}
}

// flushVotesForHeight writes accumulated votes for a given height to the database in a batch
func (c *Collector) flushVotesForHeight(height int64) {
	// Extract and remove votes for this height
	c.votesMu.Lock()
	votes, exists := c.pendingVotes[height]
	if !exists || len(votes) == 0 {
		c.votesMu.Unlock()
		return
	}
	// Remove from map to free memory
	delete(c.pendingVotes, height)
	c.votesMu.Unlock()

	if c.db == nil {
		return
	}

	// Fetch proposers for all rounds of this height
	roundProposers := make(map[int32]string) // round -> proposer address
	var proposers []models.RoundProposer
	if err := c.db.Where("height = ?", height).Find(&proposers).Error; err == nil {
		for _, rp := range proposers {
			if rp.ProposerAddress != "" {
				roundProposers[rp.Round] = rp.ProposerAddress
			}
		}
	}

	// Fill proposer address for each vote
	for _, vote := range votes {
		if proposer, ok := roundProposers[vote.Round]; ok {
			vote.ProposerAddress = proposer
		}
	}

	// Batch insert votes (GORM CreateInBatches with batch size 1000)
	// This ensures all votes for a single block are written in one batch
	// Typical block has ~70-140 votes (70 validators * 2 vote types * rounds)
	if err := c.db.CreateInBatches(votes, votesBatchSize).Error; err != nil {
		c.log.Printf("error flushing votes for height %d: %v (votes count: %d)", height, err, len(votes))
	} else {
		c.log.Printf("Flushed %d votes for height %d", len(votes), height)
	}
}

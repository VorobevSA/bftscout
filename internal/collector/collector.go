package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"consensus-monitoring/internal/config"
	"consensus-monitoring/internal/models"
	"consensus-monitoring/internal/moniker"
	"strconv"
	"strings"
	"sync"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	rpccoretypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"gorm.io/gorm"
)

type Collector struct {
	cfg             config.Config
	db              *gorm.DB
	client          *rpchttp.HTTP
	monres          *moniker.Resolver
	lastBlockTime   time.Time
	lastBlockTimeMu sync.RWMutex
	// Vote accumulation: map[height][]*models.RoundVote
	pendingVotes map[int64][]*models.RoundVote
	votesMu      sync.Mutex // Protects pendingVotes
}

func NewCollector(cfg config.Config, db *gorm.DB) (*Collector, error) {
	// rpchttp.New takes RPC base URL and WS path separately
	c, err := rpchttp.New(cfg.RPCURL, cfg.WSURL())
	if err != nil {
		return nil, err
	}
	return &Collector{
		cfg:          cfg,
		db:           db,
		client:       c,
		monres:       moniker.NewResolver(cfg.RPCURL, cfg.AppAPIURL),
		pendingVotes: make(map[int64][]*models.RoundVote),
	}, nil
}

func (c *Collector) Run(ctx context.Context) error {
	for {
		if err := c.runLoop(ctx); err != nil {
			if ctx.Err() != nil {
				return nil // Context cancelled, normal shutdown
			}
			// Only log actual errors, not planned reconnects
			if !strings.Contains(err.Error(), "reconnect:") {
				log.Printf("Run loop error: %v, reconnecting...", err)
			}
			time.Sleep(3 * time.Second) // Brief pause before reconnecting
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
		log.Printf("warning: error during client cleanup: %v", err)
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
	// These will be stopped when loopCtx is cancelled on reconnect
	c.startEventHandlers(loopCtx, subscriptions)

	// Main loop: only handles context cancellation and watchdog
	return c.watchdogLoop(loopCtx)
}

// cleanupClient stops and cleans up existing client
func (c *Collector) cleanupClient(ctx context.Context) error {
	if c.client == nil {
		return nil
	}

	unsubCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	_ = c.client.UnsubscribeAll(unsubCtx, "consmon")
	c.client.Stop()
	c.client = nil

	time.Sleep(500 * time.Millisecond) // Brief pause for cleanup
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
		log.Printf("warn: subscribe CompleteProposal failed: %v", err)
	}

	if proposeCh, err := c.client.Subscribe(ctx, "consmon", "tm.event = 'Propose'"); err == nil {
		subs.proposeCh = proposeCh
	} else {
		log.Printf("warn: subscribe Propose failed: %v", err)
	}

	log.Printf("Subscribed to events: NewRound, NewBlock, Vote, CompleteProposal?, Propose?")
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
			log.Printf("Vote event received with nil Data")
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
					log.Printf("%s event channel closed", name)
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

// watchdogLoop runs the main loop checking for missing blocks
func (c *Collector) watchdogLoop(ctx context.Context) error {
	watchdog := time.NewTicker(30 * time.Second)
	defer watchdog.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-watchdog.C:
			if c.shouldReconnect() {
				log.Printf("No blocks received for 30+ seconds, reconnecting WebSocket...")
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
		c.client.Stop()
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
		log.Printf("unknown NewRound event data type: %T", ev.Data)
		return
	}

	height := data.Height
	round := data.Round

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
		proposer = c.tryResolveProposerAddress(context.Background(), height, int32(round))
	}

	rec := models.RoundProposer{
		Height:          height,
		Round:           int32(round),
		ProposerAddress: proposer,
		ProposerMoniker: c.resolveMoniker(proposer),
		Succeeded:       false,
	}
	// Try create; if exists, ignore
	_ = c.db.Create(&rec).Error

	// If create failed due to conflict, try to ensure proposer is updated if empty
	var existing models.RoundProposer
	if err := c.db.Where("height = ? AND round = ?", height, int32(round)).First(&existing).Error; err == nil {
		if existing.ProposerAddress == "" && proposer != "" {
			existing.ProposerAddress = proposer
			if existing.ProposerMoniker == "" {
				existing.ProposerMoniker = c.resolveMoniker(proposer)
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
		log.Printf("unknown NewBlock event data type: %T", ev.Data)
		return
	}

	blk := data.Block
	if blk == nil || blk.Header.Height == 0 {
		return
	}

	// Persist block basic info
	proposerAddr := fmt.Sprintf("%X", blk.ProposerAddress)
	proposerMoniker := c.resolveMoniker(proposerAddr)
	b := models.Block{
		Height:          blk.Header.Height,
		Hash:            fmt.Sprintf("%X", blk.Hash()),
		Time:            blk.Header.Time,
		ProposerAddress: proposerAddr,
		ProposerMoniker: proposerMoniker,
		CommitSucceeded: true,
	}
	_ = c.db.Where(models.Block{Height: b.Height}).Assign(b).FirstOrCreate(&b).Error

	// Log processed block
	if proposerMoniker != "" {
		log.Printf("Block processed: height=%d hash=%s proposer=%s (%s)", b.Height, b.Hash[:16], proposerAddr[:16], proposerMoniker)
	} else {
		log.Printf("Block processed: height=%d hash=%s proposer=%s", b.Height, b.Hash[:16], proposerAddr[:16])
	}

	// Flush votes for previous height (height - 1) as a batch
	prevHeight := b.Height - 1
	if prevHeight > 0 {
		c.flushVotesForHeight(prevHeight)
	}

	// Mark succeeded round for this height.
	// We may not know exact round from event; heuristic: mark max(round) for height.
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

// tryResolveProposerAddress best-effort fetch of proposer address for a given (height, round)
func (c *Collector) tryResolveProposerAddress(ctx context.Context, height int64, round int32) string {
	// short timeout to avoid blocking event loop
	tctx, cancel := context.WithTimeout(ctx, 2*time.Second)
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
		defer resp.Body.Close()
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
	// Upsert into DB
	var rp models.RoundProposer
	if err := c.db.Where("height = ? AND round = ?", height, round).First(&rp).Error; err == nil {
		if rp.ProposerAddress == "" {
			rp.ProposerAddress = proposer
			_ = c.db.Save(&rp).Error
		}
	} else {
		_ = c.db.Create(&models.RoundProposer{
			Height:          height,
			Round:           round,
			ProposerAddress: proposer,
			ProposerMoniker: "",
			Succeeded:       false,
		}).Error
	}
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

// handleVote processes Vote events from WebSocket (includes both Prevote and Precommit)
// This function is called from a separate goroutine, so it needs to be thread-safe
// Votes are accumulated in memory and will be written to DB in batches when a new block arrives
func (c *Collector) handleVote(ev rpccoretypes.ResultEvent) {
	data, ok := ev.Data.(cmttypes.EventDataVote)
	if !ok {
		log.Printf("unknown Vote event data type: %T, data: %+v", ev.Data, ev.Data)
		return
	}
	if data.Vote == nil {
		log.Printf("Vote event has nil Vote field: %+v", data)
		return
	}
	vote := data.Vote

	// Determine vote type (1 = Prevote, 2 = Precommit)
	voteType := "prevote"
	if vote.Type == 2 { // VoteTypePrecommit
		voteType = "precommit"
	}

	// Extract block hash if present
	blockHash := ""
	if len(vote.BlockID.Hash) > 0 {
		blockHash = fmt.Sprintf("%X", vote.BlockID.Hash)
	}

	// Create RoundVote record (proposer will be filled later from RoundProposer)
	roundVote := &models.RoundVote{
		Height:           vote.Height,
		Round:            vote.Round,
		ValidatorAddress: fmt.Sprintf("%X", vote.ValidatorAddress),
		ValidatorMoniker: c.resolveMoniker(fmt.Sprintf("%X", vote.ValidatorAddress)),
		ProposerAddress:  "", // Will be filled from RoundProposer when flushing
		VoteType:         voteType,
		BlockHash:        blockHash,
		Timestamp:        vote.Timestamp,
	}

	// Accumulate vote by height
	c.votesMu.Lock()
	c.pendingVotes[vote.Height] = append(c.pendingVotes[vote.Height], roundVote)
	c.votesMu.Unlock()
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
	if err := c.db.CreateInBatches(votes, 1000).Error; err != nil {
		log.Printf("error flushing votes for height %d: %v (votes count: %d)", height, err, len(votes))
	} else {
		log.Printf("Flushed %d votes for height %d", len(votes), height)
	}
}

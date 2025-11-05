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
}

func NewCollector(cfg config.Config, db *gorm.DB) (*Collector, error) {
	// rpchttp.New takes RPC base URL and WS path separately
	c, err := rpchttp.New(cfg.RPCURL, cfg.WSURL())
	if err != nil {
		return nil, err
	}
	return &Collector{cfg: cfg, db: db, client: c, monres: moniker.NewResolver(cfg.RPCURL, cfg.AppAPIURL)}, nil
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
	// Stop and cleanup existing client if present (reconnect case)
	if c.client != nil {
		unsubCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_ = c.client.UnsubscribeAll(unsubCtx, "consmon")
		cancel()
		c.client.Stop()
		c.client = nil
		time.Sleep(500 * time.Millisecond) // Brief pause for cleanup
	}

	// Create and start new client
	newClient, err := rpchttp.New(c.cfg.RPCURL, c.cfg.WSURL())
	if err != nil {
		return fmt.Errorf("create rpc client: %w", err)
	}
	c.client = newClient

	if err := c.client.Start(); err != nil {
		c.client = nil
		return fmt.Errorf("start rpc client: %w", err)
	}

	// Subscribe to NewRound, CompleteProposal/Propose (attributes-based), and NewBlock events
	roundCh, err := c.client.Subscribe(ctx, "consmon", "tm.event = 'NewRound'")
	if err != nil {
		return fmt.Errorf("subscribe NewRound: %w", err)
	}

	// These events often carry proposer/height/round in attributes depending on version/build
	completeCh, err := c.client.Subscribe(ctx, "consmon", "tm.event = 'CompleteProposal'")
	if err != nil {
		log.Printf("warn: subscribe CompleteProposal failed: %v", err)
	}
	proposeCh, err := c.client.Subscribe(ctx, "consmon", "tm.event = 'Propose'")
	if err != nil {
		log.Printf("warn: subscribe Propose failed: %v", err)
	}

	blockCh, err := c.client.Subscribe(ctx, "consmon", "tm.event = 'NewBlock'")
	if err != nil {
		return fmt.Errorf("subscribe NewBlock: %w", err)
	}

	log.Printf("Subscribed to events: NewRound, CompleteProposal?, Propose?, NewBlock")

	// Initialize last block time
	c.lastBlockTimeMu.Lock()
	c.lastBlockTime = time.Now()
	c.lastBlockTimeMu.Unlock()

	// Watchdog: check for missing blocks every 12 seconds
	watchdog := time.NewTicker(12 * time.Second)
	defer watchdog.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-watchdog.C:
			// Check if we haven't received blocks for 12 seconds
			c.lastBlockTimeMu.RLock()
			timeSinceLastBlock := time.Since(c.lastBlockTime)
			c.lastBlockTimeMu.RUnlock()

			if timeSinceLastBlock > 12*time.Second {
				log.Printf("No blocks received for %.0f seconds, reconnecting WebSocket...", timeSinceLastBlock.Seconds())
				// Invalidate client - will be cleaned up in next runLoop
				c.client = nil
				// Reset last block time
				c.lastBlockTimeMu.Lock()
				c.lastBlockTime = time.Now()
				c.lastBlockTimeMu.Unlock()
				// Return to trigger reconnection
				return fmt.Errorf("reconnect: no blocks for 12s")
			}
		case ev := <-roundCh:
			if ev.Data == nil {
				continue
			}
			c.handleNewRound(ev)
		case ev := <-completeCh:
			if ev.Data != nil {
				c.handleProposerFromAttributes(ev)
			}
		case ev := <-proposeCh:
			if ev.Data != nil {
				c.handleProposerFromAttributes(ev)
			}
		case ev := <-blockCh:
			if ev.Data == nil {
				continue
			}
			// Update last block time
			c.lastBlockTimeMu.Lock()
			c.lastBlockTime = time.Now()
			c.lastBlockTimeMu.Unlock()
			c.handleNewBlock(ev)
		}
	}
}

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
	if data.Proposer.Address != nil && len(data.Proposer.Address) > 0 {
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
	} else {
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

// Optionally: helper to resync latest block height polling if needed.
func (c *Collector) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Touch RPC to keep connection healthy
			_, _ = c.client.Status(ctx)
		}
	}
}

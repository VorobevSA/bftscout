// Package moniker provides functionality to resolve validator consensus addresses to monikers.
package moniker

import (
	"consensus-monitoring/internal/logger"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// Cache and timeout constants
	validatorCacheTTL     = 30 * time.Minute // Validators change rarely
	resolverClientTimeout = 10 * time.Second
	percentMultiplier     = 100.0
)

// validatorCacheEntry stores validator information in cache
type validatorCacheEntry struct {
	Moniker     string
	VotingPower int64
}

// Resolver fetches and caches mapping from consensus hex address to moniker
// by querying RPC /validators and REST /cosmos/staking/v1beta1/validators
// and matching validators by pub_key.
type Resolver struct {
	rpcURL    string
	appURL    string
	log       *logger.Logger
	mu        sync.RWMutex
	cache     map[string]validatorCacheEntry // hex_cons_addr -> validator info
	lastFetch time.Time
	ttl       time.Duration
	client    *http.Client
}

// NewResolver creates a new Resolver instance with the given RPC and REST API URLs.
func NewResolver(rpcURL, appURL string, log *logger.Logger) *Resolver {
	if rpcURL == "" || appURL == "" {
		return nil
	}
	return &Resolver{
		rpcURL: rpcURL,
		appURL: strings.TrimSuffix(appURL, "/"),
		log:    log,
		cache:  map[string]validatorCacheEntry{},
		ttl:    validatorCacheTTL,
		client: &http.Client{Timeout: resolverClientTimeout},
	}
}

// Resolve resolves a consensus address (hex) to a validator moniker.
func (r *Resolver) Resolve(consAddrHex string) string {
	if r == nil || consAddrHex == "" {
		return ""
	}
	key := strings.TrimPrefix(strings.ToUpper(consAddrHex), "0X")

	// Fast path: cached
	r.mu.RLock()
	if entry, ok := r.cache[key]; ok {
		r.mu.RUnlock()
		return entry.Moniker
	}
	stale := time.Since(r.lastFetch) > r.ttl
	r.mu.RUnlock()

	if stale || len(r.cache) == 0 {
		r.refresh()
	}

	r.mu.RLock()
	entry := r.cache[key]
	r.mu.RUnlock()
	return entry.Moniker
}

func (r *Resolver) refresh() {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Double-check under lock
	if time.Since(r.lastFetch) <= r.ttl && len(r.cache) > 0 {
		return
	}

	// Fetch validators from RPC
	rpcVals, err := r.fetchRPCValidators()
	if err != nil {
		if r.log != nil {
			r.log.Printf("moniker resolver: failed to fetch RPC validators: %v", err)
		}
		return
	}
	if r.log != nil {
		r.log.Printf("moniker resolver: fetched %d validators from RPC", len(rpcVals))
	}

	// Fetch validators from REST API
	restVals := r.fetchRESTValidators()
	if r.log != nil {
		r.log.Printf("moniker resolver: fetched %d validators from REST API", len(restVals))
	}

	// Build mapping: cons_addr -> validator info by matching pub_key
	mapping, matched, skipped := r.buildValidatorMapping(rpcVals, restVals)

	r.cache = mapping
	r.lastFetch = time.Now()
	if r.log != nil {
		r.log.Printf("moniker resolver: matched %d/%d RPC validators with REST API, cached %d monikers", matched, len(rpcVals)-skipped, matched)
	}
}

// ValidatorInfo represents a validator with address, moniker, voting power and percentage
type ValidatorInfo struct {
	Address      string
	Moniker      string
	VotingPower  int64
	PowerPercent float64
}

// GetValidators returns list of all validators with voting power and percentages
func (r *Resolver) GetValidators() []ValidatorInfo {
	if r == nil {
		return nil
	}

	// Ensure cache is fresh
	r.mu.RLock()
	stale := time.Since(r.lastFetch) > r.ttl || len(r.cache) == 0
	r.mu.RUnlock()

	if stale {
		r.refresh()
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	// Calculate total voting power
	var totalPower int64
	for _, entry := range r.cache {
		totalPower += entry.VotingPower
	}

	// Build validators list with percentages
	validators := make([]ValidatorInfo, 0, len(r.cache))
	for addr, entry := range r.cache {
		powerPercent := float64(0)
		if totalPower > 0 {
			powerPercent = float64(entry.VotingPower) / float64(totalPower) * percentMultiplier
		}
		validators = append(validators, ValidatorInfo{
			Address:      addr,
			Moniker:      entry.Moniker,
			VotingPower:  entry.VotingPower,
			PowerPercent: powerPercent,
		})
	}

	// Sort validators by voting power (descending)
	sort.Slice(validators, func(i, j int) bool {
		return validators[i].VotingPower > validators[j].VotingPower
	})

	return validators
}

type rpcValidator struct {
	Address string `json:"address"`
	PubKey  struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	} `json:"pub_key"`
	VotingPower      string `json:"voting_power"`
	ProposerPriority string `json:"proposer_priority"`
}

type rpcValidatorsResp struct {
	Result struct {
		Validators []rpcValidator `json:"validators"`
	} `json:"result"`
}

type restValidator struct {
	Moniker string `json:"moniker"`
	PubKey  struct {
		Key string `json:"key"`
	} `json:"pubkey"`
}

type restValidatorsResp struct {
	Validators []struct {
		Description struct {
			Moniker string `json:"moniker"`
		} `json:"description"`
		OperatorAddress string `json:"operator_address"`
		ConsensusPubkey struct {
			Type string `json:"@type"`
			Key  string `json:"key"`
		} `json:"consensus_pubkey"`
	} `json:"validators"`
}

func (r *Resolver) fetchRPCValidators() ([]rpcValidator, error) {
	// Request up to 100 validators (per_page parameter for RPC)
	url := fmt.Sprintf("%s/validators?per_page=100", r.rpcURL)
	resp, err := r.client.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var payload rpcValidatorsResp
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Result.Validators, nil
}

func (r *Resolver) fetchRESTValidators() []restValidator {
	// Request validators from all statuses (bonded, unbonding, unbonded) to get complete list
	// Use status filter to get all validators, not just active ones
	urls := []string{
		fmt.Sprintf("%s/cosmos/staking/v1beta1/validators?pagination.limit=100&status=BOND_STATUS_BONDED", r.appURL),
		fmt.Sprintf("%s/cosmos/staking/v1beta1/validators?pagination.limit=100&status=BOND_STATUS_UNBONDING", r.appURL),
		fmt.Sprintf("%s/cosmos/staking/v1beta1/validators?pagination.limit=100&status=BOND_STATUS_UNBONDED", r.appURL),
		// Also try without status filter (default)
		fmt.Sprintf("%s/cosmos/staking/v1beta1/validators?pagination.limit=100", r.appURL),
	}

	allValidators := make(map[string]restValidator) // Use pub_key as key to deduplicate

	for _, url := range urls {
		resp, err := r.client.Get(url)
		if err != nil {
			continue // Skip failed requests
		}

		var payload restValidatorsResp
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			_ = resp.Body.Close()
			continue
		}
		_ = resp.Body.Close()

		for _, v := range payload.Validators {
			if v.ConsensusPubkey.Key != "" {
				// Use pub_key as key to avoid duplicates
				allValidators[v.ConsensusPubkey.Key] = restValidator{
					Moniker: v.Description.Moniker,
					PubKey: struct {
						Key string `json:"key"`
					}{
						Key: v.ConsensusPubkey.Key,
					},
				}
			}
		}
	}

	// Convert map to slice
	result := make([]restValidator, 0, len(allValidators))
	for _, v := range allValidators {
		result = append(result, v)
	}
	return result
}

// matchPubKey compares pub_key from RPC (base64) with pub_key from REST (base64)
// Both should be base64-encoded Ed25519 public keys
func (r *Resolver) matchPubKey(rpcPubKey, restPubKey string) bool {
	if rpcPubKey == "" || restPubKey == "" {
		return false
	}
	// Both should be base64, compare directly
	if rpcPubKey == restPubKey {
		return true
	}
	// Try decoding and comparing bytes (handles padding differences)
	rpcBytes, err1 := base64.StdEncoding.DecodeString(rpcPubKey)
	restBytes, err2 := base64.StdEncoding.DecodeString(restPubKey)
	if err1 == nil && err2 == nil {
		return string(rpcBytes) == string(restBytes)
	}
	return false
}

// buildValidatorMapping builds a mapping from consensus address to validator info by matching pub_key
func (r *Resolver) buildValidatorMapping(rpcVals []rpcValidator, restVals []restValidator) (map[string]validatorCacheEntry, int, int) {
	mapping := make(map[string]validatorCacheEntry)
	matched := 0
	skipped := 0

	// Parse voting_power from RPC validators
	for _, rpcVal := range rpcVals {
		if rpcVal.Address == "" || rpcVal.PubKey.Value == "" {
			skipped++
			continue
		}

		// Parse voting_power (it's a string in JSON)
		votingPower := r.parseVotingPower(rpcVal.VotingPower)

		// Find matching REST validator by pub_key
		addr := strings.TrimPrefix(strings.ToUpper(rpcVal.Address), "0X")
		if moniker := r.findMatchingMoniker(rpcVal.PubKey.Value, restVals); moniker != "" {
			mapping[addr] = validatorCacheEntry{
				Moniker:     moniker,
				VotingPower: votingPower,
			}
			matched++
		} else {
			// RPC validator not found in REST API (may be inactive/unbonded)
			mapping[addr] = validatorCacheEntry{
				Moniker:     "",
				VotingPower: votingPower,
			}
		}
	}

	return mapping, matched, skipped
}

// parseVotingPower parses voting power from string
func (r *Resolver) parseVotingPower(votingPowerStr string) int64 {
	if votingPowerStr == "" {
		return 0
	}
	if vp, err := strconv.ParseInt(votingPowerStr, 10, 64); err == nil {
		return vp
	}
	return 0
}

// findMatchingMoniker finds matching REST validator by pub_key and returns moniker
func (r *Resolver) findMatchingMoniker(rpcPubKey string, restVals []restValidator) string {
	for _, restVal := range restVals {
		if r.matchPubKey(rpcPubKey, restVal.PubKey.Key) {
			return restVal.Moniker
		}
	}
	return ""
}

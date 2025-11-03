package moniker

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Resolver fetches and caches mapping from consensus hex address to moniker
// by querying RPC /validators and REST /cosmos/staking/v1beta1/validators
// and matching validators by pub_key.
type Resolver struct {
	rpcURL    string
	appURL    string
	mu        sync.RWMutex
	cache     map[string]string // hex_cons_addr -> moniker
	lastFetch time.Time
	ttl       time.Duration
	client    *http.Client
}

func NewResolver(rpcURL, appURL string) *Resolver {
	if rpcURL == "" || appURL == "" {
		return nil
	}
	return &Resolver{
		rpcURL: rpcURL,
		appURL: strings.TrimSuffix(appURL, "/"),
		cache:  map[string]string{},
		ttl:    30 * time.Minute, // Validators change rarely
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (r *Resolver) Resolve(consAddrHex string) string {
	if r == nil || consAddrHex == "" {
		return ""
	}
	key := strings.TrimPrefix(strings.ToUpper(consAddrHex), "0X")

	// Fast path: cached
	r.mu.RLock()
	if m, ok := r.cache[key]; ok {
		r.mu.RUnlock()
		return m
	}
	stale := time.Since(r.lastFetch) > r.ttl
	r.mu.RUnlock()

	if stale || len(r.cache) == 0 {
		r.refresh()
	}

	r.mu.RLock()
	m := r.cache[key]
	r.mu.RUnlock()
	return m
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
		log.Printf("moniker resolver: failed to fetch RPC validators: %v", err)
		return
	}
	log.Printf("moniker resolver: fetched %d validators from RPC", len(rpcVals))

	// Fetch validators from REST API
	restVals, err := r.fetchRESTValidators()
	if err != nil {
		log.Printf("moniker resolver: failed to fetch REST validators: %v", err)
		return
	}
	log.Printf("moniker resolver: fetched %d validators from REST API", len(restVals))

	// Build mapping: cons_addr -> moniker by matching pub_key
	mapping := make(map[string]string)
	matched := 0
	skipped := 0
	for _, rpcVal := range rpcVals {
		if rpcVal.Address == "" || rpcVal.PubKey.Value == "" {
			skipped++
			continue
		}
		// Find matching REST validator by pub_key
		found := false
		for _, restVal := range restVals {
			if r.matchPubKey(rpcVal.PubKey.Value, restVal.PubKey.Key) {
				addr := strings.TrimPrefix(strings.ToUpper(rpcVal.Address), "0X")
				mapping[addr] = restVal.Moniker
				matched++
				found = true
				break
			}
		}
		if !found {
			// RPC validator not found in REST API (may be inactive/unbonded)
			addr := strings.TrimPrefix(strings.ToUpper(rpcVal.Address), "0X")
			mapping[addr] = "" // Store address even without moniker
		}
	}

	r.cache = mapping
	r.lastFetch = time.Now()
	log.Printf("moniker resolver: matched %d/%d RPC validators with REST API, cached %d monikers", matched, len(rpcVals)-skipped, matched)
}

type rpcValidator struct {
	Address string `json:"address"`
	PubKey  struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	} `json:"pub_key"`
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
	defer resp.Body.Close()

	var payload rpcValidatorsResp
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Result.Validators, nil
}

func (r *Resolver) fetchRESTValidators() ([]restValidator, error) {
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
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

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
	return result, nil
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

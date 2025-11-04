-- Statistics per validator: successful vs unsuccessful proposer rounds
-- Shows: address, moniker, successful count, unsuccessful count, success rate
SELECT 
    proposer_address AS address,
    MAX(proposer_moniker) AS moniker,
    COUNT(*) FILTER (WHERE succeeded = true) AS successful_rounds,
    COUNT(*) FILTER (WHERE succeeded = false) AS unsuccessful_rounds,
    COUNT(*) AS total_rounds,
    ROUND(
        COUNT(*) FILTER (WHERE succeeded = true)::numeric / NULLIF(COUNT(*), 0) * 100,
        2
    ) AS success_rate_percent
FROM 
    round_proposers
WHERE 
    proposer_address != ''  -- Exclude empty addresses
GROUP BY 
    proposer_address
ORDER BY 
    total_rounds DESC,  -- Most active validators first
    success_rate_percent DESC;


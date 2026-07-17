package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Per query retrieval cost telemetry — Cost Governed RAG (arXiv 2607.12188).
//
// Retrieval is today an unbilled shared cost spread across all 36 verticals: a
// vector index sits resident in memory and every query traverses it, but no
// tenant is charged for the memory it occupies or the search compute it burns.
// This layer makes retrieval an attributable, per tenant, per vertical, per
// query line so it can be billed alongside generation.
//
// Two legs, correlated by a query id:
//   - GENERATION leg — emitted IN PROCESS by this router (it already sees the
//     model, tier, input and output token counts, and the per backend token
//     price). See emitGenerationTelemetry, wired from logRequest in costs.go.
//   - RETRIEVAL leg — emitted by the per vertical RAG service, which is the only
//     place that measures vectors or nodes scanned, k returned, resident index
//     bytes and search latency. A Go RAG service links this package and calls
//     EmitRetrievalTelemetry directly; a service in any language POSTs the raw
//     retrieval metrics to POST /api/cost-telemetry/retrieval and this router
//     computes the split and stores it.
//
// The chargeback SQL view joins the two legs on query_id so a finance query can
// read total attributable cost per tenant and per vertical.
//
// EVERYTHING here is additive and OFF by default. It only does anything when
// KX_COST_TELEMETRY is set (1/true/on). When off: emitGenerationTelemetry
// returns immediately (the live routing and cost path is byte for byte
// unchanged), the HTTP ingest endpoint answers 404, and no DDL ever runs.
//
// WHY EMPIRICAL, NOT ANALYTIC. The Cost Governed RAG paper derives a closed
// form memory cost for a FLAT scan index (bytes = N * dim * bytes_per_component,
// and every query scans all N). Our production index is a GRAPH index (HNSW):
// its resident footprint includes the graph adjacency layers and entry points
// with no clean closed form, and a query touches only the small, data dependent
// subset of nodes its greedy search visits — not all N. So we attribute the two
// physical resources EMPIRICALLY from MEASURED quantities rather than a formula:
//   - memory: index_bytes_attributable is the MEASURED resident size charged to
//     this query's index (e.g. RSS delta on load, or the serialised graph size),
//     not N*dim*bytes.
//   - compute: the MEASURED search latency (a proxy for CPU or GPU seconds), or
//     the MEASURED count of vectors or nodes the greedy walk actually scanned —
//     not an analytic op count.
// This mirrors the paper's cost split S = memory_share + compute_share +
// gen_tokens*rate while staying honest about a graph index having no analytic
// memory or op formula.

// CostTelemetryEnabled reports whether the cost telemetry layer is active.
// Default OFF so the live cost routing path is unchanged until explicitly
// switched on with KX_COST_TELEMETRY=1.
func CostTelemetryEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("KX_COST_TELEMETRY"))) {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}

// RetrievalInputs are the MEASURED retrieval quantities for one query. They are
// measured, never derived from an analytic index formula (see file header).
type RetrievalInputs struct {
	// VectorsOrNodesScanned is how many vectors or graph nodes the search
	// actually visited for this query (the greedy walk length for HNSW).
	VectorsOrNodesScanned int64 `json:"vectors_or_nodes_scanned"`
	// KReturned is the number of results handed back (top k).
	KReturned int `json:"k_returned"`
	// IndexBytesAttributable is the MEASURED resident byte size of the index
	// charged to this query (graph adjacency plus vectors), not N*dim*bytes.
	IndexBytesAttributable int64 `json:"index_bytes_attributable"`
	// RetrievalLatencyUS is the MEASURED wall clock of the ANN search in
	// microseconds.
	RetrievalLatencyUS int64 `json:"retrieval_latency_us"`
}

// GenerationInputs are the generation leg quantities the router already sees.
type GenerationInputs struct {
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	Model        string `json:"model"`
	Tier         int    `json:"tier"`
}

// CostRates carries the unit prices for the split. Retrieval rates are operator
// set (they price physical resources of the self hosted index); generation
// rates mirror the per backend token price the router already uses.
type CostRates struct {
	// MemoryUSDPerGiBHour prices resident index memory. Multiplied by the
	// attributable GiB and the residency fraction of an hour.
	MemoryUSDPerGiBHour float64 `json:"memory_usd_per_gib_hour"`
	// ComputeUSDPerVectorScanned, when > 0, prices the MEASURED scanned
	// vectors or nodes. Preferred when the index reports its walk length.
	ComputeUSDPerVectorScanned float64 `json:"compute_usd_per_vector_scanned"`
	// ComputeUSDPerSecond prices MEASURED search latency (CPU or GPU seconds
	// proxy). Used when ComputeUSDPerVectorScanned is 0.
	ComputeUSDPerSecond float64 `json:"compute_usd_per_second"`
	// GenInputUSDPer1M and GenOutputUSDPer1M mirror Backend.Config.Cost*1M.
	GenInputUSDPer1M  float64 `json:"gen_input_usd_per_1m"`
	GenOutputUSDPer1M float64 `json:"gen_output_usd_per_1m"`
	// MemoryResidencySeconds sets how long the attributable resident bytes are
	// charged to a single query. Default 0 uses the MEASURED search latency
	// (occupancy model: you pay memory rent for the time your query traverses
	// the resident index). Set > 0 to amortise a fixed residency window per
	// query instead (e.g. standing_hour_seconds / expected_queries_per_hour).
	MemoryResidencySeconds float64 `json:"memory_residency_seconds"`
}

// CostSplit is the S-style attribution: cost = memory_share + compute_share +
// gen_tokens*rate.
type CostSplit struct {
	RetrievalMemoryShareUSD  float64 `json:"retrieval_memory_share_usd"`
	RetrievalComputeShareUSD float64 `json:"retrieval_compute_share_usd"`
	GenerationCostUSD        float64 `json:"generation_cost_usd"`
	TotalUSD                 float64 `json:"total_usd"`
}

// RetrievalCostShare computes the retrieval memory and compute shares for one
// query from MEASURED inputs and operator set rates.
//
//	memory_share  = attributable_GiB * MemoryUSDPerGiBHour * (residency_sec / 3600)
//	compute_share = ComputeUSDPerVectorScanned * scanned          (if that rate > 0)
//	              = ComputeUSDPerSecond        * (latency_us / 1e6) (otherwise)
func RetrievalCostShare(in RetrievalInputs, rates CostRates) (memShare, computeShare float64) {
	gib := float64(in.IndexBytesAttributable) / (1024.0 * 1024.0 * 1024.0)

	residencySec := rates.MemoryResidencySeconds
	if residencySec <= 0 {
		residencySec = float64(in.RetrievalLatencyUS) / 1e6
	}
	memShare = gib * rates.MemoryUSDPerGiBHour * (residencySec / 3600.0)

	if rates.ComputeUSDPerVectorScanned > 0 {
		computeShare = float64(in.VectorsOrNodesScanned) * rates.ComputeUSDPerVectorScanned
	} else {
		computeShare = (float64(in.RetrievalLatencyUS) / 1e6) * rates.ComputeUSDPerSecond
	}
	return memShare, computeShare
}

// GenerationCostShare computes the token cost of the generation leg.
func GenerationCostShare(in GenerationInputs, rates CostRates) float64 {
	return float64(in.InputTokens)*rates.GenInputUSDPer1M/1e6 +
		float64(in.OutputTokens)*rates.GenOutputUSDPer1M/1e6
}

// CostAttribution combines both legs into the full split. This is the
// S-style cost model: cost = memory_share + compute_share + gen_tokens*rate.
func CostAttribution(r RetrievalInputs, g GenerationInputs, rates CostRates) CostSplit {
	mem, comp := RetrievalCostShare(r, rates)
	gen := GenerationCostShare(g, rates)
	return CostSplit{
		RetrievalMemoryShareUSD:  mem,
		RetrievalComputeShareUSD: comp,
		GenerationCostUSD:        gen,
		TotalUSD:                 mem + comp + gen,
	}
}

// RetrievalTelemetryRecord is one persisted retrieval leg row.
type RetrievalTelemetryRecord struct {
	QueryID   string    `json:"query_id"`
	TenantID  string    `json:"tenant_id"`
	Vertical  string    `json:"vertical"`
	Timestamp time.Time `json:"timestamp"`
	RetrievalInputs
	RetrievalMemoryShareUSD  float64 `json:"retrieval_memory_share_usd"`
	RetrievalComputeShareUSD float64 `json:"retrieval_compute_share_usd"`
	RetrievalCostUSD         float64 `json:"retrieval_cost_usd"`
}

// GenerationTelemetryRecord is one persisted generation leg row.
type GenerationTelemetryRecord struct {
	QueryID   string    `json:"query_id"`
	TenantID  string    `json:"tenant_id"`
	Vertical  string    `json:"vertical"`
	Timestamp time.Time `json:"timestamp"`
	GenerationInputs
	Provider          string  `json:"provider"`
	GenerationCostUSD float64 `json:"generation_cost_usd"`
	LatencyUS         int64   `json:"latency_us"`
}

// costTelemetryDDL is the schema. It is applied lazily and idempotently by
// ensureCostTelemetrySchema, and ONLY when KX_COST_TELEMETRY is on — it never
// runs on the live default path.
//
//	-- ==========================================================================
//	-- Cost Governed RAG per query telemetry (arXiv 2607.12188)
//	-- Two legs joined by query_id. All amounts USD.
//	-- ==========================================================================
//
//	CREATE TABLE IF NOT EXISTS retrieval_cost_telemetry (
//	    id                          BIGSERIAL PRIMARY KEY,
//	    query_id                    TEXT        NOT NULL,
//	    tenant_id                   TEXT        NOT NULL DEFAULT '',
//	    vertical                    TEXT        NOT NULL DEFAULT '',
//	    ts                          TIMESTAMPTZ NOT NULL DEFAULT now(),
//	    -- MEASURED retrieval metrics (not analytic; graph index has no closed form)
//	    vectors_or_nodes_scanned    BIGINT      NOT NULL DEFAULT 0,
//	    k_returned                  INTEGER     NOT NULL DEFAULT 0,
//	    index_bytes_attributable    BIGINT      NOT NULL DEFAULT 0,
//	    retrieval_latency_us        BIGINT      NOT NULL DEFAULT 0,
//	    -- computed cost split (retrieval legs only)
//	    retrieval_memory_share_usd  DOUBLE PRECISION NOT NULL DEFAULT 0,
//	    retrieval_compute_share_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
//	    retrieval_cost_usd          DOUBLE PRECISION NOT NULL DEFAULT 0
//	);
//	CREATE INDEX IF NOT EXISTS ix_retrieval_cost_query  ON retrieval_cost_telemetry (query_id);
//	CREATE INDEX IF NOT EXISTS ix_retrieval_cost_tenant ON retrieval_cost_telemetry (tenant_id, vertical, ts);
//
//	CREATE TABLE IF NOT EXISTS generation_cost_telemetry (
//	    id                  BIGSERIAL PRIMARY KEY,
//	    query_id            TEXT        NOT NULL,
//	    tenant_id           TEXT        NOT NULL DEFAULT '',
//	    vertical            TEXT        NOT NULL DEFAULT '',
//	    ts                  TIMESTAMPTZ NOT NULL DEFAULT now(),
//	    input_tokens        INTEGER     NOT NULL DEFAULT 0,
//	    output_tokens       INTEGER     NOT NULL DEFAULT 0,
//	    model               TEXT        NOT NULL DEFAULT '',
//	    provider            TEXT        NOT NULL DEFAULT '',
//	    tier                INTEGER     NOT NULL DEFAULT 0,
//	    generation_cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
//	    latency_us          BIGINT      NOT NULL DEFAULT 0
//	);
//	CREATE INDEX IF NOT EXISTS ix_generation_cost_query  ON generation_cost_telemetry (query_id);
//	CREATE INDEX IF NOT EXISTS ix_generation_cost_tenant ON generation_cost_telemetry (tenant_id, vertical, ts);
//
//	-- Per query chargeback: FULL OUTER JOIN so a query with only one leg still
//	-- appears (e.g. a cache hit with no generation, or a retrieval free direct
//	-- LLM call). COALESCE folds the correlation keys across both legs.
//	CREATE OR REPLACE VIEW v_query_chargeback AS
//	SELECT
//	    COALESCE(r.query_id, g.query_id)                              AS query_id,
//	    COALESCE(NULLIF(r.tenant_id,''), g.tenant_id, '')            AS tenant_id,
//	    COALESCE(NULLIF(r.vertical,''),  g.vertical,  '')            AS vertical,
//	    COALESCE(r.ts, g.ts)                                          AS ts,
//	    COALESCE(r.retrieval_memory_share_usd, 0)                    AS retrieval_memory_share_usd,
//	    COALESCE(r.retrieval_compute_share_usd, 0)                   AS retrieval_compute_share_usd,
//	    COALESCE(r.retrieval_cost_usd, 0)                            AS retrieval_cost_usd,
//	    COALESCE(g.generation_cost_usd, 0)                           AS generation_cost_usd,
//	    COALESCE(r.retrieval_cost_usd, 0) + COALESCE(g.generation_cost_usd, 0) AS total_cost_usd
//	FROM retrieval_cost_telemetry r
//	FULL OUTER JOIN generation_cost_telemetry g ON g.query_id = r.query_id;
//
//	-- Rollup: the billable line per tenant and vertical.
//	CREATE OR REPLACE VIEW v_tenant_vertical_chargeback AS
//	SELECT
//	    tenant_id,
//	    vertical,
//	    date_trunc('day', ts)                 AS day,
//	    COUNT(*)                              AS queries,
//	    SUM(retrieval_memory_share_usd)       AS retrieval_memory_usd,
//	    SUM(retrieval_compute_share_usd)      AS retrieval_compute_usd,
//	    SUM(retrieval_cost_usd)               AS retrieval_usd,
//	    SUM(generation_cost_usd)              AS generation_usd,
//	    SUM(total_cost_usd)                   AS total_usd
//	FROM v_query_chargeback
//	GROUP BY tenant_id, vertical, date_trunc('day', ts);
const costTelemetryDDL = `
CREATE TABLE IF NOT EXISTS retrieval_cost_telemetry (
    id                          BIGSERIAL PRIMARY KEY,
    query_id                    TEXT        NOT NULL,
    tenant_id                   TEXT        NOT NULL DEFAULT '',
    vertical                    TEXT        NOT NULL DEFAULT '',
    ts                          TIMESTAMPTZ NOT NULL DEFAULT now(),
    vectors_or_nodes_scanned    BIGINT      NOT NULL DEFAULT 0,
    k_returned                  INTEGER     NOT NULL DEFAULT 0,
    index_bytes_attributable    BIGINT      NOT NULL DEFAULT 0,
    retrieval_latency_us        BIGINT      NOT NULL DEFAULT 0,
    retrieval_memory_share_usd  DOUBLE PRECISION NOT NULL DEFAULT 0,
    retrieval_compute_share_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
    retrieval_cost_usd          DOUBLE PRECISION NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_retrieval_cost_query  ON retrieval_cost_telemetry (query_id);
CREATE INDEX IF NOT EXISTS ix_retrieval_cost_tenant ON retrieval_cost_telemetry (tenant_id, vertical, ts);

CREATE TABLE IF NOT EXISTS generation_cost_telemetry (
    id                  BIGSERIAL PRIMARY KEY,
    query_id            TEXT        NOT NULL,
    tenant_id           TEXT        NOT NULL DEFAULT '',
    vertical            TEXT        NOT NULL DEFAULT '',
    ts                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    input_tokens        INTEGER     NOT NULL DEFAULT 0,
    output_tokens       INTEGER     NOT NULL DEFAULT 0,
    model               TEXT        NOT NULL DEFAULT '',
    provider            TEXT        NOT NULL DEFAULT '',
    tier                INTEGER     NOT NULL DEFAULT 0,
    generation_cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
    latency_us          BIGINT      NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_generation_cost_query  ON generation_cost_telemetry (query_id);
CREATE INDEX IF NOT EXISTS ix_generation_cost_tenant ON generation_cost_telemetry (tenant_id, vertical, ts);

CREATE OR REPLACE VIEW v_query_chargeback AS
SELECT
    COALESCE(r.query_id, g.query_id)                              AS query_id,
    COALESCE(NULLIF(r.tenant_id,''), g.tenant_id, '')             AS tenant_id,
    COALESCE(NULLIF(r.vertical,''),  g.vertical,  '')             AS vertical,
    COALESCE(r.ts, g.ts)                                          AS ts,
    COALESCE(r.retrieval_memory_share_usd, 0)                     AS retrieval_memory_share_usd,
    COALESCE(r.retrieval_compute_share_usd, 0)                    AS retrieval_compute_share_usd,
    COALESCE(r.retrieval_cost_usd, 0)                             AS retrieval_cost_usd,
    COALESCE(g.generation_cost_usd, 0)                            AS generation_cost_usd,
    COALESCE(r.retrieval_cost_usd, 0) + COALESCE(g.generation_cost_usd, 0) AS total_cost_usd
FROM retrieval_cost_telemetry r
FULL OUTER JOIN generation_cost_telemetry g ON g.query_id = r.query_id;

CREATE OR REPLACE VIEW v_tenant_vertical_chargeback AS
SELECT
    tenant_id,
    vertical,
    date_trunc('day', ts)                 AS day,
    COUNT(*)                              AS queries,
    SUM(retrieval_memory_share_usd)       AS retrieval_memory_usd,
    SUM(retrieval_compute_share_usd)      AS retrieval_compute_usd,
    SUM(retrieval_cost_usd)               AS retrieval_usd,
    SUM(generation_cost_usd)              AS generation_usd,
    SUM(total_cost_usd)                   AS total_usd
FROM v_query_chargeback
GROUP BY tenant_id, vertical, date_trunc('day', ts);
`

var (
	costTelemetrySchemaOnce sync.Once
	costTelemetrySchemaOK   bool
	costTelemetrySem        = make(chan struct{}, 50) // bound DB write concurrency
)

// ensureCostTelemetrySchema applies costTelemetryDDL once. Only ever called
// from an emit path, which is itself gated on CostTelemetryEnabled, so the DDL
// never runs on the default (disabled) configuration.
func ensureCostTelemetrySchema(dbh *sql.DB) bool {
	if dbh == nil {
		return false
	}
	costTelemetrySchemaOnce.Do(func() {
		if _, err := dbh.Exec(costTelemetryDDL); err != nil {
			logger.Printf("cost telemetry: schema init failed: %v", err)
			costTelemetrySchemaOK = false
			return
		}
		costTelemetrySchemaOK = true
		logger.Println("cost telemetry: schema ready (retrieval_cost_telemetry, generation_cost_telemetry, chargeback views)")
	})
	return costTelemetrySchemaOK
}

// costTelemetryDefaultRates reads operator set retrieval rates from the
// environment. Generation rates are supplied per call from the backend price.
func costTelemetryDefaultRates() CostRates {
	return CostRates{
		MemoryUSDPerGiBHour:        envFloat("KX_COST_MEM_USD_PER_GIB_HOUR", 0),
		ComputeUSDPerVectorScanned: envFloat("KX_COST_COMPUTE_USD_PER_VECTOR", 0),
		ComputeUSDPerSecond:        envFloat("KX_COST_COMPUTE_USD_PER_SEC", 0),
		MemoryResidencySeconds:     envFloat("KX_COST_MEM_RESIDENCY_SEC", 0),
	}
}

func envFloat(key string, def float64) float64 {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

// ---- correlation key extraction -------------------------------------------

// metaHeader reads a forwarded X-Kronaxis-* header from the request metadata.
// ForwardHeaders keys are net/http canonical, so canonicalise the lookup key.
func metaHeader(meta RouteRequest, key string) string {
	if meta.ForwardHeaders == nil {
		return ""
	}
	return strings.TrimSpace(meta.ForwardHeaders[http.CanonicalHeaderKey(key)])
}

// telemetryTenant resolves the tenant for a query: X-Kronaxis-Tenant-ID, then
// the service name as a coarse fallback (same order as extractTenant).
func telemetryTenant(meta RouteRequest) string {
	if t := metaHeader(meta, "X-Kronaxis-Tenant-ID"); t != "" {
		return t
	}
	return strings.TrimSpace(meta.Service)
}

// telemetryVertical resolves the vertical: X-Kronaxis-Vertical, then service.
func telemetryVertical(meta RouteRequest) string {
	if v := metaHeader(meta, "X-Kronaxis-Vertical"); v != "" {
		return v
	}
	return strings.TrimSpace(meta.Service)
}

// telemetryQueryID resolves the correlation id that joins the two legs. Callers
// (BoS agent or RAG gate) SHOULD mint one and set X-Kronaxis-Query-ID on both
// legs. When absent the router mints a fallback so the generation row is still
// recorded, but it will not join to a retrieval row.
func telemetryQueryID(meta RouteRequest) string {
	if q := metaHeader(meta, "X-Kronaxis-Query-ID"); q != "" {
		return q
	}
	return "gen-" + newQueryID()
}

func newQueryID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// ---- generation leg (emitted in process by the router) --------------------

// emitGenerationTelemetry records the generation leg for one query. Wired from
// logRequest (costs.go). No op unless KX_COST_TELEMETRY is on, so the live cost
// path is unchanged when the flag is off.
func emitGenerationTelemetry(meta RouteRequest, route RouteResult, inputTokens, outputTokens int, latency time.Duration) {
	if !CostTelemetryEnabled() || db == nil || route.Backend == nil {
		return
	}

	rates := CostRates{
		GenInputUSDPer1M:  route.Backend.Config.CostInput1M,
		GenOutputUSDPer1M: route.Backend.Config.CostOutput1M,
	}
	gin := GenerationInputs{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		Model:        route.ModelName,
		Tier:         meta.Tier,
	}

	rec := GenerationTelemetryRecord{
		QueryID:           telemetryQueryID(meta),
		TenantID:          telemetryTenant(meta),
		Vertical:          telemetryVertical(meta),
		Timestamp:         time.Now().UTC(),
		GenerationInputs:  gin,
		Provider:          route.Backend.Config.Type,
		GenerationCostUSD: GenerationCostShare(gin, rates),
		LatencyUS:         latency.Microseconds(),
	}
	insertGenerationTelemetry(rec)
}

func insertGenerationTelemetry(rec GenerationTelemetryRecord) {
	if !ensureCostTelemetrySchema(db) {
		return
	}
	select {
	case costTelemetrySem <- struct{}{}:
	default:
		logger.Println("cost telemetry: generation semaphore full, skipping")
		return
	}
	go func() {
		defer func() { <-costTelemetrySem }()
		_, err := db.Exec(`
			INSERT INTO generation_cost_telemetry
				(query_id, tenant_id, vertical, ts, input_tokens, output_tokens,
				 model, provider, tier, generation_cost_usd, latency_us)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			rec.QueryID, rec.TenantID, rec.Vertical, rec.Timestamp,
			rec.InputTokens, rec.OutputTokens, rec.Model, rec.Provider,
			rec.Tier, rec.GenerationCostUSD, rec.LatencyUS,
		)
		if err != nil {
			logger.Printf("cost telemetry: generation insert error: %v", err)
		}
	}()
}

// ---- retrieval leg (emitted by the RAG service) ---------------------------

// EmitRetrievalTelemetry persists a retrieval leg row. Exported so a Go RAG
// service that links this package can call it directly after a search. Services
// in other languages use the HTTP endpoint below. No op unless the flag is on.
func EmitRetrievalTelemetry(rec RetrievalTelemetryRecord) error {
	if !CostTelemetryEnabled() {
		return nil
	}
	if db == nil {
		return fmt.Errorf("cost telemetry: no database handle")
	}
	if !ensureCostTelemetrySchema(db) {
		return fmt.Errorf("cost telemetry: schema not ready")
	}
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now().UTC()
	}
	_, err := db.Exec(`
		INSERT INTO retrieval_cost_telemetry
			(query_id, tenant_id, vertical, ts, vectors_or_nodes_scanned,
			 k_returned, index_bytes_attributable, retrieval_latency_us,
			 retrieval_memory_share_usd, retrieval_compute_share_usd, retrieval_cost_usd)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		rec.QueryID, rec.TenantID, rec.Vertical, rec.Timestamp,
		rec.VectorsOrNodesScanned, rec.KReturned, rec.IndexBytesAttributable,
		rec.RetrievalLatencyUS, rec.RetrievalMemoryShareUSD,
		rec.RetrievalComputeShareUSD, rec.RetrievalCostUSD,
	)
	return err
}

// retrievalIngestPayload is the JSON body a RAG service POSTs to the retrieval
// ingest endpoint. It supplies MEASURED metrics and, optionally, its own rates;
// the router computes the split with CostAttribution's retrieval half.
type retrievalIngestPayload struct {
	QueryID  string `json:"query_id"`
	TenantID string `json:"tenant_id"`
	Vertical string `json:"vertical"`
	RetrievalInputs
	// Rates is optional. Zero valued fields fall back to the KX_COST_* env
	// defaults, so a RAG service need not know the operator's unit prices.
	Rates *CostRates `json:"rates,omitempty"`
}

// handleCostTelemetryRetrieval ingests a retrieval leg from a RAG service.
// POST /api/cost-telemetry/retrieval. Answers 404 when the flag is off, so the
// endpoint is inert by default even though it is always registered.
func handleCostTelemetryRetrieval(w http.ResponseWriter, r *http.Request) {
	if !CostTelemetryEnabled() {
		writeErrorJSON(w, http.StatusNotFound, "cost telemetry disabled")
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var p retrievalIngestPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(p.QueryID) == "" {
		writeErrorJSON(w, http.StatusBadRequest, "query_id is required to join the generation leg")
		return
	}

	rates := costTelemetryDefaultRates()
	if p.Rates != nil {
		if p.Rates.MemoryUSDPerGiBHour != 0 {
			rates.MemoryUSDPerGiBHour = p.Rates.MemoryUSDPerGiBHour
		}
		if p.Rates.ComputeUSDPerVectorScanned != 0 {
			rates.ComputeUSDPerVectorScanned = p.Rates.ComputeUSDPerVectorScanned
		}
		if p.Rates.ComputeUSDPerSecond != 0 {
			rates.ComputeUSDPerSecond = p.Rates.ComputeUSDPerSecond
		}
		if p.Rates.MemoryResidencySeconds != 0 {
			rates.MemoryResidencySeconds = p.Rates.MemoryResidencySeconds
		}
	}

	mem, comp := RetrievalCostShare(p.RetrievalInputs, rates)
	rec := RetrievalTelemetryRecord{
		QueryID:                  strings.TrimSpace(p.QueryID),
		TenantID:                 strings.TrimSpace(p.TenantID),
		Vertical:                 strings.TrimSpace(p.Vertical),
		Timestamp:                time.Now().UTC(),
		RetrievalInputs:          p.RetrievalInputs,
		RetrievalMemoryShareUSD:  mem,
		RetrievalComputeShareUSD: comp,
		RetrievalCostUSD:         mem + comp,
	}
	if err := EmitRetrievalTelemetry(rec); err != nil {
		writeErrorJSON(w, http.StatusInternalServerError, "persist failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":                      "recorded",
		"query_id":                    rec.QueryID,
		"retrieval_memory_share_usd":  mem,
		"retrieval_compute_share_usd": comp,
		"retrieval_cost_usd":          rec.RetrievalCostUSD,
	})
}

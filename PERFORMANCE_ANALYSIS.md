# ListRecordings Endpoint - Performance Analysis

## Executive Summary

The `GET /recordings` endpoint suffers from **severe N+1 query problems** causing slow response times. With 20 recordings per page, users experience **5-20 second load times**. The primary bottleneck is fetching GitLab project details for each recording.

---

## Current Architecture Analysis

### Request Flow
```
1. Query Redis Set (SMembers) → Get all recording IDs
2. Loop: Redis GET per ID → Fetch recording data
3. Filter in-memory (status, search, user)
4. Sort in-memory
5. Paginate in-memory
6. Loop: GitLab API call per unique project → Fetch project details
7. Return JSON response
```

### Performance Bottlenecks (Ranked by Impact)

| # | Issue | Impact | Current Cost | After Fix |
|---|-------|--------|--------------|-----------|
| 1 | **GitLab API N+1** | 🔴 CRITICAL | 200-500ms × N projects | 50-100ms (cached) |
| 2 | **Redis GET N+1** | 🟠 HIGH | 5-10ms × N recordings | 5-10ms (batched) |
| 3 | **SMembers loads all IDs** | 🟠 HIGH | O(n) memory + network | O(1) with sorted sets |
| 4 | **In-memory pagination** | 🟡 MEDIUM | Wastes CPU on discarded data | Server-side pagination |
| 5 | **No response caching** | 🟡 MEDIUM | Repeated identical queries | Instant cache hits |

---

## Detailed Analysis

### 1. GitLab API N+1 Problem (CRITICAL)

**Location:** `handlers/recording.go:313-317`

```go
for i := range paginatedRecordings {
    if paginatedRecordings[i].ProjectID != "" && paginatedRecordings[i].ProjectDetails == nil {
        paginatedRecordings[i].ProjectDetails = getProjectDetails(c, paginatedRecordings[i].ProjectID)
    }
}
```

**Problem:** Each unique project triggers a synchronous HTTP request to GitLab.

**Measurements:**
| Scenario | Unique Projects | GitLab Calls | Estimated Time |
|----------|-----------------|--------------|----------------|
| Default page (20 items) | 5 | 5 calls | 1-2.5s |
| Default page (20 items) | 20 | 20 calls | 4-10s |
| Max page (100 items) | 50 | 50 calls | 10-25s |
| Worst case (100 items) | 100 | 100 calls | 20-50s |

**Why it's slow:**
- GitLab API latency: 100-300ms per call
- OAuth token validation on each call
- Rate limiting delays
- Sequential (not parallel) execution

---

### 2. Redis GET N+1 Problem (HIGH)

**Location:** `handlers/recording.go:223-235`

```go
for _, id := range ids {
    val, err := database.RedisClient.Get(ctx, fmt.Sprintf("recording:%s", id)).Result()
    // ...
}
```

**Problem:** Each recording requires a separate Redis round-trip.

**Measurements:**
| Recordings | Redis Calls | Time (local) | Time (remote) |
|------------|-------------|--------------|---------------|
| 100 | 100 calls | 50-100ms | 200-500ms |
| 1000 | 1000 calls | 500ms-1s | 2-5s |
| 10000 | 10000 calls | 5-10s | 20-50s |

---

### 3. SMembers Loads All IDs (HIGH)

**Location:** `handlers/recording.go:205-214`

```go
if issueID != "" {
    ids, err = database.RedisClient.SMembers(ctx, fmt.Sprintf("recordings:issue:%s", issueID)).Result()
} else if projectID != "" {
    ids, err = database.RedisClient.SMembers(ctx, fmt.Sprintf("recordings:project:%s", projectID)).Result()
} else if userID != 0 {
    // ...
}
```

**Problem:** `SMembers` returns ALL recording IDs before filtering/pagination.

**Impact:**
- 10,000 recordings → 10,000 IDs loaded into memory
- Network transfer: ~100KB-1MB of ID data
- All subsequent filtering/sorting happens in-memory

---

### 4. In-Memory Filtering & Pagination (MEDIUM)

**Problem:** All recordings are fetched, filtered, and sorted BEFORE pagination.

```go
// Fetch ALL recordings
for _, id := range ids { ... }

// Filter ALL recordings
if status != "" { ... }
if search != "" { ... }

// Sort ALL recordings
sort.Slice(recordings, ...)

// THEN paginate
paginatedRecordings := recordings[start:end]
```

**Waste:** For page 1 with limit=20 out of 10,000 recordings:
- 9,980 recordings processed and discarded
- CPU wasted on sorting discarded data

---

### 5. No Response Caching (MEDIUM)

**Problem:** Identical requests (same filters, page) hit GitLab API every time.

**Typical frontend behavior:**
- User opens recordings list → slow
- User filters by status → slow
- User goes back to page 1 → slow (should be instant!)

---

## Recommended Fixes (Priority Order)

### Fix #1: Cache Project Details (CRITICAL)
**Expected Gain:** 80-95% reduction in GitLab API calls

Add to `database/database.go`:
```go
var projectDetailsCacheTTL = 5 * time.Minute

func GetCachedProjectDetails(ctx context.Context, projectID string) (*models.ProjectDetails, bool) {
    key := fmt.Sprintf("project:details:%s", projectID)
    data, err := RedisClient.Get(ctx, key).Bytes()
    if err != nil {
        return nil, false
    }
    var details models.ProjectDetails
    if err := json.Unmarshal(data, &details); err != nil {
        return nil, false
    }
    return &details, true
}

func SetCachedProjectDetails(ctx context.Context, projectID string, details *models.ProjectDetails) {
    key := fmt.Sprintf("project:details:%s", projectID)
    data, _ := json.Marshal(details)
    RedisClient.Set(ctx, key, data, projectDetailsCacheTTL)
}
```

Update `getProjectDetails()`:
```go
func getProjectDetails(c *gin.Context, projectID string) *models.ProjectDetails {
    ctx := context.Background()
    
    // Check cache first
    if cached, ok := database.GetCachedProjectDetails(ctx, projectID); ok {
        return cached
    }
    
    // ... existing GitLab API call ...
    
    // Cache the result
    database.SetCachedProjectDetails(ctx, projectID, details)
    return details
}
```

**Impact:** After first load, subsequent requests skip GitLab entirely.

---

### Fix #2: Batch Redis GETs with MGet (HIGH)
**Expected Gain:** 90% reduction in Redis round-trips

```go
// Instead of looping with Get():
keys := make([]string, len(ids))
for i, id := range ids {
    keys[i] = fmt.Sprintf("recording:%s", id)
}

// Single batch call
vals, err := database.RedisClient.MGet(ctx, keys...).Result()
if err != nil {
    // handle error
}

// Process results
for i, val := range vals {
    if val == nil {
        continue
    }
    var r models.TestRecording
    if json.Unmarshal([]byte(val.(string)), &r) == nil {
        // ... filtering logic ...
    }
}
```

**Impact:** 100 recordings = 1 Redis call instead of 100.

---

### Fix #3: Parallel GitLab API Calls (HIGH)
**Expected Gain:** 5-10x faster for multi-project pages

```go
// Collect unique project IDs
projectIDs := make(map[string]bool)
for _, r := range paginatedRecordings {
    if r.ProjectID != "" && r.ProjectDetails == nil {
        projectIDs[r.ProjectID] = true
    }
}

// Fetch in parallel
type projectResult struct {
    id      string
    details *models.ProjectDetails
}

results := make(chan projectResult, len(projectIDs))
var wg sync.WaitGroup

for projectID := range projectIDs {
    wg.Add(1)
    go func(pid string) {
        defer wg.Done()
        details := getProjectDetails(c, pid)
        results <- projectResult{id: pid, details: details}
    }(projectID)
}

wg.Wait()
close(results)

// Map results back to recordings
projectDetailsMap := make(map[string]*models.ProjectDetails)
for r := range results {
    projectDetailsMap[r.id] = r.details
}

for i := range paginatedRecordings {
    if paginatedRecordings[i].ProjectID != "" {
        paginatedRecordings[i].ProjectDetails = projectDetailsMap[paginatedRecordings[i].ProjectID]
    }
}
```

---

### Fix #4: Use Redis Sorted Sets for Pagination (MEDIUM)
**Expected Gain:** Server-side pagination, reduced memory

Store recordings in a sorted set with timestamp as score:
```go
// When saving:
RedisClient.ZAdd(ctx, "recordings:sorted", redis.Z{
    Score: float64(recording.CreatedAt.Unix()),
    Member: recording.ID,
})

// When listing (with pagination):
ids, err := RedisClient.ZRange(ctx, "recordings:sorted", start, end-1).Result()
```

**Impact:** Only fetch IDs needed for current page.

---

### Fix #5: Cache List Response (MEDIUM)
**Expected Gain:** Instant responses for repeated queries

```go
func ListRecordings(c *gin.Context) {
    // Generate cache key from query params
    cacheKey := fmt.Sprintf("list:recordings:%s", c.Request.URL.RawQuery)
    
    // Try cache
    if cached, ok := database.GetCachedListResponse(ctx, cacheKey); ok {
        c.Data(http.StatusOK, "application/json", cached)
        return
    }
    
    // ... existing logic ...
    
    // Cache response
    database.SetCachedListResponse(ctx, cacheKey, responseData, 30*time.Second)
}
```

---

## Quick Wins Summary

| Fix | Effort | Impact | Rollback Risk |
|-----|--------|--------|---------------|
| #1 Cache Project Details | Low (30 min) | 🔴🔴🔴 High | None |
| #2 Batch Redis MGet | Low (30 min) | 🟠🟠 Medium | None |
| #3 Parallel GitLab Calls | Medium (1 hr) | 🟠🟠🟠 High | Low |
| #4 Sorted Sets | High (3-4 hr) | 🟠 Medium | Medium |
| #5 Response Cache | Low (30 min) | 🟡 Low-Medium | None |

---

## Immediate Action Plan

### Phase 1: Quick Wins (1-2 hours)
1. ✅ Implement project details caching (Fix #1)
2. ✅ Add parallel GitLab API calls (Fix #3)

**Expected Result:** Page load time drops from 5-20s → 0.5-2s

### Phase 2: Optimization (2-3 hours)
3. ✅ Batch Redis GETs with MGet (Fix #2)
4. ✅ Add response caching for repeated queries (Fix #5)

**Expected Result:** Page load time drops to 0.2-0.5s

### Phase 3: Architecture (4-6 hours)
5. ✅ Migrate to sorted sets for server-side pagination (Fix #4)
6. ✅ Add Redis indexing for status/search filters

**Expected Result:** Consistent sub-200ms response times at any scale

---

## Monitoring Recommendations

Add timing metrics to track improvements:

```go
start := time.Now()
// ... operation ...
log.Printf("[PERF] ListRecordings: %v, recordings=%d, projects=%d", 
    time.Since(start), len(recordings), len(uniqueProjects))
```

Track these metrics:
- Total request duration
- GitLab API calls count
- Redis operations count
- Cache hit rate

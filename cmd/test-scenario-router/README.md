# Test Scenario Router

A Go-based tool to automatically map test scenarios from Excel files to frontend page routes.

## Purpose

This tool replaces the manual "knowledge graph" process for mapping test scenarios to page routes. It:

1. Extracts routes from Next.js frontend app structure
2. Parses test scenario Excel files (following specific test management format)
3. Matches test scenarios to routes using keyword analysis
4. Outputs mappings with confidence scores

## Usage

```bash
go run . <excel_file> <frontend_src_dir> [options]

# Example
go run . "/Downloads/Test Scenario.xlsx" "/path/to/frontend-service/src/app"
```

### Environment Variables

- `OUTPUT_JSON` - Path to save JSON output

```bash
OUTPUT_JSON=./mappings.json go run . <excel_file> <frontend_src_dir>
```

## Excel Format Expected

The tool expects Excel files with sheets following this structure:

| User Story | Test ID | Test Type | Test Scenario | Pre-condition | Test Step | Result | Status |
|------------|---------|-----------|---------------|---------------|-----------|--------|--------|

- Row 11 (index): Headers
- Test data starts from row 13
- Sheet name maps to a page in the frontend

## Matching Algorithm

1. **Sheet Name Matching**: Extracts keywords from sheet names and matches against route segments
2. **Test Step Analysis**: Analyzes individual test steps for route hints
3. **Confidence Scoring**: 
   - Exact keyword matches: +3 points
   - Action keyword matches (approve/reject/edit): +0.3 bonus
   - Partial/fuzzy matches: +1 point

## Output Format

### Terminal Output
```
╔══════════════════════════════════════════════════════════════════════════════════════╗
║ 🟢 Fiscal Year     │ /master-data/fiscal-years/[id]/edit                          ║
║   Confidence: 100%       │ Match: Matched: [fiscal (exact) year ~ years]         ║
╚══════════════════════════════════════════════════════════════════════════════════════╝
```

### JSON Output
```json
[
  {
    "sheet_name": "Fiscal Year",
    "route": "(authenticated)/master-data/fiscal-years/[id]/edit",
    "confidence": 1.0,
    "match_reason": "Matched: [fiscal (exact) year ~ years]",
    "match_source": "sheet_name",
    "test_cases": [
      {
        "test_id": "FY-001",
        "test_type": "Positive",
        "test_scenario": "Menampilkan data fiscal year",
        "test_step": "..."
      }
    ]
  }
]
```

## Future Improvements

- [ ] Use LLM for smarter matching when confidence is low
- [ ] Support for multiple frontend services
- [ ] Auto-generate test recordings configuration
- [ ] Integration with existing knowledge graph system

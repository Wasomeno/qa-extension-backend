# Recent Activity Feed PRD

## HR Eng

| Recent Activity Feed PRD |  | Summary: Enhance the developer dashboard with a real-time activity feed showing the latest updates (comments, label changes, status changes) on issues assigned to the user. |
| :---- | :---- | :---- |
| **Author**: Pickle Rick **Contributors**: User **Intended audience**: Engineering | **Status**: Approved **Created**: 2026-02-01 | **Context**: PLAN_RECENT_ACTIVITY_FEED.md |

## Introduction

The current dashboard is a static list of issues. It tells us *what* work exists but not *what's happening* to it. This feature introduces a dynamic "Recent Activity" feed to the `GetDashboardStats` endpoint, providing context on the latest actions taken on assigned issues.

## Problem Statement

**Current Process:** Users have to click into individual issues to see if there are new comments or status changes.
**Primary Users:** Developers using the QA Extension Dashboard.
**Pain Points:** Information siloed inside issue detail views; lack of "at-a-glance" status updates.
**Importance:** Improves developer velocity by highlighting active discussions and blockers immediately.

## Objective & Scope

**Objective:** Enhance `GetDashboardStats` to return enriched activity data for the top 5 most recently updated issues.
**Ideal Outcome:** A frontend widget displays "User X commented..." or "User Y changed label..." for the latest interaction on each active issue.

### In-scope or Goals
- Fetch top 5 issues `assigned_to_me` sorted by `updated_at`.
- Enrich each issue with the *single most recent* Note (System or Comment).
- specific `ActivityFeedItem` structure in the JSON response.
- **Performance**: Concurrent fetching of notes to avoid N+1 latency penalties.

### Not-in-scope or Non-Goals
- Real-time websockets (polling or refresh on load is sufficient).
- History deeper than the absolute latest note per issue.
- Issues not assigned to the user.

## Product Requirements

### Critical User Journeys (CUJs)
1. **[Dashboard Load]**: User opens the dashboard. The backend fetches their assigned issues. Concurrently, it fetches the latest activity note for the top 5. The response includes a `recent_activities` array. The UI renders this list.

### Functional Requirements

| Priority | Requirement | User Story |
| :---- | :---- | :---- |
| P0 | **API Endpoint Update** | As a dev, I need `GET /dashboard/stats` to include `recent_activities`. |
| P0 | **Concurrency** | As a system, I must fetch notes for 5 issues in parallel to keep response time < 500ms. |
| P1 | **Data Enrichment** | As a dev, I want to see *who* did the action and *what* the action was (comment vs system note). |
| P2 | **Error Resilience** | If fetching notes for Issue A fails, the dashboard should still load (with missing activity for A). |

## Assumptions

- GitLab API rate limits allow for the burst of calls (5 extra calls per dashboard load).
- System notes in GitLab contain parseable text for "Description".

## Risks & Mitigations

- **Risk**: API Rate Limiting. -> **Mitigation**: We are only fetching 5 items. If we scale, we will need caching.
- **Risk**: N+1 Latency. -> **Mitigation**: Strict concurrency using `sync.WaitGroup`.

## Tradeoff

- **Chosen**: Fetch-on-read. **Pros**: Always fresh. **Cons**: Slower than caching.
- **Rejected**: Background worker caching. **Pros**: Fast read. **Cons**: Complexity overkill for 5 items.

## Business Benefits/Impact/Metrics

**Success Metrics:**

| Metric | Current State (Benchmark) | Future State (Target) | Savings/Impacts |
| :---- | :---- | :---- | :---- |
| *API Latency* | ~200ms | < 400ms | minimal impact despite extra data |
| *User Clicks* | High (checking issues) | Low (glance at feed) | Improved UX |

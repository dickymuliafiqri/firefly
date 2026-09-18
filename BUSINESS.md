# BUSINESS.md — Business Architecture, System Flow, and Team Delegation Manual

This document serves as the **official operational guide** for launching and operating a Unified AI API Gateway as a Service using **Firefly**.

It is designed for a **4-person cross-functional team** to clarify architectural boundaries, transactional lifecycle, operational responsibilities, and single-source-of-truth database governance.

---

## TABLE OF CONTENTS
1. [Executive Summary & Collaboration Model](#1-executive-summary--collaboration-model)
2. [Business Model Canvas (BMC) 9 Blocks](#2-business-model-canvas-bmc-9-blocks)
3. [System Architecture & Transactional Flow](#3-system-architecture--transactional-flow)
4. [EXPLICIT DATABASE GOVERNANCE (Critical)](#4-explicit-database-governance-critical)
5. [Team Breakdown & Responsibilities (4 Roles)](#5-team-breakdown--responsibilities-4-roles)
   - [Role 1: Core Gateway & Infrastructure Lead (Firefly)](#role-1-core-gateway--infrastructure-lead-firefly)
   - [Role 2: Telegram Bot & Onboarding Engineer (Storefront)](#role-2-telegram-bot--onboarding-engineer-storefront)
   - [Role 3: Account Harvester & Token Supply Engineer (Supply)](#role-3-account-harvester--token-supply-engineer-supply)
   - [Role 4: Billing Daemon & Tenant Lifecycle Engineer (Backend)](#role-4-billing-daemon--tenant-lifecycle-engineer-backend)
6. [Unit Economics & Profit-Sharing Model](#6-unit-economics--profit-sharing-model)
7. [Fair Usage Policy (FUP) & Risk Mitigation](#7-fair-usage-policy-fup--risk-mitigation)
8. [Launch Checklist](#8-launch-checklist)

---

## 1. Executive Summary & Collaboration Model

### A. Business Vision
Provide access to high-performance AI model inference (e.g. DeepSeek V3, GPT-4o-mini, Claude, Gemini Flash, Grok) with **low pricing, high uptime, and unlimited daily tokens** within bounded concurrency, fully wire-compatible with the standard OpenAI API specification (`/v1/chat/completions`).

### B. Core Product Offering
- **Pricing:** Flat daily, weekly, or monthly subscription tier (e.g., $0.35 / 24 hours, $2.00 / week, $6.50 / month).
- **Features:** Unlimited total tokens per billing window, strictly governed by **RPS (Requests Per Second) & Concurrency Gates** to prevent automated scraping or abuse.
- **Target Audience:** Developers, students, and active users of IDE coding extensions (Cline, Roo-Code, Cursor, Continue.dev) seeking affordable, reliable AI inference without foreign credit card friction.

### C. Team Structure (4 Persons)
The system harmonizes **Core Gateway Proxy (Firefly)** + **Token Supply Pool (Commercial & Harvested Credentials)** + **Automated Storefront (Telegram Bot & Payment Provider)** + **Lifecycle Daemon (Billing & Revocation Scheduler)**.

---

## 2. Business Model Canvas (BMC) 9 Blocks

| **1. Key Partners** | **2. Key Activities** | **4. Value Propositions** | **5. Customer Relationships** | **7. Customer Segments** |
| :--- | :--- | :--- | :--- | :--- |
| • **Upstream Credential Partners:** Token pools, enterprise grants, promo credits.<br>• **Model Providers:** OpenAI, Anthropic, Google Cloud, DeepSeek, xAI Grok.<br>• **Cloud & Storage:** VPS (Hetzner/AWS) & Turso/libSQL database.<br>• **Payment Gateways:** Instant payment gateways (QRIS/Stripe/crypto).<br>• **Telegram:** Bot platform and community channel. | • **Proxy Operations:** Maintain 99.9% gateway uptime & sub-second TTFB.<br>• **Token Harvesting:** Aggregate, health-check, and rotate credentials.<br>• **Automated Storefront:** 24/7 self-service order fulfillment.<br>• **Lifecycle Enforcement:** Auto-revoke tenant access upon expiration. | • **Ultra-Low-Cost Inference:** Predictable flat rate without token anxiety.<br>• **Unified API Endpoint:** Single base URL for all client tooling.<br>• **Layered Resilience:** Zero rate-limit errors via automatic KeyRing failover.<br>• **Instant Frictionless Onboarding:** Immediate activation upon payment. | • **Self-Service Telegram Bot:** Automated setup guide and credential issuance.<br>• **Exclusive Community Support:** Peer-to-peer developer troubleshooting.<br>• **Transparent Status:** Expiration reminders and real-time validity checks. | • **Students & Hobbyists:** Affordable coding tools for coursework and side projects.<br>• **Indie Developers:** Heavy daily users of Cline, Roo-Code, and Cursor.<br>• **Software Boutiques:** Backup capacity for internal development workflows.<br>• **Internal Team Projects:** Shared inference pool across team members. |
| **3. Key Resources** | | | **6. Channels** | |
| • **Firefly High-Concurrency Gateway:** Low-latency Go proxy core.<br>• **Central Turso/libSQL Database:** Real-time multi-node synchronization.<br>• **Aggregated Credential Pool:** Managed KeyRing with cooldown rotation.<br>• **Telegram Bot & Community Group:** Primary acquisition and distribution funnel. | | | • **Telegram Community Group:** Required group membership to use the bot.<br>• **Social & Dev Media (YouTube/X/Reddit):** Setup tutorials for AI coding extensions.<br>• **Developer Communities:** Discord and local developer tech groups. | |
| **8. Cost Structure** | | | **9. Revenue Streams** | |
| • **Upstream Inference (COGS):** Commercial token usage & account maintenance.<br>• **VPS & Infrastructure:** Gateway hosting (~$10 - $20/month).<br>• **Database Hosting:** Turso database starter tier ($0 - $5/month).<br>• **Payment Processing Fees:** Transaction fee (~0.7% - 2.5% per order). | | | • **Daily Pass:** 24-hour access.<br>• **Weekly Pass:** 7-day access.<br>• **Monthly Pass:** 30-day access.<br>• **Dedicated Private Tenants:** Custom limits for teams and small agencies. | |

---

## 3. System Architecture & Transactional Flow

The architecture operates in a distributed yet synchronized pattern around **a single central Turso/libSQL database**:

```mermaid
flowchart TD
    subgraph ClientLayer ["1. Client & Storefront Layer"]
        User([Customer / Client])
        TGroup[Telegram Community Group]
        TBot[Automated Telegram Bot (Role 2)]
    end

    subgraph BusinessDaemon ["2. Backend & Billing (Role 4)"]
        PayGW[Payment Gateway / Webhook]
        BillingWorker[Billing & Expiration Daemon]
    end

    subgraph DataPlane ["3. Single Central Database (Turso / SQLite)"]
        T_Users[(Business Tables: users, orders, subscriptions)]
        T_Firefly[(Firefly Native Tables: tenants, upstreams, api_keys, catalog_revisions)]
    end

    subgraph SupplyPlane ["4. Supply Engine (Role 3)"]
        Scraper[Token Harvester / Trial Aggregator]
        HealthCheck[Pre-Flight Token Health Validator]
    end

    subgraph GatewayPlane ["5. Core Proxy (Role 1)"]
        FireflyCore[Firefly Gateway Server]
        UpstreamLLM[Upstream AI Providers]
    end

    User -->|Join| TGroup
    User -->|Command /buy| TBot
    TBot -->|Verify Group Membership| TGroup
    TBot -->|Request Payment Link| PayGW
    PayGW -->|Payment Webhook| BillingWorker
    BillingWorker -->|Persist Order| T_Users
    BillingWorker -->|Insert Tenant & Bump Revision| T_Firefly
    BillingWorker -->|Emit Generated Key| TBot
    TBot -->|Deliver sk-gw-... & Setup Guide| User

    Scraper --> HealthCheck
    HealthCheck -->|Insert Valid Credentials| T_Firefly

    T_Firefly -.->|Auto Hot-Reload Syncer| FireflyCore
    User -->|Inference Request via IDE| FireflyCore
    FireflyCore -->|Forward Request| UpstreamLLM
```

---

## 4. EXPLICIT DATABASE GOVERNANCE (Critical)

> [!CAUTION]
> ### NOTICE FOR ALL TEAM MEMBERS:
> **DO NOT SPIN UP SEPARATE DATABASE INSTANCES!**
> Firefly **ALREADY CONTAINS A CENTRAL DATABASE** powered by **Turso / libSQL / SQLite**.
> All 4 roles must connect to the **same database connection string** (either Turso Cloud `libsql://...` or a shared SQLite file).
>
> Do not deploy independent PostgreSQL, MySQL, or Redis instances. All business and proxy state lives inside the **single unified database**.

### A. Pre-Existing Firefly Native Tables (Do Not Alter Schemas)
Defined in: `internal/storage/turso/schema.go` and `internal/storage/turso/store.go`.

1. **`tenants`**: Client identities authorized to access Firefly.
   - Key columns: `id`, `name`, `key_hash` (format `sha256:<hex>`), `key_hint`, `status`, `rps`, `burst`, `max_concurrent`, `allowed_models`.
2. **`upstreams`**: Outbound provider definitions (OpenAI, Anthropic, Antigravity, Grok, etc.).
3. **`api_keys`** *(Harvester Pool Table)*: **Already built into Firefly!** Holds all upstream API keys and harvester credentials.
   - Key columns: `id`, `provider_id`, `api_key`, `status` (`active`/`cooldown`/`revoked`), `is_active`, `expires_at`, `total_requests`.
4. **`models` & `combos`**: Public model routing, virtual failovers, and latency-based balancing.
5. **`catalog_revisions`**: Atomic catalog cache invalidation. When `revision` is incremented (`UPDATE catalog_revisions SET revision = revision + 1`), **Firefly automatically hot-reloads its in-memory snapshot** without server restart or dropped connections.

### B. Business Extension Tables (Created in the Same Database)
Created and maintained by **Role 4 (Billing Engineer)** in the same Turso database to track user subscriptions:

```sql
-- BUSINESS EXTENSION DDL (Execute against the same Turso database)

-- 1. Telegram User Registry
CREATE TABLE IF NOT EXISTS users (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    telegram_user_id  BIGINT NOT NULL UNIQUE,
    username          VARCHAR(64),
    joined_group_at   BIGINT,
    created_at        BIGINT NOT NULL
);

-- 2. Invoices & Transactions
CREATE TABLE IF NOT EXISTS orders (
    id                VARCHAR(64) PRIMARY KEY, -- e.g. "INV-20260916-0001"
    user_id           INTEGER NOT NULL REFERENCES users(id),
    amount            INTEGER NOT NULL,
    payment_method    VARCHAR(32),             -- "qris", "stripe", "crypto"
    payment_status    VARCHAR(20) NOT NULL,    -- "pending", "paid", "expired"
    paid_at           BIGINT,
    created_at        BIGINT NOT NULL
);

-- 3. Subscription & Tenant Link
CREATE TABLE IF NOT EXISTS subscriptions (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    order_id          VARCHAR(64) REFERENCES orders(id),
    user_id           INTEGER NOT NULL REFERENCES users(id),
    tenant_id         INTEGER REFERENCES tenants(id), -- Relates directly to Firefly tenants!
    raw_api_key       TEXT NOT NULL,                  -- Client gateway token: sk-gw-...
    started_at        BIGINT NOT NULL,
    expires_at        BIGINT NOT NULL,                -- started_at + duration
    status            VARCHAR(20) NOT NULL DEFAULT 'active' -- 'active', 'expired'
);

CREATE INDEX IF NOT EXISTS idx_subscriptions_expiry ON subscriptions(expires_at, status);
```

---

## 5. Team Breakdown & Responsibilities (4 Roles)

Language and tooling choices for auxiliary services (bot, harvester, billing daemon) are flexible (Go, Python, TypeScript, etc.) as long as the functional invariants are met.

---

### Role 1: Core Gateway & Infrastructure Lead (Firefly)
**Owner:** Core Infrastructure Lead  
**Objective:** Maintain 24/7 gateway availability, low latency, high-concurrency SSE stream stability, and zero-downtime hot reloading.

#### Responsibilities:
1. **Firefly Runtime with Turso Integration:**
   Execute Firefly with production flags:
   ```bash
   ./bin/firefly \
     --addr "0.0.0.0:8080" \
     --turso-url "libsql://db-firefly-xxxx.turso.io" \
     --turso-token "$TURSO_AUTH_TOKEN" \
     --turso-sync-interval 10s \
     --log-level info
   ```
2. **SSE Streaming & Concurrency Tuning:**
   Monitor watchdog gaps, time-to-first-byte (TTFB), and buffer recycling (`sync.Pool`). Ensure zero goroutine leaks on client disconnects.
3. **Virtual Combos & Upstream Routing:**
   Configure model fallback sequences (e.g., primary DeepSeek V3 with automatic fallback to GPT-4o-mini or Grok).
4. **Cloud Infrastructure & Auto-TLS:**
   Oversee VPS deployments, reverse proxy layers (Caddy/Nginx), SSL termination, and Cloudflare routing.

---

### Role 2: Telegram Bot & Onboarding Engineer (Storefront)
**Owner:** Storefront Engineer  
**Objective:** Build the customer-facing interface on Telegram: enforce group membership, guide purchases, generate payment links, and deliver API keys with setup guides.

#### Responsibilities:
1. **Mandatory Group Membership Validation:**
   Before processing `/buy`, verify that `user_id` is an active group member via the Telegram API `getChatMember`. Reject non-members with an invitation link.
2. **Bot Commands:**
   - `/start`: Welcome message and product overview.
   - `/buy`: Select subscription tier.
   - `/status`: Query remaining subscription time.
   - `/guide`: Setup instructions for Cline, Roo-Code, Cursor, Continue.dev, and OpenAI SDK.
3. **Payment Integration:**
   Forward payment requests to the billing service (Role 4).
4. **Key Delivery:**
   Upon payment confirmation, privately deliver the gateway key (`sk-gw-...`) and endpoint URL.

#### Code Snippet (Python — `python-telegram-bot`):
```python
import os
from telegram import Update
from telegram.ext import ApplicationBuilder, CommandHandler, ContextTypes

GROUP_CHAT_ID = -1001234567890

async def is_user_in_group(bot, user_id: int) -> bool:
    try:
        member = await bot.get_chat_member(chat_id=GROUP_CHAT_ID, user_id=user_id)
        return member.status in ["member", "administrator", "creator"]
    except Exception:
        return False

async def buy_command(update: Update, context: ContextTypes.DEFAULT_TYPE):
    user_id = update.effective_user.id
    in_group = await is_user_in_group(context.bot, user_id)
    
    if not in_group:
        await update.message.reply_text(
            "⚠️ You must join our community group first!\n\n"
            "Join: https://t.me/OurCommunityGroup\n"
            "Once joined, run /buy again."
        )
        return

    # User is confirmed in group; generate order via Billing API (Role 4)
    # invoice = billing_client.create_order(user_id=user_id, amount=5000)
    await update.message.reply_text(
        "🎉 Unlimited AI Access (24 Hours)\n\n"
        "Complete payment here:\n"
        "https://payment-gateway.com/pay/INV-123\n\n"
        "Your API key will be delivered instantly upon payment confirmation."
    )

app = ApplicationBuilder().token(os.getenv("TELEGRAM_BOT_TOKEN")).build()
app.add_handler(CommandHandler("buy", buy_command))
app.run_polling()
```

---

### Role 3: Account Harvester & Token Supply Engineer (Supply)
**Owner:** Token Supply Engineer  
**Objective:** Maintain healthy upstream API key inventory in Firefly so that the gateway never runs out of quota or encounters prolonged provider rate limits.

#### Responsibilities:
1. **Credential Sourcing & Rotation:**
   Collect and rotate valid credentials (commercial keys, trial tiers, or harvester tokens).
2. **Pre-Flight Health Probing:**
   Before inserting credentials into the database, perform a lightweight inference probe against the provider:
   - HTTP 200 $\rightarrow$ Key healthy $\rightarrow$ Insert into database.
   - HTTP 401 / 429 $\rightarrow$ Key invalid or exhausted $\rightarrow$ Discard.
3. **Database Injection into `api_keys`:**
   Insert valid credentials directly into Firefly's native `api_keys` table:
   ```sql
   INSERT INTO api_keys (provider_id, api_key, status, is_active, created_at, updated_at)
   VALUES (1, 'sk-upstream-token-...', 'active', 1, 1710000000000, 1710000000000);
   ```
4. **Credential Pruning:**
   Run periodic checks to mark expired or revoked keys (`is_active = 0`) to maintain a clean pool.

#### Code Snippet (Python Harvester):
```python
import requests
import time

def test_openai_token(token: str) -> bool:
    """Validate token viability before database insertion."""
    headers = {"Authorization": f"Bearer {token}"}
    try:
        r = requests.get("https://api.openai.com/v1/models", headers=headers, timeout=5)
        return r.status_code == 200
    except Exception:
        return False

def inject_to_firefly_db(provider_id: int, valid_key: str, db_cursor, db_conn):
    now_ms = int(time.time() * 1000)
    query = """
    INSERT INTO api_keys (provider_id, api_key, status, is_active, created_at, updated_at)
    VALUES (?, ?, 'active', 1, ?, ?)
    """
    db_cursor.execute(query, (provider_id, valid_key, now_ms, now_ms))
    db_conn.commit()
    print(f"Successfully injected active key into provider pool {provider_id}")
```

---

### Role 4: Billing Daemon & Tenant Lifecycle Engineer (Backend)
**Owner:** Billing & Lifecycle Engineer  
**Objective:** Connect payment events with Firefly tenant lifecycle: verify incoming webhooks, provision tenant records, and automatically revoke access upon expiration.

#### Responsibilities:
1. **Schema Initialization:**
   Execute business table definitions (`users`, `orders`, `subscriptions`) on the Turso database.
2. **Payment Webhook Processing:**
   Handle payment success webhooks:
   - Generate secure client key: `rawKey = "sk-gw-" + randomString(32)`.
   - Calculate hash matching Firefly requirements: `key_hash = "sha256:" + sha256_hex(rawKey)`.
   - Extract key hint: `key_hint = rawKey[-8:]`.
   - Insert into Firefly's native `tenants` table.
   - Increment revision in `catalog_revisions` (`UPDATE catalog_revisions SET revision = revision + 1`) to trigger an **instant hot reload in Firefly without downtime**.
   - Dispatch `rawKey` to the Telegram bot (Role 2) for customer delivery.
3. **Automated Expiration Scheduler:**
   Run a periodic background worker (every 1–5 minutes):
   - Identify subscriptions where `expires_at <= NOW()` and `status = 'active'`.
   - Update Firefly's native `tenants` table: set `status = 'disabled'`.
   - Increment `catalog_revisions` to immediately block subsequent client requests.
   - Mark subscription `status = 'expired'`.
   - Notify the user via the Telegram bot with a renewal prompt.

#### Firefly Tenant Format Requirements:
```sql
INSERT INTO tenants (
    name,
    key_hash,
    key_hint,
    status,
    rps,
    burst,
    max_concurrent,
    allowed_models,
    created_at,
    updated_at
) VALUES (
    'user-tg-12345678',
    'sha256:4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945',
    '***ab12cd34',
    'active',
    0.25,
    2,
    1, -- Strict concurrency limit (anti-abuse)
    '["gpt-4o-mini", "deepseek-v3", "gemini-1.5-flash"]',
    1710000000000,
    1710000000000
);

-- Bump catalog revision for instant hot-reload
UPDATE catalog_revisions SET revision = revision + 1, updated_at = 1710000000000 WHERE id = 1;
```

#### Code Snippet (Go — Expiration Scheduler):
```go
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
)

func RunExpiryWorker(ctx context.Context, db *sql.DB) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nowMs := time.Now().UnixMilli()

			// 1. Find subscriptions past expiration
			rows, err := db.QueryContext(ctx, `
				SELECT s.id, s.tenant_id, s.user_id 
				FROM subscriptions s 
				WHERE s.expires_at <= ? AND s.status = 'active'
			`, nowMs)
			if err != nil {
				log.Println("Error querying expired subscriptions:", err)
				continue
			}

			var expiredTenantIDs []int64
			for rows.Next() {
				var subID, tenantID, userID int64
				if err := rows.Scan(&subID, &tenantID, &userID); err == nil {
					expiredTenantIDs = append(expiredTenantIDs, tenantID)
					_, _ = db.ExecContext(ctx, `UPDATE subscriptions SET status = 'expired' WHERE id = ?`, subID)
					// Dispatch renewal reminder to Telegram bot
				}
			}
			rows.Close()

			// 2. Disable corresponding tenants in Firefly native table
			if len(expiredTenantIDs) > 0 {
				for _, tid := range expiredTenantIDs {
					_, _ = db.ExecContext(ctx, `UPDATE tenants SET status = 'disabled', updated_at = ? WHERE id = ?`, nowMs, tid)
				}
				// 3. Increment catalog revision for instant gateway hot-reload
				_, _ = db.ExecContext(ctx, `UPDATE catalog_revisions SET revision = revision + 1, updated_at = ? WHERE id = 1`, nowMs)
				fmt.Printf("Disabled %d expired tenants in Firefly catalog.\n", len(expiredTenantIDs))
			}
		}
	}
}
```

---

## 6. Unit Economics & Profit-Sharing Model

### A. Per-Customer Unit Economics Example (Daily Tier)
- **Retail Price:** ~$0.35 / day (or ~$6.50 / month).
- **Typical Human Developer Consumption:** ~100 requests/day $\approx$ 100,000 tokens.
- **Upstream Token Cost (DeepSeek V3 / GPT-4o-mini / Gemini Flash):** $\approx$ $0.025 / day.
- **Payment Processing Fee:** ~$0.01.
- **Gross Profit Margin per Transaction:** $\approx \mathbf{90\%+}$.

### B. Monthly Projection (200 Daily Active Subscribers)
- **Gross Revenue:** $200 \times \$0.35 \times 30 = \mathbf{\$2,100 / \text{month}}$.
- **Upstream Inference Cost:** $\approx \$150$.
- **VPS Infrastructure & Domain:** $\approx \$30$.
- **Payment Processing Fees:** $\approx \$50$.
- **Net Team Operating Profit:** $\approx \mathbf{\$1,870 / \text{month}}$.

### C. Team Profit Distribution (4 Roles)
Net operating profits are distributed equitably across core operational pillars:
1. **Role 1 (Core Gateway & Infrastructure):** 25%
2. **Role 2 (Telegram Bot & Storefront):** 25%
3. **Role 3 (Token Harvester & Supply):** 25%
4. **Role 4 (Billing Engine & Database Lifecycle):** 25%

---

## 7. Fair Usage Policy (FUP) & Risk Mitigation

To protect uptime and prevent financial leakage from abusive scrapers, enforce the following invariants:

1. **Concurrency Gate (`max_concurrent = 1`):**
   Each customer is restricted to 1 active in-flight request at any millisecond. Concurrent requests fail immediately with HTTP 429, preventing parallel token extraction.
2. **Rate Limiting (`rps = 0.25`):**
   1 request per 4 seconds token bucket. Comfortable for human coding with IDE extensions, but infeasible for denial-of-service or high-speed bulk scraping.
3. **Upstream Isolation (Blast Radius Control):**
   Role 3 distributes tokens across Firefly's `KeyRing`. If an upstream account faces provider rate limits or quota revocation, Firefly automatically fails over across remaining keys without client-visible downtime.
4. **Automated Renewal Notifications:**
   Role 4 issues an expiration reminder 1 hour prior to access expiration to encourage recurring renewal.

---

## 8. Launch Checklist

- [ ] **Day 1:** Provision central Turso database. Role 4 deploys DDL for `users`, `orders`, and `subscriptions`.
- [ ] **Day 2:** Role 1 executes Firefly connected to Turso; confirm `/healthz` returns `200 OK`.
- [ ] **Day 3:** Role 3 populates verified active credentials into `api_keys`.
- [ ] **Day 4:** Role 2 deploys the Telegram bot and verifies `getChatMember` group validation.
- [ ] **Day 5:** Role 4 connects payment webhooks and verifies automatic tenant provisioning.
- [ ] **Day 6:** Execute full end-to-end integration test (Buy via Bot $\rightarrow$ Confirm Payment $\rightarrow$ Receive Gateway Key $\rightarrow$ Use in Cline/Cursor $\rightarrow$ Verify automatic 24-hour expiration).
- [ ] **Day 7:** Open community group and commence public onboarding.

"use strict";
/**
 * Kronaxis Router TypeScript SDK.
 *
 * Zero-dependency client that wraps the OpenAI-compatible API with
 * automatic routing metadata for cost-optimised backend selection.
 *
 * @example
 * ```typescript
 * import { KronaxisRouter, Tier } from 'kronaxis-router';
 *
 * const router = new KronaxisRouter('http://localhost:8050', { service: 'my-app' });
 * const response = await router.chat('Summarise this...', { tier: Tier.Light });
 * ```
 */
Object.defineProperty(exports, "__esModule", { value: true });
exports.RouterError = exports.KronaxisRouter = exports.Tier = void 0;
var Tier;
(function (Tier) {
    Tier[Tier["Auto"] = 0] = "Auto";
    Tier[Tier["Heavy"] = 1] = "Heavy";
    Tier[Tier["Light"] = 2] = "Light";
})(Tier || (exports.Tier = Tier = {}));
class KronaxisRouter {
    constructor(baseUrl = 'http://localhost:8050', config = {}) {
        this.baseUrl = baseUrl.replace(/\/$/, '');
        this.service = config.service || 'typescript-sdk';
        this.defaultTier = config.defaultTier ?? Tier.Auto;
        this.defaultPriority = config.defaultPriority ?? 'normal';
        this.apiToken = config.apiToken;
        this.timeout = config.timeout ?? 120000;
    }
    /**
     * Send a chat completion request through the router.
     * Returns the assistant's response text.
     */
    async chat(prompt, options = {}) {
        const messages = [];
        if (options.system) {
            messages.push({ role: 'system', content: options.system });
        }
        messages.push({ role: 'user', content: prompt });
        return this.chatMessages(messages, options);
    }
    /**
     * Send a multi-turn chat completion request.
     */
    async chatMessages(messages, options = {}) {
        const body = {
            model: options.model || 'default',
            messages,
            max_tokens: options.maxTokens || 2048,
            temperature: options.temperature ?? 0.7,
        };
        const headers = {
            'Content-Type': 'application/json',
            'X-Kronaxis-Service': this.service,
            'X-Kronaxis-Priority': options.priority || this.defaultPriority,
        };
        const tier = options.tier ?? this.defaultTier;
        if (tier !== Tier.Auto) {
            headers['X-Kronaxis-Tier'] = String(tier);
        }
        if (options.callType)
            headers['X-Kronaxis-CallType'] = options.callType;
        if (options.personaId)
            headers['X-Kronaxis-PersonaID'] = options.personaId;
        if (this.apiToken)
            headers['Authorization'] = `Bearer ${this.apiToken}`;
        const resp = await this.fetch(`${this.baseUrl}/v1/chat/completions`, {
            method: 'POST',
            headers,
            body: JSON.stringify(body),
        });
        if (!resp.choices?.length) {
            throw new RouterError(0, 'no choices in response');
        }
        return resp.choices[0].message.content;
    }
    /** Submit an async batch job for 50% cost savings. */
    async batchSubmit(backend, requests, callbackUrl) {
        const body = { backend, requests };
        if (callbackUrl)
            body.callback_url = callbackUrl;
        return this.apiCall('POST', '/api/batch/submit', body);
    }
    /** Get batch job status. */
    async batchStatus(jobId) {
        return this.apiCall('GET', `/api/batch?id=${jobId}`);
    }
    /** Get batch job results. */
    async batchResults(jobId) {
        return this.apiCall('GET', `/api/batch/results?id=${jobId}`);
    }
    /** Get cost dashboard. */
    async costs(period = 'today') {
        return this.apiCall('GET', `/api/costs?period=${period}`);
    }
    /** Get router health. */
    async health() {
        return this.apiCall('GET', '/health');
    }
    async fetch(url, init) {
        const controller = new AbortController();
        const timeoutId = setTimeout(() => controller.abort(), this.timeout);
        try {
            const response = await globalThis.fetch(url, { ...init, signal: controller.signal });
            let data;
            try {
                data = await response.json();
            }
            catch {
                const text = await response.text().catch(() => '');
                throw new RouterError(response.status, text || `HTTP ${response.status}`);
            }
            if (!response.ok) {
                throw new RouterError(response.status, data?.error?.message || JSON.stringify(data));
            }
            return data;
        }
        catch (e) {
            clearTimeout(timeoutId);
            if (e instanceof RouterError)
                throw e;
            if (e instanceof DOMException && e.name === 'AbortError') {
                throw new RouterError(0, `request timed out after ${this.timeout}ms`);
            }
            throw new RouterError(0, e instanceof Error ? e.message : String(e));
        }
        finally {
            clearTimeout(timeoutId);
        }
    }
    async apiCall(method, path, body) {
        const headers = { 'Content-Type': 'application/json' };
        if (this.apiToken)
            headers['Authorization'] = `Bearer ${this.apiToken}`;
        return this.fetch(`${this.baseUrl}${path}`, {
            method,
            headers,
            body: body ? JSON.stringify(body) : undefined,
        });
    }
}
exports.KronaxisRouter = KronaxisRouter;
class RouterError extends Error {
    constructor(statusCode, message) {
        super(`Router error ${statusCode}: ${message}`);
        this.statusCode = statusCode;
        this.name = 'RouterError';
    }
}
exports.RouterError = RouterError;

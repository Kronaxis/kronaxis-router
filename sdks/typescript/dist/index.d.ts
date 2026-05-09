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
export declare enum Tier {
    Auto = 0,
    Heavy = 1,
    Light = 2
}
export type Priority = 'interactive' | 'normal' | 'background' | 'bulk';
export interface ChatMessage {
    role: 'system' | 'user' | 'assistant';
    content: string;
}
export interface ChatOptions {
    system?: string;
    model?: string;
    maxTokens?: number;
    temperature?: number;
    tier?: Tier;
    priority?: Priority;
    callType?: string;
    personaId?: string;
}
export interface BatchRequest {
    custom_id: string;
    body: {
        model: string;
        messages: ChatMessage[];
        max_tokens?: number;
        temperature?: number;
    };
}
export interface RouterConfig {
    service?: string;
    defaultTier?: Tier;
    defaultPriority?: Priority;
    apiToken?: string;
    timeout?: number;
}
export declare class KronaxisRouter {
    private baseUrl;
    private service;
    private defaultTier;
    private defaultPriority;
    private apiToken?;
    private timeout;
    constructor(baseUrl?: string, config?: RouterConfig);
    /**
     * Send a chat completion request through the router.
     * Returns the assistant's response text.
     */
    chat(prompt: string, options?: ChatOptions): Promise<string>;
    /**
     * Send a multi-turn chat completion request.
     */
    chatMessages(messages: ChatMessage[], options?: ChatOptions): Promise<string>;
    /** Submit an async batch job for 50% cost savings. */
    batchSubmit(backend: string, requests: BatchRequest[], callbackUrl?: string): Promise<any>;
    /** Get batch job status. */
    batchStatus(jobId: string): Promise<any>;
    /** Get batch job results. */
    batchResults(jobId: string): Promise<any>;
    /** Get cost dashboard. */
    costs(period?: string): Promise<any>;
    /** Get router health. */
    health(): Promise<any>;
    private fetch;
    private apiCall;
}
export declare class RouterError extends Error {
    statusCode: number;
    constructor(statusCode: number, message: string);
}

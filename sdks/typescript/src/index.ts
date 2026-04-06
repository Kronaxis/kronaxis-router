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

export enum Tier {
  Auto = 0,
  Heavy = 1,
  Light = 2,
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

export class KronaxisRouter {
  private baseUrl: string;
  private service: string;
  private defaultTier: Tier;
  private defaultPriority: Priority;
  private apiToken?: string;
  private timeout: number;

  constructor(baseUrl: string = 'http://localhost:8050', config: RouterConfig = {}) {
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
  async chat(prompt: string, options: ChatOptions = {}): Promise<string> {
    const messages: ChatMessage[] = [];
    if (options.system) {
      messages.push({ role: 'system', content: options.system });
    }
    messages.push({ role: 'user', content: prompt });
    return this.chatMessages(messages, options);
  }

  /**
   * Send a multi-turn chat completion request.
   */
  async chatMessages(messages: ChatMessage[], options: ChatOptions = {}): Promise<string> {
    const body = {
      model: options.model || 'default',
      messages,
      max_tokens: options.maxTokens || 2048,
      temperature: options.temperature ?? 0.7,
    };

    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      'X-Kronaxis-Service': this.service,
      'X-Kronaxis-Priority': options.priority || this.defaultPriority,
    };

    const tier = options.tier ?? this.defaultTier;
    if (tier !== Tier.Auto) {
      headers['X-Kronaxis-Tier'] = String(tier);
    }
    if (options.callType) headers['X-Kronaxis-CallType'] = options.callType;
    if (options.personaId) headers['X-Kronaxis-PersonaID'] = options.personaId;
    if (this.apiToken) headers['Authorization'] = `Bearer ${this.apiToken}`;

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
  async batchSubmit(backend: string, requests: BatchRequest[], callbackUrl?: string): Promise<any> {
    const body: any = { backend, requests };
    if (callbackUrl) body.callback_url = callbackUrl;
    return this.apiCall('POST', '/api/batch/submit', body);
  }

  /** Get batch job status. */
  async batchStatus(jobId: string): Promise<any> {
    return this.apiCall('GET', `/api/batch?id=${jobId}`);
  }

  /** Get batch job results. */
  async batchResults(jobId: string): Promise<any> {
    return this.apiCall('GET', `/api/batch/results?id=${jobId}`);
  }

  /** Get cost dashboard. */
  async costs(period: string = 'today'): Promise<any> {
    return this.apiCall('GET', `/api/costs?period=${period}`);
  }

  /** Get router health. */
  async health(): Promise<any> {
    return this.apiCall('GET', '/health');
  }

  private async fetch(url: string, init: RequestInit): Promise<any> {
    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), this.timeout);

    try {
      const response = await globalThis.fetch(url, { ...init, signal: controller.signal });
      const data = await response.json();
      if (!response.ok) {
        throw new RouterError(response.status, data?.error?.message || JSON.stringify(data));
      }
      return data;
    } finally {
      clearTimeout(timeoutId);
    }
  }

  private async apiCall(method: string, path: string, body?: any): Promise<any> {
    const headers: Record<string, string> = { 'Content-Type': 'application/json' };
    if (this.apiToken) headers['Authorization'] = `Bearer ${this.apiToken}`;

    return this.fetch(`${this.baseUrl}${path}`, {
      method,
      headers,
      body: body ? JSON.stringify(body) : undefined,
    });
  }
}

export class RouterError extends Error {
  statusCode: number;

  constructor(statusCode: number, message: string) {
    super(`Router error ${statusCode}: ${message}`);
    this.statusCode = statusCode;
    this.name = 'RouterError';
  }
}

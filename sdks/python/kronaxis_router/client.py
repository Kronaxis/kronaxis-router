"""Kronaxis Router client that wraps OpenAI API calls with routing metadata."""

from enum import IntEnum
from typing import Optional, List, Dict, Any
import json
import urllib.request
import urllib.error


class Tier(IntEnum):
    """LLM capability tiers for routing."""
    AUTO = 0       # Let the router classify automatically
    HEAVY = 1      # Complex reasoning, planning, creative writing
    LIGHT = 2      # Structured extraction, classification, scoring


class KronaxisRouter:
    """Client for Kronaxis Router.

    Wraps the OpenAI-compatible /v1/chat/completions endpoint with
    automatic routing metadata (tier, service, priority, call type).

    Args:
        base_url: Router URL (e.g. "http://localhost:8050")
        service: Service name for routing and cost tracking
        default_tier: Default tier when not specified per-call
        default_priority: Default priority ("interactive", "normal", "background", "bulk")
        api_token: Bearer token for API authentication (if ROUTER_API_TOKEN is set)
        timeout: Request timeout in seconds
    """

    def __init__(
        self,
        base_url: str = "http://localhost:8050",
        service: str = "python-sdk",
        default_tier: Tier = Tier.AUTO,
        default_priority: str = "normal",
        api_token: Optional[str] = None,
        timeout: int = 120,
    ):
        self.base_url = base_url.rstrip("/")
        self.service = service
        self.default_tier = default_tier
        self.default_priority = default_priority
        self.api_token = api_token
        self.timeout = timeout

    def chat(
        self,
        prompt: str,
        *,
        system: Optional[str] = None,
        model: str = "default",
        max_tokens: int = 2048,
        temperature: float = 0.7,
        tier: Optional[Tier] = None,
        priority: Optional[str] = None,
        call_type: Optional[str] = None,
        persona_id: Optional[str] = None,
        stream: bool = False,
    ) -> str:
        """Send a chat completion request through the router.

        Args:
            prompt: User message
            system: Optional system prompt
            model: Model name or LoRA adapter name
            max_tokens: Maximum output tokens
            temperature: Sampling temperature
            tier: Override routing tier (AUTO lets the router classify)
            priority: Override priority level
            call_type: Task type for routing rules
            persona_id: For cost attribution
            stream: Enable streaming (not yet supported in SDK)

        Returns:
            The assistant's response text.
        """
        messages = []
        if system:
            messages.append({"role": "system", "content": system})
        messages.append({"role": "user", "content": prompt})

        return self.chat_messages(
            messages=messages,
            model=model,
            max_tokens=max_tokens,
            temperature=temperature,
            tier=tier,
            priority=priority,
            call_type=call_type,
            persona_id=persona_id,
        )

    def chat_messages(
        self,
        messages: List[Dict[str, str]],
        *,
        model: str = "default",
        max_tokens: int = 2048,
        temperature: float = 0.7,
        tier: Optional[Tier] = None,
        priority: Optional[str] = None,
        call_type: Optional[str] = None,
        persona_id: Optional[str] = None,
    ) -> str:
        """Send a multi-turn chat completion request.

        Args:
            messages: List of {"role": "...", "content": "..."} messages
            model: Model name or LoRA adapter name
            max_tokens: Maximum output tokens
            temperature: Sampling temperature
            tier: Override routing tier
            priority: Override priority level
            call_type: Task type for routing rules
            persona_id: For cost attribution

        Returns:
            The assistant's response text.
        """
        body = {
            "model": model,
            "messages": messages,
            "max_tokens": max_tokens,
            "temperature": temperature,
        }

        headers = {
            "Content-Type": "application/json",
            "X-Kronaxis-Service": self.service,
        }

        t = tier if tier is not None else self.default_tier
        if t != Tier.AUTO:
            headers["X-Kronaxis-Tier"] = str(int(t))

        p = priority or self.default_priority
        headers["X-Kronaxis-Priority"] = p

        if call_type:
            headers["X-Kronaxis-CallType"] = call_type
        if persona_id:
            headers["X-Kronaxis-PersonaID"] = persona_id
        if self.api_token:
            headers["Authorization"] = f"Bearer {self.api_token}"

        url = f"{self.base_url}/v1/chat/completions"
        data = json.dumps(body).encode()

        req = urllib.request.Request(url, data=data, headers=headers, method="POST")
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                result = json.loads(resp.read())
        except urllib.error.HTTPError as e:
            error_body = e.read().decode() if e.fp else str(e)
            raise RouterError(e.code, error_body) from e

        if not result.get("choices"):
            raise RouterError(0, "no choices in response")

        return result["choices"][0]["message"]["content"]

    def batch_submit(
        self,
        requests: List[Dict[str, Any]],
        backend: str,
        callback_url: Optional[str] = None,
    ) -> Dict[str, Any]:
        """Submit an async batch job for 50% cost savings.

        Args:
            requests: List of {"custom_id": "...", "body": {...}} batch requests
            backend: Target backend name
            callback_url: Webhook URL for completion notification

        Returns:
            Batch job object with id, status, etc.
        """
        body = {"backend": backend, "requests": requests}
        if callback_url:
            body["callback_url"] = callback_url

        return self._api_call("POST", "/api/batch/submit", body)

    def batch_status(self, job_id: str) -> Dict[str, Any]:
        """Get the status of a batch job."""
        return self._api_call("GET", f"/api/batch?id={job_id}")

    def batch_results(self, job_id: str) -> List[Dict[str, Any]]:
        """Get results of a completed batch job."""
        return self._api_call("GET", f"/api/batch/results?id={job_id}")

    def costs(self, period: str = "today") -> Dict[str, Any]:
        """Get cost dashboard data."""
        return self._api_call("GET", f"/api/costs?period={period}")

    def health(self) -> Dict[str, Any]:
        """Get router health status."""
        return self._api_call("GET", "/health")

    def _api_call(self, method: str, path: str, body: Any = None) -> Any:
        url = f"{self.base_url}{path}"
        data = json.dumps(body).encode() if body else None
        headers = {"Content-Type": "application/json"}
        if self.api_token:
            headers["Authorization"] = f"Bearer {self.api_token}"

        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        with urllib.request.urlopen(req, timeout=self.timeout) as resp:
            return json.loads(resp.read())


class RouterError(Exception):
    """Error from Kronaxis Router."""
    def __init__(self, status_code: int, message: str):
        self.status_code = status_code
        self.message = message
        super().__init__(f"Router error {status_code}: {message}")

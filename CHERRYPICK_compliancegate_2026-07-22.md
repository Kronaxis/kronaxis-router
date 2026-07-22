---
title: "Cherry pick: ComplianceGate (arXiv 2606.31163) into kronaxis-router"
date: "22 July 2026"
status: "Private and confidential, design note for the router session"
---

# Cherry pick: ComplianceGate into kronaxis-router

Source: Dey, *ComplianceGate: Classifier-Gated Multi-Tier LLM Routing for Inference in Regulated
Industries* (arXiv 2606.31163). A trained encoder classifier sits before any decoder inference and, in
about 7ms, scores each query for complexity and data sensitivity, then routes it: PII to a local
endpoint before any LLM compute (data residency violations become structurally impossible), simple
queries to a small fast model, complex to a large one. Reported 39% latency cut, 33 to 52% cost saving,
classifier 99.2% accuracy with near perfect PII recall.

**The core idea is already taken.** `sensitivity.go` (built this session, citing this paper) is the
classifier gated sensitivity route: a `SensitivityScore` 0 to 100, a `TrustedSensitive` tier that keeps
sensitive prompts on a self hosted model and off any external or cheaper endpoint, and a fail closed
default so an uncertain prompt goes to the trusted tier. `classifier.go` already handles the complexity
half. So do not rebuild. Three sharper pieces remain, and they are what turns a good routing heuristic
into a sellable compliance guarantee.

## Take 1: a real PII detector, not just weighted keywords

**Why.** ComplianceGate's headline result is near perfect **PII recall**. `sensitivity.go` today scores
on `sensitiveKeywords` (weighted phrases), which catches topics but not the specific thing the
regulation cares about: a name, a date of birth, a National Insurance number, an address, a company or
case reference sitting in the prompt text.

**Adapt.** Add a `piiDetect` pass before the keyword score: fast deterministic patterns first (NI number,
postcode, email, phone, DOB, IBAN, card, CH company number, case citation), then optionally a small NER
pass for person and organisation names. A positive PII hit is a hard signal, independent of the keyword
score, that forces the `TrustedSensitive` (local only) tier. Deterministic regex is cheap and needs no
training, so it can ship first; the NER upgrade is a later refinement, matching their trained encoder.

## Take 2: turn it on, measured, with the split we already built

**Why.** `sensitivity.go` is deliberately additive and **off by default**. The compliance value only
exists once it is on. The `TrustedUncertain` versus `TrustedSensitive` distinction already in the code
exists precisely to measure the cost of turning it on before enforcing it.

**Adapt.** Run a shadow phase: classify every query and log the tier it would route to, without changing
routing, for long enough to see the real volume and false positive rate of sensitive and PII hits. Then
enable enforcement once the numbers are understood. This is the safe path from "heuristic that could
break routing" to "gate we trust in production".

## Take 3: leak proof telemetry (the sellable part)

**Why.** ComplianceGate's real claim is *structural*: a residency violation cannot happen. A routing rule
alone is a policy; the claim needs proof. `cost_telemetry.go` already gives us the hook.

**Adapt.** Emit a counter and an audit line for every query classified sensitive or PII, recording which
backend it was routed to, and assert the invariant: **zero sensitive or PII queries reached an external
endpoint**. A dashboard of that invariant, always green, is the concrete evidence behind the Kronaxis
Compliance and UK Intelligence "your regulated data never leaves our infrastructure" claim. It turns a
marketing sentence into an architectural fact with an audit trail.

## Skip
The geographic multi region routing. We are single site on DL580, so there is no second region to route
to. Keep the *concept* only as product positioning for the UK data residency story (all inference on
UK infrastructure), not as router code.

## Sequence and owner
Owner: the router session. Order: **Take 1 (PII detector, deterministic first)**, then **Take 2 (shadow
then enable)**, then **Take 3 (leak proof telemetry and dashboard)**. All three build directly on
`sensitivity.go`, `classifier.go` and `cost_telemetry.go` that already exist; none is a new subsystem.

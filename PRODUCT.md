# Product

## Register

product

## Users

Two audiences, with overlapping interests:

**The agent-systems engineer.** Building AI agents that need to do real work against real systems on behalf of real users. Today they either hand the agent a long-lived service token (no link to the user, no audit trail) or have the agent impersonate the user with a stolen-looking token (no link to the agent, no narrowing of scope). They want their agent to authenticate as itself, present cryptographic evidence of *who delegated what to it*, and hold only credentials short-lived enough that revocation by TTL is sufficient. They are sophisticated; they will distrust the tool if it invents protocols or hides identity behind opaque IDs.

**The security engineer.** Owns identity for an org running both humans and agents. They want: zero standing privileges, no static long-lived secrets sitting on disk, signed end-to-end evidence of every delegation hop, and the ability to reconstruct the full *human → agent → sub-agent → tool* chain for any action — at the time the action happens, and a year later from logs. They are skeptical of new IdPs because most reinvent OAuth poorly. They will trust this one only if it is unmistakably standards-based.

## Product Purpose

bonafide is an identity provider that issues short-lived, task-scoped credentials to both humans and AI agents and treats the delegation chain between them as a first-class, signed primitive. The human authenticates via OIDC. The agent authenticates with a SPIFFE identity issued by the org's own SPIRE deployment. The token-exchange endpoint mints a task token whose `act` claim (RFC 8693) names the acting agent and nests any prior actors inside; the human's identity stays in `sub` and never mutates. The acting agent uses its SPIFFE identity to pull short-lived secrets from Vault — no static credentials. The downstream resource validates the task token, reads the act chain, and authorizes against `sub` plus the outermost actor. The audit layer can reconstruct the full chain from logs.

Success looks like a resource server that can say, in writing, *"this request was performed by agent X, acting on behalf of human H, via planner agent P, for scope S, with a credential that expired five minutes after it was issued"* — and prove every clause with a signature.

## Brand Personality

Standards-based. Conservative. Fail-closed. The product never invents a protocol where one exists, never opens a code path that runs on a missing or malformed token, never extends a credential's lifetime to be "convenient." Identity claims are made in the language of OAuth, OIDC, and SPIFFE — not in a custom dialect. Where the standard is silent (the policy gate, the audit shape), the product picks one explicit shape and documents it in `CONTRACT.md` rather than leaving it implicit.

## Anti-references

- **"AI security" theater.** No proprietary agent identifier format, no "agent risk score," no marketing layer over what is fundamentally OIDC and SPIFFE. The standards are doing the work.
- **Token-as-bearer-secret thinking.** Tokens here are short, signed, and task-scoped. The product does not have a notion of "the agent's API key." Anywhere a long-lived secret would appear, a SPIFFE identity + short-lived lease appears instead.
- **Impersonation by default.** Every delegated action carries evidence of the delegating actor. The agent is never indistinguishable from the human. Tokens that mutate `sub` are rejected at the resource.
- **Opacity.** No opaque agent IDs, no opaque task IDs. SPIFFE IDs are URI-shaped and meaningful; scopes are a documented grammar; the delegation chain is recoverable from a single decoded JWT.

## Design Principles

1. **Identity is real or it is a lie.** Every actor — human or workload — is rooted in a verifiable issuer (OIDC for humans, SPIFFE for workloads). No actor authenticates with a string the operator pasted from a config file.
2. **No static long-lived secrets.** Every credential in the system has a TTL measured in minutes. Where a tool would historically have stored an API key, it stores nothing and fetches a lease on demand.
3. **Every delegation is visible end-to-end.** A signed `act` chain at mint time, a structured audit event at exchange time, a reconstructable chain at any later time. Three independent records of the same fact.
4. **Standards over invention.** OIDC for human auth, RFC 8693 for delegation, SPIFFE for workload identity, JWT for the token itself. If the product appears to need a new protocol, the product is wrong.
5. **Fail closed.** Missing `actor_token`, unknown agent, unknown scope grammar, unparseable token, unreachable policy engine — all deny. There is no "permissive mode."
6. **TTLs are the floor of safety, not the ceiling.** The MVP relies on short TTLs in lieu of active revocation. A user JWT lives at most 15 minutes; a task token at most 5; a Vault DB lease at most 5. Later work may add active revocation; nothing about the design assumes it.

## Accessibility & Inclusion

The MVP has no user-facing UI surface beyond CLI scripts and a minimal resource server response. Accessibility considerations apply when (if) a real login UI is added — at that point WCAG AA contrast and reduced-motion respect become explicit acceptance criteria, in the SDD slice that introduces them. Out of scope for the MVP.

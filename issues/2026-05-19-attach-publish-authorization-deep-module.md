## Problem

The `attach` / publish-authorization workflow is currently split across CLI, local file helpers, grant client logic, grant server contracts, and service runtime startup.

Current cluster:

- `cmd/tubo/local_service_claims.go`
- `cmd/tubo/grants_cmd.go`
- `cmd/tubo/main.go`
- `internal/grants/*`
- `internal/app/service/app.go`
- `internal/serviceidentity/*`

Architectural friction:

- One domain concept — “prepare a service so it is authorized to publish and can safely start” — is spread across many helpers and callers.
- The caller currently knows too much about the internal state machine: local authority minting, remote grant submit/poll, claim-vs-lease validation, expiry handling, renewal, share token recovery hints, and file persistence.
- Security-sensitive decisions are not owned by one deep module, which increases the risk of inconsistent handling across `attach`, grant polling, and renewal paths.
- Understanding or changing behavior requires bouncing between multiple files and tests.

Integration risk in the seams:

- A valid `ServiceClaim` is not always sufficient; a valid `PublishLease` is also required for publication, but this distinction is easy to mishandle outside a dedicated workflow owner.
- The same service may follow different paths depending on local authority presence, stored files, grant server reachability, expiry timing, and prior pending request state.
- Renewal uses the same business rules as initial attach, but today the logic is partially duplicated across startup and background renewal code.

This makes the codebase harder to navigate, harder to test at the right boundary, and riskier to evolve in a security-sensitive area.

## Proposed Interface

Introduce one deep module responsible for attach-time publish authorization, with a small public interface and explicit ports underneath.

Suggested public shape:

```go
package attachauth

type Resolver interface {
    Resolve(ctx context.Context, req ResolveRequest) (ResolveResult, error)
    Renew(ctx context.Context, req RenewRequest) (ResolveResult, error)
}

type ResolveRequest struct {
    ConfigPath string
    Config     config.Config
}

type RenewRequest struct {
    ConfigPath     string
    Config         config.Config
    Service        config.NamespaceService
    ServicePeerID  string
}

type Decision string

const (
    DecisionReady           Decision = "ready"
    DecisionPendingApproval Decision = "pending_approval"
    DecisionDenied          Decision = "denied"
    DecisionRetryable       Decision = "retryable_failure"
)

type ResolveResult struct {
    Decision                 Decision
    Config                   config.Config
    Service                  config.NamespaceService
    ServicePeerID            string
    MembershipCapabilityFile string
    ServiceClaimFile         string
    ServicePublishLeaseFile  string
    ServiceShareToken        string
    ShareRecoveryHint        string
    PublishLeaseReused       bool
    MintedLocally            bool
    UserMessage              string
}
```

Illustrative caller usage:

```go
result, err := resolver.Resolve(ctx, attachauth.ResolveRequest{
    ConfigPath: configPath,
    Config:     cfg,
})
if err != nil {
    return err
}
if result.Decision != attachauth.DecisionReady {
    return attachauth.UserVisibleError(result)
}
printAttachShareHint(result)
startRenewalLoop(ctx, resolver, result)
return startServiceRuntime(result)
```

What this module should hide internally:

- service identity creation / validation
- local artifact path/default resolution
- publish lease verification
- service claim verification
- local authority minting
- remote grant submit/poll flow
- handling of pending / denied / expired grant outcomes
- share token generation and recovery hints
- renewal decision logic
- persistence updates to config and artifact files

## Dependency Strategy

Primary category: **Ports & adapters (remote but owned)**

The workflow depends on:

- local config + filesystem persistence
- local authority signing
- remote grant service submit/poll
- cryptographic verification
- wall clock / expiry logic

Recommended ports:

- `IdentityStore`: load/create service owner identity and peer binding inputs
- `ArtifactStore`: read/write config, claim, lease, membership capability
- `GrantClient`: submit and poll publish grants
- `AuthoritySigner`: mint local claim + publish lease when authority key is available
- `Clock`: current time for expiry-sensitive logic

Production adapters use the current file-based config/artifact storage and libp2p grant client. Tests use in-memory adapters and fake grant responses.

## Testing Strategy

Replace shallow decision-path tests with boundary tests on the new workflow module.

New boundary tests to write:

1. first attach on an authority node creates or validates identity, mints artifacts, and returns `ready`
2. fresh attach on a non-authority node with grant peer submits request and returns `pending_approval` when not yet approved
3. attach with valid stored publish lease returns `ready` and reuses artifacts
4. attach with expired publish lease but valid renewal path obtains fresh authorization and returns `ready`
5. attach with denied grant returns a user-visible denied outcome and does not start runtime
6. attach with expired/missing/invalid claim and no authority/grant path fails clearly
7. renewal path uses the same workflow rules as initial attach and refreshes artifacts correctly
8. share token / recovery hint behavior remains correct for approved and reused paths

Existing tests likely to be replaced or consolidated:

- `TestResolveAttachAuthorizationAcceptsExistingClaimWithoutAuthority`
- `TestResolveAttachAuthorizationReusedLeaseReportsGrantRecoveryHint`
- `TestResolveAttachAuthorizationGeneratesShareTokenWithAuthorityKey`
- `TestResolveAttachAuthorizationRequestsAndUsesGrantRoute`
- `TestResolveAttachAuthorizationTreatsExpiredPublishLeaseAsMissing`
- `TestResolveAttachAuthorizationRequestsGrantAndReceivesShareToken`
- `TestResolveAttachAuthorizationHandlesDeniedAndExpiredGrantRoute`
- `TestResolveAttachAuthorizationRejectsMissingOrBadClaimWithoutAuthority`
- parts of `TestGrantsRequestSubmitsPollsAndSavesApprovedClaim`

These tests do not all need to disappear, but their assertions should move upward to the new module boundary instead of re-testing internal helper branching.

Test environment needs:

- in-memory config/artifact store
- fake authority signer
- fake grant client with pending/approved/denied/expired scripted responses
- deterministic clock for expiry/renewal tests

## Implementation Recommendations

- Give one module ownership of the business question: “is this service allowed to publish now, and what validated artifacts are required to start?”
- Hide internal transitions; expose only business-level outcomes (`ready`, `pending`, `denied`, retryable/fatal failure as appropriate).
- Keep CLI command parsing and printing outside the module.
- Keep runtime startup outside the module.
- Make renewal call back into the same workflow owner instead of reimplementing attach-time rules elsewhere.
- Treat cryptographic validation and persistence consistency as core responsibilities of the module, not caller responsibilities.
- Preserve a small public API even if the internal state machine is rich.

## Acceptance Gates

Specific acceptance criteria for this refactor:

1. `attach` startup path uses the new workflow module as the single owner of publish-authorization resolution.
2. background publish-lease renewal also uses the same module/business rules instead of duplicated branching.
3. callers outside the module no longer decide independently between local mint / remote grant / stored lease reuse.
4. security validation remains enforced for scope, service identity, publisher peer binding, expiry, and signed artifacts.
5. user-visible outcomes remain clear for ready, pending approval, denied, expired, and retryable failure cases.
6. boundary tests cover the main attach and renewal scenarios listed above.
7. outdated helper-level branching tests are deleted or reduced once boundary tests make them redundant.

Repository gates before closing:

- `go test ./...`
- `./tests/smoke-compose.sh`
- `RUN_INTEGRATION=1 go test -v ./tests/integration`

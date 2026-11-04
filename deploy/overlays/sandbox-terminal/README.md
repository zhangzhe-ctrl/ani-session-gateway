# Managed Sandbox terminal overlay

This overlay extends `deploy/base` with the verified 66 main-service selectors: `ani-system` Governance Pods labelled `app=ani-governance` may connect to Session Gateway TCP 9090; Session Gateway may connect to `ani-system` Resource Pods labelled `app=ani-resource-service` on TCP 9446. The existing base DNS, Redis and Kubernetes API rules are retained. The actual Resource ingress policy and CNI must also permit the Resource connection. Adapt exact namespaces, labels and port to another environment; do not widen the policy to all Pods or all ports. The optional historical sandbox test deployments do not match these selectors.

The overlay deliberately references, but does not create, a required ConfigMap `ani-session-gateway-managed-targets` with nonempty `resource-address`, `resource-server-name`, and `resource-cluster-id` keys. Set the address to Resource's **mTLS terminal listener**, the server name to an exact DNS SAN on its certificate, and the cluster ID to the Resource provider's configured ID. The main 66 Service currently exposes TCP 9446; that observation does not prove the target's listener, certificate or cluster ID is configured. Missing keys keep the Pod from starting. An empty address disables the Resource validator, so managed sessions fail closed; validate all three values before deployment.

Create two separately scoped Secrets outside source control, with readable keys `ca.crt`, `tls.crt`, and `tls.key`:

- `ani-session-gateway-resource-client-tls`: CA used to verify the exact Resource server name, plus Session Gateway's client certificate with the `spiffe://ani.internal/service/ani-session-gateway` identity and matching private key.
- `ani-session-gateway-control-server-tls`: CA used to verify Governance clients, plus the Session Gateway server certificate and matching private key. Governance's client certificate must present exactly `spiffe://ani.internal/service/ani-governance`; Governance must trust the Session Gateway server's exact DNS name.

The overlay enables mTLS on the entire SG gRPC listener, not only Sandbox calls. Every existing client of that listener must already be configured for mTLS before rollout; plaintext callers will fail. Coordinate their transition and rollback before applying.

The existing ticket and Redis Secret remains separate. All keys are read-only mounts and the existing non-root fsGroup applies. Do not copy certificate or key contents into the repository, evidence, ConfigMaps, shell history or logs.

Before applying, render `kubectl kustomize deploy/overlays/sandbox-terminal` and use an appropriately scoped server-side dry run. Independently verify Secret identities and expiry, exact DNS SANs, the 9446 listener, Resource-side ingress policy, Gateway and Resource cluster IDs, and `pods/exec`, Pod and Namespace read RBAC. Confirm a deliberately denied peer and a valid Governance-to-SG-to-Resource flow; check that the expected CNI actually enforces NetworkPolicy. Render and retain the currently applied Deployment and both policy states for an authorized, UID/resourceVersion guarded rollout and rollback.

Rollback restores the previously captured Deployment and removes only the overlay-created `ani-session-gateway-managed-terminal` policy after no new managed sessions are needed; avoid deleting shared credentials or base policies. Existing connected streams, single-use tickets and Resource-side pending operations need their normal lifecycle handling. No deployment, network-policy enforcement or terminal live test is claimed by this overlay.

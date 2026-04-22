<!--
SPDX-FileCopyrightText: Red Hat

SPDX-License-Identifier: Apache-2.0
-->

# Proposal: Remove NAR REST API and Simplify Hardware Manager Architecture

```yaml
title: nar-api-removal
authors:
  - @dpenney
reviewers:
  - TBD
approvers:
  - TBD
creation-date: 2026-03-31
last-updated: 2026-04-21
```

## Summary

Remove the internal REST API layer between the ProvisioningRequest (PR)
controller and the NodeAllocationRequest (NAR) CRs. Replace it with direct
Kubernetes client operations, eliminate the callback mechanism in favor of
CR watches, and remove the hardwareplugin-manager-server that hosts these
internal APIs. As a final step, rename the metal3-hwmgr to drop the
"plugin" terminology.

## Motivation

The current architecture uses a REST API as an intermediary between the PR
controller and the hardware manager:

```text
PR Controller  ──REST──►  hardwareplugin-manager-server  ──CR──►  NAR CR
                                                                    │
PR Controller  ◄──REST──  hardwareplugin-manager-server  ◄──REST──  NAR Controller
                 (callback)
```

This was originally designed to support third-party hardware plugins via a
standardized API interface. In practice, the metal3 NAR controller already
watches NAR CRs directly — it does not respond to REST API calls. The REST
layer is essentially a CRUD proxy that adds complexity without providing
value for our use case.

### Problems with the Current Approach

- **Field mapping maintenance burden**: Adding a new field to the NAR requires
  changes in 7+ files (CRD type, OpenAPI spec, generated client/server code,
  server handler create path, server handler update path, response converter,
  comparison logic).
- **Inconsistent field handling**: Create and update handlers need separate
  field mapping code. The update handler must decide whether to preserve or
  override each field, leading to subtle bugs (e.g., `SkipCleanup` not
  clearing, `ClusterProvisioned` not being returned in GET responses).
- **Polling overhead**: The PR controller polls the NAR via REST at 15-second
  intervals during deletion, adding unnecessary latency to what should be
  sub-second operations.
- **Callback complexity**: The NAR controller must maintain a separate HTTP
  client to notify the PR controller of status changes, rather than simply
  updating the CR and letting the watch mechanism trigger reconciliation.
- **Test complexity**: Tests must mock the REST interface rather than using
  standard fake Kubernetes clients.

### Benefits of Direct CR Access

- **Uniform field handling**: `client.Patch` handles all fields atomically — no
  separate create/update paths to maintain.
- **Watch-based notifications**: The PR controller can watch NAR CRs for
  status changes instead of polling or receiving callbacks.
- **Simplified testing**: Use standard fake Kubernetes clients instead of mock
  REST interfaces.
- **Fewer components**: Eliminate the hardwareplugin-manager-server deployment
  and its associated service, service account, and RBAC.

## Current Architecture

### Components Involved

| Component | Role | Location |
|-----------|------|----------|
| PR Controller | Creates/updates/deletes NARs via REST | `internal/controllers/` |
| HW Plugin Client | REST client for NAR CRUD | `hwmgr-plugins/api/client/provisioning/` |
| HW Plugin Server | REST server, proxies CRUD to K8s | `hwmgr-plugins/api/server/provisioning/` |
| NAR Callback Client | Sends status notifications | `hwmgr-plugins/api/client/nar-callback/` |
| NAR Callback Server | Receives notifications, triggers reconcile | `hwmgr-plugins/api/server/nar-callback/` |
| Metal3 NAR Controller | Watches NAR CRs, manages BMHs | `hwmgr-plugins/metal3/controller/` |
| HW Plugin Manager Server | Hosts REST APIs, deployed by operator | `internal/controllers/hardwareplugin_manager_setup.go` |

### Data Flow

**Provisioning (PR → NAR):**

1. PR controller merges `hwMgmtDefaults` (from ClusterTemplate) with
   `hwMgmtParameters` (from ProvisioningRequest) into `clusterInput.hwMgmtData`
2. PR controller builds NAR request object from the merged data
3. PR controller calls REST API (`POST /hardware-manager/provisioning/v1/node-allocation-requests`)
4. HW plugin server receives request, creates NAR CR
5. Metal3 NAR controller detects NAR CR via watch, begins allocation

**Status Updates (NAR → PR):**

1. Metal3 NAR controller updates NAR CR status conditions
2. Metal3 NAR controller sends HTTP callback (`POST /nar-callback/v1/provisioning-requests/{name}`)
3. NAR callback server receives callback, triggers PR reconciliation
4. PR controller calls REST API to GET NAR status

### Code Metrics

| Category | Files | Lines |
|----------|-------|-------|
| REST API specs + generated code | ~15 | ~8,000 |
| REST server handlers | ~6 | ~1,200 |
| REST client code | ~4 | ~600 |
| NAR callback API | ~4 | ~1,500 |
| PR controller (NAR-related) | ~2 | ~500 |
| Tests (NAR REST mocks) | ~3 | ~3,000 |
| **Total** | **~34** | **~14,800** |

## Proposed Architecture

```text
PR Controller  ──K8s client──►  NAR CR  ◄──watch──  NAR Controller
      │                           │
      └──────watch (status)───────┘
```

The PR controller operates directly on NAR CRs using the Kubernetes client.
The NAR controller continues to watch NAR CRs as it does today. Status
changes on the NAR CR trigger reconciliation in the PR controller via a
watch, eliminating the callback mechanism.

## Implementation Plan

### Phase 1: Replace PR Controller REST CRUD

Estimated: ~8 files, ~1,500 lines changed

Replace the PR controller's REST API calls with direct Kubernetes client
operations on NAR CRs:

- **Use the ProvisioningRequest name as the NAR name.** There is a 1:1
  relationship between ProvisioningRequests and NARs, so the PR's
  `.metadata.name` (a UUID) is used directly as the NAR's `.metadata.name`.
  This eliminates NAR name generation, removes the need for
  `NodeAllocationRequestRef.NodeAllocationRequestID` in the PR status, and
  means the NAR can always be looked up from the PR name without any stored
  reference.
- Rewrite `buildNodeAllocationRequest` to return `pluginsv1alpha1.NodeAllocationRequest`
  (CRD type) instead of `hwmgrpluginapi.NodeAllocationRequest` (REST type).
  The function already reads from `t.clusterInput.hwMgmtData` (pre-merged
  map from the ClusterTemplate's `hwMgmtDefaults`), so no HardwareTemplate
  lookup is needed.
- Rewrite `createOrUpdateNodeAllocationRequest` to use `client.Create` and
  `client.Update`
- Rewrite `getNodeAllocationRequestResponse` to use `client.Get` and return
  the CRD type directly
- Rewrite `setNARClusterProvisioned` and `syncNARSkipCleanup` to use
  `client.Get` and `client.Patch`
- Rewrite deletion in `handleProvisioningRequestDeletion` to use
  `client.Get` and `client.Delete`
- Remove `HardwarePluginClientAdapter` usage from the PR controller
- Remove `getHardwarePluginClient` (REST client setup)
- Remove `NodeAllocationRequestRef` from the PR status (the NAR name is
  always the PR name)
- Refactor tests to use fake Kubernetes clients instead of mock REST
  interfaces
- The generated REST client/server code stays in the tree (unused) until
  Phase 3
- The callback mechanism stays until Phase 2

**RBAC changes**: The PR controller already has the necessary RBAC
permissions for NAR CRs via existing kubebuilder markers (`get`, `list`,
`watch`, `create`, `update`, `patch`, `delete` on
`nodeallocationrequests.plugins.clcm.openshift.io`). No RBAC changes
needed.

### Phase 2: Replace Callback with NAR Status Watch

Estimated: ~8 files, ~2,000 lines changed

Replace the HTTP POST callback mechanism with a watch on NAR CRs in the
PR controller:

- Add a watch or secondary reconciler in the PR controller for NAR status
  condition changes
- Remove the NAR callback client from the metal3 controller
  (`updateConditionAndSendCallback`)
- Remove the NAR callback server from the PR controller startup
- Remove the callback HTTP server (port 8090) from
  `start_controller_manager.go`
- Generated callback code stays until Phase 3

**Watch design considerations**: The PR controller needs to reconcile when
a NAR's status conditions change, mapping the NAR back to the owning
ProvisioningRequest. This can be done with an `EnqueueRequestForOwner`
handler or a custom mapping function using the NAR's `spec.clusterId` or
labels.

### Phase 3: Remove Generated Code and API Specs

Estimated: ~15 files, ~8,000 lines removed

Single clean sweep of all dead code:

- Delete `hwmgr-plugins/api/openapi/specs/provisioning.yaml`
- Delete `hwmgr-plugins/api/openapi/specs/nar_callback.yaml`
- Delete `hwmgr-plugins/api/client/provisioning/` (generated client)
- Delete `hwmgr-plugins/api/server/provisioning/` (generated server)
- Delete `hwmgr-plugins/api/client/nar-callback/` (callback client)
- Delete `hwmgr-plugins/api/server/nar-callback/` (callback server)
- Delete codegen config files
- Delete mock interfaces (`internal/controllers/mocks/`)
- Delete mock hardware plugin server
  (`internal/controllers/mock_hardware_plugin_server.go`) — no longer used
  by the PR controller after Phase 1, and the e2e tests no longer need it
  since NARs are created directly via K8s client

### Phase 4: Remove hardwareplugin-manager-server

Estimated: ~5 files, ~500 lines removed

With no remaining REST endpoints, the server has no purpose:

- Remove server startup code from `start_controller_manager.go`
- Remove `createHardwarePluginManagerClusterRole` and related RBAC setup
- Remove the Kubernetes Service and Deployment configuration
- Remove NonResourceURL RBAC rules (`/hardware-manager/provisioning/*`,
  `/nar-callback/*`)
- Delete the `HardwarePlugin` CRD entirely — its sole purpose is to store
  the REST API root URL and auth config, which are no longer needed
- Remove the `HardwarePlugin` controller/reconciler
- Remove `HardwarePluginRef` from the `HwMgmtDefaults` struct in
  ClusterTemplate and any other CRDs that reference it

### Phase 5: Rename — Drop "Plugin" Terminology

Estimated: ~50+ files, ~1,000 lines changed (mostly renames)

Once it is no longer a plugin with an API interface, rename throughout:

- Controller names, constants, labels with `hardwareplugin`/`hwplugin` prefix
- Deployment names, service account names
- Documentation references
- Sample files and GitOps templates

This is a **breaking change** requiring fresh install (no upgrade path), so
it should align with a release boundary where breaking changes are accepted.

## Post-Phase 1 Simplification Opportunities

After the mechanical REST-to-client replacement, several design choices
made to accommodate the REST API become optional:

- **`ClusterProvisioned` on NAR spec**: Was introduced because the PR
  controller needed to signal the plugin through the REST API. The metal3
  controller could instead watch ProvisioningRequests directly. However,
  keeping the field may be simpler than adding a PR watch to the metal3
  controller.

- **`SkipCleanup` propagation (PR → NAR → AllocatedNodes)**: The PR
  controller could set `SkipCleanup` directly on AllocatedNodes instead of
  propagating through the NAR. However, the current propagation is clean
  and well-tested.

- **`handleRenderHardwareTemplate` function name**: This function no longer
  renders a HardwareTemplate (which has been removed). It could be renamed
  to better reflect its current role of building the NAR from merged
  hwMgmt data.

- **`HardwarePluginLabel` on NAR and AllocatedNode**: The
  `clcm.openshift.io/hardware-plugin` label was needed when multiple
  hardware manager plugins could coexist, so each plugin's controller could
  filter for its own NARs via a label-selector watch predicate. Since the
  re-architecture consolidates to a single hardware manager, this label is
  no longer necessary. Removing it requires updating the Metal3 controller's
  `SetupWithManager` watch predicate (in both the NAR and AllocatedNode
  controllers) and removing the label from all creation sites in both the
  PR controller and the Metal3 controller.

These simplifications should be evaluated after Phase 1 based on whether
the complexity reduction justifies the churn.

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Breaking third-party plugin support | No known third-party plugins exist. The metal3 plugin is the only implementation. |
| Large change scope | Phased approach allows incremental delivery and testing |
| Test coverage gaps | Each phase includes test refactoring. Phase 1 replaces mock REST with fake K8s clients. |
| Phase 5 is a breaking change | Align with a release boundary that already includes breaking changes |
| RBAC changes for PR controller | Straightforward — add NAR CR permissions to the operator's ClusterRole |

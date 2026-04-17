# Uninstalling the Operator

This guide covers the proper procedure for uninstalling the O-Cloud Manager operator
and troubleshooting common issues.

The correct uninstall order is:

1. **Delete ProvisioningRequests** - Remove all ProvisioningRequest CRs to trigger
   proper cleanup of provisioned clusters and hardware
2. **Uninstall the operator** - Remove the operator via OLM (Console or CLI)
3. **Delete CRDs** - Remove Custom Resource Definitions and any remaining CRs
4. **Delete the namespace** - Remove the operator namespace

## Before Uninstalling

All ProvisioningRequest CRs must be deleted before uninstalling the operator. The
ProvisioningRequest controller runs finalizers that handle critical cleanup, including
deprovisioning clusters, deallocating hardware, and powering off bare-metal hosts.
If the operator is removed before these finalizers complete, the cleanup will not
run and resources will be left in an inconsistent state.

```bash
# Delete all ProvisioningRequests and wait for finalizers to complete
oc delete provisioningrequests --all

# Verify all ProvisioningRequests have been fully deleted
oc get provisioningrequests
```

> [!WARNING]
> Do not uninstall the operator while ProvisioningRequests are still being
> deleted. Wait until `oc get provisioningrequests` returns no resources before
> proceeding.

Then proceed with operator uninstallation via the OpenShift Console or CLI.

## Uninstalling via OpenShift Console

1. Navigate to **Operators** → **Installed Operators**
2. Find the **O-Cloud Manager** operator
3. Click the operator name, then click **Uninstall Operator**
4. Confirm the uninstallation

## Uninstalling via CLI

```bash
# Delete the operator subscription
oc delete subscription o-cloud-manager -n oran-o2ims

# Delete the ClusterServiceVersion
oc delete csv -n oran-o2ims -l operators.coreos.com/o-cloud-manager.oran-o2ims
```

## Deleting CRDs

After uninstalling the operator, delete the CRDs. This will also delete any
remaining CRs (infrastructure hierarchy, hardware templates, etc.):

```bash
oc get crd --no-headers -o custom-columns=NAME:.metadata.name \
  | grep -e ocloud.openshift.io -e clcm.openshift.io \
  | xargs --no-run-if-empty oc delete crd
```

## Deleting the Namespace

After deleting the CRDs, delete the operator namespace:

```bash
oc delete namespace oran-o2ims
```
